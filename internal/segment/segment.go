package segment

import (
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"sort"

	"github.com/dharmikchandel/strata/internal/bloom"
)

// Segment is a decoded, read-only view of a segment file image.
//
// Decode parses the index and bloom filter eagerly (they are small and every
// query needs them) but leaves log lines encoded; Entry decodes one on demand
// using the offset table. A Segment never mutates after Decode, so it is safe
// for concurrent use.
type Segment struct {
	hdr     header
	offsets []uint32 // start of each record, relative to records
	records []byte
	index   map[string][]uint32
	bloom   *bloom.Filter
}

// Decode validates data and returns a Segment. It retains data, so the caller
// must not modify it afterwards.
//
// Validation is deliberately strict: header checksum, body checksum, section
// bounds, offset table, and posting list ids are all checked, so a segment
// that decodes successfully can be trusted by everything downstream.
func Decode(data []byte) (*Segment, error) {
	h, err := parseHeader(data)
	if err != nil {
		return nil, err
	}
	if err := checkLayout(h, len(data)); err != nil {
		return nil, err
	}
	if crc32.ChecksumIEEE(data[headerSize:]) != h.BodyCRC {
		return nil, corrupt("body checksum mismatch")
	}

	s := &Segment{hdr: h}
	if err := s.decodeLines(data[h.LinesOff : h.LinesOff+h.LinesLen]); err != nil {
		return nil, err
	}
	if err := s.decodeIndex(data[h.IndexOff : h.IndexOff+h.IndexLen]); err != nil {
		return nil, err
	}
	s.bloom, err = bloom.Unmarshal(data[h.BloomOff : h.BloomOff+h.BloomLen])
	if err != nil {
		return nil, corrupt("bloom: %v", err)
	}
	return s, nil
}

// checkLayout verifies that the sections tile the file exactly:
// header | lines | index | bloom. Checking this before slicing means the
// slices taken from the offsets cannot go out of range.
func checkLayout(h header, size int) error {
	if h.LinesOff != uint64(headerSize) ||
		h.IndexOff != h.LinesOff+h.LinesLen ||
		h.BloomOff != h.IndexOff+h.IndexLen ||
		h.BloomOff+h.BloomLen != uint64(size) ||
		h.LinesLen > uint64(size) || h.IndexLen > uint64(size) {
		return corrupt("section layout does not match file size")
	}
	return nil
}

// ReadBloom returns the serialized bloom filter of a segment image without
// decoding the rest of it. The manifest stores this copy so queries can rule a
// segment out without fetching it. It checks the header and layout but does
// not verify the body checksum (use Decode for that).
func ReadBloom(data []byte) ([]byte, error) {
	h, err := parseHeader(data)
	if err != nil {
		return nil, err
	}
	if err := checkLayout(h, len(data)); err != nil {
		return nil, err
	}
	return append([]byte(nil), data[h.BloomOff:h.BloomOff+h.BloomLen]...), nil
}

func (s *Segment) decodeLines(sec []byte) error {
	tableLen := uint64(s.hdr.Count) * 4
	if tableLen > uint64(len(sec)) {
		return corrupt("offset table larger than lines section")
	}
	s.records = sec[tableLen:]
	s.offsets = make([]uint32, s.hdr.Count)
	for i := range s.offsets {
		off := binary.LittleEndian.Uint32(sec[4*i:])
		// Offsets must start at zero, strictly increase, and stay in range,
		// so Entry can slice records without further checks on the start.
		if (i == 0 && off != 0) || (i > 0 && off <= s.offsets[i-1]) || uint64(off) >= uint64(len(s.records)) {
			return corrupt("bad record offset %d at line %d", off, i)
		}
		s.offsets[i] = off
	}
	return nil
}

func (s *Segment) decodeIndex(sec []byte) error {
	c := &cursor{b: sec}
	nTerms := c.uvarint()
	// Every term needs at least 3 bytes (length, one byte of term, count), so
	// a larger claimed count is a lie; reject it before allocating the map.
	if c.err == nil && nTerms > uint64(len(sec)) {
		return corrupt("term count exceeds index size")
	}
	s.index = make(map[string][]uint32, nTerms)
	var prevTerm string
	for i := uint64(0); i < nTerms && c.err == nil; i++ {
		term := string(c.bytes(c.uvarint()))
		n := c.uvarint()
		if c.err != nil {
			break
		}
		if i > 0 && term <= prevTerm {
			return corrupt("index terms out of order")
		}
		prevTerm = term
		if n == 0 || n > uint64(s.hdr.Count) {
			return corrupt("bad posting count %d for %q", n, term)
		}
		list := make([]uint32, 0, n)
		var prev uint64
		for j := uint64(0); j < n; j++ {
			delta := c.uvarint()
			if c.err != nil {
				break
			}
			id := prev + delta
			if (j > 0 && delta == 0) || id >= uint64(s.hdr.Count) {
				return corrupt("bad posting id for %q", term)
			}
			list = append(list, uint32(id))
			prev = id
		}
		s.index[term] = list
	}
	if c.err != nil {
		return c.err
	}
	if len(c.b) != 0 {
		return corrupt("trailing bytes in index")
	}
	return nil
}

