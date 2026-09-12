// Package store persists which jobs have already been processed, so
// repeated CronJob runs never re-score or re-notify about the same
// posting.
package store

import (
	"context"
	"time"

	"github.com/Erik-Schuetze/go-get-a-job/internal/model"
)

// Record is a persisted row describing the outcome of processing one job.
type Record struct {
	Job         model.Job
	FirstSeenAt time.Time
	AIScore     float64
	AIReason    string
	// NotifiedAt is nil until MarkNotified has been called for this job.
	NotifiedAt *time.Time
}

// Store persists which jobs have already been processed.
type Store interface {
	// Seen reports whether a job with this ID has already been recorded,
	// so the runner can skip re-fetching/re-scoring it.
	Seen(ctx context.Context, jobID string) (bool, error)

	// Save records the outcome of processing a newly-seen job (whether or
	// not it ultimately triggered a notification). Called once per job,
	// after the keyword/AI pipeline has run on it.
	Save(ctx context.Context, rec Record) error

	// MarkNotified stamps a previously-saved job as notified at the given
	// time.
	MarkNotified(ctx context.Context, jobID string, at time.Time) error

	// Close releases any underlying resources (e.g. the database handle).
	Close() error
}
