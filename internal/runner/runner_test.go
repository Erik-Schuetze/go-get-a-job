package runner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Erik-Schuetze/go-get-a-job/internal/config"
	"github.com/Erik-Schuetze/go-get-a-job/internal/filter"
	"github.com/Erik-Schuetze/go-get-a-job/internal/model"
	"github.com/Erik-Schuetze/go-get-a-job/internal/notify"
	"github.com/Erik-Schuetze/go-get-a-job/internal/sources"
	"github.com/Erik-Schuetze/go-get-a-job/internal/store"
)

// --- fakes -----------------------------------------------------------

type fakeSource struct {
	name  string
	label string
	jobs  []model.Job
	err   error
}

func (f *fakeSource) Name() string { return f.name }

// Label defaults to the name so simple tests that use one fake per name keep
// working; tests that exercise per-source state set it explicitly.
func (f *fakeSource) Label() string {
	if f.label != "" {
		return f.label
	}
	return f.name
}

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
	mu        sync.Mutex
	records   map[string]store.Record
	health    map[string]store.SourceHealth
	recordErr error
}

func newFakeStore() *fakeStore {
	return &fakeStore{records: map[string]store.Record{}, health: map[string]store.SourceHealth{}}
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

// recordErr, when set, makes RecordSourceFetch fail so the runner's
// best-effort handling of a health-write failure can be tested.
func (f *fakeStore) RecordSourceFetch(_ context.Context, source string, jobCount int, at time.Time) (store.SourceHealth, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.recordErr != nil {
		return store.SourceHealth{}, f.recordErr
	}
	prev := f.health[source]
	next := prev
	if jobCount == 0 {
		next.ConsecutiveZeroRuns++
	} else {
		next.ConsecutiveZeroRuns = 0
	}
	next.Source = source
	next.PreviousZeroRuns = prev.ConsecutiveZeroRuns
	next.LastJobCount = jobCount
	next.LastFetchAt = at
	if jobCount > 0 {
		next.LastNonEmptyAt = at
	}
	f.health[source] = next
	return next, nil
}

func (f *fakeStore) SourceHealth(_ context.Context, source string) (store.SourceHealth, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	h, ok := f.health[source]
	return h, ok, nil
}

type fakeNotifier struct {
	mu       sync.Mutex
	notified []model.Job
	matches  []notify.Match
	errIDs   map[string]error
	failed   int
	warnings []fakeWarning
}

// fakeWarning captures one NotifyWarning call.
type fakeWarning struct {
	Title string
	Body  string
}

func newFakeNotifier() *fakeNotifier {
	return &fakeNotifier{errIDs: map[string]error{}}
}

func (f *fakeNotifier) Notify(_ context.Context, match notify.Match) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err, ok := f.errIDs[match.Job.ID]; ok {
		return err
	}
	f.notified = append(f.notified, match.Job)
	f.matches = append(f.matches, match)
	return nil
}

func (f *fakeNotifier) NotifyFailure(_ context.Context, _ error) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failed++
	return nil
}

func (f *fakeNotifier) NotifyWarning(_ context.Context, title, body string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.warnings = append(f.warnings, fakeWarning{Title: title, Body: body})
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

// --- log hygiene -----------------------------------------------------

// recordingHandler captures each record's attributes as raw Go values, before
// any encoder escapes them. That matters here: a text handler would render a
// newline as the two-character sequence \n and hide the very thing under
// test, so the assertions have to run against the value the runner passed.
type recordingHandler struct {
	mu      sync.Mutex
	records []recordedRecord
}

type recordedRecord struct {
	message string
	attrs   map[string]any
}

func (h *recordingHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *recordingHandler) Handle(_ context.Context, r slog.Record) error {
	rec := recordedRecord{message: r.Message, attrs: map[string]any{}}
	r.Attrs(func(a slog.Attr) bool {
		rec.attrs[a.Key] = a.Value.Any()
		return true
	})
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, rec)
	return nil
}

func (h *recordingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *recordingHandler) WithGroup(string) slog.Handler      { return h }

func (h *recordingHandler) stringAttrs() map[string]string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := map[string]string{}
	for _, rec := range h.records {
		for k, v := range rec.attrs {
			if s, ok := v.(string); ok {
				out[rec.message+"."+k] = s
			}
		}
	}
	return out
}

