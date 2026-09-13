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
	"github.com/Erik-Schuetze/go-get-a-job/internal/sanitize"
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
	// Guard tunes the outbound-request limits and the dead-source detector.
	// The zero value disables the dead-source detector (see GuardConfig);
	// request limits are applied by sources.ConfigureClient, not here.
	Guard config.GuardConfig

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

	// FilteredByLocation counts postings dropped by the location
	// pre-filter, which under the whitelist model means an unmatched
	// location only: one that names a place the accept list does not cover.
	// It is reported separately from the keyword pre-filter because a
	// too-narrow accept list is invisible otherwise: the run simply looks
	// like a quiet day.
	FilteredByLocation int

	// LocationRejections names the first few location rejections, for an
	// end-of-run summary. Bounded so a misconfigured accept list cannot turn
	// the log into a wall of text.
	LocationRejections []LocationRejection

	// VetoedByLocation counts postings the AI scorer explicitly ruled out on
	// location. They are saved but not notified. Reported for the same
	// reason FilteredByLocation is: without a count, a run that vetoed
	// everything looks exactly like a run that found nothing.
	VetoedByLocation int

	// SilentSources lists sources that crossed the dead-source threshold on
	// this run (or are due a repeat reminder). A run with matches *and*
	// entries here is normal and expected: one board going quiet says
	// nothing about the others.
	SilentSources []SilentSource
}

// LocationRejection describes one posting dropped by the location
// pre-filter, sanitized for logging.
type LocationRejection struct {
	Company  string
	Title    string
	Location string
	Rule     string
	Kind     string
}

// maxReportedLocationRejections bounds Summary.LocationRejections.
const maxReportedLocationRejections = 10

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
	runAt := now()

	// One entry per source that fetched successfully. Sources that errored
	// are deliberately absent: a failed fetch is not a source that returned
	// zero postings, and conflating the two would let a flaky network push
	// a healthy board towards a false "gone quiet" alert.
	fetches := make([]sourceFetch, 0, len(r.Sources))

	for _, src := range r.Sources {
		jobs, err := src.Fetch(ctx)
		if err != nil {
			logger.Error("source fetch failed", "source", sanitize.SingleLine(src.Name(), 120), "error", sanitize.SingleLine(err.Error(), 500))
			summary.SourceErrors = append(summary.SourceErrors, fmt.Errorf("%s: %w", src.Name(), err))
			continue
		}
		summary.Fetched += len(jobs)
		fetches = append(fetches, sourceFetch{label: src.Label(), count: len(jobs)})

		for _, job := range jobs {
			res, err := r.processJob(ctx, job, runAt)
			if err != nil {
				// The ID and title come from the ATS response. Structured
				// logs are the one place where a newline from a hostile
				// posting could be mistaken for a new record, so both are
				// collapsed to a single line before they're written.
				logger.Error("processing job failed",
					"job", sanitize.SingleLine(job.ID, 120),
					"title", sanitize.SingleLine(job.Title, 200),
					"error", sanitize.SingleLine(err.Error(), 500),
				)
				summary.ProcessErrors = append(summary.ProcessErrors, fmt.Errorf("%s: %w", job.ID, err))
				continue
			}
			if res.isNew {
				summary.New++
			}
			if res.matched {
				summary.Matched++
			}
			if res.locationVetoed {
				summary.VetoedByLocation++
			}
			if res.locationRejected {
				recordLocationRejection(logger, &summary, job, res.location)
			}
		}
	}

	// Recorded before the summary line so the log reports the health guard's
	// verdict in the same record as the counts it is easy to compare it
	// against.
	summary.SilentSources = r.recordSourceHealth(ctx, logger, fetches, runAt)

	logger.Info("run complete",
		"fetched", summary.Fetched,
		"new", summary.New,
		"matched", summary.Matched,
		"filtered_by_location", summary.FilteredByLocation,
		"vetoed_by_location", summary.VetoedByLocation,
		"source_errors", len(summary.SourceErrors),
		"process_errors", len(summary.ProcessErrors),
		"silent_sources", len(summary.SilentSources),
	)

	if len(summary.LocationRejections) > 0 {
		logger.Info("location-filtered postings",
			"count", summary.FilteredByLocation,
			"showing_up_to", maxReportedLocationRejections,
			"sample", summary.LocationRejections,
		)
	}

	r.notifySilentSources(ctx, logger, summary.SilentSources)

	return summary
}

