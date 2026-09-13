package runner

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Erik-Schuetze/go-get-a-job/internal/config"
	"github.com/Erik-Schuetze/go-get-a-job/internal/filter"
	"github.com/Erik-Schuetze/go-get-a-job/internal/model"
	"github.com/Erik-Schuetze/go-get-a-job/internal/sources"
	"github.com/Erik-Schuetze/go-get-a-job/internal/store"
)

// guardTestRunner builds a runner whose sources produce no postings at all,
// with the guard enabled at the given threshold.
func guardTestRunner(st *fakeStore, notifier *fakeNotifier, threshold int, srcs ...sources.Source) *Runner {
	return &Runner{
		Sources:  srcs,
		Filter:   baseFilterConfig(),
		Profile:  "platform engineer",
		Scorer:   newFakeScorer(),
		Store:    st,
		Notifier: notifier,
		Guard:    config.GuardConfig{DeadSourceRuns: threshold},
		Now:      func() time.Time { return time.Date(2026, 3, 1, 6, 0, 0, 0, time.UTC) },
	}
}

func emptySource(label string) sources.Source {
	return &fakeSource{name: "fake", label: label}
}

// TestRunner_Guard_WarnsOnCrossingAndRepeats checks the alert cadence: silent
// until the threshold, one warning on the run that crosses it, then a reminder
// every threshold runs after that. Without the repeat, a warning that arrives
// once is a warning that gets swiped away and never acted on; with a warning
// every single run, the alert becomes wallpaper.
func TestRunner_Guard_WarnsOnCrossingAndRepeats(t *testing.T) {
	st := newFakeStore()
	notifier := newFakeNotifier()
	r := guardTestRunner(st, notifier, 3, emptySource("greenhouse/dead"))

	for run := 1; run <= 9; run++ {
		r.Run(context.Background())

		// Warnings accumulate: one on run 3, another on 6, another on 9.
		wantTotal := run / 3
		if len(notifier.warnings) != wantTotal {
			t.Fatalf("after %d silent run(s): got %d warning(s) in total, want %d", run, len(notifier.warnings), wantTotal)
		}
	}

	if len(notifier.warnings) != 3 {
		t.Fatalf("expected warnings on runs 3, 6 and 9, got %d", len(notifier.warnings))
	}
	if !strings.Contains(notifier.warnings[0].Title, "no postings") {
		t.Errorf("warning title %q should say what is wrong", notifier.warnings[0].Title)
	}
	if !strings.Contains(notifier.warnings[0].Body, "greenhouse/dead") {
		t.Errorf("warning body %q must name the silent source", notifier.warnings[0].Body)
	}
}

// TestRunner_Guard_ResetsOnPostingsAndIgnoresFailures is the pair of ways the
// guard could cry wolf. A board that comes back to life must be forgotten, and
// a board that *errors* is not a board that returned nothing - folding the two
// together would let a flaky network day trigger a dead-source alert.
func TestRunner_Guard_ResetsOnPostingsAndIgnoresFailures(t *testing.T) {
	st := newFakeStore()
	notifier := newFakeNotifier()

	empty := &fakeSource{name: "fake", label: "greenhouse/quiet"}
	broken := &fakeSource{name: "fake", label: "greenhouse/broken", err: errors.New("connection reset")}
	r := guardTestRunner(st, notifier, 2, empty, broken)

	r.Run(context.Background()) // quiet: 1
	r.Run(context.Background()) // quiet: 2 -> crossing
	if got := len(notifier.warnings); got != 1 {
		t.Fatalf("expected 1 warning at the threshold, got %d", got)
	}
	if strings.Contains(notifier.warnings[0].Body, "greenhouse/broken") {
		t.Error("a source that failed to fetch must never be reported as silent")
	}
	if _, ok, _ := st.SourceHealth(context.Background(), "greenhouse/broken"); ok {
		t.Error("a failed fetch must not be recorded as a fetch at all")
	}

	// The quiet board starts returning postings again.
	empty.jobs = []model.Job{{ID: "job-1", Title: "Platform Engineer"}}
	r.Run(context.Background())

	h, ok, err := st.SourceHealth(context.Background(), "greenhouse/quiet")
	if err != nil || !ok {
		t.Fatalf("SourceHealth = (ok=%v, err=%v), want a recorded entry", ok, err)
	}
	if h.ConsecutiveZeroRuns != 0 {
		t.Errorf("ConsecutiveZeroRuns = %d after postings reappeared, want 0", h.ConsecutiveZeroRuns)
	}
	if len(notifier.warnings) != 1 {
		t.Errorf("a recovered source must not add warnings, got %d", len(notifier.warnings))
	}
}

