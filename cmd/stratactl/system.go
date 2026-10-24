package main

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	stratav1 "github.com/dharmikchandel/strata/gen/strata/v1"
)

// systemJSON is what the system page draws: the segments as they are now,
// what the server has done since the page last asked, and its vital signs.
type systemJSON struct {
	Segments      []systemSegmentJSON `json:"segments"`
	SegmentsTotal int64               `json:"segments_total"`
	Events        []eventJSON         `json:"events"`
	Started       string              `json:"started"` // when the event log began, UTC
	LinesPerSec   float64             `json:"lines_per_sec"`
	Buffer        bufferJSON          `json:"buffer"`
	Compaction    bool                `json:"compaction"`
}

type systemSegmentJSON struct {
	ID         string `json:"id"`
	Size       int64  `json:"size"`
	Lines      int64  `json:"lines"`
	First      string `json:"first"` // first and last log time, UTC
	Last       string `json:"last"`
	Generation int    `json:"generation"`
	Created    string `json:"created"` // when the segment was written, UTC
}

type eventJSON struct {
	Seq     uint64   `json:"seq"`
	At      string   `json:"at"` // UTC, to the millisecond
	Kind    string   `json:"kind"`
	Segment string   `json:"segment,omitempty"`
	Inputs  []string `json:"inputs,omitempty"`
	Lines   int64    `json:"lines,omitempty"`
	Bytes   int64    `json:"bytes,omitempty"`
	BytesIn int64    `json:"bytes_in,omitempty"`
	Files   int64    `json:"files,omitempty"`
}

type bufferJSON struct {
	Bytes     int64 `json:"bytes"`         // waiting to be sealed
	SealAt    int64 `json:"seal_at"`       // a segment is sealed at this size
	SealAfter int64 `json:"seal_after_ms"` // or when its oldest line has waited this long
	Limit     int64 `json:"limit"`         // new lines are refused at this size
}

var eventKinds = map[stratav1.SystemEvent_Kind]string{
	stratav1.SystemEvent_KIND_SEALED:  "sealed",
	stratav1.SystemEvent_KIND_MERGED:  "merged",
	stratav1.SystemEvent_KIND_CLEANED: "cleaned",
}

const (
	minuteForm = "2006-01-02 15:04"
	milliForm  = "2006-01-02 15:04:05.000"
)

// system serves /api/system?after=<last event seq seen>.
func (h *uiHandler) system(w http.ResponseWriter, r *http.Request) {
	var after uint64
	if s := r.URL.Query().Get("after"); s != "" {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "after must be an event number")
			return
		}
		after = n
	}
	resp, err := h.client.System(r.Context(), &stratav1.SystemRequest{EventsAfter: after})
	if err != nil {
		code, msg := httpStatusFor(err)
		writeError(w, code, msg)
		return
	}
	utc := func(ns int64, form string) string { return time.Unix(0, ns).UTC().Format(form) }
	out := systemJSON{
		Segments: make([]systemSegmentJSON, 0, len(resp.Segments)), SegmentsTotal: int64(resp.SegmentsTotal),
		Events: make([]eventJSON, 0, len(resp.Events)), LinesPerSec: resp.IngestLinesPerSecond,
		Buffer: bufferJSON{Bytes: int64(resp.BufferBytes), SealAt: int64(resp.BufferSealBytes),
			SealAfter: resp.BufferSealAfterMillis, Limit: int64(resp.BufferLimitBytes)},
		Compaction: resp.Compaction,
	}
	if resp.StartedUnixNano != 0 {
		out.Started = utc(resp.StartedUnixNano, minuteForm)
	}
	for _, s := range resp.Segments {
		out.Segments = append(out.Segments, systemSegmentJSON{
			ID: s.SegmentId, Size: int64(s.SizeBytes), Lines: int64(s.Lines),
			First: utc(s.MinUnixNano, minuteForm), Last: utc(s.MaxUnixNano, minuteForm),
			Generation: int(s.Generation), Created: utc(s.CreatedUnixNano, milliForm),
		})
	}
	for _, e := range resp.Events {
		kind, ok := eventKinds[e.Kind]
		if !ok {
			continue // an event this page doesn't know how to describe
		}
		out.Events = append(out.Events, eventJSON{
			Seq: e.Seq, At: utc(e.AtUnixNano, milliForm), Kind: kind, Segment: e.SegmentId, Inputs: e.Inputs,
			Lines: int64(e.Lines), Bytes: int64(e.Bytes), BytesIn: int64(e.BytesIn), Files: int64(e.Files),
		})
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(out)
}