// recordLocationRejection logs one location rejection at debug level and
// keeps a bounded sample for the end-of-run summary. The rule and kind are
// included because the useful question - "why did this Germany-based search
// drop a posting?" - needs the entry that matched, not just the fact of a
// rejection.
func recordLocationRejection(logger *slog.Logger, summary *Summary, job model.Job, decision filter.LocationDecision) {
	summary.FilteredByLocation++

	rejection := LocationRejection{
		Company:  sanitize.SingleLine(job.Company, 200),
		Title:    sanitize.SingleLine(job.Title, 200),
		Location: sanitize.SingleLine(job.Location, 200),
		Rule:     sanitize.SingleLine(decision.Rule, 200),
		Kind:     decision.Kind,
	}
	if len(summary.LocationRejections) < maxReportedLocationRejections {
		summary.LocationRejections = append(summary.LocationRejections, rejection)
	}

	logger.Debug("location rejected",
		"company", rejection.Company,
		"title", rejection.Title,
		"location", rejection.Location,
		"rule", rejection.Rule,
		"kind", rejection.Kind,
	)
}

// processResult is what processJob reports back to Run.
type processResult struct {
	isNew bool
	// matched reports whether the job resulted in a delivered notification.
	matched bool
	// locationVetoed reports whether the AI scorer explicitly ruled the job
	// out on location. The job is still saved (so it is never re-scored and
	// re-billed) but deliberately not notified.
	locationVetoed bool
	// locationRejected reports whether the location pre-filter - not the
	// keyword pre-filter - dropped the job.
	locationRejected bool
	// location is the location decision, meaningful when locationRejected
	// is set.
	location filter.LocationDecision
}

// processJob handles a single fetched job end to end: dedup check,
// keyword pre-filter, AI scoring, persistence, and (if it clears the
// threshold) notification. It reports whether the job was new (i.e. not
// already recorded from a prior run) and whether it resulted in a
// delivered notification.
func (r *Runner) processJob(ctx context.Context, job model.Job, now time.Time) (processResult, error) {
	seen, err := r.Store.Seen(ctx, job.ID)
	if err != nil {
		return processResult{}, fmt.Errorf("checking seen status: %w", err)
	}
	if seen {
		return processResult{}, nil
	}

	rec := store.Record{Job: job, FirstSeenAt: now}

	// The location check is re-run separately when the combined pre-filter
	// rejects, so a rejection can be attributed to the location rules
	// rather than to the keyword list. Without that attribution, a
	// too-narrow accept list is invisible in the logs.
	if !filter.Passes(job, r.Filter) {
		if err := r.Store.Save(ctx, rec); err != nil {
			return processResult{}, fmt.Errorf("saving filtered-out job: %w", err)
		}

		res := processResult{isNew: true}
		if loc := filter.MatchLocation(job, r.Filter.Locations); !loc.Passed {
			res.locationRejected = true
			res.location = loc
		}
		return res, nil
	}

	score, err := r.Scorer.Score(ctx, job, r.Profile)
	if err != nil {
		// Deliberately not saved: leaving the job unseen means it's
		// retried next run instead of silently dropped after a transient
		// AI-provider hiccup.
		return processResult{}, fmt.Errorf("scoring job: %w", err)
	}
	rec.AIScore = score.Score
	rec.AIReason = score.Reason

	if err := r.Store.Save(ctx, rec); err != nil {
		return processResult{}, fmt.Errorf("saving scored job: %w", err)
	}

	if score.VetoesLocation() {
		// Saved above, so this posting is never scored again; simply not
		// announced. Counted, not discarded silently - an unannounced drop
		// with no trace in the run summary is indistinguishable from the
		// watcher having missed the posting, which is the one failure this
		// program exists to prevent.
		return processResult{isNew: true, locationVetoed: true}, nil
	}

	if score.Score < r.Filter.MinAIScore {
		return processResult{isNew: true}, nil
	}

	if err := r.Notifier.Notify(ctx, notify.Match{
		Job:     job,
		Score:   score.Score,
		Reason:  score.Reason,
		Signals: score.Signals,
	}); err != nil {
		return processResult{}, fmt.Errorf("sending notification: %w", err)
	}
	if err := r.Store.MarkNotified(ctx, job.ID, now); err != nil {
		return processResult{}, fmt.Errorf("marking notified: %w", err)
	}

	return processResult{isNew: true, matched: true}, nil
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
