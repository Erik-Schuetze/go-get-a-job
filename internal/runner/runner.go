// Package runner orchestrates a single end-to-end pass of the go-get-a-job
// pipeline: fetch from every configured source, dedupe against the
// store, apply the keyword pre-filter, score with AI, persist the
// outcome, and notify on matches above threshold.
package runner

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/Erik-Schuetze/go-get-a-job/internal/config"
	"github.com/Erik-Schuetze/go-get-a-job/internal/filter"
	"github.com/Erik-Schuetze/go-get-a-job/internal/model"
	"github.com/Erik-Schuetze/go-get-a-job/internal/notify"
	"github.com/Erik-Schuetze/go-get-a-job/internal/sources"
	"github.com/Erik-Schuetze/go-get-a-job/internal/store"
)

// Runner wires together all the pipeline stages. Every dependency is an
// interface (or a plain value), so tests can substitute fakes for
// sources, the AI scorer, the store, and the notifier.
type Runner struct {
	Sources  []sources.Source
	Filter   config.FilterConfig
	Profile  string
	Scorer   filter.Scorer
	Store    store.Store
	Notifier notify.Notifier

	// Logger defaults to slog.Default() if nil.
	Logger *slog.Logger
	// Now defaults to time.Now if nil; overridable for deterministic tests.
	Now func() time.Time
}

// Summary reports what happened during a single Run.
type Summary struct {
	Fetched       int
	New           int
	Matched       int
	SourceErrors  []error
	ProcessErrors []error
}

// AllSourcesFailed reports whether every configured source failed to
// fetch - a signal that something is systemically wrong (e.g. no network),
// as opposed to one flaky board among several.
func (s Summary) AllSourcesFailed(totalSources int) bool {
	return totalSources > 0 && len(s.SourceErrors) == totalSources
}

// Run executes a single end-to-end pass across all configured sources.
// Per-source and per-job errors are collected and logged rather than
// aborting the whole run: a single flaky source or one bad AI call should
// never prevent notifying about everything else that succeeded. Both
// error categories are also self-healing - a job that failed to fully
// process is never marked as seen, so it's simply retried on the next run.
func (r *Runner) Run(ctx context.Context) Summary {
	var summary Summary
	logger := r.logger()
	now := r.now()

	for _, src := range r.Sources {
		jobs, err := src.Fetch(ctx)
		if err != nil {
			logger.Error("source fetch failed", "source", src.Name(), "error", err)
			summary.SourceErrors = append(summary.SourceErrors, fmt.Errorf("%s: %w", src.Name(), err))
			continue
		}
		summary.Fetched += len(jobs)

		for _, job := range jobs {
			isNew, matched, err := r.processJob(ctx, job, now())
			if err != nil {
				logger.Error("processing job failed", "job", job.ID, "title", job.Title, "error", err)
				summary.ProcessErrors = append(summary.ProcessErrors, fmt.Errorf("%s: %w", job.ID, err))
				continue
			}
			if isNew {
				summary.New++
			}
			if matched {
				summary.Matched++
			}
		}
	}

	logger.Info("run complete",
		"fetched", summary.Fetched,
		"new", summary.New,
		"matched", summary.Matched,
		"source_errors", len(summary.SourceErrors),
		"process_errors", len(summary.ProcessErrors),
	)
	return summary
}

// processJob handles a single fetched job end to end: dedup check,
// keyword pre-filter, AI scoring, persistence, and (if it clears the
// threshold) notification. It reports whether the job was new (i.e. not
// already recorded from a prior run) and whether it resulted in a
// delivered notification.
func (r *Runner) processJob(ctx context.Context, job model.Job, now time.Time) (isNew bool, matched bool, err error) {
	seen, err := r.Store.Seen(ctx, job.ID)
	if err != nil {
		return false, false, fmt.Errorf("checking seen status: %w", err)
	}
	if seen {
		return false, false, nil
	}

	rec := store.Record{Job: job, FirstSeenAt: now}

	if !filter.Passes(job, r.Filter) {
		if err := r.Store.Save(ctx, rec); err != nil {
			return false, false, fmt.Errorf("saving filtered-out job: %w", err)
		}
		return true, false, nil
	}

	score, err := r.Scorer.Score(ctx, job, r.Profile)
	if err != nil {
		// Deliberately not saved: leaving the job unseen means it's
		// retried next run instead of silently dropped after a transient
		// AI-provider hiccup.
		return false, false, fmt.Errorf("scoring job: %w", err)
	}
	rec.AIScore = score.Score
	rec.AIReason = score.Reason

	if err := r.Store.Save(ctx, rec); err != nil {
		return false, false, fmt.Errorf("saving scored job: %w", err)
	}

	if score.Score < r.Filter.MinAIScore {
		return true, false, nil
	}

	if err := r.Notifier.Notify(ctx, job, score.Reason); err != nil {
		return false, false, fmt.Errorf("sending notification: %w", err)
	}
	if err := r.Store.MarkNotified(ctx, job.ID, now); err != nil {
		return false, false, fmt.Errorf("marking notified: %w", err)
	}

	return true, true, nil
}

func (r *Runner) logger() *slog.Logger {
	if r.Logger != nil {
		return r.Logger
	}
	return slog.Default()
}

func (r *Runner) now() func() time.Time {
	if r.Now != nil {
		return r.Now
	}
	return time.Now
}
