package gen

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	stratav1 "github.com/dharmikchandel/strata/gen/strata/v1"
)

// templates_bgl.json holds the 500 most common message shapes of the BGL
// supercomputer log, with their real frequencies, derived by
// bench/demo/derive_templates.py. They cover 96% of the real lines, so sampling
// them by weight gives the generated stream the real word frequencies: a few
// messages that dominate (one is 36% of all lines), and a long tail of rare
// ones, which is exactly what makes bloom filters and rare-word searches
// interesting.
//
//go:embed templates_bgl.json
var templatesJSON []byte

// Template is one message shape.
type Template struct {
	Weight    float64 `json:"weight"`
	Label     string  `json:"label"` // "-" for ordinary lines, otherwise an alert category
	Component string  `json:"component"`
	Level     string  `json:"level"`
	Text      string  `json:"text"` // may contain {n}, {hex} and {node}
}

// Content turns templates into log lines.
type Content struct {
	templates  []Template
	cumulative []float64
}

// NewContent loads the embedded templates.
func NewContent() (*Content, error) {
	var file struct {
		Templates []Template `json:"templates"`
	}
	if err := json.Unmarshal(templatesJSON, &file); err != nil {
		return nil, fmt.Errorf("gen: templates: %w", err)
	}
	return newContent(file.Templates)
}

func newContent(ts []Template) (*Content, error) {
	if len(ts) == 0 {
		return nil, fmt.Errorf("gen: no templates")
	}
	c := &Content{templates: ts, cumulative: make([]float64, len(ts))}
	var sum float64
	for i, t := range ts {
		if t.Weight <= 0 {
			return nil, fmt.Errorf("gen: template %d has weight %v", i, t.Weight)
		}
		sum += t.Weight
		c.cumulative[i] = sum
	}
	return c, nil
}

// Templates returns the loaded templates (read-only).
func (c *Content) Templates() []Template { return c.templates }

func (c *Content) pick(rng *rand.Rand) *Template {
	r := rng.Float64() * c.cumulative[len(c.cumulative)-1]
	i := sort.SearchFloat64s(c.cumulative, r)
	if i >= len(c.templates) {
		i = len(c.templates) - 1
	}
	return &c.templates[i]
}

// Source generates the lines of one stream. It is not safe for concurrent use;
// each stream has its own.
type Source struct {
	ID    int
	runID string
	c     *Content
	rng   *rand.Rand
	seq   uint64
}

// NewSource returns a source whose output is reproducible for a given seed.
// runID labels every line so the run can be found again and verified.
func (c *Content) NewSource(seed uint64, id int, runID string) *Source {
	return &Source{ID: id, runID: runID, c: c, rng: rand.New(rand.NewPCG(seed, uint64(id)+1))}
}

// Seq is how many lines this source has produced.
func (s *Source) Seq() uint64 { return s.seq }

// Next produces one log line with timestamp ts. Its message has the same shape
// as a BGL line (so the same searches work), ending in a marker, "~<run>.<source>.<sequence>",
// which lets a verification pass tell exactly which lines arrived.
func (s *Source) Next(ts time.Time) *stratav1.LogEntry {
	t := s.c.pick(s.rng)
	node := s.node()
	u := ts.UTC()
	msg := fmt.Sprintf("%s %s %s %s RAS %s %s %s ~%s.%d.%d",
		u.Format("2006.01.02"), node, u.Format("2006-01-02-15.04.05.000000"), node,
		t.Component, t.Level, s.expand(t.Text), s.runID, s.ID, s.seq)
	s.seq++
	tags := map[string]string{"component": t.Component, "level": t.Level}
	if t.Label != "-" {
		tags["alert"] = t.Label
	}
	return &stratav1.LogEntry{TimestampUnixNano: u.UnixNano(), Message: msg, Tags: tags}
}

// node returns a plausible BlueGene/L location, e.g. R02-M1-N0-C:J12-U11.
func (s *Source) node() string {
	return fmt.Sprintf("R%02d-M%d-N%X-C:J%02d-U%02d", s.rng.IntN(78), s.rng.IntN(2), s.rng.IntN(16), 2+s.rng.IntN(16), []int{1, 11}[s.rng.IntN(2)])
}

// expand fills the placeholders: {n} a number, {hex} a 0x value, {node} a location.
func (s *Source) expand(text string) string {
	if !strings.Contains(text, "{") {
		return text
	}
	var b strings.Builder
	for i := 0; i < len(text); {
		if text[i] == '{' {
			switch {
			case strings.HasPrefix(text[i:], "{n}"):
				b.WriteString(strconv.Itoa(s.rng.IntN(100000)))
				i += 3
				continue
			case strings.HasPrefix(text[i:], "{hex}"):
				fmt.Fprintf(&b, "0x%08x", s.rng.Uint32())
				i += 5
				continue
			case strings.HasPrefix(text[i:], "{node}"):
				b.WriteString(s.node())
				i += 6
				continue
			}
		}
		b.WriteByte(text[i])
		i++
	}
	return b.String()
}

var markerRe = regexp.MustCompile(`~(run[0-9a-f]{8})\.(\d+)\.(\d+)$`)

// ParseMarker extracts (run, source, sequence) from a generated message.
func ParseMarker(message string) (run string, source int, seq uint64, ok bool) {
	m := markerRe.FindStringSubmatch(message)
	if m == nil {
		return "", 0, 0, false
	}
	src, err1 := strconv.Atoi(m[2])
	sq, err2 := strconv.ParseUint(m[3], 10, 64)
	if err1 != nil || err2 != nil {
		return "", 0, 0, false
	}
	return m[1], src, sq, true
}
