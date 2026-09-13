package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS jobs (
	id            TEXT PRIMARY KEY,
	source        TEXT NOT NULL,
	company       TEXT NOT NULL,
	title         TEXT NOT NULL,
	location      TEXT,
	url           TEXT,
	first_seen_at TEXT NOT NULL,
	ai_score      REAL NOT NULL DEFAULT 0,
	ai_reason     TEXT,
	notified_at   TEXT
);

CREATE TABLE IF NOT EXISTS source_health (
	source                TEXT PRIMARY KEY,
	consecutive_zero_runs INTEGER NOT NULL DEFAULT 0,
	last_job_count        INTEGER NOT NULL DEFAULT 0,
	last_fetch_at         TEXT NOT NULL,
	last_non_empty_at     TEXT
);
`

// SQLiteStore is a Store backed by a local SQLite file, using a pure-Go
// driver (modernc.org/sqlite) so the binary needs no cgo/cross-compile
// toolchain to build a small, static container image.
type SQLiteStore struct {
	db *sql.DB
}

// OpenSQLite opens (creating the file and any parent directories if
// necessary) the SQLite database at path and ensures the schema exists. The
// context bounds the initial schema statement.
func OpenSQLite(ctx context.Context, path string) (*SQLiteStore, error) {
	if dir := filepath.Dir(path); dir != "." {
		// 0700 rather than 0755: the database is the operator's own
		// job-search history, and nothing outside this container has any
		// reason to traverse into it.
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("creating store directory %q: %w", dir, err)
		}
		// Tighten a directory that already exists with looser permissions.
		// Best-effort: when the path is a mounted volume root the mount
		// point is owned by root and cannot be chmodded by the (non-root)
		// process, which is fine because the volume's own permissions
		// already govern access there.
		_ = os.Chmod(dir, 0o700) //nolint:gosec // G302: directories need the execute bit; 0700 is owner-only
	}

	if err := ensurePrivateFile(path); err != nil {
		return nil, err
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("opening sqlite database %q: %w", path, err)
	}

	// A CronJob run is a single process; capping at one connection avoids
	// "database is locked" errors entirely rather than just reducing
	// their likelihood under SQLite's single-writer model.
	db.SetMaxOpenConns(1)

	if _, err := db.ExecContext(ctx, schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("creating schema: %w", err)
	}

	return &SQLiteStore{db: db}, nil
}

// ensurePrivateFile creates the database file if needed and makes sure it is
// only readable by its owner. The database holds the operator's job-search
// history, so it has no reason to be group- or world-readable; the driver
// would otherwise create it 0644 & umask.
func ensurePrivateFile(path string) error {
	// The path comes from store.path in the operator's own config file,
	// not from any untrusted input.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600) //nolint:gosec // G304: operator-configured path
	if err != nil {
		return fmt.Errorf("creating sqlite database file %q: %w", path, err)
	}

	// Chmod explicitly rather than relying on the creation mode above: the
	// mode is masked by umask, and an existing file keeps whatever mode it
	// was created with.
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return fmt.Errorf("setting permissions on sqlite database file %q: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("closing sqlite database file %q: %w", path, err)
	}
	return nil
}

// Seen reports whether a job with this ID has already been recorded.
func (s *SQLiteStore) Seen(ctx context.Context, jobID string) (bool, error) {
	var exists int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM jobs WHERE id = ?`, jobID).Scan(&exists)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("checking seen status for %q: %w", jobID, err)
	}
	return true, nil
}

// Save records the outcome of processing rec.Job. If the job somehow
// already exists (e.g. a retried run), its AI score/reason are refreshed
// but first_seen_at and notified_at are left untouched.
func (s *SQLiteStore) Save(ctx context.Context, rec Record) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO jobs (id, source, company, title, location, url, first_seen_at, ai_score, ai_reason, notified_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			ai_score = excluded.ai_score,
			ai_reason = excluded.ai_reason
	`,
		rec.Job.ID, rec.Job.Source, rec.Job.Company, rec.Job.Title, rec.Job.Location, rec.Job.URL,
		formatTime(rec.FirstSeenAt), rec.AIScore, rec.AIReason, formatTimePtr(rec.NotifiedAt),
	)
	if err != nil {
		return fmt.Errorf("saving job %q: %w", rec.Job.ID, err)
	}
	return nil
}

// MarkNotified stamps jobID as notified at the given time. It returns an
// error if no such job has been Saved yet.
func (s *SQLiteStore) MarkNotified(ctx context.Context, jobID string, at time.Time) error {
	res, err := s.db.ExecContext(ctx, `UPDATE jobs SET notified_at = ? WHERE id = ?`, formatTime(at), jobID)
	if err != nil {
		return fmt.Errorf("marking job %q notified: %w", jobID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("checking rows affected for %q: %w", jobID, err)
	}
	if n == 0 {
		return fmt.Errorf("marking job %q notified: no such job in store", jobID)
	}
	return nil
}

// Get returns the persisted record for jobID, or ok=false if there isn't
// one. It's mainly used by tests and ad-hoc inspection; the runner itself
// only needs Seen/Save/MarkNotified.
func (s *SQLiteStore) Get(ctx context.Context, jobID string) (Record, bool, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, source, company, title, location, url, first_seen_at, ai_score, ai_reason, notified_at
		FROM jobs WHERE id = ?`, jobID)

	var (
		rec         Record
		firstSeenAt string
		notifiedAt  sql.NullString
	)
	err := row.Scan(
		&rec.Job.ID, &rec.Job.Source, &rec.Job.Company, &rec.Job.Title, &rec.Job.Location, &rec.Job.URL,
		&firstSeenAt, &rec.AIScore, &rec.AIReason, &notifiedAt,
	)
	if err == sql.ErrNoRows {
		return Record{}, false, nil
	}
	if err != nil {
		return Record{}, false, fmt.Errorf("getting job %q: %w", jobID, err)
	}

	rec.FirstSeenAt, _ = time.Parse(time.RFC3339, firstSeenAt)
	if notifiedAt.Valid && notifiedAt.String != "" {
		if t, err := time.Parse(time.RFC3339, notifiedAt.String); err == nil {
			rec.NotifiedAt = &t
		}
	}
	return rec, true, nil
}

