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

// SourceHealth is what the store remembers about one configured source's
// recent fetch history, so the runner can tell a source that is briefly empty
// from one that has quietly died.
type SourceHealth struct {
	// Source is the source's stable label (see sources.Source.Label).
	Source string

	// ConsecutiveZeroRuns is how many runs in a row this source has
	// returned zero postings, counting the fetch just recorded.
	ConsecutiveZeroRuns int

	// PreviousZeroRuns is the streak before the fetch just recorded. It
	// exists so a caller can tell a crossing into the alert zone from a
	// continuing streak without a second query - which matters because the
	// difference between those two is whether an alert should be sent.
	PreviousZeroRuns int

	// LastJobCount is how many postings the most recent successful fetch
	// returned.
	LastJobCount int

	// LastFetchAt is when the most recent successful fetch happened.
	LastFetchAt time.Time

	// LastNonEmptyAt is when this source last returned at least one posting,
	// for the whole recorded history. Zero if it never has. This is what
	// makes a warning actionable: "silent for 3 runs" is ambiguous, whereas
	// "silent since 2026-03-01" is not.
	LastNonEmptyAt time.Time
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

	// RecordSourceFetch records the result of one successful fetch of one
	// source and returns that source's health afterwards.
	//
	// Only successful fetches are recorded: a source that errored did not
	// "return zero postings", it failed, and that is already reported as an
	// error. Counting a failure here would mean one flaky network run
	// pushes a perfectly healthy board towards a false dead-source alert.
	RecordSourceFetch(ctx context.Context, source string, jobCount int, at time.Time) (SourceHealth, error)

	// SourceHealth returns the recorded health for one source, or ok=false
	// if it has never been fetched successfully.
	SourceHealth(ctx context.Context, source string) (SourceHealth, bool, error)

	// Close releases any underlying resources (e.g. the database handle).
	Close() error
}
