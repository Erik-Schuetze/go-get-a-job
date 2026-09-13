// Package notify delivers job-match notifications (and run-failure
// alerts) through a pluggable Notifier interface. Currently only ntfy is
// implemented, but the interface is designed so more channels (Telegram,
// email, Slack, ...) can be added later without touching the runner.
package notify

import (
	"context"

	"github.com/Erik-Schuetze/go-get-a-job/internal/model"
)

// Match is everything one delivered match carries: the posting itself, the
// scorer's verdict on it, and the terms the scorer said drove that verdict.
//
// It is a struct rather than a longer argument list because this is the third
// piece of information the notifier has needed and the fourth would otherwise
// be another parameter on every implementation and every test fake.
type Match struct {
	Job model.Job

	// Score is the AI scorer's 0-1 judgment, and is what selects the emoji
	// and priority a notification is presented with. It is deliberately the
	// only input to that decision: letting a notifier re-derive importance
	// from anything else would put two definitions of "how good is this" in
	// the codebase.
	Score float64

	// Reason is the scorer's human-readable explanation. May be empty, in
	// which case implementations fall back to a generic message.
	Reason string

	// Signals lists the specific terms that drove the score, for display
	// alongside the reason. May be empty.
	Signals []string

	// LocationRule is the configured location entry that selected this
	// posting, and is empty when no entry did - an ambiguous location handed
	// to the scorer, an unmatched one the mode passed through, or location
	// filtering switched off entirely.
	//
	// It exists because a posting may be listed against several places and
	// the board's own order is no guide to which one applies. Canonical
	// leads "Home Based - Americas; Home based - EMEA" with the region that
	// rules the posting out for a reader in Germany, so showing the first
	// segment hides the one that makes it a match - a false negative
	// manufactured in the one place the operator cannot correct it from
	// context.
	LocationRule string
}

// Notifier delivers notifications about matched jobs and run failures.
type Notifier interface {
	// Notify delivers a single job match.
	Notify(ctx context.Context, match Match) error

	// NotifyFailure delivers a best-effort alert that a run failed
	// unexpectedly, so a broken watcher doesn't silently go quiet forever.
	NotifyFailure(ctx context.Context, runErr error) error

	// NotifyWarning delivers a best-effort alert about something that needs
	// the operator's attention even though the run itself succeeded - most
	// importantly a source that has stopped returning postings.
	//
	// It is separate from NotifyFailure because the two call for different
	// reactions: a failed run is loud and self-evident, while a source that
	// returns zero postings looks like a quiet week. A warning's whole job
	// is to make that distinction visible, which is why an implementation
	// must mark it as a warning in the notification title - titles are what
	// a phone shows first, and a warning that reads like a job match is a
	// warning the operator will open expecting to apply to something.
	NotifyWarning(ctx context.Context, title, body string) error
}
