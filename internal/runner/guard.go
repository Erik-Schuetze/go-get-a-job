package runner

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/Erik-Schuetze/go-get-a-job/internal/sanitize"
	"github.com/Erik-Schuetze/go-get-a-job/internal/store"
)

// sourceFetch is one successful fetch of one source during a run: what the
// health guard needs to know about it, and nothing else.
type sourceFetch struct {
	label string
	count int
}

// SilentSource is a source that answered successfully but returned no
// postings for long enough that the absence is more likely to be broken than
// real.
type SilentSource struct {
	// Label is the source's unique label (see sources.Source.Label).
	Label string
	// ConsecutiveRuns is how many successful runs in a row returned zero
	// postings, counting this one.
	ConsecutiveRuns int
	// LastNonEmptyAt is when this source last returned at least one posting,
	// or the zero time if it never has during the recorded history.
	LastNonEmptyAt time.Time
	// LastJobCount is what the most recent fetch returned (always zero for
	// a silent source, kept so the message can say so explicitly).
	LastJobCount int
}

// maxReportedSilentSources bounds how many sources one warning lists.
const maxReportedSilentSources = 20

// recordSourceHealth writes this run's per-source fetch results to the store
// and returns the sources that should be warned about.
//
// Two independent failure modes are covered, which is why this is not simply
// "did the fetch error?":
//
//  1. A board that no longer exists or has been renamed. Most ATS APIs answer
//     a nonexistent board with a real HTTP error, and that shows up as a
//     source error - already visible. But SmartRecruiters answers *any* slug
//     with 200 and an empty list, so a typo there is perfectly silent.
//  2. A board that still answers 200 with a well-formed but stale or
//     truncated payload. No per-request check can see this: the request
//     succeeded, the JSON parsed, the list was empty.
//
// Both look identical from the outside - a company that has simply stopped
// hiring looks exactly the same - so the signal is "this source has returned
// nothing for N consecutive runs", not any single response.
func (r *Runner) recordSourceHealth(ctx context.Context, logger *slog.Logger, fetches []sourceFetch, at time.Time) []SilentSource {
	if r.Guard.DeadSourceRuns <= 0 {
		// A config loaded through config.Load never reaches here with a
		// zero: applyDefaults() has already resolved every unset guard value
		// to its default. Reaching it means a Runner was built by hand
		// without a guard (as the tests do), so there is nothing to record.
		return nil
	}

	var silent []SilentSource
	for _, f := range fetches {
		health, err := r.Store.RecordSourceFetch(ctx, f.label, f.count, at)
		if err != nil {
			// Not fatal: failing to remember this run's health must never
			// cost the operator the matches this run actually found.
			logger.Error("recording source health failed",
				"source", sanitize.SingleLine(f.label, 200),
				"error", sanitize.SingleLine(err.Error(), 500),
			)
			continue
		}
		if shouldWarnSilent(health, r.Guard.DeadSourceRuns) {
			silent = append(silent, SilentSource{
				Label:           health.Source,
				ConsecutiveRuns: health.ConsecutiveZeroRuns,
				LastNonEmptyAt:  health.LastNonEmptyAt,
				LastJobCount:    health.LastJobCount,
			})
		}
	}
	return silent
}

// shouldWarnSilent decides whether a source's recorded health warrants a
// warning on this run.
//
// It fires on the run that crosses the threshold and then every
// deadSourceRuns runs afterwards, rather than only once. A reminder that never
// repeats is a reminder that gets swiped away and forgotten, and the condition
// it reports - a company you wanted to hear from going quiet - is exactly the
// kind that stays true for months. The alternative of warning on every run
// after the threshold would train the operator to ignore the alert, which is
// worse than not sending it.
func shouldWarnSilent(health store.SourceHealth, threshold int) bool {
	if threshold <= 0 || health.ConsecutiveZeroRuns < threshold {
		return false
	}
	return (health.ConsecutiveZeroRuns-threshold)%threshold == 0
}

// notifySilentSources warns about sources that have gone quiet. It is
// best-effort: the run itself succeeded, so a failed warning notification is
// logged, not returned.
func (r *Runner) notifySilentSources(ctx context.Context, logger *slog.Logger, silent []SilentSource) {
	if len(silent) == 0 {
		return
	}

	logger.Warn("sources returned no postings for several consecutive runs",
		"count", len(silent),
		"sources", silentLabels(silent, maxReportedSilentSources),
	)

	if r.Notifier == nil {
		return
	}

	title := fmt.Sprintf("%d source(s) with no postings", len(silent))
	if err := r.Notifier.NotifyWarning(ctx, title, silentSourcesBody(silent, r.Guard.DeadSourceRuns)); err != nil {
		logger.Error("sending no-postings warning failed",
			"error", sanitize.SingleLine(err.Error(), 500),
		)
	}
}

// silentSourcesBody builds the human-readable part of the warning. It is
// written for the operator's phone: which boards, for how many runs, and the
// two or three things worth checking first.
func silentSourcesBody(silent []SilentSource, threshold int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d source(s) returned no postings for %d consecutive successful runs.\n\n", len(silent), threshold)

	for _, s := range silent {
		label := sanitize.SingleLine(s.Label, 120)
		if s.LastNonEmptyAt.IsZero() {
			fmt.Fprintf(&b, "- %s (%d runs, nothing recorded before)\n", label, s.ConsecutiveRuns)
			continue
		}
		fmt.Fprintf(&b, "- %s (%d runs, last had postings %s)\n",
			label, s.ConsecutiveRuns, s.LastNonEmptyAt.UTC().Format("2006-01-02"))
	}

	hidden := len(silent) - maxReportedSilentSources
	if hidden > 0 {
		fmt.Fprintf(&b, "- ...and %d more\n", hidden)
	}

	b.WriteString("\nWorth checking: did the company rename its board slug or move ATS, and does the board still load in a browser?")
	return b.String()
}

// silentLabels returns up to max labels, for a structured log line where a
// full list would be noise.
func silentLabels(silent []SilentSource, max int) []string {
	labels := make([]string, 0, len(silent))
	for i, s := range silent {
		if i >= max {
			break
		}
		labels = append(labels, sanitize.SingleLine(s.Label, 120))
	}
	return labels
}
