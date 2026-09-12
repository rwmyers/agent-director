package director

import (
	"context"
	"fmt"
	"sort"
	"time"
)

// AttachGrace is how long a director is considered claimed by the conversation
// that attached to it.
//
// Two conversations directing one fleet is the failure this bounds: they would
// both spawn, both answer, and share a read cursor — so the second would
// silently see nothing of what the first had already read. There is no way to
// ask a conversation whether it is still alive, so a recent claim is taken at
// face value and an old one is assumed abandoned. Half an hour is longer than a
// pause for thought and shorter than a working session somebody walked away
// from.
//
// It governs the claim and nothing else. It used to gate waking a director as
// well, and that was wrong twice over: AttachedAt is written only by attach, so
// a director half an hour into a working session was as stale as one that had
// gone home, and the engagements most worth ringing about are the long ones
// whose director has been waiting longest. Whether an address is safe to type
// into is not a question about elapsed time — see notify.
//
// A clock is a weak proxy here too, and is known to be. What the claim actually
// wants to know is whether the conversation sitting here is the same one that
// attached, which is a question about identity that a duration can only guess
// at. It stands for now because the collision it guards against is real and
// nothing better is recorded yet; replacing it means recording an identity the
// harness supplies and the agent cannot fabricate, which is a larger change
// than retiring it from the wake path was.
const AttachGrace = 30 * time.Minute

// DirectorSummary is one director, as the attach decision sees it.
type DirectorSummary struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Workflow string `json:"workflow"`

	Engagements    int            `json:"engagements"`
	NeedsAttention int            `json:"needs_attention"`
	ByHealth       map[Health]int `json:"by_health,omitempty"`

	// AttachedAt is when a conversation last claimed this director.
	AttachedAt time.Time `json:"attached_at,omitempty"`
	// Claimed means somebody attached recently enough that they are probably
	// still working.
	Claimed bool `json:"claimed"`
}

// Recommendation is what the survey concluded.
type Recommendation string

const (
	// RecommendAttach means take over an existing director.
	RecommendAttach Recommendation = "attach"
	// RecommendCreate means start a new one.
	RecommendCreate Recommendation = "create"
	// RecommendAsk means the choice is genuinely the person's.
	RecommendAsk Recommendation = "ask"
)

// Survey is the state of every director in a root, and what to do about it.
type Survey struct {
	Root      string            `json:"root"`
	Directors []DirectorSummary `json:"directors"`

	Recommend   Recommendation `json:"recommend"`
	RecommendID string         `json:"recommend_id,omitempty"`
	Reason      string         `json:"reason"`
}

// TakeSurvey inspects every director in a root and decides whether a new
// conversation should attach to one or start its own.
//
// It observes engagements, which costs one harness call each. That is
// affordable because this runs once when a conversation starts, and the whole
// point is to know whether anything is waiting — a summary that could not say
// "one engagement is blocked" would not be worth running.
func TakeSurvey(ctx context.Context, roots Roots, clock Clock) (*Survey, error) {
	if clock == nil {
		clock = SystemClock
	}
	// A state file nobody can read is not a reason to refuse to survey the ones
	// that are fine. `director directors` is where an unreadable record gets
	// named, and `director repair` is where it gets fixed.
	states, _ := ListDirectors(roots.Primary)

	survey := &Survey{Root: roots.Primary}
	for _, state := range states {
		summary := DirectorSummary{
			ID:         state.DirectorID,
			Name:       state.Name,
			Workflow:   state.Workflow,
			ByHealth:   map[Health]int{},
			AttachedAt: state.AttachedAt,
			Claimed:    !state.AttachedAt.IsZero() && clock().Sub(state.AttachedAt) < AttachGrace,
		}

		// Observing needs the director's workflow, which may have been removed
		// since. A director we cannot describe is still reported — it exists,
		// and hiding it would make the choice look simpler than it is.
		if d, err := Open(roots, state.DirectorID, clock); err == nil {
			if engagements, err := d.Status(ctx); err == nil {
				summary.Engagements = len(engagements)
				for _, engagement := range engagements {
					summary.ByHealth[engagement.Health]++
					if engagement.Health.NeedsDirector() {
						summary.NeedsAttention++
					}
				}
			}
		}
		survey.Directors = append(survey.Directors, summary)
	}

	decide(survey)
	return survey, nil
}

