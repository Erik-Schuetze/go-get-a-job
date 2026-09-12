package runner

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Erik-Schuetze/go-get-a-job/internal/config"
	"github.com/Erik-Schuetze/go-get-a-job/internal/filter"
	"github.com/Erik-Schuetze/go-get-a-job/internal/model"
	"github.com/Erik-Schuetze/go-get-a-job/internal/sources"
	"github.com/Erik-Schuetze/go-get-a-job/internal/store"
)

// --- fakes -----------------------------------------------------------

type fakeSource struct {
	name string
	jobs []model.Job
	err  error
}

func (f *fakeSource) Name() string { return f.name }
func (f *fakeSource) Fetch(_ context.Context) ([]model.Job, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.jobs, nil
}

type fakeScorer struct {
	mu     sync.Mutex
	scores map[string]filter.AIScore
	errIDs map[string]error
	calls  []string
}

func newFakeScorer() *fakeScorer {
	return &fakeScorer{scores: map[string]filter.AIScore{}, errIDs: map[string]error{}}
}

func (f *fakeScorer) Score(_ context.Context, job model.Job, _ string) (filter.AIScore, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, job.ID)
	if err, ok := f.errIDs[job.ID]; ok {
		return filter.AIScore{}, err
	}
	if s, ok := f.scores[job.ID]; ok {
		return s, nil
	}
	return filter.AIScore{Score: 0.5, Reason: "default"}, nil
}

type fakeStore struct {
	mu      sync.Mutex
	records map[string]store.Record
}

func newFakeStore() *fakeStore {
	return &fakeStore{records: map[string]store.Record{}}
}

func (f *fakeStore) Seen(_ context.Context, jobID string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.records[jobID]
	return ok, nil
}

func (f *fakeStore) Save(_ context.Context, rec store.Record) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.records[rec.Job.ID] = rec
	return nil
}

func (f *fakeStore) MarkNotified(_ context.Context, jobID string, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	rec, ok := f.records[jobID]
	if !ok {
		return fmt.Errorf("no such job %q", jobID)
	}
	t := at
	rec.NotifiedAt = &t
	f.records[jobID] = rec
	return nil
}

func (f *fakeStore) Close() error { return nil }

type fakeNotifier struct {
	mu       sync.Mutex
	notified []model.Job
	errIDs   map[string]error
	failed   int
}

func newFakeNotifier() *fakeNotifier {
	return &fakeNotifier{errIDs: map[string]error{}}
}

func (f *fakeNotifier) Notify(_ context.Context, job model.Job, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err, ok := f.errIDs[job.ID]; ok {
		return err
	}
	f.notified = append(f.notified, job)
	return nil
}

func (f *fakeNotifier) NotifyFailure(_ context.Context, _ error) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failed++
	return nil
}

// --- tests -------------------------------------------------------------

func baseFilterConfig() config.FilterConfig {
	return config.FilterConfig{
		Keywords:   []string{"platform", "devops"},
		MinAIScore: 0.7,
	}
}

func TestRunner_Run_NotifiesOnlyMatchesAboveThreshold(t *testing.T) {
	high := model.Job{ID: "job-high", Title: "Platform Engineer", Company: "Acme"}
	low := model.Job{ID: "job-low", Title: "DevOps Engineer", Company: "Acme"}

	scorer := newFakeScorer()
	scorer.scores["job-high"] = filter.AIScore{Score: 0.95, Reason: "great fit"}
	scorer.scores["job-low"] = filter.AIScore{Score: 0.3, Reason: "not really"}

	st := newFakeStore()
	notifier := newFakeNotifier()

	r := &Runner{
		Sources:  []sources.Source{&fakeSource{name: "fake", jobs: []model.Job{high, low}}},
		Filter:   baseFilterConfig(),
		Profile:  "platform engineer looking for IaC roles",
		Scorer:   scorer,
		Store:    st,
		Notifier: notifier,
		Now:      func() time.Time { return time.Unix(1000, 0) },
	}

	summary := r.Run(context.Background())

	if summary.Fetched != 2 {
		t.Errorf("expected Fetched=2, got %d", summary.Fetched)
	}
	if summary.New != 2 {
		t.Errorf("expected New=2, got %d", summary.New)
	}
	if summary.Matched != 1 {
		t.Errorf("expected Matched=1, got %d", summary.Matched)
	}
	if len(notifier.notified) != 1 || notifier.notified[0].ID != "job-high" {
		t.Errorf("expected only job-high to be notified, got %+v", notifier.notified)
	}

	// Both jobs should be persisted regardless of match outcome.
	if _, ok := st.records["job-high"]; !ok {
		t.Error("expected job-high to be saved")
	}
	if _, ok := st.records["job-low"]; !ok {
		t.Error("expected job-low to be saved")
	}
	if st.records["job-high"].NotifiedAt == nil {
		t.Error("expected job-high to be marked notified")
	}
	if st.records["job-low"].NotifiedAt != nil {
		t.Error("expected job-low to NOT be marked notified")
	}
}

func TestRunner_Run_SkipsAlreadySeenJobs(t *testing.T) {
	job := model.Job{ID: "job-1", Title: "Platform Engineer", Company: "Acme"}

	st := newFakeStore()
	st.records["job-1"] = store.Record{Job: job, FirstSeenAt: time.Unix(1, 0)}

	scorer := newFakeScorer()
	notifier := newFakeNotifier()

	r := &Runner{
		Sources:  []sources.Source{&fakeSource{name: "fake", jobs: []model.Job{job}}},
		Filter:   baseFilterConfig(),
		Scorer:   scorer,
		Store:    st,
		Notifier: notifier,
	}

	summary := r.Run(context.Background())

	if summary.New != 0 {
		t.Errorf("expected New=0 for an already-seen job, got %d", summary.New)
	}
	if len(scorer.calls) != 0 {
		t.Errorf("expected scorer to never be called for an already-seen job, got %v", scorer.calls)
	}
	if len(notifier.notified) != 0 {
		t.Error("expected no notification for an already-seen job")
	}
}

