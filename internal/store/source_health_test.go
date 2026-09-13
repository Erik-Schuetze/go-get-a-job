package store

import (
	"context"
	"testing"
	"time"
)

// TestSQLiteStore_RecordSourceFetch_Streak covers the state machine the
// dead-source guard is built on: a run with postings resets the streak, and
// runs without them extend it. Getting this backwards would mean a warning
// that either never fires or fires immediately, which is the whole feature.
func TestSQLiteStore_RecordSourceFetch_Streak(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	base := time.Date(2026, 3, 1, 6, 0, 0, 0, time.UTC)

	// First ever fetch of a board that has postings.
	h, err := s.RecordSourceFetch(ctx, "greenhouse/acme", 12, base)
	if err != nil {
		t.Fatalf("RecordSourceFetch returned error: %v", err)
	}
	if h.ConsecutiveZeroRuns != 0 {
		t.Errorf("after a non-empty fetch: ConsecutiveZeroRuns = %d, want 0", h.ConsecutiveZeroRuns)
	}
	if h.LastJobCount != 12 {
		t.Errorf("LastJobCount = %d, want 12", h.LastJobCount)
	}
	if !h.LastNonEmptyAt.Equal(base) {
		t.Errorf("LastNonEmptyAt = %v, want %v", h.LastNonEmptyAt, base)
	}

	// Three empty runs in a row.
	for i := 1; i <= 3; i++ {
		at := base.AddDate(0, 0, i)
		h, err = s.RecordSourceFetch(ctx, "greenhouse/acme", 0, at)
		if err != nil {
			t.Fatalf("RecordSourceFetch (empty run %d) returned error: %v", i, err)
		}
		if h.ConsecutiveZeroRuns != i {
			t.Errorf("empty run %d: ConsecutiveZeroRuns = %d, want %d", i, h.ConsecutiveZeroRuns, i)
		}
		if h.PreviousZeroRuns != i-1 {
			t.Errorf("empty run %d: PreviousZeroRuns = %d, want %d", i, h.PreviousZeroRuns, i-1)
		}
		if h.LastJobCount != 0 {
			t.Errorf("empty run %d: LastJobCount = %d, want 0", i, h.LastJobCount)
		}
		// The anchor for "when did this board last have postings?" must
		// survive empty runs, which is the only reason it is stored apart
		// from LastFetchAt.
		if !h.LastNonEmptyAt.Equal(base) {
			t.Errorf("empty run %d: LastNonEmptyAt = %v, want %v", i, h.LastNonEmptyAt, base)
		}
	}

	// A single non-empty run clears the streak entirely.
	recovered := base.AddDate(0, 0, 4)
	h, err = s.RecordSourceFetch(ctx, "greenhouse/acme", 1, recovered)
	if err != nil {
		t.Fatalf("RecordSourceFetch (recovery) returned error: %v", err)
	}
	if h.ConsecutiveZeroRuns != 0 {
		t.Errorf("after recovery: ConsecutiveZeroRuns = %d, want 0", h.ConsecutiveZeroRuns)
	}
	if !h.LastNonEmptyAt.Equal(recovered) {
		t.Errorf("after recovery: LastNonEmptyAt = %v, want %v", h.LastNonEmptyAt, recovered)
	}
}

// TestSQLiteStore_SourceHealth_PersistsAndIsPerSource checks that health
// survives a reopen (a CronJob restart must not erase a two-week silence) and
// that one source's history never leaks into another's - the bug that would
// hide a dead board behind a healthy sibling on the same connector.
func TestSQLiteStore_SourceHealth_PersistsAndIsPerSource(t *testing.T) {
	ctx := context.Background()
	path := t.TempDir() + "/test.db"
	base := time.Date(2026, 3, 1, 6, 0, 0, 0, time.UTC)

	s, err := OpenSQLite(ctx, path)
	if err != nil {
		t.Fatalf("OpenSQLite returned error: %v", err)
	}
	for i := 0; i < 3; i++ {
		if _, err := s.RecordSourceFetch(ctx, "greenhouse/acme", 0, base.AddDate(0, 0, i)); err != nil {
			t.Fatalf("RecordSourceFetch returned error: %v", err)
		}
	}
	if _, err := s.RecordSourceFetch(ctx, "greenhouse/other", 5, base); err != nil {
		t.Fatalf("RecordSourceFetch returned error: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}

	s2, err := OpenSQLite(ctx, path)
	if err != nil {
		t.Fatalf("reopening returned error: %v", err)
	}
	defer func() { _ = s2.Close() }()

	acme, ok, err := s2.SourceHealth(ctx, "greenhouse/acme")
	if err != nil {
		t.Fatalf("SourceHealth returned error: %v", err)
	}
	if !ok {
		t.Fatal("expected greenhouse/acme to have recorded health after reopen")
	}
	if acme.ConsecutiveZeroRuns != 3 {
		t.Errorf("greenhouse/acme ConsecutiveZeroRuns = %d, want 3", acme.ConsecutiveZeroRuns)
	}
	if !acme.LastNonEmptyAt.IsZero() {
		t.Errorf("greenhouse/acme LastNonEmptyAt = %v, want zero (it never had postings)", acme.LastNonEmptyAt)
	}

	other, ok, err := s2.SourceHealth(ctx, "greenhouse/other")
	if err != nil {
		t.Fatalf("SourceHealth returned error: %v", err)
	}
	if !ok {
		t.Fatal("expected greenhouse/other to have recorded health")
	}
	if other.ConsecutiveZeroRuns != 0 || other.LastJobCount != 5 {
		t.Errorf("greenhouse/other health = %+v, want a clean streak with 5 postings", other)
	}

	if _, ok, err := s2.SourceHealth(ctx, "greenhouse/never-seen"); err != nil || ok {
		t.Errorf("SourceHealth for an unknown source = (ok=%v, err=%v), want (false, nil)", ok, err)
	}
}