// The source name, the error string, and the job ID/title all originate
// outside this program: the source name from config, and the rest from a
// third-party response. A newline among them would let a hostile posting
// forge extra structured-log records, and a terminal escape would let it
// rewrite the operator's terminal when the logs are tailed by hand.
func TestRunner_Run_SanitizesUntrustedLogAttributes(t *testing.T) {
	hostileErr := errors.New("boom\x1b]0;pwned\x07\nlevel=ERROR msg=forged")
	job := model.Job{
		ID:    "job-1\nlevel=ERROR msg=forged",
		Title: "Engineer\x1b[31m\r\nContent-Length: 0",
	}

	handler := &recordingHandler{}
	scorer := newFakeScorer()
	scorer.errIDs["job-1\nlevel=ERROR msg=forged"] = hostileErr

	r := &Runner{
		Sources: []sources.Source{
			&fakeSource{name: "greenhouse\nlevel=ERROR msg=forged", err: hostileErr},
			&fakeSource{name: "lever", jobs: []model.Job{job}},
		},
		Filter:   config.FilterConfig{MinAIScore: 0.7},
		Profile:  "profile",
		Scorer:   scorer,
		Store:    newFakeStore(),
		Notifier: &fakeNotifier{},
		Logger:   slog.New(handler),
	}

	r.Run(context.Background())

	attrs := handler.stringAttrs()
	if len(attrs) == 0 {
		t.Fatal("expected the runner to log at least one record with attributes")
	}

	for name, value := range attrs {
		if strings.ContainsAny(value, "\n\r\x1b\x07\x00") {
			t.Errorf("log attribute %s = %q still contains control characters", name, value)
		}
	}

	// The readable part must survive: sanitizing is not allowed to blank the
	// value, or the log stops being useful for diagnosis.
	if got := attrs["source fetch failed.source"]; !strings.Contains(got, "greenhouse") {
		t.Errorf("expected the source name to remain readable, got %q", got)
	}
	if got := attrs["processing job failed.title"]; !strings.Contains(got, "Engineer") {
		t.Errorf("expected the job title to remain readable, got %q", got)
	}
	if got := attrs["source fetch failed.error"]; !strings.Contains(got, "boom") {
		t.Errorf("expected the source error to remain readable, got %q", got)
	}
}

func TestRunner_Run_CapsUntrustedLogAttributeLength(t *testing.T) {
	job := model.Job{ID: strings.Repeat("i", 2000), Title: strings.Repeat("t", 2000)}

	handler := &recordingHandler{}
	scorer := newFakeScorer()
	scorer.errIDs[strings.Repeat("i", 2000)] = errors.New("nope")

	r := &Runner{
		Sources:  []sources.Source{&fakeSource{name: "greenhouse", jobs: []model.Job{job}}},
		Filter:   config.FilterConfig{MinAIScore: 0.7},
		Scorer:   scorer,
		Store:    newFakeStore(),
		Notifier: &fakeNotifier{},
		Logger:   slog.New(handler),
	}

	r.Run(context.Background())

	attrs := handler.stringAttrs()
	for name, value := range attrs {
		if len([]rune(value)) > 600 {
			t.Errorf("log attribute %s is %d runes, expected it to be capped", name, len([]rune(value)))
		}
	}
}

// --- location pre-filter diagnostics -----------------------------------

// These tests pin down what the pre-filter reports: naming a place the accept
// list does not cover is dropped and reported, while anything that is not tied
// to a country reaches the scorer, which holds the relocation rule.
func TestRunner_Run_DropsUnlistedLocationAndReportsWhy(t *testing.T) {
	unlisted := model.Job{
		ID:       "job-ca",
		Title:    "Platform Engineer",
		Company:  "Acme",
		Location: "Toronto, Canada",
	}

	handler := &recordingHandler{}
	scorer := newFakeScorer()
	st := newFakeStore()
	notifier := newFakeNotifier()

	filterCfg := baseFilterConfig()
	filterCfg.Locations = config.LocationConfig{
		Accept:    []string{"Germany", "EMEA"},
		Unmatched: config.LocationUnmatchedReject,
	}

	r := &Runner{
		Sources:  []sources.Source{&fakeSource{name: "fake", jobs: []model.Job{unlisted}}},
		Filter:   filterCfg,
		Profile:  "profile",
		Scorer:   scorer,
		Store:    st,
		Notifier: notifier,
		Logger:   slog.New(handler),
	}

	summary := r.Run(context.Background())

	if summary.FilteredByLocation != 1 {
		t.Errorf("expected FilteredByLocation=1, got %d", summary.FilteredByLocation)
	}
	if len(summary.LocationRejections) != 1 {
		t.Fatalf("expected one recorded rejection, got %d", len(summary.LocationRejections))
	}
	rej := summary.LocationRejections[0]
	if rej.Kind != filter.LocationKindUnmatched {
		t.Errorf("expected kind=%q, got kind=%q", filter.LocationKindUnmatched, rej.Kind)
	}
	if rej.Rule != "" {
		t.Errorf("expected no deciding rule for an unlisted place, got rule=%q", rej.Rule)
	}

	// A location rejection must not cost an AI call.
	if len(scorer.calls) != 0 {
		t.Errorf("expected no AI calls for an unlisted location, got %v", scorer.calls)
	}
	if len(notifier.notified) != 0 {
		t.Errorf("expected no notification, got %d", len(notifier.notified))
	}

	// The posting is still recorded as seen, so it is not re-evaluated (and
	// re-notified) on every subsequent run.
	if _, ok := st.records["job-ca"]; !ok {
		t.Error("expected the location-filtered job to be persisted as seen")
	}

	attrs := handler.stringAttrs()
	if got := attrs["location rejected.location"]; got != "Toronto, Canada" {
		t.Errorf("expected the debug log to carry the location, got %q", got)
	}
	if got := attrs["location rejected.kind"]; got != filter.LocationKindUnmatched {
		t.Errorf("expected the debug log to name the decision kind, got %q", got)
	}
}

