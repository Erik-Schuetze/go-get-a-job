package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Erik-Schuetze/go-get-a-job/internal/model"
)

func openTestStore(t *testing.T) *SQLiteStore {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := OpenSQLite(path)
	if err != nil {
		t.Fatalf("OpenSQLite returned error: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestSQLiteStore_SeenAndSave(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	seen, err := s.Seen(ctx, "job-1")
	if err != nil {
		t.Fatalf("Seen returned error: %v", err)
	}
	if seen {
		t.Fatal("expected job-1 to be unseen before Save")
	}

	job := model.Job{ID: "job-1", Source: "greenhouse", Company: "Acme", Title: "Platform Engineer", URL: "https://example.com/1"}
	rec := Record{Job: job, FirstSeenAt: time.Now(), AIScore: 0.85, AIReason: "Great match"}
	if err := s.Save(ctx, rec); err != nil {
		t.Fatalf("Save returned error: %v", err)
	}

	seen, err = s.Seen(ctx, "job-1")
	if err != nil {
		t.Fatalf("Seen returned error: %v", err)
	}
	if !seen {
		t.Fatal("expected job-1 to be seen after Save")
	}

	got, ok, err := s.Get(ctx, "job-1")
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if !ok {
		t.Fatal("expected Get to find job-1")
	}
	if got.Job.Title != "Platform Engineer" {
		t.Errorf("unexpected title: %q", got.Job.Title)
	}
	if got.AIScore != 0.85 {
		t.Errorf("unexpected AIScore: %v", got.AIScore)
	}
	if got.NotifiedAt != nil {
		t.Errorf("expected NotifiedAt to be nil before MarkNotified, got %v", got.NotifiedAt)
	}
}

func TestSQLiteStore_MarkNotified(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	job := model.Job{ID: "job-2", Source: "lever", Company: "Acme", Title: "SRE"}
	if err := s.Save(ctx, Record{Job: job, FirstSeenAt: time.Now(), AIScore: 0.9}); err != nil {
		t.Fatalf("Save returned error: %v", err)
	}

	notifyTime := time.Now().Truncate(time.Second)
	if err := s.MarkNotified(ctx, "job-2", notifyTime); err != nil {
		t.Fatalf("MarkNotified returned error: %v", err)
	}

	got, ok, err := s.Get(ctx, "job-2")
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if !ok {
		t.Fatal("expected Get to find job-2")
	}
	if got.NotifiedAt == nil {
		t.Fatal("expected NotifiedAt to be set")
	}
	if !got.NotifiedAt.Equal(notifyTime.UTC()) {
		t.Errorf("expected NotifiedAt %v, got %v", notifyTime.UTC(), got.NotifiedAt)
	}
}

func TestSQLiteStore_MarkNotified_UnknownJob(t *testing.T) {
	s := openTestStore(t)
	if err := s.MarkNotified(context.Background(), "does-not-exist", time.Now()); err == nil {
		t.Fatal("expected error when marking an unknown job as notified, got nil")
	}
}

func TestSQLiteStore_Save_UpsertKeepsFirstSeenAndNotifiedAt(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	job := model.Job{ID: "job-3", Source: "ashby", Company: "Acme", Title: "DevOps"}
	firstSeen := time.Now().Add(-24 * time.Hour).Truncate(time.Second)
	if err := s.Save(ctx, Record{Job: job, FirstSeenAt: firstSeen, AIScore: 0.5}); err != nil {
		t.Fatalf("first Save returned error: %v", err)
	}
	if err := s.MarkNotified(ctx, "job-3", firstSeen.Add(time.Minute)); err != nil {
		t.Fatalf("MarkNotified returned error: %v", err)
	}

	// Re-saving (e.g. a retried run) should refresh the score but not
	// clobber first_seen_at or notified_at.
	if err := s.Save(ctx, Record{Job: job, FirstSeenAt: time.Now(), AIScore: 0.99}); err != nil {
		t.Fatalf("second Save returned error: %v", err)
	}

	got, ok, err := s.Get(ctx, "job-3")
	if err != nil || !ok {
		t.Fatalf("Get failed: ok=%v err=%v", ok, err)
	}
	if got.AIScore != 0.99 {
		t.Errorf("expected refreshed AIScore 0.99, got %v", got.AIScore)
	}
	if !got.FirstSeenAt.Equal(firstSeen.UTC()) {
		t.Errorf("expected first_seen_at to remain %v, got %v", firstSeen.UTC(), got.FirstSeenAt)
	}
	if got.NotifiedAt == nil {
		t.Error("expected notified_at to survive the re-save")
	}
}

func TestSQLiteStore_Get_NotFound(t *testing.T) {
	s := openTestStore(t)
	_, ok, err := s.Get(context.Background(), "nope")
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if ok {
		t.Fatal("expected ok=false for missing job")
	}
}

func TestSQLiteStore_PersistsAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "test.db")

	s1, err := OpenSQLite(path)
	if err != nil {
		t.Fatalf("OpenSQLite returned error: %v", err)
	}
	if err := s1.Save(context.Background(), Record{
		Job:         model.Job{ID: "job-4", Source: "workday", Company: "Acme", Title: "Platform"},
		FirstSeenAt: time.Now(),
	}); err != nil {
		t.Fatalf("Save returned error: %v", err)
	}
	if err := s1.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}

	s2, err := OpenSQLite(path)
	if err != nil {
		t.Fatalf("reopening store returned error: %v", err)
	}
	defer s2.Close()

	seen, err := s2.Seen(context.Background(), "job-4")
	if err != nil {
		t.Fatalf("Seen returned error: %v", err)
	}
	if !seen {
		t.Fatal("expected job-4 to still be recorded after reopening the store")
	}
}
