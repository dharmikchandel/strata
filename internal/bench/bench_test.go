package bench

import (
	"strings"
	"testing"
	"time"

	stratav1 "github.com/dharmikchandel/strata/gen/strata/v1"
)

const normalLine = "- 1117838570 2005.06.03 R02-M1-N0-C:J12-U11 2005-06-03-15.42.50.363779 R02-M1-N0-C:J12-U11 RAS KERNEL INFO instruction cache parity error corrected"
const alertLine = "KERNDTLB 1117838571 2005.06.03 R02-M1-N0-C:J12-U11 2005-06-03-15.42.51.000001 R02-M1-N0-C:J12-U11 RAS KERNEL FATAL data TLB error interrupt"

func TestParseBGLLine(t *testing.T) {
	e, err := ParseBGLLine(normalLine)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2005, 6, 3, 15, 42, 50, 363779000, time.UTC).UnixNano()
	if e.TimestampUnixNano != want {
		t.Errorf("timestamp %d, want %d (microsecond precision)", e.TimestampUnixNano, want)
	}
	if !strings.HasPrefix(e.Message, "2005.06.03 R02-M1-N0-C:J12-U11 2005-06-03-15.42.50.363779") ||
		!strings.HasSuffix(e.Message, "instruction cache parity error corrected") {
		t.Errorf("message: %q", e.Message)
	}
	if strings.HasPrefix(e.Message, "-") || strings.Contains(e.Message, "1117838570") {
		t.Errorf("label/epoch not stripped: %q", e.Message)
	}
	if e.Tags["level"] != "INFO" || e.Tags["component"] != "KERNEL" {
		t.Errorf("tags: %v", e.Tags)
	}
	if _, has := e.Tags["alert"]; has {
		t.Errorf("ordinary line got an alert tag: %v", e.Tags)
	}

	e, err = ParseBGLLine(alertLine)
	if err != nil {
		t.Fatal(err)
	}
	if e.Tags["alert"] != "KERNDTLB" || e.Tags["level"] != "FATAL" {
		t.Errorf("alert tags: %v", e.Tags)
	}
}

func TestParseBGLLineRejectsGarbage(t *testing.T) {
	for _, bad := range []string{"", "too few fields", "- 1 2 3 not-a-time 5 6 7 8"} {
		if _, err := ParseBGLLine(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestReadBGLBatchesAndLimits(t *testing.T) {
	input := strings.Join([]string{normalLine, "garbage line", alertLine, normalLine, normalLine}, "\n")
	var sizes []int
	lines, skipped, err := readBGL(strings.NewReader(input), 2, 0, func(b []*stratav1.LogEntry) error {
		sizes = append(sizes, len(b))
		return nil
	})
	if err != nil || lines != 4 || skipped != 1 {
		t.Fatalf("lines=%d skipped=%d err=%v", lines, skipped, err)
	}
	if len(sizes) != 2 || sizes[0] != 2 || sizes[1] != 2 {
		t.Fatalf("batch sizes %v", sizes)
	}
	lines, _, _ = readBGL(strings.NewReader(input), 10, 3, func([]*stratav1.LogEntry) error { return nil })
	if lines != 3 {
		t.Fatalf("maxLines ignored: %d", lines)
	}
}

func TestPercentileNearestRank(t *testing.T) {
	var s []time.Duration
	for i := 1; i <= 100; i++ {
		s = append(s, time.Duration(i)*time.Millisecond)
	}
	for _, c := range []struct {
		p    float64
		want time.Duration
	}{{50, 50 * time.Millisecond}, {99, 99 * time.Millisecond}, {100, 100 * time.Millisecond}, {1, 1 * time.Millisecond}} {
		if got := Percentile(s, c.p); got != c.want {
			t.Errorf("p%v = %v, want %v", c.p, got, c.want)
		}
	}
	// Order of the input must not matter, and the input must not be modified.
	shuffled := []time.Duration{5, 1, 4, 2, 3}
	if got := Percentile(shuffled, 50); got != 3 {
		t.Errorf("median of 1..5 = %v", got)
	}
	if shuffled[0] != 5 {
		t.Error("Percentile sorted the caller's slice")
	}
	if Percentile(nil, 50) != 0 || Mean(nil) != 0 {
		t.Error("empty input must give 0")
	}
	if Mean([]time.Duration{2, 4}) != 3 {
		t.Error("mean")
	}
	// A single sample is every percentile.
	if Percentile([]time.Duration{7}, 99) != 7 {
		t.Error("single sample")
	}
}