// decide works out the recommendation.
//
// The ordering encodes what actually goes wrong. Taking over a director
// somebody is still using is worse than making a redundant one, so a claimed
// director is never recommended. Leaving blocked work untended is worse than
// inheriting a stranger's fleet, so an unclaimed director with waiting work
// wins over starting clean. And when two unclaimed directors both have work,
// there is no signal that distinguishes them — so it asks rather than guessing,
// because picking wrong means quietly operating on the wrong fleet.
func decide(survey *Survey) {
	var unclaimed []DirectorSummary
	for _, summary := range survey.Directors {
		if !summary.Claimed {
			unclaimed = append(unclaimed, summary)
		}
	}

	if len(survey.Directors) == 0 {
		survey.Recommend = RecommendCreate
		survey.Reason = "no directors exist here yet"
		return
	}
	if len(unclaimed) == 0 {
		survey.Recommend = RecommendCreate
		survey.Reason = fmt.Sprintf("every director here was claimed within the last %s, so another conversation is probably driving them", AttachGrace)
		return
	}

	// Prefer whoever has work waiting, then whoever has any work at all.
	sort.SliceStable(unclaimed, func(i, j int) bool {
		if unclaimed[i].NeedsAttention != unclaimed[j].NeedsAttention {
			return unclaimed[i].NeedsAttention > unclaimed[j].NeedsAttention
		}
		return unclaimed[i].Engagements > unclaimed[j].Engagements
	})

	best := unclaimed[0]
	if best.NeedsAttention > 0 {
		// More than one with waiting work is a genuine ambiguity: nothing here
		// distinguishes them, and choosing wrong is invisible.
		if len(unclaimed) > 1 && unclaimed[1].NeedsAttention > 0 {
			survey.Recommend = RecommendAsk
			survey.Reason = "more than one unattended director has engagements waiting; only a person can say which is yours"
			return
		}
		survey.Recommend = RecommendAttach
		survey.RecommendID = best.ID
		survey.Reason = fmt.Sprintf("%s has %d engagement(s) waiting and nobody tending them", best.Name, best.NeedsAttention)
		return
	}

	if len(unclaimed) == 1 {
		survey.Recommend = RecommendAttach
		survey.RecommendID = best.ID
		if best.Engagements == 0 {
			survey.Reason = fmt.Sprintf("%s is the only director here and is idle", best.Name)
		} else {
			survey.Reason = fmt.Sprintf("%s is the only unattended director here", best.Name)
		}
		return
	}

	survey.Recommend = RecommendAsk
	survey.Reason = "several directors are unattended and none has work waiting; only a person can say which is yours"
}

// Attach claims a director for this conversation, or creates one.
//
// Claiming is recorded so that a second conversation arriving shortly after can
// tell somebody is already here. It is advisory — nothing stops two
// conversations sharing a director if they insist — but it is the only signal
// available, since a conversation cannot be asked whether it still exists.
func Attach(ctx context.Context, roots Roots, id string, createNew bool, name, workflow string, clock Clock) (*Director, bool, error) {
	if clock == nil {
		clock = SystemClock
	}

	if createNew {
		if workflow == "" {
			available, err := roots.ListWorkflows()
			if err != nil || len(available) == 0 {
				return nil, false, fmt.Errorf("no workflows are defined under %s — run `director setup` first", roots.Primary)
			}
			if len(available) > 1 {
				names := make([]string, len(available))
				for i, w := range available {
					names[i] = w.Name
				}
				return nil, false, fmt.Errorf("more than one workflow is defined here; pass --workflow (available: %v)", names)
			}
			workflow = available[0].Name
		}
		state, err := Init(roots, workflow, name, clock)
		if err != nil {
			return nil, false, err
		}
		id = state.DirectorID
	}

	d, err := Open(roots, id, clock)
	if err != nil {
		return nil, false, err
	}
	if err := d.claim(); err != nil {
		return nil, false, err
	}
	return d, createNew, nil
}

// claim records that a conversation is here, and where it is sitting.
//
// The host is worked out fresh and replaces whatever was there rather than
// being merged into it. What was recorded belonged to the previous
// conversation: it may have been in a different harness, and its address rings
// somebody else's screen. Re-detecting per attach is also what makes the last
// attacher the one an engagement can wake — an earlier conversation falls back
// to checking on its own turn, which is the behaviour with no host at all.
func (d *Director) claim() error {
	host := d.locateHost()
	return d.mutate(func(state *State) error {
		state.AttachedAt = d.now()
		state.Host = host
		return nil
	})
}
