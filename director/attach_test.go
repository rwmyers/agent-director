package director

import (
	"testing"
	"time"
)

// The attach decision is a pure function over a survey, so it is tested as one.
// Each case here is a situation a conversation genuinely lands in, and the
// orderings matter more than they look: getting them wrong means either two
// conversations driving one fleet, or blocked work nobody picks up.
func TestDecide(t *testing.T) {
	t.Parallel()

	director := func(name string, engagements, waiting int, claimed bool) DirectorSummary {
		return DirectorSummary{
			ID: "dir_" + name, Name: name,
			Engagements: engagements, NeedsAttention: waiting, Claimed: claimed,
		}
	}

	t.Run("an empty project means start one", func(t *testing.T) {
		t.Parallel()
		survey := &Survey{}
		decide(survey)
		if survey.Recommend != RecommendCreate {
			t.Errorf("Recommend = %q, want %q", survey.Recommend, RecommendCreate)
		}
	})

	t.Run("the only unattended director is taken over", func(t *testing.T) {
		t.Parallel()
		survey := &Survey{Directors: []DirectorSummary{director("lead", 0, 0, false)}}
		decide(survey)
		if survey.Recommend != RecommendAttach || survey.RecommendID != "dir_lead" {
			t.Errorf("Recommend = %q/%q, want attach to dir_lead", survey.Recommend, survey.RecommendID)
		}
	})

	t.Run("a director somebody else is driving is never taken over", func(t *testing.T) {
		t.Parallel()
		// Two conversations on one fleet would both spawn, both answer, and
		// share a read cursor — so the second would silently see nothing of
		// what the first had already read. A redundant director is cheaper.
		survey := &Survey{Directors: []DirectorSummary{director("lead", 3, 2, true)}}
		decide(survey)
		if survey.Recommend != RecommendCreate {
			t.Errorf("Recommend = %q, want %q even though that director has waiting work",
				survey.Recommend, RecommendCreate)
		}
	})

	t.Run("waiting work outranks a clean slate", func(t *testing.T) {
		t.Parallel()
		// Leaving a blocked engagement untended is worse than inheriting
		// somebody's fleet: it has been stopped, doing nothing, and nobody
		// else is going to notice.
		survey := &Survey{Directors: []DirectorSummary{
			director("idle", 0, 0, false),
			director("busy", 2, 1, false),
		}}
		decide(survey)
		if survey.Recommend != RecommendAttach || survey.RecommendID != "dir_busy" {
			t.Errorf("Recommend = %q/%q, want attach to the one with work waiting",
				survey.Recommend, survey.RecommendID)
		}
	})

	t.Run("two unattended directors with waiting work is a question, not a guess", func(t *testing.T) {
		t.Parallel()
		// Nothing distinguishes them, and choosing wrong means quietly
		// operating on the wrong fleet with nothing in the output to show it.
		survey := &Survey{Directors: []DirectorSummary{
			director("one", 2, 1, false),
			director("two", 5, 3, false),
		}}
		decide(survey)
		if survey.Recommend != RecommendAsk {
			t.Errorf("Recommend = %q, want %q — picking the busier one is still a guess",
				survey.Recommend, RecommendAsk)
		}
	})

	t.Run("several idle directors is also a question", func(t *testing.T) {
		t.Parallel()
		survey := &Survey{Directors: []DirectorSummary{
			director("one", 0, 0, false),
			director("two", 0, 0, false),
		}}
		decide(survey)
		if survey.Recommend != RecommendAsk {
			t.Errorf("Recommend = %q, want %q", survey.Recommend, RecommendAsk)
		}
	})

	t.Run("a claimed director does not make an unclaimed one ambiguous", func(t *testing.T) {
		t.Parallel()
		survey := &Survey{Directors: []DirectorSummary{
			director("mine", 1, 1, true),
			director("free", 2, 1, false),
		}}
		decide(survey)
		if survey.Recommend != RecommendAttach || survey.RecommendID != "dir_free" {
			t.Errorf("Recommend = %q/%q, want attach to the unclaimed one",
				survey.Recommend, survey.RecommendID)
		}
	})
}

func TestAttachClaimsAndExpires(t *testing.T) {
	t.Parallel()
	// The claim is the only signal that another conversation is here, since a
	// conversation cannot be asked whether it still exists. It has to be
	// recorded on attach and it has to expire, or a crashed conversation would
	// hold a director forever.
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	d := newTestDirector(t, &fakeAdapter{name: "fake"}, now)

	if err := d.claim(); err != nil {
		t.Fatalf("claim() = %v, want no error", err)
	}

	reloaded, err := LoadState(d.State.Path)
	if err != nil {
		t.Fatalf("LoadState() = %v, want no error", err)
	}
	if reloaded.AttachedAt.IsZero() {
		t.Fatal("attaching did not record a claim; a second conversation could not tell somebody is here")
	}

	fresh := DirectorSummary{Claimed: now.Sub(reloaded.AttachedAt) < AttachGrace}
	if !fresh.Claimed {
		t.Error("a claim made now does not read as claimed")
	}

	later := now.Add(AttachGrace + time.Minute)
	stale := DirectorSummary{Claimed: later.Sub(reloaded.AttachedAt) < AttachGrace}
	if stale.Claimed {
		t.Error("a claim older than the grace window still reads as claimed; a crashed conversation would hold it forever")
	}
}
