package herdr

import "fmt"

// fleetWorkspaceLabel is the window a director with no pane of its own places
// its engagements in. It is matched and created by this label and by nothing
// else, so a person can find an unattended fleet by reading the window bar.
const fleetWorkspaceLabel = "director"

// paneInfoResponse is what pane.get returns: herdr's `pane_info` envelope, with
// the pane nested under "pane" rather than returned at the top level. Decoding
// a level too high is silent — every field stays zero — and would read as a
// pane belonging to no window.
type paneInfoResponse struct {
	Pane paneInfo `json:"pane"`
}

// workspaceInfo is herdr's view of one workspace: a window, in the word a
// person uses for it.
type workspaceInfo struct {
	WorkspaceID string `json:"workspace_id"`
	Label       string `json:"label"`
}

type workspaceListResult struct {
	Workspaces []workspaceInfo `json:"workspaces"`
}

// workspaceCreatedResult is what workspace.create returns: herdr's
// `workspace_created` envelope. It also carries a tab and a root pane herdr
// made along with the window, which are of no use here — the spawn creates its
// own tab immediately afterwards, exactly as it would in a window a person had
// opened.
type workspaceCreatedResult struct {
	Workspace workspaceInfo `json:"workspace"`
}

// anchor resolves the window this director's engagements belong in.
//
// # Why the question is asked at all
//
// tab.create takes an optional workspace_id, and with it left out herdr puts
// the tab in whatever window is focused. That is the wrong thing to decide with
// the person's attention. A director spawns from a script, seconds after
// somebody moved to another space to read something, and a fleet meant to sit
// together ends up scattered across the session — two engagements beside the
// director, the third wherever the cursor happened to be. Nothing records why,
// and nothing puts it back.
//
// # A director sitting in a pane
//
// Its own pane names its window, and that window is the anchor. The pane comes
// from Locate — the same SelfLocator answer `director remove` consults before
// it closes a slot — because there must be one notion of where this director is
// sitting rather than two that can drift apart.
//
// It is asked on every spawn rather than remembered, and that is what makes
// placement stable rather than merely repeatable: a director's own pane cannot
// move out from under it while it is running there, so two spawns a minute
// apart resolve the same window however far the person has wandered between
// them.
//
// # A director with no pane of its own
//
// A headless director, or one hosted by claude-code, has nothing to sit beside.
// It places into a window of director's own, found by its label and created if
// it is not there. That is deterministic with nothing remembered between runs,
// and it gives an unattended fleet one place to go and look — which is the
// point of not following the cursor.
//
// The alternatives were a window per director id, which leaves an empty labelled
// window behind for every director that ever ran, and reusing whichever window
// the previous engagements went to, which is a memory this adapter does not have
// and would have to be handed. Neither buys anything over one named window.
//
// # Focus is never touched
//
// Everything this creates passes focus: false. Putting an agent in the right
// window must not drag the person to it; they are working somewhere, and a
// spawn is not a reason to move them.
func (a *Adapter) anchor() (string, error) {
	if paneID, inside := a.Locate(); inside && paneID != "" {
		return a.workspaceOfPane(paneID)
	}
	return a.fleetWorkspace()
}

// workspaceOfPane asks herdr which window a pane is in.
//
// A pane herdr no longer has fails the spawn rather than falling back. Any
// fallback here is a window with no relation to this director, chosen because a
// lookup failed — the scattering this exists to remove, now unexplained as well
// as wrong. The refusal names the pane, because the only way to reach this state
// is a director outliving the pane its environment says it occupies, and the
// pane id is what makes that recognisable.
func (a *Adapter) workspaceOfPane(paneID string) (string, error) {
	var found paneInfoResponse
	if err := a.rpc().call("pane.get", map[string]any{"pane_id": paneID}, &found); err != nil {
		if notFound(err) {
			return "", fmt.Errorf(
				"this director is running in herdr pane %s and herdr no longer has that pane, so there is no window to place the engagement in. "+
					"The pane was closed and the director outlived it: re-attach from a live pane, or pass --harness to place this work somewhere else",
				paneID)
		}
		return "", fmt.Errorf("asking herdr which window pane %s is in: %w", paneID, err)
	}
	if found.Pane.WorkspaceID == "" {
		return "", fmt.Errorf("herdr named no window for pane %s, so where this director is sitting cannot be established", paneID)
	}
	return found.Pane.WorkspaceID, nil
}

// fleetWorkspace finds director's own window, creating it if it is not there.
//
// Matched on the label rather than on anything stored, so it survives a director
// restart, a fresh state file, and a herdr session that has renumbered its
// windows. A person who renames the window has moved it out of director's reach
// and gets a new one, which is the honest reading of a rename: the label is the
// only handle either side has.
func (a *Adapter) fleetWorkspace() (string, error) {
	var listed workspaceListResult
	if err := a.rpc().call("workspace.list", map[string]any{}, &listed); err != nil {
		return "", fmt.Errorf("looking for the %q window to place this engagement in: %w", fleetWorkspaceLabel, err)
	}
	for _, workspace := range listed.Workspaces {
		if workspace.Label == fleetWorkspaceLabel && workspace.WorkspaceID != "" {
			return workspace.WorkspaceID, nil
		}
	}

	var created workspaceCreatedResult
	err := a.rpc().call("workspace.create", map[string]any{
		"label": fleetWorkspaceLabel,
		"focus": false,
	}, &created)
	if err != nil {
		return "", fmt.Errorf("creating the %q window to place this engagement in: %w", fleetWorkspaceLabel, err)
	}
	if created.Workspace.WorkspaceID == "" {
		return "", fmt.Errorf("herdr made the %q window but named no id for it, so nothing can be placed in it", fleetWorkspaceLabel)
	}
	return created.Workspace.WorkspaceID, nil
}
