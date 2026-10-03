// Command strata-bench measures a running Strata server with a real dataset:
// ingest throughput, then query latency (P50/P99) for several kinds of search,
// together with how many segments each search skipped.
//
// It does not estimate anything: every number it prints was measured by this
// run, and the results file records the machine and settings they came from.
//
//	strata-bench -dataset ~/strata-datasets/BGL.log -out bench/results/run.md
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	stratav1 "github.com/dharmikchandel/strata/gen/strata/v1"
	"github.com/dharmikchandel/strata/internal/bench"
)

type config struct {
	Addr         string        `json:"addr"`
	Dataset      string        `json:"dataset"`
	MaxLines     int           `json:"max_lines"`
	Streams      int           `json:"ingest_streams"`
	Batch        int           `json:"batch_lines"`
	Iterations   int           `json:"iterations_per_query"`
	HeavyIters   int           `json:"iterations_for_full_scan_query"`
	Settle       time.Duration `json:"settle_window"`
	SealWait     time.Duration `json:"-"`
	VerifySample int           `json:"verify_sample"`
	Seed         int64         `json:"seed"`
	Note         string        `json:"deployment_note"`
	Out          string        `json:"-"`
	SkipIngest   bool          `json:"skip_ingest"`
}

type envInfo struct {
	CPU       string `json:"cpu"`
	Cores     int    `json:"logical_cores"`
	MemoryMiB int    `json:"memory_mib"`
	Kernel    string `json:"kernel"`
	Go        string `json:"go"`
	Docker    string `json:"docker"`
	When      string `json:"date"`
}

type ingestResult struct {
	Lines             int64   `json:"lines"`
	SkippedLines      int     `json:"unparseable_lines_skipped"`
	DatasetBytes      int64   `json:"dataset_bytes"`
	Seconds           float64 `json:"seconds_to_last_ack"`
	LinesPerSec       float64 `json:"lines_per_sec"`
	MBPerSec          float64 `json:"dataset_mb_per_sec"`
	ParseOnlySeconds  float64 `json:"read_and_parse_only_seconds"`
	ParseOnlyLinesSec float64 `json:"read_and_parse_only_lines_per_sec"`
	BatchP50Ms        float64 `json:"batch_ack_p50_ms"`
	BatchP99Ms        float64 `json:"batch_ack_p99_ms"`
	BatchMaxMs        float64 `json:"batch_ack_max_ms"`
	BufferFullRetries int64   `json:"buffer_full_retries"`
	MinTimestamp      string  `json:"first_log_time"`
	MaxTimestamp      string  `json:"last_log_time"`
}

type settleResult struct {
	SegmentsAfterIngest int     `json:"segments_right_after_ingest"`
	SegmentsSettled     int     `json:"segments_after_compaction_settled"`
	SecondsToSettle     float64 `json:"seconds_until_settled"`
	StoredBytes         int64   `json:"stored_bytes"`
	Amplification       float64 `json:"stored_bytes_per_dataset_byte"`
}

type verifyResult struct {
	Sampled int `json:"sampled_lines"`
	Found   int `json:"found_exactly"`
}

type queryResult struct {
	Name        string  `json:"name"`
	Description string  `json:"description"`
	N           int     `json:"n"`
	Errors      int     `json:"errors"`
	MinMs       float64 `json:"min_ms"`
	P50Ms       float64 `json:"p50_ms"`
	P99Ms       float64 `json:"p99_ms"`
	MaxMs       float64 `json:"max_ms"`
	Considered  float64 `json:"avg_segments_considered"`
	SkipTime    float64 `json:"avg_skipped_by_time"`
	SkipBloom   float64 `json:"avg_skipped_by_bloom"`
	Scanned     float64 `json:"avg_segments_scanned"`
	MBRead      float64 `json:"avg_mb_read"`
	Hits        float64 `json:"avg_hits"`
}

type report struct {
	Env     envInfo       `json:"environment"`
	Config  config        `json:"config"`
	Ingest  *ingestResult `json:"ingest,omitempty"`
	Settle  *settleResult `json:"settle,omitempty"`
	Verify  *verifyResult `json:"verify,omitempty"`
	Queries []queryResult `json:"queries"`
}

