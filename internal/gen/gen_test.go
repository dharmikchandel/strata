package gen

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	stratav1 "github.com/dharmikchandel/strata/gen/strata/v1"
	"github.com/dharmikchandel/strata/internal/app"
	"github.com/dharmikchandel/strata/internal/bench"
	"github.com/dharmikchandel/strata/internal/storage/storagetest"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// ---- schedule -------------------------------------------------------------

func TestScheduleCumulativeCount(t *testing.T) {
	steady := Schedule{Rate: 100, BurstFactor: 1}
	for _, d := range []time.Duration{0, time.Second, 90 * time.Second} {
		if got, want := steady.Cumulative(d), 100*d.Seconds(); got != want {
			t.Errorf("steady %s: %v, want %v", d, got, want)
		}
	}
	// 100/s, 3x bursts of 2s that end every 10s (so a burst covers seconds 8 to 10).
	b := Schedule{Rate: 100, BurstFactor: 3, BurstEvery: 10 * time.Second, BurstFor: 2 * time.Second}
	cases := []struct {
		at   time.Duration
		want float64
	}{
		{8 * time.Second, 800},                 // no burst yet
		{9 * time.Second, 900 + 200},           // 1s into the first burst: 1s extra at +200/s
		{10 * time.Second, 1000 + 400},         // the whole burst
		{18 * time.Second, 1800 + 400},         // between bursts
		{20 * time.Second, 2000 + 2*400},       // two full bursts
		{25 * time.Second, 2500 + 2*400},       // the third has not started
		{29 * time.Second, 2900 + 2*400 + 200}, // 1s into the third
	}
	for _, c := range cases {
		if got := b.Cumulative(c.at); math.Abs(got-c.want) > 1e-6 {
			t.Errorf("burst schedule at %s: %v, want %v", c.at, got, c.want)
		}
	}
	// The count never goes backwards.
	prev := 0.0
	for ms := 0; ms < 60000; ms += 37 {
		got := b.Cumulative(time.Duration(ms) * time.Millisecond)
		if got < prev {
			t.Fatalf("cumulative count decreased at %dms", ms)
		}
		prev = got
	}
	if b.Peak() != 300 || steady.Peak() != 100 {
		t.Errorf("peaks: %v %v", b.Peak(), steady.Peak())
	}
}

func TestScheduleValidation(t *testing.T) {
	bad := []Schedule{
		{Rate: 0, BurstFactor: 1}, {Rate: -5, BurstFactor: 1}, {Rate: math.NaN(), BurstFactor: 1}, {Rate: math.Inf(1), BurstFactor: 1},
		{Rate: 10, BurstFactor: 0.5},
		{Rate: 10, BurstFactor: 2}, // bursts without a period
		{Rate: 10, BurstFactor: 2, BurstEvery: time.Second, BurstFor: time.Second}, // a burst that never ends
	}
	for _, s := range bad {
		if s.Validate() == nil {
			t.Errorf("accepted %+v", s)
		}
	}
	if err := (Schedule{Rate: 10, BurstFactor: 1}).Validate(); err != nil {
		t.Error(err)
	}
}

// ---- content --------------------------------------------------------------

func TestTemplatesAreLoadedAndRealistic(t *testing.T) {
	c, err := NewContent()
	if err != nil {
		t.Fatal(err)
	}
	if n := len(c.Templates()); n < 100 {
		t.Fatalf("only %d templates", n)
	}
	// Sampling follows the real frequencies: the biggest template is about 37% of
	// the (renormalised) mix, and rarer ones still appear.
	src := c.NewSource(1, 0, "run00000000")
	counts := map[string]int{}
	const n = 100000
	for i := 0; i < n; i++ {
		e := src.Next(time.Unix(1700000000, 0))
		counts[e.Tags["level"]+"/"+e.Tags["component"]]++
	}
	var total float64
	for _, tp := range c.Templates() {
		total += tp.Weight
	}
	top := c.Templates()[0]
	topShare := top.Weight / total
	if topShare < 0.30 || topShare > 0.45 {
		t.Fatalf("expected the biggest real template to be around 37%%, it is %.1f%%", 100*topShare)
	}
	if len(counts) < 4 {
		t.Fatalf("only %d level/component combinations in %d lines", len(counts), n)
	}
}

