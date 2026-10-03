package main

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	stratav1 "github.com/dharmikchandel/strata/gen/strata/v1"
)

//go:embed ui
var uiFiles embed.FS

// uiMaxLimit caps the lines the web page can ask for, to keep pages small.
const uiMaxLimit = 1000

func runUI(args []string) error {
	fs := flag.NewFlagSet("ui", flag.ContinueOnError)
	server := fs.String("server", "127.0.0.1:7070", "Strata server address")
	listen := fs.String("listen", "127.0.0.1:7080", "address to serve the web page on")
	corpus := fs.String("corpus-name", "", "what the stored logs are, shown in the page header (e.g. \"BGL supercomputer logs\")")
	credit := fs.String("credit", "", "attribution line shown in the page footer (e.g. the dataset's citation)")
	about := fs.String("about-url", "", "link target for \"How this works\" in the footer (http or https)")
	examples := fs.String("examples", "", "JSON file of up to 4 example searches shown as one-click buttons (see bench/demo/bgl-examples.json)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	opts := uiOptions{CorpusName: *corpus, Credit: *credit, AboutURL: *about}
	if *examples != "" {
		list, err := loadExamples(*examples)
		if err != nil {
			return err
		}
		opts.Examples = list
	}
	if err := opts.validate(); err != nil {
		return err
	}
	conn, err := dial(*server)
	if err != nil {
		return err
	}
	defer conn.Close()

	h, err := newUIHandler(stratav1.NewQueryServiceClient(conn), opts)
	if err != nil {
		return err
	}
	srv := &http.Server{
		Addr:              *listen,
		Handler:           h,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      90 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	log.Printf("search UI on http://%s (Strata server %s)", *listen, *server)
	return srv.ListenAndServe()
}

// uiClient is the part of the query client the page needs (a stub in tests).
type uiClient interface {
	Search(ctx context.Context, in *stratav1.SearchRequest, opts ...grpc.CallOption) (*stratav1.SearchResponse, error)
	Stats(ctx context.Context, in *stratav1.StatsRequest, opts ...grpc.CallOption) (*stratav1.StatsResponse, error)
}

type uiHandler struct {
	client uiClient
	opts   uiOptions
	mux    *http.ServeMux
}

func newUIHandler(client uiClient, opts uiOptions) (*uiHandler, error) {
	sub, err := fs.Sub(uiFiles, "ui")
	if err != nil {
		return nil, err
	}
	h := &uiHandler{client: client, opts: opts, mux: http.NewServeMux()}
	h.mux.Handle("GET /", http.FileServerFS(sub))
	h.mux.HandleFunc("GET /api/search", h.search)
	h.mux.HandleFunc("GET /api/overview", h.overview)
	return h, nil
}

func (h *uiHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// The page is static files plus one JSON endpoint, so it can be locked down
	// hard: only its own scripts and styles may run, and nothing may frame it.
	// Log lines are untrusted text; the page only ever renders them as text.
	hd := w.Header()
	hd.Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; frame-ancestors 'none'")
	hd.Set("X-Content-Type-Options", "nosniff")
	hd.Set("Referrer-Policy", "no-referrer")
	h.mux.ServeHTTP(w, r)
}

type hitJSON struct {
	Time      string            `json:"time"`
	Message   string            `json:"message"`
	Tags      map[string]string `json:"tags,omitempty"`
	SegmentID string            `json:"segment_id"`
}

// segmentJSON is what one search did with one segment, ready to draw.
type segmentJSON struct {
	Kind      string `json:"kind"` // "time", "bloom" or "scanned"
	ID        string `json:"id"`
	First     string `json:"first"` // the segment's first and last log time, UTC
	Last      string `json:"last"`
	Size      int64  `json:"size"`
	BytesRead int64  `json:"bytes_read"`
	Hits      int64  `json:"hits"` // lines in this response that came from it
}

type searchJSON struct {
	Hits        []hitJSON        `json:"hits"`
	Truncated   bool             `json:"truncated"`
	Metrics     map[string]int64 `json:"metrics"`
	RoundTripMs float64          `json:"round_trip_ms"`
	// Segments is every segment, oldest first. Empty when the server judged
	// there were too many to list, in which case SegmentsTruncated is set and
	// the page falls back to the counts in Metrics.
	Segments          []segmentJSON `json:"segments"`
	SegmentsTruncated bool          `json:"segments_truncated"`
}

var segmentKinds = map[stratav1.SegmentOutcome_Kind]string{
	stratav1.SegmentOutcome_KIND_SKIPPED_BY_TIME:  "time",
	stratav1.SegmentOutcome_KIND_SKIPPED_BY_BLOOM: "bloom",
	stratav1.SegmentOutcome_KIND_SCANNED:          "scanned",
}

func (h *uiHandler) search(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	req := &stratav1.SearchRequest{Text: q.Get("text"), Tags: map[string]string{}, Limit: 100}

	for _, t := range q["tag"] {
		k, v, ok := strings.Cut(t, "=")
		if !ok || k == "" {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("tag %q must look like key=value", t))
			return
		}
		req.Tags[k] = v
	}
	var err error
	if req.FromUnixNano, err = parseUITime(q.Get("from")); err != nil {
		writeError(w, http.StatusBadRequest, "from: "+err.Error())
		return
	}
	if req.ToUnixNano, err = parseUITime(q.Get("to")); err != nil {
		writeError(w, http.StatusBadRequest, "to: "+err.Error())
		return
	}
	if s := q.Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > uiMaxLimit {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("limit must be between 1 and %d", uiMaxLimit))
			return
		}
		req.Limit = uint32(n)
	}

	start := time.Now()
	resp, err := h.client.Search(r.Context(), req)
	if err != nil {
		code, msg := httpStatusFor(err)
		writeError(w, code, msg)
		return
	}
	out := searchJSON{
		Truncated: resp.Truncated, RoundTripMs: float64(time.Since(start).Microseconds()) / 1000, Hits: make([]hitJSON, len(resp.Hits)),
		Segments: make([]segmentJSON, 0, len(resp.Segments)), SegmentsTruncated: resp.SegmentsTruncated,
	}
	for _, o := range resp.Segments {
		kind, ok := segmentKinds[o.Kind]
		if !ok {
			continue // an outcome this page doesn't know how to draw
		}
		out.Segments = append(out.Segments, segmentJSON{
			Kind: kind, ID: o.SegmentId,
			First: time.Unix(0, o.MinUnixNano).UTC().Format("2006-01-02 15:04"),
			Last:  time.Unix(0, o.MaxUnixNano).UTC().Format("2006-01-02 15:04"),
			Size:  int64(o.SizeBytes), BytesRead: int64(o.BytesRead), Hits: int64(o.Hits),
		})
	}
	for i, hit := range resp.Hits {
		out.Hits[i] = hitJSON{
			Time:      time.Unix(0, hit.TimestampUnixNano).UTC().Format("2006-01-02 15:04:05.000000"),
			Message:   hit.Message,
			Tags:      hit.Tags,
			SegmentID: hit.SegmentId,
		}
	}
	if m := resp.Metrics; m != nil {
		out.Metrics = map[string]int64{
			"segments_considered": int64(m.SegmentsConsidered),
			"skipped_by_time":     int64(m.SkippedByTime),
			"skipped_by_bloom":    int64(m.SkippedByBloom),
			"segments_scanned":    int64(m.SegmentsScanned),
			"bytes_read":          int64(m.BytesRead),
			"retries":             int64(m.Retries),
			"server_micros":       int64(m.ServerMicros),
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(out)
}

// parseUITime reads the formats an HTML datetime-local input produces
// ("2005-06-03T15:04" and with seconds, taken as UTC) as well as RFC 3339.
func parseUITime(s string) (int64, error) {
	if s == "" {
		return 0, nil
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05", "2006-01-02T15:04", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UnixNano(), nil
		}
	}
	return 0, errors.New("not a date/time")
}

// httpStatusFor maps a gRPC error to an HTTP status and a message that is safe
// to show to a visitor: validation messages pass through, internal details don't.
func httpStatusFor(err error) (int, string) {
	st, ok := status.FromError(err)
	if !ok {
		return http.StatusBadGateway, "the search server is unreachable"
	}
	switch st.Code() {
	case codes.InvalidArgument:
		return http.StatusBadRequest, st.Message()
	case codes.ResourceExhausted:
		return http.StatusTooManyRequests, "the server is busy; try again in a moment"
	case codes.DeadlineExceeded:
		return http.StatusGatewayTimeout, "the search took too long"
	case codes.Unavailable:
		return http.StatusServiceUnavailable, "the search server is unavailable"
	case codes.DataLoss:
		return http.StatusBadGateway, "some stored data could not be read"
	case codes.Canceled:
		return 499, "cancelled"
	default:
		return http.StatusInternalServerError, "internal error"
	}
}

func writeError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
