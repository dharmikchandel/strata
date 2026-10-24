// Package activity keeps a short record of what the server has been doing:
// segments sealed, merged and cleaned up, and how fast lines are arriving. It
// lives in memory only, so it starts empty when the server starts; it is for
// watching the system work, not an audit log.
package activity

import (
	"context"
	"sync"
	"time"
)

// Kind is what happened.
type Kind int

const (
	// Sealed: the ingest buffer wrote a new segment.
	Sealed Kind = iota + 1
	// Merged: compaction replaced several segments with one.
	Merged
	// Cleaned: garbage collection removed files nothing needed any more.
	Cleaned
)

// Event is one thing that happened. Which fields are set depends on Kind.
type Event struct {
	Seq  uint64 // increases by one per event, from 1
	At   time.Time
	Kind Kind

	Segment string   // Sealed: the new segment; Merged: the merged one
	Inputs  []string // Merged: the segments it replaced
	Lines   int64    // Sealed, Merged
	Bytes   int64    // Sealed: segment size; Merged: merged size
	BytesIn int64    // Merged: total size of the inputs
	Files   int      // Cleaned: files removed
}

// Log is a ring of the most recent events. Safe for concurrent use.
type Log struct {
	mu      sync.Mutex
	events  []Event // oldest first, at most cap(events)
	next    uint64
	started time.Time
}

// NewLog keeps the last capacity events.
func NewLog(capacity int) *Log {
	return &Log{events: make([]Event, 0, capacity), next: 1, started: time.Now()}
}

// Add records e, giving it the next sequence number (and the time, if unset).
func (l *Log) Add(e Event) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e.Seq = l.next
	l.next++
	if e.At.IsZero() {
		e.At = time.Now()
	}
	if len(l.events) == cap(l.events) {
		copy(l.events, l.events[1:])
		l.events = l.events[:len(l.events)-1]
	}
	l.events = append(l.events, e)
}

// Since returns the events after sequence number after, oldest first. A
// client that polls with the last Seq it saw gets each event once; if it
// fell further behind than the ring holds, it gets what is left.
func (l *Log) Since(after uint64) []Event {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []Event
	for _, e := range l.events {
		if e.Seq > after {
			out = append(out, e)
		}
	}
	return out
}

// Started is when the log began: nothing before it is recorded.
func (l *Log) Started() time.Time { return l.started }

// Rate measures how fast a counter grows, from samples taken once per tick
// over a sliding window.
type Rate struct {
	read   func() int64
	window time.Duration

	mu      sync.Mutex
	samples []sample
}

type sample struct {
	at time.Time
	n  int64
}

// NewRate samples read (a counter that only grows) and averages its growth
// over window.
func NewRate(read func() int64, window time.Duration) *Rate {
	return &Rate{read: read, window: window}
}

// Sample takes one sample now. Run calls it once per tick.
func (r *Rate) Sample(now time.Time) {
	n := r.read()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.samples = append(r.samples, sample{now, n})
	// Keep one sample at or before the window's start, so the average spans
	// the whole window.
	for len(r.samples) > 2 && now.Sub(r.samples[1].at) >= r.window {
		r.samples = r.samples[1:]
	}
}

// Run samples every tick until ctx is cancelled.
func (r *Rate) Run(ctx context.Context, tick time.Duration) {
	t := time.NewTicker(tick)
	defer t.Stop()
	r.Sample(time.Now())
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			r.Sample(now)
		}
	}
}

// PerSecond is the average growth per second over the window, or 0 before
// there are two samples.
func (r *Rate) PerSecond() float64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.samples) < 2 {
		return 0
	}
	first, last := r.samples[0], r.samples[len(r.samples)-1]
	span := last.at.Sub(first.at).Seconds()
	if span <= 0 || last.n < first.n {
		return 0
	}
	return float64(last.n-first.n) / span
}

// Monitor is what the server tells its system view: the event log, the
// ingest rate, and how full the ingest buffer is.
type Monitor struct {
	Log        *Log
	Rate       *Rate
	Load       func() (pending, sealAt, refuseAt int)
	SealAfter  time.Duration // a segment is also sealed when its oldest line is this old
	Compaction bool          // whether background compaction runs
}
