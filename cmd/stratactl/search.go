package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	stratav1 "github.com/dharmikchandel/strata/gen/strata/v1"
)

// tagFlags collects repeated -tag key=value flags.
type tagFlags map[string]string

func (t tagFlags) String() string { return fmt.Sprint(map[string]string(t)) }
func (t tagFlags) Set(v string) error {
	k, val, ok := strings.Cut(v, "=")
	if !ok || k == "" {
		return fmt.Errorf("want key=value, got %q", v)
	}
	t[k] = val
	return nil
}

// parseTime accepts an RFC 3339 time ("2005-06-03T15:00:00Z"), a plain number
// of Unix nanoseconds, or "" for unbounded.
func parseTime(s string) (int64, error) {
	if s == "" {
		return 0, nil
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return n, nil
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return 0, fmt.Errorf("%q is neither RFC 3339 nor Unix nanoseconds", s)
	}
	return t.UnixNano(), nil
}

func runSearch(args []string) error {
	fs := flag.NewFlagSet("search", flag.ContinueOnError)
	addr := fs.String("addr", "127.0.0.1:7070", "server address")
	text := fs.String("text", "", "words that must all appear in the message")
	from := fs.String("from", "", "start of the time range (inclusive): RFC 3339 or Unix ns")
	to := fs.String("to", "", "end of the time range (exclusive): RFC 3339 or Unix ns")
	limit := fs.Uint("limit", 20, "maximum number of lines to return")
	asJSON := fs.Bool("json", false, "print the raw response as JSON")
	tags := tagFlags{}
	fs.Var(tags, "tag", "exact-match tag filter key=value (repeatable)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	fromNs, err := parseTime(*from)
	if err != nil {
		return fmt.Errorf("-from: %w", err)
	}
	toNs, err := parseTime(*to)
	if err != nil {
		return fmt.Errorf("-to: %w", err)
	}

	conn, err := dial(*addr)
	if err != nil {
		return err
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	start := time.Now()
	resp, err := stratav1.NewQueryServiceClient(conn).Search(ctx, &stratav1.SearchRequest{
		Text: *text, Tags: tags, FromUnixNano: fromNs, ToUnixNano: toNs, Limit: uint32(*limit),
	})
	if err != nil {
		return err
	}
	roundTrip := time.Since(start)

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(resp)
	}
	for _, h := range resp.Hits {
		fmt.Printf("%s  %s", time.Unix(0, h.TimestampUnixNano).UTC().Format("2006-01-02T15:04:05.000000Z"), h.Message)
		if len(h.Tags) > 0 {
			fmt.Printf("  %v", h.Tags)
		}
		fmt.Println()
	}
	if resp.Truncated {
		fmt.Printf("... more lines match; showing the earliest %d (raise -limit)\n", len(resp.Hits))
	}
	if len(resp.Hits) == 0 {
		fmt.Println("(no matches)")
	}
	fmt.Println()
	fmt.Print(formatMetrics(resp.Metrics, roundTrip))
	return nil
}

// formatMetrics shows what the search read and what it skipped, with a bar
// that makes the ratio visible at a glance.
func formatMetrics(m *stratav1.SearchMetrics, roundTrip time.Duration) string {
	if m == nil {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "segments  considered %d | skipped by time %d | skipped by bloom filter %d | scanned %d\n",
		m.SegmentsConsidered, m.SkippedByTime, m.SkippedByBloom, m.SegmentsScanned)
	fmt.Fprintf(&b, "          %s\n", bar(m))
	fmt.Fprintf(&b, "read      %s from storage | server time %s | round trip %s\n",
		humanBytes(m.BytesRead), (time.Duration(m.ServerMicros) * time.Microsecond).Round(10*time.Microsecond), roundTrip.Round(100*time.Microsecond))
	if m.Retries > 0 {
		fmt.Fprintf(&b, "          (restarted %d time(s): a segment was replaced during the search)\n", m.Retries)
	}
	return b.String()
}

// bar draws segments as characters: '.' skipped by time, '-' skipped by bloom
// filter, '#' scanned.
func bar(m *stratav1.SearchMetrics) string {
	const width = 60
	total := int(m.SegmentsConsidered)
	if total == 0 {
		return "(no segments yet)"
	}
	scale := func(n uint32) int { return int(float64(n) / float64(total) * width) }
	t, bl, sc := scale(m.SkippedByTime), scale(m.SkippedByBloom), scale(m.SegmentsScanned)
	if m.SegmentsScanned > 0 && sc == 0 {
		sc = 1 // always show that something was read
	}
	return "[" + strings.Repeat(".", t) + strings.Repeat("-", bl) + strings.Repeat("#", sc) + "]  . time  - bloom  # scanned"
}

func humanBytes(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := uint64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGT"[exp])
}