func TestContentIsReproducibleForASeed(t *testing.T) {
	c, _ := NewContent()
	gen := func(seed uint64) []string {
		s := c.NewSource(seed, 3, "run12345678")
		var out []string
		for i := 0; i < 50; i++ {
			out = append(out, s.Next(time.Unix(1700000000, int64(i)*1000)).Message)
		}
		return out
	}
	a, b, other := gen(7), gen(7), gen(8)
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("same seed, different line %d:\n%s\n%s", i, a[i], b[i])
		}
	}
	same := 0
	for i := range a {
		if a[i] == other[i] {
			same++
		}
	}
	if same > 5 {
		t.Fatalf("a different seed produced %d identical lines of 50", same)
	}
}

// The generated message must have exactly the shape of a real BGL line, so
// that the page's examples, tag filters and searches behave on it the same way,
// and so the same parser reads both.
func TestGeneratedLinesParseLikeRealBGLLines(t *testing.T) {
	c, _ := NewContent()
	src := c.NewSource(1, 2, "runabcdef01")
	ts := time.Date(2026, 10, 4, 12, 30, 45, 123456000, time.UTC)
	alerts := 0
	for i := 0; i < 3000; i++ {
		e := src.Next(ts.Add(time.Duration(i) * time.Millisecond))
		if strings.ContainsAny(e.Message, "{}") {
			t.Fatalf("an unexpanded placeholder: %s", e.Message)
		}
		label := "-"
		if a, ok := e.Tags["alert"]; ok {
			label = a
			alerts++
		}
		parsed, err := bench.ParseBGLLine(label + " 1117838570 " + e.Message)
		if err != nil {
			t.Fatalf("not a valid BGL line: %v\n%s", err, e.Message)
		}
		if parsed.Message != e.Message || parsed.TimestampUnixNano != e.TimestampUnixNano {
			t.Fatalf("parsed differently:\n got  %q @%d\n want %q @%d", parsed.Message, parsed.TimestampUnixNano, e.Message, e.TimestampUnixNano)
		}
		for k, v := range e.Tags {
			if parsed.Tags[k] != v {
				t.Fatalf("tag %s: generated %q, parsed %q", k, v, parsed.Tags[k])
			}
		}
		if len(parsed.Tags) != len(e.Tags) {
			t.Fatalf("tags differ: %v vs %v", e.Tags, parsed.Tags)
		}
	}
	if alerts == 0 || alerts > 600 {
		t.Fatalf("%d alert lines in 3000; real data has about 7%%", alerts)
	}
}

func TestMarkerRoundTrip(t *testing.T) {
	c, _ := NewContent()
	src := c.NewSource(1, 5, "runcafe0001")
	for want := uint64(0); want < 3; want++ {
		run, source, seq, ok := ParseMarker(src.Next(time.Now()).Message)
		if !ok || run != "runcafe0001" || source != 5 || seq != want {
			t.Fatalf("marker: %q %d %d %v (want seq %d)", run, source, seq, ok, want)
		}
	}
	for _, bad := range []string{"", "no marker here", "~runcafe0001.1", "x ~runzzzzzzzz.1.2", "~runcafe0001.1.2 trailing"} {
		if _, _, _, ok := ParseMarker(bad); ok {
			t.Errorf("accepted %q", bad)
		}
	}
	// The marker must be a searchable run label: tokenised, the run ID is one word.
	if !strings.Contains(src.Next(time.Now()).Message, "~runcafe0001.5.") {
		t.Fatal("marker format changed; verification searches for the run ID as a word")
	}
}

// ---- configuration --------------------------------------------------------

func env(kv ...string) func(string) string {
	m := map[string]string{}
	for i := 0; i < len(kv); i += 2 {
		m[kv[i]] = kv[i+1]
	}
	return func(k string) string { return m[k] }
}

