package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	stratav1 "github.com/dharmikchandel/strata/gen/strata/v1"
)

func TestOverviewDescribesTheCorpus(t *testing.T) {
	first := time.Date(2005, 6, 3, 15, 42, 50, 0, time.UTC)
	last := time.Date(2006, 1, 4, 8, 0, 5, 0, time.UTC)
	stub := &stubSearcher{stats: &stratav1.StatsResponse{
		Segments: 17, Entries: 4747963, StoredBytes: 1056964608, MinUnixNano: first.UnixNano(), MaxUnixNano: last.UnixNano(),
	}}
	h, _ := newUIHandler(stub, uiOptions{
		CorpusName: "BGL supercomputer logs", Credit: "Data: BGL via LogHub.", AboutURL: "https://example.com/readme",
		Examples: []example{{Title: "A rare word", Note: "bloom filters skip most", Text: "hangtest", Limit: 20}},
	})
	rec := get(t, h, "/api/overview")
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var o overviewJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &o); err != nil {
		t.Fatal(err)
	}
	c := o.Corpus
	if c == nil || c.Name != "BGL supercomputer logs" || c.Segments != 17 || c.Entries != 4747963 || c.StoredBytes != 1056964608 {
		t.Fatalf("corpus: %+v", c)
	}
	if c.First != "2005-06-03 15:42" || c.Last != "2006-01-04 08:00" {
		t.Fatalf("display times: %q .. %q", c.First, c.Last)
	}
	// The date inputs must include the first and last line: a minute earlier and
	// later than the (minute-truncated) bounds, never excluding real data.
	if c.MinInput != "2005-06-03T15:42" || c.MaxInput != "2006-01-04T08:01" {
		t.Fatalf("input bounds: %q .. %q", c.MinInput, c.MaxInput)
	}
	if o.Credit != "Data: BGL via LogHub." || o.AboutURL != "https://example.com/readme" || len(o.Examples) != 1 || o.Examples[0].Text != "hangtest" {
		t.Fatalf("options not passed through: %+v", o)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("overview must not be cached: the counts change as logs arrive")
	}
}

func TestOverviewWithNothingStoredHasNoCorpus(t *testing.T) {
	h, _ := newUIHandler(&stubSearcher{}, uiOptions{})
	rec := get(t, h, "/api/overview")
	var raw map[string]json.RawMessage
	json.Unmarshal(rec.Body.Bytes(), &raw)
	if string(raw["corpus"]) != "null" {
		t.Fatalf("corpus should be null for an empty store, got %s", raw["corpus"])
	}
	if string(raw["examples"]) != "[]" { // an empty list, never null, so the page needn't special-case it
		t.Fatalf("examples should be [], got %s", raw["examples"])
	}
}

func TestOverviewMapsServerErrors(t *testing.T) {
	h, _ := newUIHandler(&stubSearcher{sErr: status.Error(codes.Unavailable, "10.0.0.5:7070 refused")}, uiOptions{})
	rec := get(t, h, "/api/overview")
	if rec.Code != 503 || strings.Contains(rec.Body.String(), "10.0.0.5") {
		t.Fatalf("status %d body %s", rec.Code, rec.Body)
	}
}

func TestExamplesAreValidated(t *testing.T) {
	ok := example{Title: "Fine", Text: "x"}
	bad := map[string]func() uiOptions{
		"too many": func() uiOptions { return uiOptions{Examples: []example{ok, ok, ok, ok, ok}} },
		"no title": func() uiOptions { return uiOptions{Examples: []example{{Text: "x"}}} },
		"long title": func() uiOptions {
			return uiOptions{Examples: []example{{Title: strings.Repeat("a", maxTitleLen+1), Text: "x"}}}
		},
		"long note": func() uiOptions {
			return uiOptions{Examples: []example{{Title: "t", Note: strings.Repeat("a", maxNoteLen+1), Text: "x"}}}
		},
		"empty query": func() uiOptions { return uiOptions{Examples: []example{{Title: "t"}}} },
		"bad limit":   func() uiOptions { return uiOptions{Examples: []example{{Title: "t", Text: "x", Limit: 5000}}} },
		"bad from":    func() uiOptions { return uiOptions{Examples: []example{{Title: "t", Text: "x", From: "yesterday"}}} },
		"backwards": func() uiOptions {
			return uiOptions{Examples: []example{{Title: "t", From: "2005-06-02", To: "2005-06-01"}}}
		},
		"javascript link": func() uiOptions { return uiOptions{AboutURL: "javascript:alert(1)"} },
		"relative link":   func() uiOptions { return uiOptions{AboutURL: "/readme"} },
		"data link":       func() uiOptions { return uiOptions{AboutURL: "data:text/html,hi"} },
	}
	for name, mk := range bad {
		if err := mk().validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	good := uiOptions{AboutURL: "https://github.com/x/y#readme", Examples: []example{ok, {Title: "Window", From: "2005-06-20T13:00", To: "2005-06-20T14:00", Tags: map[string]string{"level": "FATAL"}}}}
	if err := good.validate(); err != nil {
		t.Errorf("valid options rejected: %v", err)
	}
}

func TestLoadExamplesRejectsTypos(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		os.WriteFile(p, []byte(body), 0o644)
		return p
	}
	if list, err := loadExamples(write("ok.json", `[{"title":"A","note":"n","text":"hangtest","limit":20}]`)); err != nil || len(list) != 1 || list[0].Limit != 20 {
		t.Fatalf("%v %v", list, err)
	}
	for name, body := range map[string]string{
		"typo.json":    `[{"title":"A","text":"x","limt":5}]`, // misspelt field must not be silently ignored
		"notjson.json": `not json`,
		"object.json":  `{"title":"A"}`,
		"invalid.json": `[{"title":"","text":"x"}]`,
	} {
		if _, err := loadExamples(write(name, body)); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if _, err := loadExamples(filepath.Join(dir, "missing.json")); err == nil || !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing file: %v", err)
	}
}

// The examples file shipped for the demo dataset must stay valid: a typo there
// would only surface when someone starts the demo.
func TestShippedExamplesFileIsValid(t *testing.T) {
	list, err := loadExamples("../../bench/demo/bgl-examples.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) < 2 || len(list) > maxExamples {
		t.Fatalf("%d examples", len(list))
	}
	for _, e := range list {
		if e.Note == "" {
			t.Errorf("example %q has no note: each should say what it demonstrates", e.Title)
		}
	}
}
