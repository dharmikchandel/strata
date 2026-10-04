package gen

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	stratav1 "github.com/dharmikchandel/strata/gen/strata/v1"
	"github.com/dharmikchandel/strata/internal/bench"
)

const (
	attemptTimeout = 30 * time.Second // one request and its reply must not hang forever
	backoffStart   = 50 * time.Millisecond
	backoffMax     = 2 * time.Second
	// A verification window should hold well under the 10,000 lines one search
	// can return, with room for late lines landing in old windows.
	targetPerWindow = 6000
)

// Report is what a run did.
type Report struct {
	RunID   string
	Profile string
	Started time.Time
	Ended   time.Time
	Sent    int64 // lines handed to the sender
	Acked   int64 // lines the server acknowledged (in its buffer, not necessarily sealed yet)
	// BufferFull counts replies telling the generator to back off and retry.
	BufferFull int64
	// ResentBatches counts batches sent again because a reply never came (a
	// broken stream). Each one risks a duplicate: the first copy may have been stored.
	ResentBatches int64
	Reconnects    int64
	// BacklogMax is the most lines that were due but not yet sent at one moment.
	// Anything above a tick's worth means the generator, or the server, fell behind.
	BacklogMax             int64
	AckP50, AckP99, AckMax time.Duration

	// Kept for verification.
	BucketWidth   time.Duration
	Expected      map[int64]int // acknowledged lines per time bucket (bucket = timestamp / BucketWidth)
	AckedBySource []uint64
	MinTS, MaxTS  int64

	Verify *VerifyResult
}

func (r *Report) String() string {
	d := r.Ended.Sub(r.Started).Round(time.Millisecond)
	s := fmt.Sprintf("run %s (%s profile) for %s\n  sent %d, acknowledged %d (%.0f lines/s)\n  ack latency p50 %s, p99 %s, max %s\n  backoffs (buffer full) %d, resent batches %d, reconnects %d, max backlog %d lines",
		r.RunID, r.Profile, d, r.Sent, r.Acked, float64(r.Acked)/math.Max(d.Seconds(), 0.001),
		r.AckP50.Round(100*time.Microsecond), r.AckP99.Round(100*time.Microsecond), r.AckMax.Round(100*time.Microsecond),
		r.BufferFull, r.ResentBatches, r.Reconnects, r.BacklogMax)
	if r.Verify != nil {
		s += "\n" + r.Verify.String()
	}
	return s
}

type stats struct {
	sent, acked, bufferFull, resent, reconnects atomic.Int64
	backlogMax                                  atomic.Int64 // max since the last report line
	backlogMaxAll                               atomic.Int64
	mu                                          sync.Mutex
	latInterval, latAll                         []time.Duration
}

func (s *stats) observeBacklog(n int64) {
	for {
		cur := s.backlogMax.Load()
		if n <= cur || s.backlogMax.CompareAndSwap(cur, n) {
			break
		}
	}
	for {
		cur := s.backlogMaxAll.Load()
		if n <= cur || s.backlogMaxAll.CompareAndSwap(cur, n) {
			return
		}
	}
}

func (s *stats) observeLatency(d time.Duration) {
	s.mu.Lock()
	s.latInterval = append(s.latInterval, d)
	s.latAll = append(s.latAll, d)
	s.mu.Unlock()
}

// bucketWidth picks the verification window: short enough that one window never
// holds more lines than a single search can return.
func bucketWidth(c Config) time.Duration {
	w := time.Duration(float64(time.Second) * targetPerWindow / c.Schedule.Peak())
	w = w.Round(100 * time.Millisecond)
	switch {
	case w < 100*time.Millisecond:
		w = 100 * time.Millisecond
	case w > time.Minute:
		w = time.Minute
	}
	return w
}

