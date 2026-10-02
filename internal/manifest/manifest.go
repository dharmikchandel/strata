// Package manifest is Strata's card catalog: a small SQLite database that
// records which segments exist, what time range each covers, and whether each
// is still in use.
//
// The manifest, not the object store, is the source of truth for "what data
// exists". Objects in storage that the manifest doesn't list are invisible to
// queries (they may be leftovers from a crash or a retried upload), and
// segments the manifest marks deleted are ignored even if the file is still
// there. That is what makes it safe to replace many small segments with one
// big one: the swap is a single SQLite transaction, so it happens entirely or
// not at all.
//
// SQLite was chosen over a separate database so Strata stays one binary with
// real ACID transactions. The driver is modernc.org/sqlite, a pure-Go
// implementation, so no C compiler is needed to build.
package manifest

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver
)

// Status is the lifecycle state of a segment.
type Status string

const (
	// StatusActive segments are visible to queries.
	StatusActive Status = "active"
	// StatusDeleted segments were replaced (e.g. by compaction). Their files
	// may still exist until a cleanup removes them, but queries must ignore them.
	StatusDeleted Status = "deleted"
)

var (
	// ErrInvalidSegment means a Segment failed validation and nothing was written.
	ErrInvalidSegment = errors.New("manifest: invalid segment")
	// ErrDuplicate means a segment with that ID or storage key already exists.
	ErrDuplicate = errors.New("manifest: segment already exists")
	// ErrNotActive means a segment to be replaced doesn't exist or is already deleted.
	ErrNotActive = errors.New("manifest: segment is not active")
)

// Segment is one row of the manifest.
type Segment struct {
	ID        string
	Key       string // object key in storage
	MinTS     int64  // unix nanoseconds
	MaxTS     int64
	Count     int   // number of log entries
	Size      int64 // bytes of the segment file
	Status    Status
	CreatedAt time.Time
}

func (s Segment) validate() error {
	switch {
	case s.ID == "":
		return fmt.Errorf("%w: empty id", ErrInvalidSegment)
	case s.Key == "":
		return fmt.Errorf("%w: empty storage key", ErrInvalidSegment)
	case s.MinTS > s.MaxTS:
		return fmt.Errorf("%w: min timestamp after max", ErrInvalidSegment)
	case s.Count <= 0:
		return fmt.Errorf("%w: count must be positive", ErrInvalidSegment)
	case s.Size < 0:
		return fmt.Errorf("%w: negative size", ErrInvalidSegment)
	}
	return nil
}

// Manifest is a handle to the manifest database. It is safe for concurrent use.
type Manifest struct {
	db *sql.DB

	// testHook, if set, is called at named points inside Replace so tests can
	// crash the process at an exact moment. Always nil in production.
	testHook func(stage string)
}

// Open opens (creating if needed) the manifest at path and brings its schema
// up to date.
//
// Settings, and why:
//   - journal_mode=WAL: readers don't block the writer and vice versa, so
//     queries keep working while a segment is being recorded.
//   - synchronous=FULL: a committed transaction is fsynced before COMMIT
//     returns, so it survives power loss, not just a process crash. The
//     manifest takes few writes, so the cost is negligible.
//   - busy_timeout: wait for a competing writer instead of failing at once.
//   - _txlock=immediate: write transactions take the write lock at BEGIN.
//     SQLite's default (deferred) can fail with "database is locked" when two
//     transactions both start by reading and then try to write.
//   - foreign_keys is not needed: there is one table.
func Open(path string) (*Manifest, error) {
	dsn := "file:" + path +
		"?_pragma=journal_mode(WAL)" +
		"&_pragma=synchronous(FULL)" +
		"&_pragma=busy_timeout(5000)" +
		"&_txlock=immediate"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("manifest: open: %w", err)
	}
	if err := migrate(context.Background(), db); err != nil {
		db.Close()
		return nil, err
	}
	return &Manifest{db: db}, nil
}

// Close releases the database.
func (m *Manifest) Close() error { return m.db.Close() }

// migrations are applied in order; the number applied is stored in SQLite's
// built-in PRAGMA user_version. To change the schema later, append a new
// entry. Never edit an old one: existing databases have already run it.
var migrations = []string{
	`CREATE TABLE segments (
		id          TEXT    PRIMARY KEY,
		storage_key TEXT    NOT NULL UNIQUE,
		min_ts      INTEGER NOT NULL,
		max_ts      INTEGER NOT NULL,
		entry_count INTEGER NOT NULL CHECK (entry_count > 0),
		size_bytes  INTEGER NOT NULL CHECK (size_bytes >= 0),
		status      TEXT    NOT NULL CHECK (status IN ('active', 'deleted')),
		created_at  INTEGER NOT NULL,
		CHECK (min_ts <= max_ts)
	);
	CREATE INDEX segments_status_time ON segments (status, min_ts, max_ts);`,
}