func main() {
	var c config
	flag.StringVar(&c.Addr, "addr", "127.0.0.1:7070", "Strata server address")
	flag.StringVar(&c.Dataset, "dataset", "", "path of the BGL log file (required)")
	flag.IntVar(&c.MaxLines, "max-lines", 0, "only use the first N lines (0 = all)")
	flag.IntVar(&c.Streams, "streams", 4, "parallel ingest streams")
	flag.IntVar(&c.Batch, "batch", 500, "lines per ingest batch")
	flag.IntVar(&c.Iterations, "iterations", 100, "timed runs per query type")
	flag.IntVar(&c.HeavyIters, "heavy-iterations", 10, "timed runs for the query that has to read every segment")
	flag.DurationVar(&c.Settle, "settle", 90*time.Second, "how long the segment count must stay unchanged before queries are measured")
	flag.DurationVar(&c.SealWait, "seal-wait", 8*time.Second, "pause after ingest so the server's buffer age (default 5s) seals the last partial segment")
	flag.IntVar(&c.VerifySample, "verify-sample", 300, "random lines to look up afterwards to check nothing was lost")
	flag.Int64Var(&c.Seed, "seed", 42, "random seed (sampling is reproducible)")
	flag.StringVar(&c.Note, "note", "", "free text describing how the server was deployed")
	flag.StringVar(&c.Out, "out", "", "write results here (Markdown); a .json file is written next to it")
	flag.BoolVar(&c.SkipIngest, "skip-ingest", false, "assume the dataset is already loaded; only run queries")
	flag.Parse()
	if c.Dataset == "" {
		log.Fatal("-dataset is required")
	}

	ctx := context.Background()
	conn, err := grpc.NewClient(c.Addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()
	ingestClient := stratav1.NewIngestServiceClient(conn)
	queryClient := stratav1.NewQueryServiceClient(conn)

	rep := &report{Env: gatherEnv(), Config: c}

	if !c.SkipIngest {
		res, err := runIngest(ctx, ingestClient, c)
		if err != nil {
			log.Fatalf("ingest: %v", err)
		}
		rep.Ingest = res
		settle, err := waitForSettle(ctx, queryClient, c, res)
		if err != nil {
			log.Fatalf("settle: %v", err)
		}
		rep.Settle = settle
	}

	samples, err := sampleLines(c)
	if err != nil {
		log.Fatalf("sampling: %v", err)
	}
	rep.Verify = runVerify(ctx, queryClient, samples)
	if rep.Verify.Found != rep.Verify.Sampled {
		log.Printf("WARNING: only %d of %d sampled lines were found. Data is missing; results are not valid.", rep.Verify.Found, rep.Verify.Sampled)
	}
	rep.Queries = runQueries(ctx, queryClient, c, samples)

	md := renderMarkdown(rep)
	fmt.Println(md)
	if c.Out != "" {
		if err := os.WriteFile(c.Out, []byte(md), 0o644); err != nil {
			log.Fatal(err)
		}
		js, _ := json.MarshalIndent(rep, "", "  ")
		if err := os.WriteFile(strings.TrimSuffix(c.Out, ".md")+".json", js, 0o644); err != nil {
			log.Fatal(err)
		}
		log.Printf("wrote %s", c.Out)
	}
}

func gatherEnv() envInfo {
	e := envInfo{Cores: runtime.NumCPU(), Go: runtime.Version(), When: time.Now().UTC().Format("2006-01-02")}
	if b, err := os.ReadFile("/proc/cpuinfo"); err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			if k, v, ok := strings.Cut(l, ":"); ok && strings.TrimSpace(k) == "model name" {
				e.CPU = strings.TrimSpace(v)
				break
			}
		}
	}
	if b, err := os.ReadFile("/proc/meminfo"); err == nil {
		var kb int
		fmt.Sscanf(string(b), "MemTotal: %d kB", &kb)
		e.MemoryMiB = kb / 1024
	}
	if b, err := os.ReadFile("/proc/version"); err == nil {
		f := strings.Fields(string(b))
		if len(f) >= 3 {
			e.Kernel = f[0] + " " + f[2]
		}
	}
	if out, err := exec.Command("docker", "--version").Output(); err == nil {
		e.Docker = strings.TrimSpace(string(out))
	}
	return e
}

// ---- ingest ---------------------------------------------------------------