func TestProfilesAndOverrides(t *testing.T) {
	c, err := ParseConfig(nil, env())
	if err != nil || c.Profile != "demo" || c.Schedule.Rate != 20 || c.Sources != 1 || c.Verify {
		t.Fatalf("defaults: %+v %v", c, err)
	}
	c, err = ParseConfig([]string{"-profile", "busy"}, env())
	if err != nil || c.Schedule.Rate != 1000 || c.Sources != 2 || c.Schedule.BurstFactor != 3 || c.LateFraction != 0.02 {
		t.Fatalf("busy: %+v %v", c, err)
	}
	// A flag beats the profile; the rest of the profile stays.
	c, err = ParseConfig([]string{"-profile", "busy", "-rate", "300", "-late-fraction", "0"}, env())
	if err != nil || c.Schedule.Rate != 300 || c.LateFraction != 0 || c.Schedule.BurstFactor != 3 || c.Sources != 2 {
		t.Fatalf("override: %+v %v", c, err)
	}
	// So does an environment variable, and a flag beats the variable.
	c, err = ParseConfig(nil, env("STRATA_GEN_PROFILE", "peak", "STRATA_GEN_RATE", "777"))
	if err != nil || c.Profile != "peak" || c.Schedule.Rate != 777 || c.Sources != 4 {
		t.Fatalf("env: %+v %v", c, err)
	}
	c, _ = ParseConfig([]string{"-rate", "5"}, env("STRATA_GEN_RATE", "777"))
	if c.Schedule.Rate != 5 {
		t.Fatalf("flag should beat env: %v", c.Schedule.Rate)
	}
	// A finite run is a test, so it verifies unless told not to; an endless one cannot.
	c, _ = ParseConfig([]string{"-duration", "30s"}, env())
	if !c.Verify {
		t.Error("a run with a duration should verify by default")
	}
	c, _ = ParseConfig([]string{"-duration", "30s", "-no-verify"}, env())
	if c.Verify {
		t.Error("-no-verify ignored")
	}
}

func TestInvalidConfigurationIsRejected(t *testing.T) {
	cases := map[string]struct {
		args []string
		e    func(string) string
		want string
	}{
		"unknown profile":    {[]string{"-profile", "huge"}, env(), "unknown profile"},
		"zero rate":          {[]string{"-rate", "0"}, env(), "rate"},
		"negative rate":      {[]string{"-rate", "-3"}, env(), "rate"},
		"no sources":         {[]string{"-sources", "0"}, env(), "sources"},
		"too many sources":   {[]string{"-sources", "500"}, env(), "sources"},
		"burst never ends":   {[]string{"-profile", "busy", "-burst-for", "2m"}, env(), "shorter"},
		"late without max":   {[]string{"-late-fraction", "0.1"}, env(), "late-max"},
		"late fraction >1":   {[]string{"-late-fraction", "2", "-late-max", "1m"}, env(), "late-fraction"},
		"verify without end": {[]string{"-verify"}, env(), "duration"},
		"bad env duration":   {nil, env("STRATA_GEN_DURATION", "5sec"), "STRATA_GEN_DURATION"},
		"bad env number":     {nil, env("STRATA_GEN_RATE", "fast"), "STRATA_GEN_RATE"},
		"bad seed":           {[]string{"-seed", "abc"}, env(), "seed"},
		"stray argument":     {[]string{"now"}, env(), "unexpected"},
		"batch too big":      {[]string{"-batch", "99999"}, env(), "batch"},
		"negative duration":  {[]string{"-duration", "-1s"}, env(), "duration"},
	}
	for name, c := range cases {
		if _, err := ParseConfig(c.args, c.e); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: want an error mentioning %q, got %v", name, c.want, err)
		}
	}
}

