// Package ingest accepts log entries and turns them into sealed segments.
//
// Entries arrive one at a time but segments are built in batches (one index
// and one bloom filter per batch), so something has to collect entries in
// memory until a batch is worth sealing. That is Buffer.
package ingest

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/dharmikchandel/strata/internal/segment"
	"github.com/dharmikchandel/strata/internal/storage"
)

const (
	DefaultMaxBytes = 4 << 20
	DefaultMaxAge   = 5 * time.Second

	// maxPendingFactor bounds memory when sealing keeps failing: once the
	// buffer holds this many multiples of MaxBytes, Add pushes back with
	// ErrBufferFull instead of growing without limit.
	maxPendingFactor = 4

	// SegmentPrefix is the storage key prefix for sealed segments.
	SegmentPrefix = "segments/"
)

var (
	// ErrClosed is returned by Add after Close. The entry was not accepted.
	ErrClosed = errors.New("ingest: buffer closed")
	// ErrBufferFull means sealing is failing and the buffer hit its memory
	// cap. The entry was not accepted.
	ErrBufferFull = errors.New("ingest: buffer full")
	// ErrSealFailed wraps errors from a seal attempt triggered by AddBatch.
	// The batch that triggered it was still accepted.
	ErrSealFailed = errors.New("ingest: seal failed")
)

// SealedSegment describes a segment that was durably written to storage.
type SealedSegment struct {
	ID    string
	Key   string
	Count int
	MinTS int64
	MaxTS int64
	Size  int64 // bytes of the segment file
}

// Config controls a Buffer.
type Config struct {
	Storage storage.Storage

	// A batch is sealed when its approximate size reaches MaxBytes, or when
	// its oldest entry has waited MaxAge, whichever happens first.
	// Zero values use the defaults above.
	MaxBytes int
	MaxAge   time.Duration

	// OnSeal, if set, is called after each segment is durably stored. If it
	// returns an error the seal is treated as failed: the stored object is
	// deleted and the entries go back into the buffer. (This is the hook the
	// manifest uses to record the segment.) It may be called concurrently.
	OnSeal func(SealedSegment) error

	// OnError, if set, receives errors from background (timer-driven) seals,
	// which have no caller to return them to.
	OnError func(error)
}

type batch struct {
	entries []segment.Entry
	bytes   int
	firstAt time.Time // when the oldest entry in the batch was added
}

// Buffer collects entries and seals them into segments. It is safe for
// concurrent use.
//
// Concurrency design: a mutex guards only the in-memory batch. When a batch
// is due, the lock is held just long enough to swap it out for an empty one;
// the slow work (encoding, writing to storage) happens outside the lock. So
// writers keep appending to the new batch while an old one is being written.
// The goroutine whose Add crossed the size threshold performs the seal itself,
// which gives natural backpressure: a producer that fills batches faster than
// storage can absorb them is the one that gets slowed down.
type Buffer struct {
	cfg Config

	mu      sync.Mutex
	pending batch
	closed  bool

	stop chan struct{} // closed by Close to stop the timer goroutine
	done chan struct{} // closed when the timer goroutine has exited
}

// NewBuffer validates cfg, applies defaults, and starts the age-based flusher.
func NewBuffer(cfg Config) (*Buffer, error) {
	if cfg.Storage == nil {
		return nil, errors.New("ingest: Config.Storage is required")
	}
	if cfg.MaxBytes < 0 || cfg.MaxAge < 0 {
		return nil, errors.New("ingest: MaxBytes and MaxAge must not be negative")
	}
	if cfg.MaxBytes == 0 {
		cfg.MaxBytes = DefaultMaxBytes
	}
	if cfg.MaxAge == 0 {
		cfg.MaxAge = DefaultMaxAge
	}
	b := &Buffer{cfg: cfg, stop: make(chan struct{}), done: make(chan struct{})}
	go b.flushLoop()
	return b, nil
}

// Add buffers one entry. It is AddBatch with a single entry.
func (b *Buffer) Add(ctx context.Context, e segment.Entry) error {
	return b.AddBatch(ctx, []segment.Entry{e})
}

// AddBatch buffers several entries as one all-or-nothing unit, sealing a
// segment if the size threshold is reached.
//
// All-or-nothing means: if any entry is invalid, or the buffer is closed or
// full, NONE of the entries are added. A caller never has to work out which
// part of a batch was taken. It also takes the lock once per batch instead of
// once per entry.
//
// Error contract:
//   - ErrClosed, ErrBufferFull and validation errors: the batch was NOT accepted.
//   - an error wrapping ErrSealFailed: the batch IS accepted (buffered, not
//     lost), but a seal attempt triggered by it failed. The seal will be
//     retried by later calls, the timer, or Flush. Use Accepted(err) to tell
//     the two apart.
func (b *Buffer) AddBatch(ctx context.Context, entries []segment.Entry) error {
	if len(entries) == 0 {
		return nil
	}
	for _, e := range entries {
		if err := e.Validate(); err != nil {
			return err
		}
	}
	// Copy tag maps: the caller may reuse or mutate them after we return, and
	// the entries will sit in memory for a while before being sealed.
	owned := make([]segment.Entry, len(entries))
	size := 0
	for i, e := range entries {
		if e.Tags != nil {
			tags := make(map[string]string, len(e.Tags))
			for k, v := range e.Tags {
				tags[k] = v
			}
			e.Tags = tags
		}
		owned[i] = e
		size += entrySize(e)
	}

	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return ErrClosed
	}
	if b.pending.bytes >= b.cfg.MaxBytes*maxPendingFactor {
		b.mu.Unlock()
		return ErrBufferFull
	}
	if len(b.pending.entries) == 0 {
		b.pending.firstAt = time.Now()
	}
	b.pending.entries = append(b.pending.entries, owned...)
	b.pending.bytes += size
	var full *batch
	if b.pending.bytes >= b.cfg.MaxBytes {
		full = b.takeLocked()
	}
	b.mu.Unlock()

	if full != nil {
		if err := b.seal(ctx, full); err != nil {
			return fmt.Errorf("%w: %w", ErrSealFailed, err)
		}
	}
	return nil
}

