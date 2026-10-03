// Package compact merges many small segments into fewer large ones, and
// cleans up the files that merging and crashes leave behind.
//
// Why compaction exists: the ingest buffer seals a segment every few seconds,
// so a busy system accumulates thousands of tiny segments. Every query pays
// per-segment costs (a manifest row, a bloom check, possibly a fetch), so
// many small segments make queries slower. Merging them keeps the count down.
//
// The order of steps is the whole safety argument. For one merge:
//
//  1. read the input segments
//  2. write the merged segment to storage
//  3. read it back and check it                     <- "durably written"
//  4. ONE manifest transaction: add merged, mark inputs deleted   <- the commit
//  5. delete the input files
//
// The manifest transaction (step 4) is the single moment the system switches
// from "the inputs are the data" to "the merged segment is the data". A crash
// before it leaves the inputs untouched (plus a harmless unrecorded file); a
// crash after it leaves the merged segment live (plus leftover input files
// that nothing reads). Either way, no data is lost and none is counted twice.
// Leftovers are removed later by GarbageCollect.
package compact

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/dharmikchandel/strata/internal/manifest"
	"github.com/dharmikchandel/strata/internal/segment"
	"github.com/dharmikchandel/strata/internal/storage"
)

const (
	DefaultSmallBytes  = 8 << 20
	DefaultTargetBytes = 64 << 20
	DefaultMinSegments = 4
	DefaultMaxSegments = 32
	DefaultInterval    = 30 * time.Second
	DefaultGCInterval  = 10 * time.Minute
	// DefaultOrphanGrace must be longer than the slowest possible seal (a
	// storage write plus the manifest insert). Younger unrecorded files may
	// belong to a segment that is being written right now.
	DefaultOrphanGrace = 10 * time.Minute
)

// Config controls a Compactor. Zero values use the defaults above.
type Config struct {
	Manifest *manifest.Manifest
	Storage  storage.Storage

	// Only segments smaller than SmallBytes are merged, so big, finished
	// segments aren't rewritten over and over.
	SmallBytes int64
	// A merge stops adding segments once the total would pass TargetBytes.
	// This also bounds memory, since a merge holds its input in memory.
	TargetBytes int64
	// A merge needs at least MinSegments inputs (merging 1 is pointless, and
	// merging 2 rewrites a lot of data for little gain) and takes at most MaxSegments.
	MinSegments int
	MaxSegments int

	Interval    time.Duration // how often Run looks for work
	GCInterval  time.Duration // how often Run garbage-collects
	OrphanGrace time.Duration

	Logger *slog.Logger
}

// Report describes one successful merge.
type Report struct {
	InputIDs     []string
	OutputID     string
	Entries      int
	BytesIn      int64
	BytesOut     int64
	DeleteErrors int // input files that could not be deleted (GC will retry)
}

// Compactor merges segments. Methods are safe for concurrent use, but merges
// run one at a time.
type Compactor struct {
	cfg Config
	log *slog.Logger

	mu sync.Mutex // serializes CompactOnce
	// bad remembers segments that failed to load (missing or corrupt). They
	// are left out of future merges so one broken segment can't block
	// compaction forever. It is in memory only: after a restart they are
	// tried again.
	bad map[string]error

	// stuckWarnedFor remembers the last "compaction is stuck" warning so it is
	// logged once per situation instead of on every tick.
	stuckWarnedFor int

	// crashHook, if set, is called at named points so tests can stop the
	// process "mid-merge". Always nil in production.
	crashHook func(stage string)
}

// New returns a Compactor.
func New(cfg Config) (*Compactor, error) {
	if cfg.Manifest == nil || cfg.Storage == nil {
		return nil, errors.New("compact: Config.Manifest and Config.Storage are required")
	}
	if cfg.SmallBytes == 0 {
		cfg.SmallBytes = DefaultSmallBytes
	}
	if cfg.TargetBytes == 0 {
		cfg.TargetBytes = DefaultTargetBytes
	}
	if cfg.MinSegments == 0 {
		cfg.MinSegments = DefaultMinSegments
	}
	if cfg.MaxSegments == 0 {
		cfg.MaxSegments = DefaultMaxSegments
	}
	if cfg.Interval == 0 {
		cfg.Interval = DefaultInterval
	}
	if cfg.GCInterval == 0 {
		cfg.GCInterval = DefaultGCInterval
	}
	if cfg.OrphanGrace == 0 {
		cfg.OrphanGrace = DefaultOrphanGrace
	}
	if cfg.MinSegments < 2 || cfg.MaxSegments < cfg.MinSegments {
		return nil, errors.New("compact: need 2 <= MinSegments <= MaxSegments")
	}
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Compactor{cfg: cfg, log: log, bad: map[string]error{}}, nil
}