// TestRunner_Guard_HealthWriteFailureIsNotFatal pins the priority: the guard
// is an extra, and it must never cost the operator the matches this run found.
func TestRunner_Guard_HealthWriteFailureIsNotFatal(t *testing.T) {
	st := newFakeStore()
	st.recordErr = errors.New("database is locked")
	notifier := newFakeNotifier()

	scorer := newFakeScorer()
	scorer.scores["job-1"] = filter.AIScore{Score: 0.95, Reason: "great fit"}

	r := guardTestRunner(st, notifier, 3, &fakeSource{
		name:  "fake",
		label: "greenhouse/acme",
		jobs:  []model.Job{{ID: "job-1", Title: "Platform Engineer"}},
	})
	r.Scorer = scorer

	summary := r.Run(context.Background())

	if summary.Matched != 1 {
		t.Errorf("expected the match to still be notified, got Matched=%d", summary.Matched)
	}
	if len(notifier.notified) != 1 {
		t.Errorf("expected 1 notification despite the health-write failure, got %d", len(notifier.notified))
	}
	if len(summary.SilentSources) != 0 {
		t.Errorf("expected no silent sources when health could not be read, got %+v", summary.SilentSources)
	}
}

// TestRunner_Guard_DisabledWhenThresholdUnset documents the zero value: a
// hand-built Runner (as tests use) has no guard configured and must not send
// warnings, while a config-loaded one always has a threshold.
func TestRunner_Guard_DisabledWhenThresholdUnset(t *testing.T) {
	st := newFakeStore()
	notifier := newFakeNotifier()
	r := guardTestRunner(st, notifier, 0, emptySource("greenhouse/dead"))

	for i := 0; i < 5; i++ {
		r.Run(context.Background())
	}
	if len(notifier.warnings) != 0 {
		t.Errorf("expected no warnings with the guard unconfigured, got %d", len(notifier.warnings))
	}
	if _, ok, _ := st.SourceHealth(context.Background(), "greenhouse/dead"); ok {
		t.Error("expected no health to be recorded when the guard is off")
	}
}

// TestShouldWarnSilent covers the cadence rule directly, including the
// boundaries that a run-based integration test would only reach by accident.
func TestShouldWarnSilent(t *testing.T) {
	tests := []struct {
		name      string
		zeroRuns  int
		threshold int
		want      bool
	}{
		{"before the threshold", 2, 3, false},
		{"exactly at the threshold", 3, 3, true},
		{"between the two reminders", 4, 3, false},
		{"second reminder", 6, 3, true},
		{"third reminder", 9, 3, true},
		{"threshold of one warns every run", 4, 1, true},
		{"a higher threshold also repeats", 28, 14, true},
		{"a higher threshold still waits", 27, 14, false},
		{"zero threshold means no guard", 100, 0, false},
		{"negative threshold means no guard", 100, -1, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := shouldWarnSilent(store.SourceHealth{ConsecutiveZeroRuns: tc.zeroRuns}, tc.threshold)
			if got != tc.want {
				t.Errorf("shouldWarnSilent(zeroRuns=%d, threshold=%d) = %v, want %v",
					tc.zeroRuns, tc.threshold, got, tc.want)
			}
		})
	}
}

// TestSilentSourcesBody_NamesTheSourceAndSaysWhatToCheck covers the branch the
// state-machine tests above never reach: with a real LastNonEmptyAt on record,
// the body has to say when the source last had postings, because "this board
// has been empty for two weeks" and "this board has never once returned
// anything" call for different investigations.
func TestSilentSourcesBody_NamesTheSourceAndSaysWhatToCheck(t *testing.T) {
	lastSeen := time.Date(2026, 8, 30, 6, 0, 0, 0, time.UTC)
	body := silentSourcesBody([]SilentSource{
		{Label: "greenhouse/grafanalabs", ConsecutiveRuns: 14, LastNonEmptyAt: lastSeen, LastJobCount: 0},
		{Label: "workday/acme/AcmeCareers", ConsecutiveRuns: 30},
	}, 14)

	for _, want := range []string{
		"greenhouse/grafanalabs",
		"2026-08-30",
		"nothing recorded before",
		"14 consecutive",
		"rename its board slug",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body should contain %q, got:\n%s", want, body)
		}
	}
}
