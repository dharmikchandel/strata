package segment

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"reflect"
	"sort"
	"testing"

	"github.com/dharmikchandel/strata/internal/storage/storagetest"
)

func sampleEntries() []Entry {
	return []Entry{
		{Timestamp: 3000, Message: "Connection RESET by peer: 10.0.0.1", Tags: map[string]string{"service": "api", "env": "prod"}},
		{Timestamp: 1000, Message: "server started on port 8080", Tags: map[string]string{"service": "api"}},
		{Timestamp: 2000, Message: "disk usage high on /dev/sda1", Tags: nil},
		{Timestamp: 2000, Message: "error error ERROR repeated", Tags: map[string]string{"service": "db"}},
		{Timestamp: 4000, Message: "", Tags: nil},
	}
}

func mustRoundTrip(t *testing.T, entries []Entry) *Segment {
	t.Helper()
	data, err := Encode(entries)
	if err != nil {
		t.Fatal(err)
	}
	seg, err := Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	return seg
}

func TestRoundTripContentAndHeader(t *testing.T) {
	in := sampleEntries()
	seg := mustRoundTrip(t, in)

	if seg.Len() != len(in) {
		t.Fatalf("len: got %d want %d", seg.Len(), len(in))
	}
	if seg.MinTimestamp() != 1000 || seg.MaxTimestamp() != 4000 {
		t.Fatalf("bounds: got [%d,%d]", seg.MinTimestamp(), seg.MaxTimestamp())
	}

	// Expected order: sorted by timestamp, ties in input order.
	want := []Entry{in[1], in[2], in[3], in[0], in[4]}
	got, err := seg.Entries()
	if err != nil {
		t.Fatal(err)
	}
	for i := range want {
		if got[i].Timestamp != want[i].Timestamp || got[i].Message != want[i].Message {
			t.Errorf("entry %d: got %+v want %+v", i, got[i], want[i])
		}
		if len(got[i].Tags) != len(want[i].Tags) || (len(want[i].Tags) > 0 && !reflect.DeepEqual(got[i].Tags, want[i].Tags)) {
			t.Errorf("entry %d tags: got %v want %v", i, got[i].Tags, want[i].Tags)
		}
	}
}

func TestEncodeDoesNotMutateInput(t *testing.T) {
	in := sampleEntries()
	before := sampleEntries()
	if _, err := Encode(in); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(in, before) {
		t.Fatal("Encode reordered or modified its input")
	}
}

func TestEncodeIsDeterministic(t *testing.T) {
	a, _ := Encode(sampleEntries())
	b, _ := Encode(sampleEntries())
	if !bytes.Equal(a, b) {
		t.Fatal("same input produced different bytes")
	}
}

func TestEncodeRejectsInvalid(t *testing.T) {
	cases := map[string][]Entry{
		"empty":         nil,
		"empty tag key": {{Timestamp: 1, Message: "x", Tags: map[string]string{"": "v"}}},
		"= in tag key":  {{Timestamp: 1, Message: "x", Tags: map[string]string{"a=b": "v"}}},
	}
	for name, in := range cases {
		if _, err := Encode(in); !errors.Is(err, ErrInvalidEntry) {
			t.Errorf("%s: want ErrInvalidEntry, got %v", name, err)
		}
	}
}

// bruteForce computes the expected postings by scanning decoded entries,
// independent of how the index was built or stored.
func bruteForce(t *testing.T, seg *Segment) map[string][]uint32 {
	t.Helper()
	want := map[string][]uint32{}
	for id := 0; id < seg.Len(); id++ {
		e, err := seg.Entry(uint32(id))
		if err != nil {
			t.Fatal(err)
		}
		seen := map[string]bool{}
		terms := Tokenize(e.Message)
		for k, v := range e.Tags {
			terms = append(terms, TagTerm(k, v))
		}
		for _, term := range terms {
			if !seen[term] {
				seen[term] = true
				want[term] = append(want[term], uint32(id))
			}
		}
	}
	return want
}

func checkIndexMatchesContent(t *testing.T, seg *Segment) {
	t.Helper()
	want := bruteForce(t, seg)

	// Same set of terms: nothing missing, nothing invented.
	wantTerms := make([]string, 0, len(want))
	for term := range want {
		wantTerms = append(wantTerms, term)
	}
	sort.Strings(wantTerms)
	if got := seg.Terms(); !reflect.DeepEqual(got, wantTerms) {
		t.Fatalf("terms differ:\n got %v\nwant %v", got, wantTerms)
	}
	// Same posting list for every term.
	for term, ids := range want {
		if got := seg.Lookup(term); !reflect.DeepEqual(got, ids) {
			t.Errorf("postings for %q: got %v want %v", term, got, ids)
		}
	}
	// Bloom filter: never a false negative for an indexed term.
	for term := range want {
		if !seg.MayContain(term) {
			t.Errorf("bloom false negative for %q", term)
		}
	}
}

func TestIndexAndBloomMatchContent(t *testing.T) {
	checkIndexMatchesContent(t, mustRoundTrip(t, sampleEntries()))
}