func (c *Compactor) crash(stage string) {
	if c.crashHook != nil {
		c.crashHook(stage)
	}
}

// pickBatch chooses which segments to merge: the oldest small segments (by
// time range, so the result covers a tight window and still lets time-range
// queries skip it), up to MaxSegments and TargetBytes. It returns nil if
// there aren't enough to be worth merging.
func (c *Compactor) pickBatch(active []manifest.Segment) []manifest.Segment {
	var batch []manifest.Segment
	var total int64
	for _, s := range active { // List returns segments ordered by min timestamp
		if s.Size >= c.cfg.SmallBytes {
			continue
		}
		if _, broken := c.bad[s.ID]; broken {
			continue
		}
		if len(batch) > 0 && total+s.Size > c.cfg.TargetBytes {
			break
		}
		batch = append(batch, s)
		total += s.Size
		if len(batch) == c.cfg.MaxSegments {
			break
		}
	}
	if len(batch) < c.cfg.MinSegments {
		return nil
	}
	return batch
}

// warnIfStuck logs when there are enough small segments to merge but the size
// settings make a merge impossible. The usual cause is TargetBytes being too
// small: if only fewer than MinSegments segments fit under it, no batch ever
// qualifies, compaction silently does nothing, and the segment count grows
// without limit. (This was found in practice by lowering TargetBytes.)
func (c *Compactor) warnIfStuck(active []manifest.Segment) {
	small := 0
	var smallest int64
	for _, s := range active {
		if s.Size >= c.cfg.SmallBytes {
			continue
		}
		if _, broken := c.bad[s.ID]; broken {
			continue
		}
		if small == 0 || s.Size < smallest {
			smallest = s.Size
		}
		small++
	}
	if small < c.cfg.MinSegments || small == c.stuckWarnedFor {
		return
	}
	c.stuckWarnedFor = small
	c.log.Warn("compaction is stuck: there are enough small segments to merge, but no batch fits the size limits. "+
		"Raise compact-target-bytes (it must fit at least min-segments segments) or lower compact-min-segments",
		"small_segments", small, "min_segments", c.cfg.MinSegments,
		"target_bytes", c.cfg.TargetBytes, "smallest_small_segment_bytes", smallest)
}