func TestRunner_Run_RemoteLocationInAForeignCountryReachesTheScorer(t *testing.T) {
	// The counterpart to the test above: a remote role in a country the accept
	// list does not name is not caught by the pre-filter at all, so it reaches
	// the AI scorer, which holds the relocation rule, and is dropped there
	// rather than by a list that would have to name every country.
	job := model.Job{ID: "job-ca-remote", Title: "Platform Engineer", Location: "Remote - Canada"}

	scorer := newFakeScorer()
	filterCfg := baseFilterConfig()
	filterCfg.Locations = config.LocationConfig{
		Accept:    []string{"Germany", "EMEA"},
		Unmatched: config.LocationUnmatchedReject,
	}

	r := &Runner{
		Sources:  []sources.Source{&fakeSource{name: "fake", jobs: []model.Job{job}}},
		Filter:   filterCfg,
		Profile:  "profile",
		Scorer:   scorer,
		Store:    newFakeStore(),
		Notifier: &fakeNotifier{},
	}

	summary := r.Run(context.Background())

	if summary.FilteredByLocation != 0 {
		t.Errorf("expected the location pre-filter to let a remote posting through, got %d", summary.FilteredByLocation)
	}
	if len(scorer.calls) != 1 {
		t.Errorf("expected exactly one AI call, got %v", scorer.calls)
	}
}

func TestRunner_Run_ReportedKindDistinguishesUnlistedPlaceFromRemote(t *testing.T) {
	// A run that drops a lot of postings has to say which of the two
	// happened: a place that is simply not accepted (widen the accept list
	// if that is wrong) or a posting the pre-filter declined to judge at all
	// (it never should be dropped). Only a place is ever dropped.
	unlisted := model.Job{ID: "job-ca", Title: "Platform Engineer", Location: "Toronto, Canada"}

	scorer := newFakeScorer()
	filterCfg := baseFilterConfig()
	filterCfg.Locations = config.LocationConfig{
		Accept:    []string{"Germany"},
		Unmatched: config.LocationUnmatchedReject,
	}

	r := &Runner{
		Sources:  []sources.Source{&fakeSource{name: "fake", jobs: []model.Job{unlisted}}},
		Filter:   filterCfg,
		Profile:  "profile",
		Scorer:   scorer,
		Store:    newFakeStore(),
		Notifier: &fakeNotifier{},
	}

	summary := r.Run(context.Background())

	if summary.FilteredByLocation != 1 {
		t.Fatalf("expected FilteredByLocation=1, got %d", summary.FilteredByLocation)
	}
	if got := summary.LocationRejections[0].Kind; got != filter.LocationKindUnmatched {
		t.Errorf("expected kind=%q, got %q", filter.LocationKindUnmatched, got)
	}
	if got := summary.LocationRejections[0].Rule; got != "" {
		t.Errorf("expected no deciding rule for an unlisted location, got %q", got)
	}
}

func TestRunner_Run_UnmatchedPassReachesTheScorer(t *testing.T) {
	// The opt-in escape hatch: a posting in a country that is not accepted
	// is handed to the AI, which is where the profile's legal-location rules
	// can judge it.
	job := model.Job{ID: "job-ca", Title: "Platform Engineer", Location: "Toronto, Canada"}

	scorer := newFakeScorer()
	scorer.scores["job-ca"] = filter.AIScore{Score: 0.9, Reason: "good fit"}

	filterCfg := baseFilterConfig()
	filterCfg.Locations = config.LocationConfig{
		Accept:    []string{"Germany"},
		Unmatched: config.LocationUnmatchedPass,
	}

	notifier := newFakeNotifier()
	r := &Runner{
		Sources:  []sources.Source{&fakeSource{name: "fake", jobs: []model.Job{job}}},
		Filter:   filterCfg,
		Profile:  "profile",
		Scorer:   scorer,
		Store:    newFakeStore(),
		Notifier: notifier,
		Now:      func() time.Time { return time.Unix(1000, 0) },
	}

	summary := r.Run(context.Background())

	if summary.FilteredByLocation != 0 {
		t.Errorf("expected no location filtering, got %d", summary.FilteredByLocation)
	}
	if summary.Matched != 1 {
		t.Errorf("expected the posting to be notified, got Matched=%d", summary.Matched)
	}
	if len(scorer.calls) != 1 {
		t.Errorf("expected exactly one AI call, got %v", scorer.calls)
	}
}