func TestHelpListsTheProfiles(t *testing.T) {
	_, err := ParseConfig([]string{"-h"}, env())
	var h *HelpRequested
	if !errors.As(err, &h) {
		t.Fatalf("want HelpRequested, got %v", err)
	}
	for _, p := range Profiles {
		if !strings.Contains(h.Usage, p.Name) || !strings.Contains(h.Usage, p.Description) {
			t.Errorf("usage does not describe the %s profile", p.Name)
		}
	}
}

func TestEveryProfileIsValid(t *testing.T) {
	for _, p := range Profiles {
		c, err := ParseConfig([]string{"-profile", p.Name}, env())
		if err != nil {
			t.Errorf("%s: %v", p.Name, err)
			continue
		}
		// Verification windows must fit one search at the profile's peak rate, with late lines too.
		w := bucketWidth(c)
		if perWindow := c.Schedule.Peak() * w.Seconds(); perWindow > 7000 {
			t.Errorf("%s: a %s window would hold %.0f lines at peak, too many for one search", p.Name, w, perWindow)
		}
	}
}

// ---- verification logic (no server) ----------------------------------------

type stubSearcher struct {
	respond func(from, to int64, call int) *stratav1.SearchResponse
	mu      sync.Mutex // Verify calls Search from several workers
	calls   map[int64]int
}

func (s *stubSearcher) Search(_ context.Context, in *stratav1.SearchRequest, _ ...grpc.CallOption) (*stratav1.SearchResponse, error) {
	s.mu.Lock()
	if s.calls == nil {
		s.calls = map[int64]int{}
	}
	s.calls[in.FromUnixNano]++
	n := s.calls[in.FromUnixNano]
	s.mu.Unlock()
	return s.respond(in.FromUnixNano, in.ToUnixNano, n), nil
}

func hitFor(run string, src int, seq uint64) *stratav1.SearchHit {
	return &stratav1.SearchHit{Message: fmt.Sprintf("2026.01.01 N ts N RAS KERNEL INFO text ~%s.%d.%d", run, src, seq)}
}

// A report in which source 0 sent seqs 0..9 and the buckets hold them 5 and 5.
func stubReport() *Report {
	return &Report{RunID: "run00000001", BucketWidth: time.Second, Expected: map[int64]int{100: 5, 101: 5}, AckedBySource: []uint64{10}}
}