func runIngest(ctx context.Context, client stratav1.IngestServiceClient, c config) (*ingestResult, error) {
	res := &ingestResult{}
	if fi, err := os.Stat(c.Dataset); err == nil {
		res.DatasetBytes = fi.Size()
	}

	// First, how fast can the file be read and parsed with nothing else going
	// on? This is the ceiling the ingest numbers can't exceed, and shows
	// whether the client (not the server) was the bottleneck.
	start := time.Now()
	n, _, err := bench.ReadBGL(c.Dataset, c.Batch, c.MaxLines, func([]*stratav1.LogEntry) error { return nil })
	if err != nil {
		return nil, err
	}
	parse := time.Since(start)
	res.ParseOnlySeconds = parse.Seconds()
	res.ParseOnlyLinesSec = float64(n) / parse.Seconds()
	log.Printf("read+parse only: %d lines in %s (%.0f lines/s)", n, parse.Round(time.Millisecond), res.ParseOnlyLinesSec)

	// Real run: one goroutine reads and parses, several streams send.
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	batches := make(chan []*stratav1.LogEntry, c.Streams*2)
	var accepted, retries atomic.Int64
	var minTS, maxTS int64 = 1<<63 - 1, 0
	var skipped int
	var readErr error
	go func() {
		defer close(batches)
		_, skipped, readErr = bench.ReadBGL(c.Dataset, c.Batch, c.MaxLines, func(b []*stratav1.LogEntry) error {
			for _, e := range b {
				if e.TimestampUnixNano < minTS {
					minTS = e.TimestampUnixNano
				}
				if e.TimestampUnixNano > maxTS {
					maxTS = e.TimestampUnixNano
				}
			}
			select {
			case batches <- b:
				return nil
			case <-cctx.Done():
				return cctx.Err()
			}
		})
	}()

	var mu sync.Mutex
	var latencies []time.Duration
	var firstErr error
	var wg sync.WaitGroup
	begin := time.Now()
	for i := 0; i < c.Streams; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			st, err := client.Ingest(cctx)
			if err != nil {
				mu.Lock()
				firstErr = err
				mu.Unlock()
				cancel()
				return
			}
			var local []time.Duration
			var id uint64
			for b := range batches {
				id++
				for { // retry the same batch if the server pushes back
					t0 := time.Now()
					if err := st.Send(&stratav1.IngestRequest{BatchId: id, Entries: b}); err != nil {
						fail(&mu, &firstErr, cancel, fmt.Errorf("send: %w", err))
						return
					}
					resp, err := st.Recv()
					if err != nil {
						fail(&mu, &firstErr, cancel, fmt.Errorf("recv: %w", err))
						return
					}
					local = append(local, time.Since(t0))
					if resp.Status == stratav1.IngestStatus_INGEST_STATUS_OK {
						accepted.Add(int64(resp.Accepted))
						break
					}
					if resp.Status == stratav1.IngestStatus_INGEST_STATUS_BUFFER_FULL {
						retries.Add(1)
						time.Sleep(50 * time.Millisecond)
						continue
					}
					fail(&mu, &firstErr, cancel, fmt.Errorf("batch rejected: %s %s", resp.Status, resp.Message))
					return
				}
			}
			st.CloseSend()
			mu.Lock()
			latencies = append(latencies, local...)
			mu.Unlock()
		}()
	}
	wg.Wait()
	took := time.Since(begin)
	if firstErr != nil {
		return nil, firstErr
	}
	if readErr != nil {
		return nil, readErr
	}

	res.Lines = accepted.Load()
	res.SkippedLines = skipped
	res.Seconds = took.Seconds()
	res.LinesPerSec = float64(res.Lines) / took.Seconds()
	res.MBPerSec = float64(res.DatasetBytes) / (1 << 20) / took.Seconds()
	if c.MaxLines > 0 {
		res.MBPerSec = 0 // dataset bytes don't correspond to a partial run
	}
	res.BatchP50Ms = ms(bench.Percentile(latencies, 50))
	res.BatchP99Ms = ms(bench.Percentile(latencies, 99))
	res.BatchMaxMs = ms(bench.Percentile(latencies, 100))
	res.BufferFullRetries = retries.Load()
	res.MinTimestamp = time.Unix(0, minTS).UTC().Format(time.RFC3339)
	res.MaxTimestamp = time.Unix(0, maxTS).UTC().Format(time.RFC3339)
	log.Printf("ingested %d lines in %s (%.0f lines/s)", res.Lines, took.Round(time.Millisecond), res.LinesPerSec)
	return res, nil
}

