package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"time"

	stratav1 "github.com/dharmikchandel/strata/gen/strata/v1"
)

const (
	maxExamples  = 4  // more than four options at one decision point is too many
	maxTitleLen  = 60 // keep the buttons readable
	maxNoteLen   = 140
	datetimeForm = "2006-01-02T15:04" // what an HTML datetime-local input accepts
)

// example is one search the page offers as a one-click button. It exists to
// show a visitor, in seconds, what the search can do and what it skips.
type example struct {
	Title string            `json:"title"` // the button label
	Note  string            `json:"note"`  // one line saying what this search demonstrates
	Text  string            `json:"text"`
	Tags  map[string]string `json:"tags"`
	From  string            `json:"from"` // UTC, e.g. 2005-06-20T13:00
	To    string            `json:"to"`
	Limit int               `json:"limit"`
}

// uiOptions is the page's presentation settings. All of it is optional: with
// none set, the page works with no strip, examples or footer.
type uiOptions struct {
	CorpusName string
	Credit     string
	AboutURL   string
	Examples   []example
}

func (o uiOptions) validate() error {
	if o.AboutURL != "" {
		u, err := url.Parse(o.AboutURL)
		// Only http(s): the URL becomes a link, and a "javascript:" link would
		// run script when clicked.
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("-about-url must be an http or https URL, got %q", o.AboutURL)
		}
	}
	if len(o.Examples) > maxExamples {
		return fmt.Errorf("at most %d examples are allowed, got %d", maxExamples, len(o.Examples))
	}
	for i, e := range o.Examples {
		where := fmt.Sprintf("example %d", i+1)
		if e.Title == "" || len(e.Title) > maxTitleLen {
			return fmt.Errorf("%s: title must be 1 to %d characters", where, maxTitleLen)
		}
		if len(e.Note) > maxNoteLen {
			return fmt.Errorf("%s: note must be at most %d characters", where, maxNoteLen)
		}
		if e.Text == "" && len(e.Tags) == 0 && e.From == "" && e.To == "" {
			return fmt.Errorf("%s: needs at least words, a tag or a time range", where)
		}
		if e.Limit < 0 || e.Limit > uiMaxLimit {
			return fmt.Errorf("%s: limit must be between 1 and %d", where, uiMaxLimit)
		}
		from, err := parseUITime(e.From)
		if err != nil {
			return fmt.Errorf("%s: from: %w", where, err)
		}
		to, err := parseUITime(e.To)
		if err != nil {
			return fmt.Errorf("%s: to: %w", where, err)
		}
		if from != 0 && to != 0 && from >= to {
			return fmt.Errorf("%s: from must be earlier than to", where)
		}
		for k := range e.Tags {
			if k == "" {
				return fmt.Errorf("%s: empty tag key", where)
			}
		}
	}
	return nil
}

// loadExamples reads and validates an examples file. Unknown fields are
// rejected so a typo ("limt") fails loudly instead of being silently ignored.
func loadExamples(path string) ([]example, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var list []example
	if err := dec.Decode(&list); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if err := (uiOptions{Examples: list}).validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return list, nil
}

type corpusJSON struct {
	Name        string `json:"name,omitempty"`
	Segments    int64  `json:"segments"`
	Entries     int64  `json:"entries"`
	StoredBytes int64  `json:"stored_bytes"`
	// First and last log times, for display (UTC) and as the limits of the
	// date inputs (a minute earlier / later so the first and last lines are inside).
	First    string `json:"first"`
	Last     string `json:"last"`
	MinInput string `json:"min_input"`
	MaxInput string `json:"max_input"`
}

type overviewJSON struct {
	// Corpus is null when nothing is stored yet.
	Corpus   *corpusJSON `json:"corpus"`
	Examples []example   `json:"examples"`
	Credit   string      `json:"credit,omitempty"`
	AboutURL string      `json:"about_url,omitempty"`
}

// overview gives the page what it needs to introduce itself: what is stored,
// what to try, and where to read more.
func (h *uiHandler) overview(w http.ResponseWriter, r *http.Request) {
	st, err := h.client.Stats(r.Context(), &stratav1.StatsRequest{})
	if err != nil {
		code, msg := httpStatusFor(err)
		writeError(w, code, msg)
		return
	}
	out := overviewJSON{Examples: h.opts.Examples, Credit: h.opts.Credit, AboutURL: h.opts.AboutURL}
	if out.Examples == nil {
		out.Examples = []example{}
	}
	if st.Entries > 0 {
		first, last := time.Unix(0, st.MinUnixNano).UTC(), time.Unix(0, st.MaxUnixNano).UTC()
		out.Corpus = &corpusJSON{
			Name: h.opts.CorpusName, Segments: int64(st.Segments), Entries: int64(st.Entries), StoredBytes: int64(st.StoredBytes),
			First:    first.Format("2006-01-02 15:04"),
			Last:     last.Format("2006-01-02 15:04"),
			MinInput: first.Truncate(time.Minute).Format(datetimeForm),
			MaxInput: last.Truncate(time.Minute).Add(time.Minute).Format(datetimeForm),
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(out)
}