// CompactOnce performs at most one merge. It returns a nil Report (and nil
// error) when there is nothing worth merging.
func (c *Compactor) CompactOnce(ctx context.Context) (*Report, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	active, err := c.cfg.Manifest.List(ctx, manifest.StatusActive)
	if err != nil {
		return nil, err
	}
	batch := c.pickBatch(active)
	if batch == nil {
		c.warnIfStuck(active)
		return nil, nil
	}
	c.stuckWarnedFor = 0

	// 1. Read the inputs.
	var entries []segment.Entry
	var bytesIn int64
	for _, s := range batch {
		es, n, err := c.load(ctx, s)
		if err != nil {
			if ctx.Err() == nil {
				c.bad[s.ID] = err
				c.log.Warn("excluding unreadable segment from compaction", "segment", s.ID, "err", err)
			}
			return nil, fmt.Errorf("compact: read input %s: %w", s.ID, err)
		}
		entries = append(entries, es...)
		bytesIn += n
	}
	c.crash("inputs-read")

	// 2. Write the merged segment.
	data, err := segment.Encode(entries)
	if err != nil {
		return nil, fmt.Errorf("compact: encode merged segment: %w", err)
	}
	bloomBytes, err := segment.ReadBloom(data)
	if err != nil {
		return nil, fmt.Errorf("compact: %w", err)
	}
	merged, err := segment.Decode(data)
	if err != nil {
		return nil, fmt.Errorf("compact: %w", err)
	}
	row := manifest.Segment{
		ID:    segment.NewID(),
		MinTS: merged.MinTimestamp(), MaxTS: merged.MaxTimestamp(),
		Count: merged.Len(), Size: int64(len(data)), Bloom: bloomBytes,
	}
	row.Key = segment.KeyForID(row.ID)
	if err := c.cfg.Storage.Put(ctx, row.Key, bytes.NewReader(data)); err != nil {
		// Nothing was committed. A partly written object, if any, is an
		// unrecorded orphan that GarbageCollect removes.
		return nil, fmt.Errorf("compact: write merged segment: %w", err)
	}
	c.crash("merged-written")

	// 3. Read it back and check it, before anything depends on it.
	if err := c.verify(ctx, row); err != nil {
		c.discard(row.Key)
		return nil, fmt.Errorf("compact: merged segment failed verification: %w", err)
	}
	c.crash("merged-verified")

	// 4. The commit: one transaction swaps the inputs for the merged segment.
	ids := make([]string, len(batch))
	for i, s := range batch {
		ids[i] = s.ID
	}
	if err := c.cfg.Manifest.Replace(ctx, []manifest.Segment{row}, ids); err != nil {
		// Not committed, so the inputs are still the data. Remove the file we
		// wrote. (If this is ErrNotActive, someone else already replaced an
		// input, and our merged copy must not become visible.)
		c.discard(row.Key)
		return nil, fmt.Errorf("compact: commit: %w", err)
	}
	c.crash("committed")

	// 5. Only now are the old files unreferenced and safe to delete. Failures
	// are not fatal: the rows are marked deleted, and GarbageCollect retries.
	rep := &Report{InputIDs: ids, OutputID: row.ID, Entries: len(entries), BytesIn: bytesIn, BytesOut: row.Size}
	deleted := make([]string, 0, len(batch))
	for _, s := range batch {
		if err := c.cfg.Storage.Delete(ctx, s.Key); err != nil {
			rep.DeleteErrors++
			c.log.Warn("could not delete replaced segment file; GC will retry", "segment", s.ID, "err", err)
			continue
		}
		deleted = append(deleted, s.ID)
	}
	if err := c.cfg.Manifest.PurgeDeleted(ctx, deleted); err != nil {
		c.log.Warn("could not purge replaced segment rows; GC will retry", "err", err)
	}
	return rep, nil
}

// load reads and decodes one input segment, returning its entries and size.
func (c *Compactor) load(ctx context.Context, s manifest.Segment) ([]segment.Entry, int64, error) {
	r, err := c.cfg.Storage.Get(ctx, s.Key)
	if err != nil {
		return nil, 0, err
	}
	data, err := io.ReadAll(r)
	r.Close()
	if err != nil {
		return nil, 0, err
	}
	seg, err := segment.Decode(data)
	if err != nil {
		return nil, 0, err
	}
	// The manifest's count is what queries and sizing trust; disagreement
	// means the row and the file describe different data.
	if seg.Len() != s.Count {
		return nil, 0, fmt.Errorf("segment holds %d entries but the manifest says %d", seg.Len(), s.Count)
	}
	es, err := seg.Entries()
	return es, int64(len(data)), err
}

// verify re-reads the merged segment from storage and checks it is intact
// and matches what we intended to write. A successful Put is normally enough,
// but a silent write fault, a misbehaving store or a bug in our encoding would
// otherwise only be found after the originals were already replaced.
func (c *Compactor) verify(ctx context.Context, row manifest.Segment) error {
	r, err := c.cfg.Storage.Get(ctx, row.Key)
	if err != nil {
		return err
	}
	data, err := io.ReadAll(r)
	r.Close()
	if err != nil {
		return err
	}
	seg, err := segment.Decode(data)
	if err != nil {
		return err
	}
	if seg.Len() != row.Count || int64(len(data)) != row.Size {
		return fmt.Errorf("read back %d entries / %d bytes, wrote %d / %d", seg.Len(), len(data), row.Count, row.Size)
	}
	return nil
}

// discard removes a file that must not become visible. Best effort: if it
// fails, the file is an orphan and GarbageCollect handles it. A fresh context
// is used so cancellation of the merge doesn't also cancel the cleanup.
func (c *Compactor) discard(key string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := c.cfg.Storage.Delete(ctx, key); err != nil {
		c.log.Warn("could not remove unrecorded segment; GC will retry", "key", key, "err", err)
	}
}