// Run sends logs until cfg.Duration passes or ctx is cancelled, then (if asked)
// verifies the run. Cancelling ctx stops generating new lines; a batch already
// in flight is allowed up to cfg.Grace to be delivered.
func Run(ctx context.Context, cfg Config) (*Report, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	content, err := NewContent()
	if err != nil {
		return nil, err
	}
	conn, err := grpc.NewClient(cfg.Addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	var idBytes [4]byte
	if _, err := rand.Read(idBytes[:]); err != nil {
		return nil, err
	}
	runID := "run" + hex.EncodeToString(idBytes[:])
	width := bucketWidth(cfg)

	// ctx ends when we should stop generating. The send context outlives it by
	// Grace, so the last batch is not abandoned half delivered.
	runCtx := ctx
	if cfg.Duration > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, cfg.Duration)
		defer cancel()
	}
	sendCtx, cancelSend := context.WithCancel(context.Background())
	defer cancelSend()
	context.AfterFunc(runCtx, func() { time.AfterFunc(cfg.Grace, cancelSend) })

	st := &stats{}
	start := time.Now()
	client := stratav1.NewIngestServiceClient(conn)
	cfg.Logger.Info("starting", "run", runID, "profile", cfg.Profile, "rate", cfg.Schedule.Rate, "sources", cfg.Sources,
		"burst_factor", cfg.Schedule.BurstFactor, "late_fraction", cfg.LateFraction, "duration", cfg.Duration, "server", cfg.Addr)

	runners := make([]*runner, cfg.Sources)
	errs := make(chan error, cfg.Sources)
	var wg sync.WaitGroup
	for i := range runners {
		runners[i] = &runner{cfg: cfg, client: client, st: st, width: width,
			src: content.NewSource(cfg.Seed, i, runID), expected: map[int64]int{}, minTS: math.MaxInt64}
		wg.Add(1)
		go func(r *runner) {
			defer wg.Done()
			if err := r.run(runCtx, sendCtx, start); err != nil {
				errs <- err
			}
		}(runners[i])
	}

	stopReport := make(chan struct{})
	go reportLoop(cfg, st, start, stopReport)
	wg.Wait()
	close(stopReport)
	close(errs)

	rep := &Report{RunID: runID, Profile: cfg.Profile, Started: start, Ended: time.Now(),
		Sent: st.sent.Load(), Acked: st.acked.Load(), BufferFull: st.bufferFull.Load(), ResentBatches: st.resent.Load(),
		Reconnects: st.reconnects.Load(), BacklogMax: st.backlogMaxAll.Load(),
		BucketWidth: width, Expected: map[int64]int{}, MinTS: math.MaxInt64}
	st.mu.Lock()
	rep.AckP50, rep.AckP99, rep.AckMax = bench.Percentile(st.latAll, 50), bench.Percentile(st.latAll, 99), bench.Percentile(st.latAll, 100)
	st.mu.Unlock()
	for _, r := range runners {
		rep.AckedBySource = append(rep.AckedBySource, r.acked)
		for b, n := range r.expected {
			rep.Expected[b] += n
		}
		rep.MinTS, rep.MaxTS = min(rep.MinTS, r.minTS), max(rep.MaxTS, r.maxTS)
	}

	var runErr error
	for e := range errs {
		runErr = errors.Join(runErr, e)
	}
	if runErr != nil {
		return rep, runErr
	}

	if cfg.Verify {
		cfg.Logger.Info("run finished; verifying", "settle", cfg.VerifySettle)
		select {
		case <-time.After(cfg.VerifySettle):
		case <-sendCtx.Done():
		}
		res, err := Verify(context.Background(), stratav1.NewQueryServiceClient(conn), rep, VerifyOptions{
			Workers: cfg.VerifyWorkers, Settle: cfg.VerifySettle, Logger: cfg.Logger})
		if err != nil {
			return rep, fmt.Errorf("verification: %w", err)
		}
		rep.Verify = res
	}
	return rep, nil
}

func reportLoop(cfg Config, st *stats, start time.Time, stop <-chan struct{}) {
	t := time.NewTicker(cfg.Report)
	defer t.Stop()
	var lastAcked int64
	last := start
	for {
		select {
		case <-stop:
			return
		case now := <-t.C:
			acked := st.acked.Load()
			st.mu.Lock()
			p50, p99 := bench.Percentile(st.latInterval, 50), bench.Percentile(st.latInterval, 99)
			st.latInterval = st.latInterval[:0]
			st.mu.Unlock()
			cfg.Logger.Info("progress",
				"elapsed", now.Sub(start).Round(time.Second).String(),
				"acked", acked, "rate", fmt.Sprintf("%.0f/s", float64(acked-lastAcked)/now.Sub(last).Seconds()),
				"ack_p50", p50.Round(100*time.Microsecond).String(), "ack_p99", p99.Round(100*time.Microsecond).String(),
				"backlog_max", st.backlogMax.Swap(0), "buffer_full", st.bufferFull.Load(),
				"resent", st.resent.Load(), "reconnects", st.reconnects.Load())
			lastAcked, last = acked, now
		}
	}
}

// runner is one stream: it owns a Source, a connection, and the accounting of
// which of its lines were acknowledged.
type runner struct {
	cfg    Config
	client stratav1.IngestServiceClient
	st     *stats
	src    *Source
	width  time.Duration

	stream       stratav1.IngestService_IngestClient
	cancelStream context.CancelFunc
	connected    bool
	batchID      uint64

	acked        uint64
	expected     map[int64]int
	minTS, maxTS int64
}