func fail(mu *sync.Mutex, dst *error, cancel context.CancelFunc, err error) {
	mu.Lock()
	if *dst == nil {
		*dst = err
	}
	mu.Unlock()
	cancel()
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

// waitForSettle waits until every buffered line has been sealed and the
// compactor has stopped merging, by watching the number of segments. A search
// for a word that is not in the data costs almost nothing (every bloom filter
// rules its segment out) and reports how many segments exist.
func waitForSettle(ctx context.Context, q stratav1.QueryServiceClient, c config, ing *ingestResult) (*settleResult, error) {
	count := func() (int, error) {
		resp, err := q.Search(ctx, &stratav1.SearchRequest{Text: "zzzneverpresentzzz", Limit: 1})
		if err != nil {
			return 0, err
		}
		return int(resp.Metrics.SegmentsConsidered), nil
	}
	// The server seals a partly filled buffer after its max age (default 5s).
	time.Sleep(c.SealWait)
	first, err := count()
	if err != nil {
		return nil, err
	}
	res := &settleResult{SegmentsAfterIngest: first}
	last, lastChange := first, time.Now()
	start := time.Now()
	log.Printf("segments after ingest: %d; waiting for compaction to settle (%s without change)", first, c.Settle)
	for time.Since(lastChange) < c.Settle {
		time.Sleep(3 * time.Second)
		n, err := count()
		if err != nil {
			return nil, err
		}
		if n != last {
			log.Printf("segments: %d -> %d", last, n)
			last, lastChange = n, time.Now()
		}
	}
	res.SegmentsSettled = last
	res.SecondsToSettle = lastChange.Sub(start).Seconds()

	// Total stored size: a search with no words and no tags can't be ruled
	// out by any bloom filter, so it reads every segment, and the bytes it
	// reports are the total. (A search for a word would skip the segments that
	// lack it and undercount.)
	resp, err := q.Search(ctx, &stratav1.SearchRequest{Limit: 1})
	if err != nil {
		return nil, fmt.Errorf("measuring stored size: %w", err)
	}
	if resp.Metrics.SegmentsScanned != resp.Metrics.SegmentsConsidered {
		return nil, fmt.Errorf("size measurement scanned %d of %d segments", resp.Metrics.SegmentsScanned, resp.Metrics.SegmentsConsidered)
	}
	res.StoredBytes = int64(resp.Metrics.BytesRead)
	if ing.DatasetBytes > 0 && c.MaxLines == 0 {
		res.Amplification = float64(res.StoredBytes) / float64(ing.DatasetBytes)
	}
	return res, nil
}

// ---- sampling and verification --------------------------------------------

type sample struct {
	raw   string
	entry *stratav1.LogEntry
}

// sampleLines draws a reproducible random sample of dataset lines (reservoir
// sampling over one pass).
func sampleLines(c config) ([]sample, error) {
	rng := rand.New(rand.NewSource(c.Seed))
	var raws []string
	seen := 0
	_, _, err := readRaw(c.Dataset, c.MaxLines, func(line string) {
		if _, err := bench.ParseBGLLine(line); err != nil {
			return
		}
		seen++
		if len(raws) < c.VerifySample {
			raws = append(raws, line)
		} else if j := rng.Intn(seen); j < c.VerifySample {
			raws[j] = line
		}
	})
	if err != nil {
		return nil, err
	}
	var out []sample
	for _, l := range raws {
		e, _ := bench.ParseBGLLine(l)
		out = append(out, sample{raw: l, entry: e})
	}
	return out, nil
}

// runVerify looks every sampled line up again by its node name and exact
// microsecond timestamp. If any is missing, data was lost somewhere between the
// client and the store, and the benchmark numbers mean nothing.
func runVerify(ctx context.Context, q stratav1.QueryServiceClient, samples []sample) *verifyResult {
	res := &verifyResult{Sampled: len(samples)}
	var found atomic.Int64
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for _, s := range samples {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			f := strings.SplitN(s.raw, " ", 9)
			resp, err := q.Search(ctx, &stratav1.SearchRequest{
				Text:         f[3] + " " + f[4], // node + full timestamp
				FromUnixNano: s.entry.TimestampUnixNano,
				ToUnixNano:   s.entry.TimestampUnixNano + 1000, // that microsecond only
				Limit:        100,
			})
			if err != nil {
				return
			}
			for _, h := range resp.Hits {
				if h.Message == s.entry.Message {
					found.Add(1)
					return
				}
			}
		}()
	}
	wg.Wait()
	res.Found = int(found.Load())
	log.Printf("verification: %d of %d sampled lines found", res.Found, res.Sampled)
	return res
}