// RecordSourceFetch records the result of one successful fetch of one source
// and returns the health afterwards.
//
// The read and the write share one transaction so two sources fetched
// concurrently cannot each read the same "previous streak" and both write
// the same next value. (The runner happens to fetch sources sequentially
// today, but the streak is stored state, and a lost update here would mean a
// dead board never crossing the alert threshold.)
func (s *SQLiteStore) RecordSourceFetch(ctx context.Context, source string, jobCount int, at time.Time) (SourceHealth, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SourceHealth{}, fmt.Errorf("recording fetch for %q: %w", source, err)
	}
	defer func() { _ = tx.Rollback() }()

	// A source with no row yet is treated as a streak of zero, which makes
	// a first-ever fetch of an empty board count as run one rather than
	// silently consuming an extra run before the guard notices.
	prev := 0
	var prevNonEmpty sql.NullString
	err = tx.QueryRowContext(ctx,
		`SELECT consecutive_zero_runs, last_non_empty_at FROM source_health WHERE source = ?`, source).
		Scan(&prev, &prevNonEmpty)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		prev = 0
		prevNonEmpty = sql.NullString{}
	case err != nil:
		return SourceHealth{}, fmt.Errorf("reading health for %q: %w", source, err)
	}

	next := prev
	if jobCount == 0 {
		next++
	} else {
		next = 0
	}

	// Only a non-empty fetch moves the "last seen postings" timestamp; an
	// empty one must leave it alone, since it is the anchor the warning
	// reports.
	nonEmptyAt := prevNonEmpty
	if jobCount > 0 {
		nonEmptyAt = sql.NullString{String: formatTime(at), Valid: true}
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO source_health (source, consecutive_zero_runs, last_job_count, last_fetch_at, last_non_empty_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(source) DO UPDATE SET
			consecutive_zero_runs = excluded.consecutive_zero_runs,
			last_job_count        = excluded.last_job_count,
			last_fetch_at         = excluded.last_fetch_at,
			last_non_empty_at     = excluded.last_non_empty_at
	`, source, next, jobCount, formatTime(at), nonEmptyAt); err != nil {
		return SourceHealth{}, fmt.Errorf("recording fetch for %q: %w", source, err)
	}

	if err := tx.Commit(); err != nil {
		return SourceHealth{}, fmt.Errorf("recording fetch for %q: %w", source, err)
	}

	h := SourceHealth{
		Source:              source,
		ConsecutiveZeroRuns: next,
		PreviousZeroRuns:    prev,
		LastJobCount:        jobCount,
		LastFetchAt:         at,
	}
	if nonEmptyAt.Valid {
		h.LastNonEmptyAt, _ = time.Parse(time.RFC3339, nonEmptyAt.String)
	}
	return h, nil
}

// SourceHealth returns the recorded health for one source, or ok=false if it
// has never been fetched successfully.
func (s *SQLiteStore) SourceHealth(ctx context.Context, source string) (SourceHealth, bool, error) {
	var (
		h           SourceHealth
		lastFetchAt string
		nonEmptyAt  sql.NullString
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT source, consecutive_zero_runs, last_job_count, last_fetch_at, last_non_empty_at
		FROM source_health WHERE source = ?`, source).
		Scan(&h.Source, &h.ConsecutiveZeroRuns, &h.LastJobCount, &lastFetchAt, &nonEmptyAt)
	if err == sql.ErrNoRows {
		return SourceHealth{}, false, nil
	}
	if err != nil {
		return SourceHealth{}, false, fmt.Errorf("reading health for %q: %w", source, err)
	}
	h.LastFetchAt, _ = time.Parse(time.RFC3339, lastFetchAt)
	if nonEmptyAt.Valid {
		h.LastNonEmptyAt, _ = time.Parse(time.RFC3339, nonEmptyAt.String)
	}
	return h, true, nil
}

// Close releases the underlying database handle.
func (s *SQLiteStore) Close() error {
	return s.db.Close()
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func formatTimePtr(t *time.Time) any {
	if t == nil {
		return nil
	}
	return formatTime(*t)
}

var _ Store = (*SQLiteStore)(nil)