// run is the open-loop send loop for one stream.
func (r *runner) run(runCtx, sendCtx context.Context, start time.Time) error {
	defer r.dropStream()
	tick := time.NewTicker(r.cfg.Tick)
	defer tick.Stop()
	share := 1 / float64(r.cfg.Sources)
	var produced int64
	last := start
	for {
		stopping := false
		select {
		case <-runCtx.Done():
			stopping = true
		case <-tick.C:
		}
		now := time.Now()
		due := int64(r.cfg.Schedule.Cumulative(now.Sub(start))*share) - produced
		if due > 0 {
			r.st.observeBacklog(due)
			// Spread the lines over the time since the last tick, so their
			// timestamps are what a real service would have logged, not one instant.
			step := now.Sub(last) / time.Duration(due)
			for sent := int64(0); sent < due; {
				n := min(int64(r.cfg.Batch), due-sent)
				entries := make([]*stratav1.LogEntry, n)
				for i := range entries {
					ts := last.Add(step * time.Duration(sent+int64(i)+1))
					if r.cfg.LateFraction > 0 && r.src.rng.Float64() < r.cfg.LateFraction {
						ts = ts.Add(-time.Duration(r.src.rng.Float64() * float64(r.cfg.LateMax)))
					}
					entries[i] = r.src.Next(ts)
				}
				r.st.sent.Add(n)
				if err := r.sendBatch(sendCtx, entries); err != nil {
					return fmt.Errorf("source %d: %w", r.src.ID, err)
				}
				sent += n
			}
			produced += due
			last = now
		}
		if stopping {
			return nil
		}
	}
}

// sendBatch delivers one batch, however long it takes (within the send context):
// it reconnects after a broken stream and backs off when the server is full.
func (r *runner) sendBatch(ctx context.Context, entries []*stratav1.LogEntry) error {
	r.batchID++
	backoff := backoffStart
	sleep := func() error {
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return ctx.Err()
		}
		backoff = min(backoff*2, backoffMax)
		return nil
	}
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("gave up on a batch of %d lines: %w", len(entries), err)
		}
		if r.stream == nil {
			if err := r.connect(ctx); err != nil {
				return err
			}
		}
		t0 := time.Now()
		resp, err := r.exchange(entries)
		if err != nil {
			// The reply never came. The batch may or may not have been stored;
			// sending it again can duplicate it, but not sending it can lose it.
			r.dropStream()
			r.st.resent.Add(1)
			if err := sleep(); err != nil {
				return err
			}
			continue
		}
		switch resp.Status {
		case stratav1.IngestStatus_INGEST_STATUS_OK:
			r.st.acked.Add(int64(len(entries)))
			r.st.observeLatency(time.Since(t0))
			r.record(entries)
			return nil
		case stratav1.IngestStatus_INGEST_STATUS_BUFFER_FULL:
			r.st.bufferFull.Add(1)
			if err := sleep(); err != nil {
				return err
			}
		default:
			return fmt.Errorf("the server rejected a batch it should accept: %s %s", resp.Status, resp.Message)
		}
	}
}

func (r *runner) record(entries []*stratav1.LogEntry) {
	r.acked += uint64(len(entries))
	w := int64(r.width)
	for _, e := range entries {
		r.expected[e.TimestampUnixNano/w]++
		r.minTS, r.maxTS = min(r.minTS, e.TimestampUnixNano), max(r.maxTS, e.TimestampUnixNano)
	}
}

func (r *runner) exchange(entries []*stratav1.LogEntry) (*stratav1.IngestResponse, error) {
	// A hung server must not hang us: cancelling the stream makes Send/Recv return.
	timer := time.AfterFunc(attemptTimeout, r.cancelStream)
	defer timer.Stop()
	if err := r.stream.Send(&stratav1.IngestRequest{BatchId: r.batchID, Entries: entries}); err != nil {
		return nil, err
	}
	resp, err := r.stream.Recv()
	if err != nil {
		return nil, err
	}
	if resp.BatchId != r.batchID {
		return nil, fmt.Errorf("reply for batch %d, sent %d", resp.BatchId, r.batchID)
	}
	return resp, nil
}

func (r *runner) connect(ctx context.Context) error {
	streamCtx, cancel := context.WithCancel(ctx)
	// WaitForReady makes the call wait for the server instead of failing at once;
	// the timer bounds how long we are willing to wait.
	timer := time.AfterFunc(r.cfg.ConnectTimeout, cancel)
	stream, err := r.client.Ingest(streamCtx, grpc.WaitForReady(true))
	timer.Stop()
	if err != nil {
		cancel()
		return fmt.Errorf("could not connect to %s within %s: %w", r.cfg.Addr, r.cfg.ConnectTimeout, err)
	}
	if r.connected {
		r.st.reconnects.Add(1)
	}
	r.connected = true
	r.stream, r.cancelStream = stream, cancel
	return nil
}

func (r *runner) dropStream() {
	if r.cancelStream != nil {
		r.cancelStream()
	}
	r.stream, r.cancelStream = nil, nil
}