// ---- queries --------------------------------------------------------------

type queryCase struct {
	name, desc string
	n          int
	build      func(i int) *stratav1.SearchRequest
}

func runQueries(ctx context.Context, q stratav1.QueryServiceClient, c config, samples []sample) []queryResult {
	// Time windows are centred on randomly sampled real lines, so a window
	// always has data in it (a window over an empty stretch would be answered
	// instantly and flatter the numbers).
	window := func(i int, half time.Duration) (int64, int64) {
		ts := samples[i%len(samples)].entry.TimestampUnixNano
		return ts - int64(half), ts + int64(half)
	}
	cases := []queryCase{
		{"rare word, all history", `word "hangtest" (about 250 lines in total), no time range`, c.Iterations,
			func(int) *stratav1.SearchRequest { return &stratav1.SearchRequest{Text: "hangtest", Limit: 100} }},
		{"rarer word, all history", `word "rmdir", no time range`, c.Iterations,
			func(int) *stratav1.SearchRequest { return &stratav1.SearchRequest{Text: "rmdir", Limit: 100} }},
		{"common word, 1-hour window", `word "error" (about 750k lines), 1-hour window around a random real line`, c.Iterations,
			func(i int) *stratav1.SearchRequest {
				f, t := window(i, 30*time.Minute)
				return &stratav1.SearchRequest{Text: "error", FromUnixNano: f, ToUnixNano: t, Limit: 100}
			}},
		{"tag level=FATAL, 1-day window", `tag level=FATAL (about 855k lines), 1-day window around a random real line`, c.Iterations,
			func(i int) *stratav1.SearchRequest {
				f, t := window(i, 12*time.Hour)
				return &stratav1.SearchRequest{Tags: map[string]string{"level": "FATAL"}, FromUnixNano: f, ToUnixNano: t, Limit: 100}
			}},
		{"alert tag, all history", `tag alert=KERNDTLB (about 150k lines, clustered in time), no time range`, c.Iterations,
			func(int) *stratav1.SearchRequest {
				return &stratav1.SearchRequest{Tags: map[string]string{"alert": "KERNDTLB"}, Limit: 100}
			}},
		{"word not in the data", `word that appears nowhere, no time range`, c.Iterations,
			func(int) *stratav1.SearchRequest {
				return &stratav1.SearchRequest{Text: "zzzneverpresentzzz", Limit: 100}
			}},
		{"common word, all history (worst case)", `word "error", no time range: every segment contains it, so every segment is read`, c.HeavyIters,
			func(int) *stratav1.SearchRequest { return &stratav1.SearchRequest{Text: "error", Limit: 100} }},
	}

	var out []queryResult
	for _, qc := range cases {
		log.Printf("query case: %s (%d runs)", qc.name, qc.n)
		for i := 0; i < 2; i++ { // warm-up, not counted
			q.Search(ctx, qc.build(i))
		}
		r := queryResult{Name: qc.name, Description: qc.desc, N: qc.n}
		var lat []time.Duration
		for i := 0; i < qc.n; i++ {
			req := qc.build(i + 2)
			t0 := time.Now()
			resp, err := q.Search(ctx, req)
			d := time.Since(t0)
			if err != nil {
				r.Errors++
				log.Printf("  error: %v", err)
				continue
			}
			lat = append(lat, d)
			m := resp.Metrics
			r.Considered += float64(m.SegmentsConsidered)
			r.SkipTime += float64(m.SkippedByTime)
			r.SkipBloom += float64(m.SkippedByBloom)
			r.Scanned += float64(m.SegmentsScanned)
			r.MBRead += float64(m.BytesRead) / (1 << 20)
			r.Hits += float64(len(resp.Hits))
		}
		if k := float64(len(lat)); k > 0 {
			r.Considered /= k
			r.SkipTime /= k
			r.SkipBloom /= k
			r.Scanned /= k
			r.MBRead /= k
			r.Hits /= k
		}
		r.MinMs, r.P50Ms, r.P99Ms, r.MaxMs = ms(bench.Percentile(lat, 0.0001)), ms(bench.Percentile(lat, 50)), ms(bench.Percentile(lat, 99)), ms(bench.Percentile(lat, 100))
		out = append(out, r)
	}
	return out
}

// ---- output ---------------------------------------------------------------