// GCReport describes one garbage-collection pass.
type GCReport struct {
	ReplacedFilesDeleted int // files of segments the manifest marks deleted
	OrphansDeleted       int // old files the manifest has never heard of
	OrphansTooYoung      int // unrecorded files kept because they may still be in flight
}

// GarbageCollect removes files nothing needs any more:
//
//   - files of segments the manifest marks deleted (a merge committed but
//     crashed, or failed to delete them), and then those manifest rows;
//   - files in storage that the manifest doesn't know at all and that are older
//     than OrphanGrace (a crash between writing a segment and recording it, a
//     retried upload, a merge that failed after writing its output).
//
// It never touches files of active segments, and never touches a young
// unrecorded file, because a segment is written to storage a moment before it
// is recorded in the manifest and deleting it in that window would lose data.
func (c *Compactor) GarbageCollect(ctx context.Context) (*GCReport, error) {
	rep := &GCReport{}

	deletedRows, err := c.cfg.Manifest.List(ctx, manifest.StatusDeleted)
	if err != nil {
		return nil, err
	}
	var purged []string
	for _, s := range deletedRows {
		if err := c.cfg.Storage.Delete(ctx, s.Key); err != nil {
			c.log.Warn("gc: could not delete replaced segment", "segment", s.ID, "err", err)
			continue
		}
		purged = append(purged, s.ID)
		rep.ReplacedFilesDeleted++
	}
	if err := c.cfg.Manifest.PurgeDeleted(ctx, purged); err != nil {
		return rep, err
	}

	// Orphans. Order matters: list storage FIRST, then read the manifest. A
	// segment that is recorded between the two steps is already in the listing
	// and must therefore also be in the manifest we read next. (Reading the
	// manifest first could make a just-recorded segment look unrecorded. The
	// age check below is a second line of defence for the same race.)
	keys, err := c.cfg.Storage.List(ctx, segment.KeyPrefix)
	if err != nil {
		return rep, err
	}
	known := map[string]bool{}
	for _, st := range []manifest.Status{manifest.StatusActive, manifest.StatusDeleted} {
		rows, err := c.cfg.Manifest.List(ctx, st)
		if err != nil {
			return rep, err
		}
		for _, r := range rows {
			known[r.Key] = true
		}
	}
	for _, key := range keys {
		if known[key] {
			continue
		}
		created, ok := segment.CreatedAt(key)
		if !ok {
			continue // not named like one of our segments: not ours to delete
		}
		if time.Since(created) < c.cfg.OrphanGrace {
			rep.OrphansTooYoung++
			continue
		}
		if err := c.cfg.Storage.Delete(ctx, key); err != nil {
			c.log.Warn("gc: could not delete orphan", "key", key, "err", err)
			continue
		}
		rep.OrphansDeleted++
	}
	return rep, nil
}

// Run compacts and garbage-collects in the background until ctx is cancelled.
// Errors are logged, and the loop carries on: a transient storage failure
// must not stop compaction for good.
func (c *Compactor) Run(ctx context.Context) {
	tick := time.NewTicker(c.cfg.Interval)
	defer tick.Stop()
	nextGC := time.Now() // collect once at startup, to clear a previous crash's leftovers
	for {
		// Merge until there is nothing left to do (or something fails).
		for ctx.Err() == nil {
			rep, err := c.CompactOnce(ctx)
			if err != nil {
				if ctx.Err() == nil {
					c.log.Error("compaction failed", "err", err)
				}
				break
			}
			if rep == nil {
				break
			}
			c.log.Info("compacted", "inputs", len(rep.InputIDs), "output", rep.OutputID, "entries", rep.Entries, "bytes_in", rep.BytesIn, "bytes_out", rep.BytesOut)
		}
		if ctx.Err() == nil && !time.Now().Before(nextGC) {
			if rep, err := c.GarbageCollect(ctx); err != nil && ctx.Err() == nil {
				c.log.Error("garbage collection failed", "err", err)
			} else if rep != nil && (rep.ReplacedFilesDeleted+rep.OrphansDeleted > 0) {
				c.log.Info("garbage collected", "replaced", rep.ReplacedFilesDeleted, "orphans", rep.OrphansDeleted)
			}
			nextGC = time.Now().Add(c.cfg.GCInterval)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}