func TestIndexLookups(t *testing.T) {
	seg := mustRoundTrip(t, sampleEntries())
	// After sorting: 0 started, 1 disk, 2 error, 3 reset, 4 empty.
	if got := seg.Lookup("error"); !reflect.DeepEqual(got, []uint32{2}) {
		t.Errorf("error (repeated in one line, listed once): got %v", got)
	}
	if got := seg.Lookup("reset"); !reflect.DeepEqual(got, []uint32{3}) {
		t.Errorf("reset (case-folded): got %v", got)
	}
	if got := seg.Lookup(TagTerm("service", "api")); !reflect.DeepEqual(got, []uint32{0, 3}) {
		t.Errorf("tag service=api: got %v", got)
	}
	if got := seg.Lookup("nonexistent"); got != nil {
		t.Errorf("missing term: got %v", got)
	}
	// A message token that looks like a tag term must not match a tag.
	if got := seg.Lookup("api"); got != nil {
		t.Errorf("tag value leaked into message terms: %v", got)
	}
}

func TestRandomizedIndexMatchesContent(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	words := []string{"alpha", "beta", "gamma", "delta", "ERR", "timeout", "ok", "db", "42", "x"}
	var entries []Entry
	for i := 0; i < 2000; i++ {
		msg := ""
		for j, n := 0, rng.Intn(8); j < n; j++ {
			msg += words[rng.Intn(len(words))] + " "
		}
		e := Entry{Timestamp: int64(rng.Intn(500)), Message: msg}
		if rng.Intn(2) == 0 {
			e.Tags = map[string]string{"svc": fmt.Sprintf("s%d", rng.Intn(5))}
		}
		entries = append(entries, e)
	}
	seg := mustRoundTrip(t, entries)
	checkIndexMatchesContent(t, seg)

	// Output must be time-ordered.
	prev := int64(-1)
	for i := 0; i < seg.Len(); i++ {
		e, _ := seg.Entry(uint32(i))
		if e.Timestamp < prev {
			t.Fatalf("entries not sorted at %d", i)
		}
		prev = e.Timestamp
	}
}

func TestBloomRejectsMostAbsentTerms(t *testing.T) {
	var entries []Entry
	for i := 0; i < 5000; i++ {
		entries = append(entries, Entry{Timestamp: int64(i), Message: fmt.Sprintf("word%d common", i)})
	}
	seg := mustRoundTrip(t, entries)
	fp := 0
	const probes = 20000
	for i := 0; i < probes; i++ {
		if seg.MayContain(fmt.Sprintf("absent%d", i)) {
			fp++
		}
	}
	if rate := float64(fp) / probes; rate > 0.02 {
		t.Fatalf("segment bloom false positive rate %.4f", rate)
	}
}

func TestEntryOutOfRange(t *testing.T) {
	seg := mustRoundTrip(t, sampleEntries())
	if _, err := seg.Entry(99); err == nil {
		t.Fatal("expected error")
	}
}

func TestDecodeRejectsCorruption(t *testing.T) {
	good, err := Encode(sampleEntries())
	if err != nil {
		t.Fatal(err)
	}

	// Flipping any single byte must be detected (checksums cover everything).
	for i := range good {
		bad := append([]byte{}, good...)
		bad[i] ^= 0xFF
		if _, err := Decode(bad); err == nil {
			t.Fatalf("byte %d flipped but Decode succeeded", i)
		}
	}
	// Every truncation must be detected.
	for n := 0; n < len(good); n++ {
		if _, err := Decode(good[:n]); err == nil {
			t.Fatalf("truncated to %d bytes but Decode succeeded", n)
		}
	}
	// Appended garbage must be detected.
	if _, err := Decode(append(append([]byte{}, good...), 0)); err == nil {
		t.Fatal("trailing byte accepted")
	}
	// And the errors are classified as corruption.
	if _, err := Decode(good[:10]); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("want ErrCorrupt, got %v", err)
	}
}

// FuzzDecode checks that arbitrary input never panics. Run with:
//
//	go test ./internal/segment -fuzz FuzzDecode -fuzztime 20s
func FuzzDecode(f *testing.F) {
	good, _ := Encode(sampleEntries())
	f.Add(good)
	f.Add([]byte("STRA"))
	f.Fuzz(func(t *testing.T, data []byte) {
		seg, err := Decode(data)
		if err != nil {
			return
		}
		_, _ = seg.Entries()
	})
}

// TestWriteToStorageAndReadBack is the end-to-end Phase 1 path: encode,
// store in the object store, fetch, decode, and confirm the content survived.
func TestWriteToStorageAndReadBack(t *testing.T) {
	store := storagetest.NewMem()
	ctx := context.Background()
	data, err := Encode(sampleEntries())
	if err != nil {
		t.Fatal(err)
	}
	const key = "segments/seg-0001.strata"
	if err := store.Put(ctx, key, bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}

	r, err := store.Get(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	fetched, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	seg, err := Decode(fetched)
	if err != nil {
		t.Fatal(err)
	}
	checkIndexMatchesContent(t, seg)
	if seg.Len() != 5 {
		t.Fatalf("len %d", seg.Len())
	}
}

func TestTokenize(t *testing.T) {
	got := Tokenize("Connection RESET by peer: 10.0.0.1, user=Zoë_42")
	want := []string{"connection", "reset", "by", "peer", "10", "0", "0", "1", "user", "zoë", "42"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}
