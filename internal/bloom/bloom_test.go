package bloom

import (
	"fmt"
	"testing"
)

func TestNoFalseNegatives(t *testing.T) {
	const n = 5000
	f := New(n, 0.01)
	for i := 0; i < n; i++ {
		f.Add(fmt.Sprintf("term-%d", i))
	}
	for i := 0; i < n; i++ {
		if !f.MayContain(fmt.Sprintf("term-%d", i)) {
			t.Fatalf("false negative for term-%d", i)
		}
	}
}

func TestFalsePositiveRateIsNearTarget(t *testing.T) {
	const n = 10000
	f := New(n, 0.01)
	for i := 0; i < n; i++ {
		f.Add(fmt.Sprintf("present-%d", i))
	}
	const probes = 100000
	fp := 0
	for i := 0; i < probes; i++ {
		if f.MayContain(fmt.Sprintf("absent-%d", i)) {
			fp++
		}
	}
	rate := float64(fp) / probes
	// Target is 1%; allow generous slack so the test is not flaky, while
	// still catching a broken hash (which would land far above this).
	if rate > 0.02 {
		t.Fatalf("false positive rate %.4f exceeds 0.02", rate)
	}
	t.Logf("measured false positive rate: %.4f", rate)
}

func TestMarshalRoundTrip(t *testing.T) {
	f := New(100, 0.01)
	for i := 0; i < 100; i++ {
		f.Add(fmt.Sprintf("t%d", i))
	}
	g, err := Unmarshal(f.Marshal())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		if !g.MayContain(fmt.Sprintf("t%d", i)) {
			t.Fatalf("false negative after round trip: t%d", i)
		}
	}
	// Same bytes in, same answers out for absent terms too.
	for i := 0; i < 1000; i++ {
		s := fmt.Sprintf("absent%d", i)
		if f.MayContain(s) != g.MayContain(s) {
			t.Fatalf("round-tripped filter disagrees on %q", s)
		}
	}
}

func TestUnmarshalRejectsGarbage(t *testing.T) {
	good := New(10, 0.01).Marshal()
	cases := map[string][]byte{
		"empty":     nil,
		"short":     good[:5],
		"truncated": good[:len(good)-1],
		"extra":     append(append([]byte{}, good...), 0),
		"zero k":    append([]byte{0, 0, 0, 0}, good[4:]...),
	}
	for name, data := range cases {
		if _, err := Unmarshal(data); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestNewHandlesDegenerateInput(t *testing.T) {
	f := New(0, -1)
	f.Add("x")
	if !f.MayContain("x") {
		t.Fatal("false negative on degenerate filter")
	}
}
