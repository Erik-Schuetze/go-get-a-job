// Package notify delivers job-match notifications (and run-failure
// alerts) through a pluggable Notifier interface. Currently only ntfy is
// implemented, but the interface is designed so more channels (Telegram,
// email, Slack, ...) can be added later without touching the runner.
package notify

import (
	"context"

	"github.com/Erik-Schuetze/go-get-a-job/internal/model"
)

// Notifier delivers notifications about matched jobs and run failures.
type Notifier interface {
	// Notify delivers a single job match, including the AI scorer's
	// human-readable reason for the match.
	Notify(ctx context.Context, job model.Job, reason string) error

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