func TestRunner_Run_CapsReportedLocationRejections(t *testing.T) {
	// A misconfigured accept list can reject hundreds of postings in one run.
	// The count is unbounded, but the retained sample must not be.
	jobs := make([]model.Job, 0, 50)
	for i := range 50 {
		jobs = append(jobs, model.Job{
			ID:       "job-" + strconv.Itoa(i),
			Title:    "Platform Engineer",
			Location: "Toronto, Canada",
		})
	}

	filterCfg := baseFilterConfig()
	filterCfg.Locations = config.LocationConfig{
		Accept:    []string{"Germany"},
		Unmatched: config.LocationUnmatchedReject,
	}

	r := &Runner{
		Sources:  []sources.Source{&fakeSource{name: "fake", jobs: jobs}},
		Filter:   filterCfg,
		Profile:  "profile",
		Scorer:   newFakeScorer(),
		Store:    newFakeStore(),
		Notifier: &fakeNotifier{},
	}

	summary := r.Run(context.Background())

	if summary.FilteredByLocation != 50 {
		t.Errorf("expected FilteredByLocation=50, got %d", summary.FilteredByLocation)
	}
	if len(summary.LocationRejections) != maxReportedLocationRejections {
		t.Errorf("expected the sample to be capped at %d, got %d",
			maxReportedLocationRejections, len(summary.LocationRejections))
	}
}

// TestRunner_Run_HonoursTheLocationVeto covers the one outcome that is neither
// a match nor a miss: the scorer says the posting is a fit but the location
// rules it out.
//
// Two things have to happen together, and each is wrong on its own. The
// posting must be persisted, because a job that is not saved is scored again
// on every subsequent run and billed again each time - forever, since the veto
// repeats. And it must be counted, because a posting that is dropped with no
// trace in the run summary is indistinguishable from one the watcher never
// saw, which is the failure this program exists to prevent.
func TestRunner_Run_HonoursTheLocationVeto(t *testing.T) {
	vetoed := model.Job{ID: "job-us-only", Title: "Platform Engineer", Company: "Acme"}
	kept := model.Job{ID: "job-remote-eu", Title: "DevOps Engineer", Company: "Acme"}

	no := false
	yes := true
	scorer := newFakeScorer()
	// A high score on the vetoed posting is deliberate: it proves the veto is
	// not riding on the threshold, since scoring alone would have notified it.
	scorer.scores["job-us-only"] = filter.AIScore{Score: 0.95, Reason: "Great stack, US only.", LocationOK: &no}
	scorer.scores["job-remote-eu"] = filter.AIScore{Score: 0.95, Reason: "Great stack.", LocationOK: &yes}

	st := newFakeStore()
	notifier := newFakeNotifier()

	r := &Runner{
		Sources:  []sources.Source{&fakeSource{name: "fake", jobs: []model.Job{vetoed, kept}}},
		Filter:   baseFilterConfig(),
		Profile:  "platform engineer in Germany",
		Scorer:   scorer,
		Store:    st,
		Notifier: notifier,
		Now:      func() time.Time { return time.Unix(1000, 0) },
	}

	summary := r.Run(context.Background())

	if summary.VetoedByLocation != 1 {
		t.Errorf("expected VetoedByLocation=1, got %d", summary.VetoedByLocation)
	}
	if summary.Matched != 1 {
		t.Errorf("expected Matched=1, got %d", summary.Matched)
	}
	if len(notifier.notified) != 1 || notifier.notified[0].ID != "job-remote-eu" {
		t.Errorf("expected only job-remote-eu to be notified, got %+v", notifier.notified)
	}
	if _, ok := st.records["job-us-only"]; !ok {
		t.Error("expected the vetoed job to be saved, so it is never scored and billed again")
	}
	// Saved, but never marked notified: the row has to stay distinguishable
	// from one that was actually announced.
	if rec := st.records["job-us-only"]; rec.NotifiedAt != nil {
		t.Errorf("vetoed job was marked notified at %v", *rec.NotifiedAt)
	}
}