func migrate(ctx context.Context, db *sql.DB) error {
	// A dedicated connection so the version check and the migration see the
	// same state; the transaction makes each migration all-or-nothing.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("manifest: migrate: %w", err)
	}
	defer tx.Rollback()

	var version int
	if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("manifest: read schema version: %w", err)
	}
	if version > len(migrations) {
		return fmt.Errorf("manifest: database schema version %d is newer than this binary supports (%d)", version, len(migrations))
	}
	for i := version; i < len(migrations); i++ {
		if _, err := tx.ExecContext(ctx, migrations[i]); err != nil {
			return fmt.Errorf("manifest: apply migration %d: %w", i+1, err)
		}
		// PRAGMA doesn't accept bound parameters; i is our own integer.
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", i+1)); err != nil {
			return fmt.Errorf("manifest: record migration %d: %w", i+1, err)
		}
	}
	return tx.Commit()
}

// AddSegment records a newly sealed segment as active. This is the
// insert-on-seal step: it is meant to be called from the ingest buffer's
// OnSeal hook, after the segment file is safely in storage.
func (m *Manifest) AddSegment(ctx context.Context, s Segment) error {
	return m.Replace(ctx, []Segment{s}, nil)
}

// Replace atomically adds the given segments (as active) and marks the
// segments in deleteIDs deleted. All of it happens in one transaction: after
// a crash or error, either every change is visible or none is.
//
// Compaction uses this to swap many small segments for one merged segment. A
// reader never sees the merged segment alongside the originals (double
// counting) or neither (missing data).
//
// It fails, changing nothing, if a segment is invalid, an ID or key already
// exists (ErrDuplicate), or a segment to delete is missing or already deleted
// (ErrNotActive).
func (m *Manifest) Replace(ctx context.Context, add []Segment, deleteIDs []string) error {
	for _, s := range add {
		if err := s.validate(); err != nil {
			return err
		}
	}
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("manifest: begin: %w", err)
	}
	// If we return before Commit succeeds, this undoes everything. After a
	// successful Commit it is a harmless no-op.
	defer tx.Rollback()

	now := time.Now().UnixNano()
	for _, s := range add {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO segments (id, storage_key, min_ts, max_ts, entry_count, size_bytes, status, created_at)
			 VALUES (?, ?, ?, ?, ?, ?, 'active', ?)`,
			s.ID, s.Key, s.MinTS, s.MaxTS, s.Count, s.Size, now)
		if err != nil {
			if isConstraintViolation(err) {
				return fmt.Errorf("%w: id %q, key %q", ErrDuplicate, s.ID, s.Key)
			}
			return fmt.Errorf("manifest: insert segment %q: %w", s.ID, err)
		}
	}
	m.hook("inserted")

	for _, id := range deleteIDs {
		// "AND status = 'active'" makes the check and the change one atomic
		// statement; zero rows affected means it wasn't an active segment.
		res, err := tx.ExecContext(ctx,
			`UPDATE segments SET status = 'deleted' WHERE id = ? AND status = 'active'`, id)
		if err != nil {
			return fmt.Errorf("manifest: delete segment %q: %w", id, err)
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return fmt.Errorf("%w: %q", ErrNotActive, id)
		}
	}
	m.hook("marked")

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("manifest: commit: %w", err)
	}
	m.hook("committed")
	return nil
}

func (m *Manifest) hook(stage string) {
	if m.testHook != nil {
		m.testHook(stage)
	}
}

// List returns all segments with the given status, ordered by min timestamp
// then ID. It reads a consistent snapshot: a concurrent Replace is seen
// entirely or not at all.
func (m *Manifest) List(ctx context.Context, status Status) ([]Segment, error) {
	rows, err := m.db.QueryContext(ctx,
		`SELECT id, storage_key, min_ts, max_ts, entry_count, size_bytes, status, created_at
		 FROM segments WHERE status = ? ORDER BY min_ts, id`, string(status))
	if err != nil {
		return nil, fmt.Errorf("manifest: list: %w", err)
	}
	defer rows.Close()
	var out []Segment
	for rows.Next() {
		var s Segment
		var created int64
		var st string
		if err := rows.Scan(&s.ID, &s.Key, &s.MinTS, &s.MaxTS, &s.Count, &s.Size, &st, &created); err != nil {
			return nil, fmt.Errorf("manifest: scan: %w", err)
		}
		s.Status = Status(st)
		s.CreatedAt = time.Unix(0, created)
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("manifest: list: %w", err)
	}
	return out, nil
}

// isConstraintViolation reports whether err is a SQLite constraint failure
// (primary key / unique violation) rather than an I/O or syntax problem.
// SQLite's result code for constraint failures is 19 (SQLITE_CONSTRAINT);
// extended codes keep 19 in the low byte.
func isConstraintViolation(err error) bool {
	var coder interface{ Code() int }
	return errors.As(err, &coder) && coder.Code()&0xff == 19
}
