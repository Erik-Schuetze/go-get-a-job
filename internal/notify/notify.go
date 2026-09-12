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
}