func TestRunner_Run_KeywordFilterSkipsAICall(t *testing.T) {
	job := model.Job{ID: "job-1", Title: "Sales Manager", Company: "Acme", Description: "Sell things."}

	st := newFakeStore()
	scorer := newFakeScorer()
	notifier := newFakeNotifier()

	r := &Runner{
		Sources:  []sources.Source{&fakeSource{name: "fake", jobs: []model.Job{job}}},
		Filter:   baseFilterConfig(), // keywords: platform, devops - won't match "Sales Manager"
		Scorer:   scorer,
		Store:    st,
		Notifier: notifier,
	}

	summary := r.Run(context.Background())

	if summary.New != 1 {
		t.Errorf("expected New=1, got %d", summary.New)
	}
	if summary.Matched != 0 {
		t.Errorf("expected Matched=0, got %d", summary.Matched)
	}
	if len(scorer.calls) != 0 {
		t.Errorf("expected AI scorer to be skipped for a keyword-filtered job, got %v", scorer.calls)
	}
	if _, ok := st.records["job-1"]; !ok {
		t.Error("expected the filtered-out job to still be saved (so it isn't re-fetched forever)")
	}
}

func TestRunner_Run_SourceErrorIsolated(t *testing.T) {
	good := model.Job{ID: "job-good", Title: "Platform Engineer", Company: "Acme"}

	st := newFakeStore()
	scorer := newFakeScorer()
	scorer.scores["job-good"] = filter.AIScore{Score: 0.9}
	notifier := newFakeNotifier()

	r := &Runner{
		Sources: []sources.Source{
			&fakeSource{name: "broken", err: errors.New("boom")},
			&fakeSource{name: "fine", jobs: []model.Job{good}},
		},
		Filter:   baseFilterConfig(),
		Scorer:   scorer,
		Store:    st,
		Notifier: notifier,
	}

	summary := r.Run(context.Background())

	if len(summary.SourceErrors) != 1 {
		t.Fatalf("expected 1 source error, got %d", len(summary.SourceErrors))
	}
	if summary.Matched != 1 {
		t.Errorf("expected the working source's job to still be matched, got Matched=%d", summary.Matched)
	}
	if summary.AllSourcesFailed(2) {
		t.Error("expected AllSourcesFailed(2) to be false when only 1 of 2 sources failed")
	}
	if !((Summary{SourceErrors: []error{errors.New("x"), errors.New("y")}}).AllSourcesFailed(2)) {
		t.Error("expected AllSourcesFailed(2) to be true when both sources failed")
	}
}

func TestRunner_Run_ScoringErrorIsolatedAndRetryable(t *testing.T) {
	failing := model.Job{ID: "job-fail", Title: "Platform Engineer", Company: "Acme"}
	good := model.Job{ID: "job-good", Title: "Platform Engineer", Company: "Acme"}

	st := newFakeStore()
	scorer := newFakeScorer()
	scorer.errIDs["job-fail"] = errors.New("ai provider down")
	scorer.scores["job-good"] = filter.AIScore{Score: 0.9}
	notifier := newFakeNotifier()

	r := &Runner{
		Sources:  []sources.Source{&fakeSource{name: "fake", jobs: []model.Job{failing, good}}},
		Filter:   baseFilterConfig(),
		Scorer:   scorer,
		Store:    st,
		Notifier: notifier,
	}

	summary := r.Run(context.Background())

	if len(summary.ProcessErrors) != 1 {
		t.Fatalf("expected 1 process error, got %d", len(summary.ProcessErrors))
	}
	if summary.Matched != 1 {
		t.Errorf("expected the good job to still be matched, got Matched=%d", summary.Matched)
	}
	// The failing job must NOT be persisted, so it's retried on the next run.
	if _, ok := st.records["job-fail"]; ok {
		t.Error("expected the job whose scoring failed to remain unsaved (retryable next run)")
	}
}

func TestRunner_Run_NotifyErrorIsolated(t *testing.T) {
	job := model.Job{ID: "job-1", Title: "Platform Engineer", Company: "Acme"}

	st := newFakeStore()
	scorer := newFakeScorer()
	scorer.scores["job-1"] = filter.AIScore{Score: 0.9, Reason: "great"}
	notifier := newFakeNotifier()
	notifier.errIDs["job-1"] = errors.New("ntfy unreachable")

	r := &Runner{
		Sources:  []sources.Source{&fakeSource{name: "fake", jobs: []model.Job{job}}},
		Filter:   baseFilterConfig(),
		Scorer:   scorer,
		Store:    st,
		Notifier: notifier,
	}

	summary := r.Run(context.Background())

	if len(summary.ProcessErrors) != 1 {
		t.Fatalf("expected 1 process error, got %d", len(summary.ProcessErrors))
	}
	if summary.Matched != 0 {
		t.Errorf("expected Matched=0 when notify fails, got %d", summary.Matched)
	}
	// The job's score should still have been persisted (from the Save call
	// before the notify attempt), just without notified_at set.
	rec, ok := st.records["job-1"]
	if !ok {
		t.Fatal("expected job to be saved even though notification failed")
	}
	if rec.NotifiedAt != nil {
		t.Error("expected NotifiedAt to remain nil after a failed notify")
	}
}