// Accepted reports whether err from Add/AddBatch still means the entries were
// accepted (nil, or a failed seal) as opposed to rejected.
func Accepted(err error) bool {
	return err == nil || errors.Is(err, ErrSealFailed)
}

// Flush seals whatever is buffered right now. It is a no-op if empty.
func (b *Buffer) Flush(ctx context.Context) error {
	b.mu.Lock()
	taken := b.takeLocked()
	b.mu.Unlock()
	if taken == nil {
		return nil
	}
	return b.seal(ctx, taken)
}

// Close stops the timer and seals the remaining entries. After Close, Add
// returns ErrClosed. Calling Close again is a no-op.
func (b *Buffer) Close(ctx context.Context) error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.closed = true
	b.mu.Unlock()

	close(b.stop)
	<-b.done
	return b.Flush(ctx)
}

// takeLocked swaps the pending batch for an empty one. b.mu must be held.
func (b *Buffer) takeLocked() *batch {
	if len(b.pending.entries) == 0 {
		return nil
	}
	taken := b.pending
	b.pending = batch{}
	return &taken
}

// requeue puts a batch whose seal failed back at the front of the buffer so
// nothing is lost. The old batch keeps its original firstAt, so the age
// trigger fires again immediately and the seal is retried on the next tick.
func (b *Buffer) requeue(failed *batch) {
	b.mu.Lock()
	defer b.mu.Unlock()
	merged := make([]segment.Entry, 0, len(failed.entries)+len(b.pending.entries))
	merged = append(merged, failed.entries...)
	merged = append(merged, b.pending.entries...)
	b.pending = batch{
		entries: merged,
		bytes:   failed.bytes + b.pending.bytes,
		firstAt: failed.firstAt,
	}
}

// seal encodes a batch, writes it to storage, and reports it via OnSeal.
// On any failure the batch is requeued.
func (b *Buffer) seal(ctx context.Context, bt *batch) error {
	if err := b.write(ctx, bt); err != nil {
		b.requeue(bt)
		return err
	}
	return nil
}

func (b *Buffer) write(ctx context.Context, bt *batch) error {
	data, err := segment.Encode(bt.entries)
	if err != nil {
		return fmt.Errorf("ingest: encode segment: %w", err)
	}
	info := SealedSegment{
		ID:    newSegmentID(),
		Count: len(bt.entries),
		MinTS: bt.entries[0].Timestamp,
		MaxTS: bt.entries[0].Timestamp,
		Size:  int64(len(data)),
	}
	for _, e := range bt.entries {
		info.MinTS = min(info.MinTS, e.Timestamp)
		info.MaxTS = max(info.MaxTS, e.Timestamp)
	}
	info.Key = SegmentPrefix + info.ID + ".strata"

	if err := b.cfg.Storage.Put(ctx, info.Key, bytes.NewReader(data)); err != nil {
		return fmt.Errorf("ingest: store segment: %w", err)
	}
	if b.cfg.OnSeal != nil {
		if err := b.cfg.OnSeal(info); err != nil {
			// The segment must not exist unless it was recorded. Remove it
			// (best effort, and with a fresh context in case ctx is what failed)
			// and let the entries go back to the buffer.
			_ = b.cfg.Storage.Delete(context.WithoutCancel(ctx), info.Key)
			return fmt.Errorf("ingest: record segment: %w", err)
		}
	}
	return nil
}

// flushLoop seals batches whose oldest entry has waited MaxAge.
func (b *Buffer) flushLoop() {
	defer close(b.done)
	interval := max(b.cfg.MaxAge/4, time.Millisecond)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-b.stop:
			return
		case <-t.C:
		}
		b.mu.Lock()
		var due *batch
		if len(b.pending.entries) > 0 && time.Since(b.pending.firstAt) >= b.cfg.MaxAge {
			due = b.takeLocked()
		}
		b.mu.Unlock()
		if due == nil {
			continue
		}
		if err := b.seal(context.Background(), due); err != nil && b.cfg.OnError != nil {
			b.cfg.OnError(err)
		}
	}
}

// entrySize approximates an entry's in-memory/on-disk footprint. It only
// needs to be proportional, since it drives the size threshold.
func entrySize(e segment.Entry) int {
	n := 8 + len(e.Message)
	for k, v := range e.Tags {
		n += len(k) + len(v)
	}
	return n
}

// newSegmentID returns a unique, roughly time-sortable ID: the seal time in
// nanoseconds (hex, fixed width) plus random bytes so two segments sealed in
// the same nanosecond, or by two processes, never collide.
func newSegmentID() string {
	var r [4]byte
	if _, err := rand.Read(r[:]); err != nil {
		panic(err) // crypto/rand failing means the OS is broken
	}
	return fmt.Sprintf("%016x-%s", time.Now().UnixNano(), hex.EncodeToString(r[:]))
}
