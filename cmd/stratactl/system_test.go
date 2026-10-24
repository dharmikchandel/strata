package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	stratav1 "github.com/dharmikchandel/strata/gen/strata/v1"
)

func TestSystemShapesSegmentsEventsAndLoad(t *testing.T) {
	at := time.Date(2026, 10, 25, 9, 30, 15, 250e6, time.UTC).UnixNano()
	stub := &stubSearcher{sys: &stratav1.SystemResponse{
		Segments:      []*stratav1.SegmentInfo{{SegmentId: "s1", SizeBytes: 4096, Lines: 10, MinUnixNano: at, MaxUnixNano: at, Generation: 2, CreatedUnixNano: at}},
		SegmentsTotal: 5000,
		Events: []*stratav1.SystemEvent{
			{Seq: 8, AtUnixNano: at, Kind: stratav1.SystemEvent_KIND_MERGED, SegmentId: "m", Inputs: []string{"a", "b"}, Lines: 20, Bytes: 900, BytesIn: 1000},
			{Seq: 9, AtUnixNano: at, Kind: stratav1.SystemEvent_KIND_UNSPECIFIED}, // from a newer server: left out
		},
		StartedUnixNano: at, IngestLinesPerSecond: 12.5, BufferBytes: 1, BufferSealBytes: 2, BufferLimitBytes: 8, BufferSealAfterMillis: 5000, Compaction: true,
	}}
	h, _ := newUIHandler(stub, uiOptions{})
	rec := get(t, h, "/api/system?after=7")
	if rec.Code != 200 || stub.sysReq.EventsAfter != 7 {
		t.Fatalf("status %d, after %v", rec.Code, stub.sysReq)
	}
	var out systemJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if s := out.Segments[0]; s.ID != "s1" || s.Generation != 2 || s.First != "2026-10-25 09:30" || s.Created != "2026-10-25 09:30:15.250" || out.SegmentsTotal != 5000 {
		t.Fatalf("segments: %+v", out)
	}
	if len(out.Events) != 1 || out.Events[0].Kind != "merged" || len(out.Events[0].Inputs) != 2 || out.Events[0].At != "2026-10-25 09:30:15.250" {
		t.Fatalf("events: %+v", out.Events)
	}
	if out.LinesPerSec != 12.5 || out.Buffer != (bufferJSON{1, 2, 5000, 8}) || !out.Compaction || out.Started != "2026-10-25 09:30" {
		t.Fatalf("vitals: %+v", out)
	}
	if rec := get(t, h, "/api/system?after=x"); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad after: %d", rec.Code)
	}
}

func TestOverviewListsTags(t *testing.T) {
	stub := &stubSearcher{stats: &stratav1.StatsResponse{Entries: 1, Tags: []*stratav1.TagKey{{Key: "level", Values: []string{"FATAL", "INFO"}}, {Key: "id", Values: []string{"1"}, Capped: true}}}}
	h, _ := newUIHandler(stub, uiOptions{})
	var out overviewJSON
	json.Unmarshal(get(t, h, "/api/overview").Body.Bytes(), &out)
	if len(out.Tags) != 2 || out.Tags[0].Key != "level" || len(out.Tags[0].Values) != 2 || !out.Tags[1].Capped {
		t.Fatalf("tags: %+v", out.Tags)
	}
	// Nothing stored: an empty list, not null.
	stub.stats = &stratav1.StatsResponse{}
	rec := get(t, h, "/api/overview")
	if body := rec.Body.String(); !json.Valid([]byte(body)) || !strings.Contains(body, `"tags":[]`) {
		t.Fatalf("empty store: %s", body)
	}
}