func TestVerifyFindsEverythingWhenItIsAllThere(t *testing.T) {
	q := &stubSearcher{respond: func(from, _ int64, _ int) *stratav1.SearchResponse {
		base := uint64(0)
		if from == 101*int64(time.Second) {
			base = 5
		}
		var hits []*stratav1.SearchHit
		for i := uint64(0); i < 5; i++ {
			hits = append(hits, hitFor("run00000001", 0, base+i))
		}
		return &stratav1.SearchResponse{Hits: hits}
	}}
	res, err := Verify(context.Background(), q, stubReport(), VerifyOptions{Workers: 2, Settle: time.Millisecond})
	if err != nil || res.Lost != 0 || res.Found != 10 || res.Duplicates != 0 || res.Attempts != 1 || len(res.LostRanges) != 0 {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestVerifyReportsLostLinesAndTheirSequenceNumbers(t *testing.T) {
	q := &stubSearcher{respond: func(from, _ int64, _ int) *stratav1.SearchResponse {
		if from == 100*int64(time.Second) {
			return &stratav1.SearchResponse{Hits: []*stratav1.SearchHit{hitFor("run00000001", 0, 0), hitFor("run00000001", 0, 1), hitFor("run00000001", 0, 4)}} // 2 and 3 missing
		}
		var hits []*stratav1.SearchHit
		for i := uint64(5); i < 10; i++ {
			hits = append(hits, hitFor("run00000001", 0, i))
		}
		return &stratav1.SearchResponse{Hits: hits}
	}}
	res, err := Verify(context.Background(), q, stubReport(), VerifyOptions{Workers: 1, Settle: time.Millisecond, MaxAttempts: 2})
	if err != nil {
		t.Fatal(err)
	}
	if res.Lost != 2 || res.Found != 8 || res.Attempts != 2 {
		t.Fatalf("%+v", res)
	}
	if len(res.LostRanges) != 1 || res.LostRanges[0] != (SeqRange{Source: 0, From: 2, To: 3}) {
		t.Fatalf("lost ranges: %+v", res.LostRanges)
	}
	if !strings.Contains(res.String(), "LOST 2") || !strings.Contains(res.String(), "#2-3") {
		t.Fatalf("the report must say what was lost:\n%s", res)
	}
}

// A line the server acknowledged may still be sitting in its buffer, so it is
// searched for again before being called lost.
func TestVerifyGivesUnsealedLinesAnotherChance(t *testing.T) {
	q := &stubSearcher{respond: func(from, _ int64, call int) *stratav1.SearchResponse {
		base := uint64(0)
		if from == 101*int64(time.Second) {
			base = 5
		}
		var hits []*stratav1.SearchHit
		for i := uint64(0); i < 5; i++ {
			if call == 1 && from == 101*int64(time.Second) && i >= 3 {
				continue // not sealed yet on the first look
			}
			hits = append(hits, hitFor("run00000001", 0, base+i))
		}
		return &stratav1.SearchResponse{Hits: hits}
	}}
	res, err := Verify(context.Background(), q, stubReport(), VerifyOptions{Workers: 1, Settle: time.Millisecond})
	if err != nil || res.Lost != 0 || res.Attempts != 2 {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestVerifyCountsDuplicatesAndIgnoresOtherRuns(t *testing.T) {
	q := &stubSearcher{respond: func(from, _ int64, _ int) *stratav1.SearchResponse {
		base := uint64(0)
		if from == 101*int64(time.Second) {
			base = 5
		}
		var hits []*stratav1.SearchHit
		for i := uint64(0); i < 5; i++ {
			hits = append(hits, hitFor("run00000001", 0, base+i))
		}
		hits = append(hits, hitFor("run00000001", 0, base), hitFor("run00000001", 0, base+1)) // resent batch stored twice
		hits = append(hits, hitFor("runffffffff", 0, 999), &stratav1.SearchHit{Message: "unrelated line"})
		return &stratav1.SearchResponse{Hits: hits}
	}}
	res, _ := Verify(context.Background(), q, stubReport(), VerifyOptions{Workers: 1, Settle: time.Millisecond})
	if res.Lost != 0 || res.Duplicates != 4 || res.Found != 10 {
		t.Fatalf("%+v", res)
	}
}

func TestVerifyMarksWindowsTooDenseToCheck(t *testing.T) {
	q := &stubSearcher{respond: func(int64, int64, int) *stratav1.SearchResponse {
		return &stratav1.SearchResponse{Truncated: true, Hits: []*stratav1.SearchHit{hitFor("run00000001", 0, 0)}}
	}}
	res, _ := Verify(context.Background(), q, stubReport(), VerifyOptions{Workers: 1, Settle: time.Millisecond})
	if res.Unverifiable != 2 || res.Lost != 0 || res.Found != 0 {
		t.Fatalf("a truncated window must be reported as unverifiable, not as lost lines: %+v", res)
	}
}

// ---- full runs against a real server ---------------------------------------

type testServer struct {
	app  *app.App
	cfg  app.Config
	mem  *storagetest.Mem
	addr string
}

func startServer(t *testing.T, mod func(*app.Config)) *testServer {
	t.Helper()
	mem := storagetest.NewMem()
	cfg := app.Config{
		ListenAddr: "127.0.0.1:0", ManifestPath: filepath.Join(t.TempDir(), "m.db"), Storage: mem,
		BufferBytes: 1 << 20, BufferAge: 100 * time.Millisecond, MaxRecvMsgBytes: 4 << 20,
		ShutdownGrace: time.Second, OrphanGrace: time.Hour, LogLevel: "error", Logger: quiet,
	}
	if mod != nil {
		mod(&cfg)
	}
	a, err := app.Start(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Shutdown(context.Background()) })
	return &testServer{app: a, cfg: cfg, mem: mem, addr: a.Addr().String()}
}

func testConfig(addr string, d time.Duration) Config {
	return Config{
		Addr: addr, Profile: "test", Schedule: Schedule{Rate: 200, BurstFactor: 1}, Sources: 2, Batch: 200, Tick: 50 * time.Millisecond,
		Seed: 1, Report: time.Hour, Duration: d, Verify: true, VerifySettle: 400 * time.Millisecond, VerifyWorkers: 4,
		ConnectTimeout: 5 * time.Second, Grace: 5 * time.Second, Logger: quiet,
	}
}

func mustRun(t *testing.T, cfg Config) *Report {
	t.Helper()
	rep, err := Run(context.Background(), cfg)
	if err != nil {
		t.Fatalf("run: %v\n%v", err, rep)
	}
	return rep
}

func requireClean(t *testing.T, rep *Report) {
	t.Helper()
	v := rep.Verify
	if v == nil {
		t.Fatal("no verification result")
	}
	if v.Lost != 0 || v.Found != v.Acked || v.Acked != rep.Acked || v.Unverifiable != 0 {
		t.Fatalf("lines went missing:\n%s", rep)
	}
}

func TestSteadyRunDeliversEverythingAtTheRequestedRate(t *testing.T) {
	srv := startServer(t, nil)
	rep := mustRun(t, testConfig(srv.addr, 2*time.Second))
	requireClean(t, rep)
	// 200 lines/s for 2s: about 400, not wildly more or less.
	if rep.Acked < 340 || rep.Acked > 480 {
		t.Fatalf("acknowledged %d lines, expected about 400", rep.Acked)
	}
	if rep.Verify.Duplicates != 0 || rep.BufferFull != 0 || rep.ResentBatches != 0 {
		t.Fatalf("a quiet run should have no retries or duplicates:\n%s", rep)
	}
	if rep.AckP99 <= 0 || rep.AckP99 > 3*time.Second {
		t.Fatalf("ack latency p99 %s", rep.AckP99)
	}
	// Both streams took part.
	if len(rep.AckedBySource) != 2 || rep.AckedBySource[0] == 0 || rep.AckedBySource[1] == 0 {
		t.Fatalf("per-source counts: %v", rep.AckedBySource)
	}
}

func TestBurstsAddTheExtraLines(t *testing.T) {
	srv := startServer(t, nil)
	cfg := testConfig(srv.addr, 3*time.Second)
	cfg.Schedule = Schedule{Rate: 100, BurstFactor: 4, BurstEvery: time.Second, BurstFor: 500 * time.Millisecond}
	rep := mustRun(t, cfg)
	requireClean(t, rep)
	want := cfg.Schedule.Cumulative(3 * time.Second) // 300 steady + 3 bursts of 0.5s at +300/s = 750
	if math.Abs(float64(rep.Acked)-want) > want*0.2 {
		t.Fatalf("acknowledged %d, the schedule says about %.0f", rep.Acked, want)
	}
}

// Late lines carry timestamps in the past. They must still be found, in the
// window for their own timestamp, and must widen the run's time range.
func TestLateLinesAreStillFound(t *testing.T) {
	srv := startServer(t, nil)
	cfg := testConfig(srv.addr, 2*time.Second)
	cfg.LateFraction, cfg.LateMax = 0.3, 5*time.Second
	start := time.Now()
	rep := mustRun(t, cfg)
	requireClean(t, rep)
	if rep.MinTS >= start.UnixNano()-int64(time.Second) {
		t.Fatalf("no line was stamped noticeably in the past: min %s before start", time.Duration(start.UnixNano()-rep.MinTS))
	}
}

// Storage fails, so the server fills its buffer and tells clients to back off.
// The generator must back off, keep every line, and deliver them once storage is back.
func TestBackpressureThenRecoveryLosesNothing(t *testing.T) {
	srv := startServer(t, func(c *app.Config) { c.BufferBytes = 4000; c.BufferAge = 300 * time.Millisecond })
	srv.mem.FailNextPuts(1 << 30)
	time.AfterFunc(1200*time.Millisecond, func() { srv.mem.FailNextPuts(0) })
	cfg := testConfig(srv.addr, 2500*time.Millisecond)
	cfg.Schedule.Rate = 400
	// One stream, and a buffer age that is long compared with a seal: the
	// server's "buffer full" check only counts bytes not already taken by a seal
	// in progress, so a background seal running at the moment of the check can
	// hide the buffer and the push-back is not certain.
	cfg.Sources = 1
	rep := mustRun(t, cfg)
	if rep.BufferFull == 0 {
		t.Fatalf("the server never pushed back, so this test proved nothing:\n%s", rep)
	}
	requireClean(t, rep)
}

// The server is restarted in the middle of a run. The generator reconnects and
// carries on, and nothing the server acknowledged is lost.
func TestServerRestartMidRun(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	srv := startServer(t, func(c *app.Config) { c.ListenAddr = addr })

	go func() {
		time.Sleep(1 * time.Second)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.app.Shutdown(ctx)
		time.Sleep(700 * time.Millisecond) // the server is down for a moment
		a, err := app.Start(context.Background(), srv.cfg)
		if err != nil {
			t.Errorf("restart: %v", err)
			return
		}
		t.Cleanup(func() { a.Shutdown(context.Background()) })
	}()

	cfg := testConfig(addr, 4*time.Second)
	rep := mustRun(t, cfg)
	if rep.Reconnects < 1 {
		t.Fatalf("the generator never had to reconnect:\n%s", rep)
	}
	requireClean(t, rep)
}

func TestUnreachableServerFailsWithAClearError(t *testing.T) {
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := l.Addr().String()
	l.Close() // nothing listens here
	cfg := testConfig(addr, time.Second)
	cfg.ConnectTimeout = 400 * time.Millisecond
	start := time.Now()
	_, err := Run(context.Background(), cfg)
	if err == nil || !strings.Contains(err.Error(), "could not connect") {
		t.Fatalf("want a connection error, got %v", err)
	}
	if time.Since(start) > 10*time.Second {
		t.Fatal("took too long to give up")
	}
}

// Without a duration the run goes on until cancelled, then stops cleanly and
// reports what was delivered.
func TestCancellationStopsCleanly(t *testing.T) {
	srv := startServer(t, nil)
	cfg := testConfig(srv.addr, 0)
	cfg.Verify = false
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	rep, err := Run(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Acked < 150 || rep.Acked != rep.Sent {
		t.Fatalf("sent %d, acknowledged %d", rep.Sent, rep.Acked)
	}
}

// Real loss must be reported as loss: delete the only copy of some lines and
// the run must say so, and name them.
func TestRealLossIsDetected(t *testing.T) {
	srv := startServer(t, nil)
	cfg := testConfig(srv.addr, 1500*time.Millisecond)
	cfg.Verify = false
	rep := mustRun(t, cfg)
	time.Sleep(500 * time.Millisecond) // let it all seal

	// Remove one segment's row from the manifest, as if those lines had never
	// been recorded (a second connection, the way a damaged file would look).
	keys, _ := srv.mem.List(context.Background(), "segments/")
	if len(keys) == 0 {
		t.Fatal("nothing was sealed")
	}
	db, err := sql.Open("sqlite", "file:"+srv.cfg.ManifestPath+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if res, err := db.Exec(`DELETE FROM segments WHERE storage_key = ?`, keys[0]); err != nil {
		t.Fatal(err)
	} else if n, _ := res.RowsAffected(); n != 1 {
		t.Fatalf("expected to remove 1 manifest row, removed %d", n)
	}

	conn, _ := grpc.NewClient(srv.addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	defer conn.Close()
	res, err := Verify(context.Background(), stratav1.NewQueryServiceClient(conn), rep, VerifyOptions{Workers: 2, Settle: 50 * time.Millisecond, MaxAttempts: 2})
	if err != nil {
		t.Fatal(err)
	}
	if res.Lost == 0 || len(res.LostRanges) == 0 {
		t.Fatalf("a segment's lines were removed, but verification found nothing missing:\n%s", res)
	}
}