// Len is the number of entries in the segment.
func (s *Segment) Len() int { return int(s.hdr.Count) }

// MinTimestamp and MaxTimestamp bound the entry timestamps (unix nanoseconds).
func (s *Segment) MinTimestamp() int64 { return s.hdr.MinTS }
func (s *Segment) MaxTimestamp() int64 { return s.hdr.MaxTS }

// Bloom returns the segment's bloom filter over all indexed terms.
func (s *Segment) Bloom() *bloom.Filter { return s.bloom }

// MayContain is a cheap pre-check: false means the term is definitely not
// in this segment, so the segment can be skipped without a lookup.
func (s *Segment) MayContain(term string) bool { return s.bloom.MayContain(term) }

// Lookup returns the ascending ids of lines containing term (an already
// tokenized term, or a TagTerm). The returned slice is shared; do not modify it.
func (s *Segment) Lookup(term string) []uint32 { return s.index[term] }

// Terms returns every indexed term in sorted order.
func (s *Segment) Terms() []string {
	terms := make([]string, 0, len(s.index))
	for t := range s.index {
		terms = append(terms, t)
	}
	sort.Strings(terms)
	return terms
}

// Entry decodes line id.
func (s *Segment) Entry(id uint32) (Entry, error) {
	if int(id) >= len(s.offsets) {
		return Entry{}, fmt.Errorf("segment: line %d out of range (have %d)", id, len(s.offsets))
	}
	c := &cursor{b: s.records[s.offsets[id]:]}
	e := Entry{Timestamp: c.int64()}
	e.Message = string(c.bytes(c.uvarint()))
	nTags := c.uvarint()
	if c.err == nil && nTags > uint64(len(c.b)) {
		return Entry{}, corrupt("tag count exceeds record size at line %d", id)
	}
	if nTags > 0 {
		e.Tags = make(map[string]string, nTags)
	}
	for i := uint64(0); i < nTags && c.err == nil; i++ {
		k := string(c.bytes(c.uvarint()))
		v := string(c.bytes(c.uvarint()))
		e.Tags[k] = v
	}
	if c.err != nil {
		return Entry{}, fmt.Errorf("line %d: %w", id, c.err)
	}
	return e, nil
}

// Timestamp returns the timestamp of line id without decoding the rest of
// the line.
func (s *Segment) Timestamp(id uint32) (int64, error) {
	if int(id) >= len(s.offsets) {
		return 0, fmt.Errorf("segment: line %d out of range (have %d)", id, len(s.offsets))
	}
	rec := s.records[s.offsets[id]:]
	if len(rec) < 8 {
		return 0, corrupt("record %d too short", id)
	}
	return int64(binary.LittleEndian.Uint64(rec)), nil
}

// IDRange returns the line ids whose timestamps fall in [from, to) as the
// half-open id range [lo, hi). Lines are stored sorted by timestamp, so this
// is a binary search that decodes only O(log n) timestamps, instead of
// scanning every line.
func (s *Segment) IDRange(from, to int64) (lo, hi uint32, err error) {
	n := s.Len()
	firstAtOrAfter := func(ts int64) (uint32, error) {
		var searchErr error
		i := sort.Search(n, func(i int) bool {
			t, err := s.Timestamp(uint32(i))
			if err != nil {
				searchErr = err
				return true
			}
			return t >= ts
		})
		return uint32(i), searchErr
	}
	if lo, err = firstAtOrAfter(from); err != nil {
		return 0, 0, err
	}
	if hi, err = firstAtOrAfter(to); err != nil {
		return 0, 0, err
	}
	if hi < lo {
		hi = lo
	}
	return lo, hi, nil
}

// Entries decodes every line in order.
func (s *Segment) Entries() ([]Entry, error) {
	out := make([]Entry, s.Len())
	for i := range out {
		e, err := s.Entry(uint32(i))
		if err != nil {
			return nil, err
		}
		out[i] = e
	}
	return out, nil
}