func renderMarkdown(r *report) string {
	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format, a...) }

	w("# Strata benchmark results\n\n")
	w("Measured on %s.\n\n", r.Env.When)
	w("## Environment\n\n")
	w("- CPU: %s (%d logical cores)\n- Memory: %d MiB\n- OS: %s\n- Go: %s\n- %s\n", r.Env.CPU, r.Env.Cores, r.Env.MemoryMiB, r.Env.Kernel, r.Env.Go, r.Env.Docker)
	if r.Config.Note != "" {
		w("- Deployment: %s\n", r.Config.Note)
	}
	w("- Client: strata-bench on the same machine, gRPC over loopback\n\n")

	if r.Ingest != nil {
		i := r.Ingest
		w("## Dataset and ingest\n\n")
		w("- Dataset: BGL (BlueGene/L supercomputer logs, LogHub), %d lines, %.0f MiB, covering %s to %s\n",
			i.Lines, float64(i.DatasetBytes)/(1<<20), i.MinTimestamp, i.MaxTimestamp)
		w("- Settings: %d parallel gRPC streams, %d lines per batch; server defaults (buffer 4 MiB / 5 s, compaction on)\n\n", r.Config.Streams, r.Config.Batch)
		w("| Measure | Value |\n|---|---|\n")
		w("| Lines ingested | %d |\n", i.Lines)
		w("| Time to last acknowledgement | %.1f s |\n", i.Seconds)
		w("| **Ingest throughput** | **%.0f lines/s** |\n", i.LinesPerSec)
		if i.MBPerSec > 0 {
			w("| Ingest throughput (dataset bytes) | %.1f MiB/s |\n", i.MBPerSec)
		}
		w("| Batch acknowledgement latency P50 / P99 / max | %.1f / %.1f / %.1f ms |\n", i.BatchP50Ms, i.BatchP99Ms, i.BatchMaxMs)
		w("| Pushed back by the server (BUFFER_FULL retries) | %d |\n", i.BufferFullRetries)
		w("| Client read+parse ceiling (no sending) | %.0f lines/s |\n\n", i.ParseOnlyLinesSec)
		w("An acknowledgement means the batch is in the server's memory buffer; it becomes durable when sealed into a segment (within 5 s or 4 MiB). The throughput above is therefore the rate of *accepting* data; the segment settle time below shows when it was all stored and compacted.\n\n")
	}
	if s := r.Settle; s != nil {
		w("## Storage and compaction\n\n")
		w("| Measure | Value |\n|---|---|\n")
		w("| Segments right after ingest | %d |\n", s.SegmentsAfterIngest)
		w("| Segments after compaction settled | %d |\n", s.SegmentsSettled)
		w("| Time until the segment count stopped changing | %.0f s |\n", s.SecondsToSettle)
		w("| Total stored size | %.0f MiB |\n", float64(s.StoredBytes)/(1<<20))
		if s.Amplification > 0 {
			w("| Stored bytes per dataset byte | %.2f |\n", s.Amplification)
		}
		w("\n")
	}
	if v := r.Verify; v != nil {
		w("## Correctness check\n\n%d randomly sampled lines were looked up by node name and exact microsecond timestamp: **%d found**.\n\n", v.Sampled, v.Found)
	}

	w("## Query latency\n\n")
	w("Each query type ran sequentially after 2 warm-up runs, measured at the client (round trip over loopback, including the server's work). Time windows are centred on randomly sampled real lines. \"Skipped\" columns are per-query averages of the server's own counters.\n\n")
	w("| Query | Runs | P50 | P99 | Max | Segments considered | Skipped by time | Skipped by bloom | Scanned | MiB read | Hits |\n|---|---|---|---|---|---|---|---|---|---|---|\n")
	for _, q := range r.Queries {
		errs := ""
		if q.Errors > 0 {
			errs = fmt.Sprintf(" (%d errors)", q.Errors)
		}
		w("| %s | %d%s | %.1f ms | %.1f ms | %.1f ms | %.0f | %.1f | %.1f | %.1f | %.1f | %.0f |\n",
			q.Name, q.N, errs, q.P50Ms, q.P99Ms, q.MaxMs, q.Considered, q.SkipTime, q.SkipBloom, q.Scanned, q.MBRead, q.Hits)
	}
	w("\n")
	for _, q := range r.Queries {
		w("- **%s**: %s\n", q.Name, q.Description)
	}
	w("\nWith few runs, P99 is simply the slowest or second-slowest run; treat it as such.\n")
	return b.String()
}
