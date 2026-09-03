package harness

import "context"

// Disposer is the optional ability to reclaim the slot a conversation occupies
// in its harness's own interface — a herdr pane, a window, a tab.
//
// A slot is not the agent's work and it is not the conversation. It is the seat
// the harness allocated when director asked for a conversation, and for some
// harnesses nothing ever gives it back: the agent finishes, the transcript
// stays wherever it was written, and a dead pane sits in somebody's session for
// good. Reclaiming it is therefore director's to ask for, because director is
// what caused it to be allocated.
//
// It is deliberately not part of Stop. Stopping ends a conversation and must
// leave it resumable and readable; disposing gives back the seat, and doing
// both at once would take away the thing every other command needs to still be
// there. An adapter whose stop happens to close the slot as well — herdr's
// StopEnd does — still implements this, because the two are asked for at
// different moments and only one of them is about the record being forgotten.
//
// Declaring it is a claim about the harness, not about one conversation. A
// harness with no visible slot to give back — Claude Code has a transcript and
// nothing to close — must not implement this, and every command then behaves
// exactly as it did before disposal existed: no error, no warning, nothing
// reported.
type Disposer interface {
	// Disposes reports whether this harness has a slot to reclaim at all.
	//
	// It is a separate question from Dispose so that a caller can find out
	// without acting. Disposal is destructive and irreversible, and the two
	// callers that most need the answer are the one that has already decided
	// NOT to dispose and the one reporting what it did — neither of which may
	// discover the answer by closing something.
	Disposes() bool

	// Dispose reclaims the slot named by the request.
	//
	// A slot that has already gone is a success. What was asked for is that it
	// no longer be held, and it is not.
	//
	// It says nothing about whether the conversation was finished. Only the
	// caller knows that, because only the caller knows what it just did to the
	// engagement, and an adapter that refused on its own guess would be
	// second-guessing a decision made with more information than it has.
	Dispose(ctx context.Context, req DisposeRequest) error
}

// DisposeRequest names the conversation whose slot is being reclaimed.
type DisposeRequest struct {
	Ref string
}

// DisposerFor returns an adapter's disposer, and false when the adapter does
// not declare one.
//
// Silence is the answer for every adapter written before this existed, and it
// has to mean "carry on exactly as you did" rather than "fail" or "warn": a
// harness with nothing to reclaim has done nothing wrong, and a warning on
// every removal would train people to ignore the one that matters.
func DisposerFor(adapter Adapter) (Disposer, bool) {
	disposer, ok := adapter.(Disposer)
	if !ok || !disposer.Disposes() {
		return nil, false
	}
	return disposer, true
}
