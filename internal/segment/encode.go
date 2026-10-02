package segment

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"math"
	"sort"
	"strings"

	"github.com/dharmikchandel/strata/internal/bloom"
)

// bloomFalsePositiveRate is the target rate for the per-segment filter.
// 1% costs about 9.6 bits per distinct term.
const bloomFalsePositiveRate = 0.01

// Entry is one log line.
type Entry struct {
	Timestamp int64 // unix nanoseconds
	Message   string
	Tags      map[string]string
}

// ErrInvalidEntry is returned by Encode for entries that cannot be stored.
var ErrInvalidEntry = errors.New("segment: invalid entry")

// Encode builds a complete segment file image from entries.
//
// Entries are sorted by timestamp (stable, so equal timestamps keep their
// input order) and a line's id is its position after sorting. Sorting at
// write time means every segment is already time-ordered, which lets the
// query path merge results from several segments cheaply later on.
//
// The output is deterministic: the same entries always produce the same
// bytes. That makes segments easy to test and to compare after compaction.
func Encode(entries []Entry) ([]byte, error) {
	if len(entries) == 0 {
		return nil, fmt.Errorf("%w: no entries", ErrInvalidEntry)
	}
	if uint64(len(entries)) > math.MaxUint32 {
		return nil, fmt.Errorf("%w: too many entries", ErrInvalidEntry)
	}
	for _, e := range entries {
		for k := range e.Tags {
			if k == "" || strings.Contains(k, "=") {
				return nil, fmt.Errorf("%w: bad tag key %q", ErrInvalidEntry, k)
			}
		}
	}

	// Copy before sorting so the caller's slice is left untouched.
	sorted := make([]Entry, len(entries))
	copy(sorted, entries)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Timestamp < sorted[j].Timestamp })

	lines := encodeLines(sorted)
	postings := buildPostings(sorted)
	index, terms := encodeIndex(postings)

	// Size the bloom filter for the number of distinct terms so that the
	// false-positive rate holds regardless of segment size.
	bf := bloom.New(len(terms), bloomFalsePositiveRate)
	for _, t := range terms {
		bf.Add(t)
	}
	bloomBytes := bf.Marshal()

	h := header{
		Version: version,
		Count:   uint32(len(sorted)),
		MinTS:   sorted[0].Timestamp,
		MaxTS:   sorted[len(sorted)-1].Timestamp,
	}
	copy(h.Magic[:], magic)
	h.LinesOff = uint64(headerSize)
	h.LinesLen = uint64(len(lines))
	h.IndexOff = h.LinesOff + h.LinesLen
	h.IndexLen = uint64(len(index))
	h.BloomOff = h.IndexOff + h.IndexLen
	h.BloomLen = uint64(len(bloomBytes))

	body := make([]byte, 0, len(lines)+len(index)+len(bloomBytes))
	body = append(body, lines...)
	body = append(body, index...)
	body = append(body, bloomBytes...)
	h.BodyCRC = crc32.ChecksumIEEE(body)

	out := make([]byte, 0, headerSize+len(body))
	out = append(out, h.marshal()...)
	out = append(out, body...)
	return out, nil
}

// encodeLines produces the lines section:
//
//	offset table: Count x uint32 LE, each the start of a record relative to
//	              the end of the table
//	records:      ts (8 bytes LE) | uvarint len + message |
//	              uvarint tag count | (uvarint len + key, uvarint len + value)...
//
// The offset table gives O(1) access to line N. Without it, fetching one
// matching line would mean decoding every record before it, defeating the
// point of having an index.
func encodeLines(entries []Entry) []byte {
	var records []byte
	offsets := make([]byte, 4*len(entries))
	for i, e := range entries {
		binary.LittleEndian.PutUint32(offsets[4*i:], uint32(len(records)))
		records = binary.LittleEndian.AppendUint64(records, uint64(e.Timestamp))
		records = binary.AppendUvarint(records, uint64(len(e.Message)))
		records = append(records, e.Message...)

		// Sort tag keys so the encoding is deterministic (Go randomizes map order).
		keys := make([]string, 0, len(e.Tags))
		for k := range e.Tags {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		records = binary.AppendUvarint(records, uint64(len(keys)))
		for _, k := range keys {
			v := e.Tags[k]
			records = binary.AppendUvarint(records, uint64(len(k)))
			records = append(records, k...)
			records = binary.AppendUvarint(records, uint64(len(v)))
			records = append(records, v...)
		}
	}
	return append(offsets, records...)
}

// buildPostings creates the inverted index: for each term, the ascending
// list of line ids whose message (or tags) contain it.
func buildPostings(entries []Entry) map[string][]uint32 {
	postings := make(map[string][]uint32)
	add := func(term string, id uint32) {
		list := postings[term]
		// A term repeated within one line must only be listed once. Ids are
		// visited in ascending order, so checking the last element suffices.
		if n := len(list); n > 0 && list[n-1] == id {
			return
		}
		postings[term] = append(list, id)
	}
	for i, e := range entries {
		id := uint32(i)
		for _, tok := range Tokenize(e.Message) {
			add(tok, id)
		}
		for k, v := range e.Tags {
			add(TagTerm(k, v), id)
		}
	}
	return postings
}

// encodeIndex serializes postings and returns the sorted term list too (the
// bloom filter is built from it).
//
// Format: uvarint term count, then per term (sorted):
//
//	uvarint len + term bytes | uvarint posting count | posting deltas
//
// Posting lists are ascending, so each id is stored as the difference from
// the previous one (the first is absolute). Small deltas take one byte as
// varints, which shrinks big posting lists substantially.
func encodeIndex(postings map[string][]uint32) ([]byte, []string) {
	terms := make([]string, 0, len(postings))
	for t := range postings {
		terms = append(terms, t)
	}
	sort.Strings(terms)

	out := binary.AppendUvarint(nil, uint64(len(terms)))
	for _, t := range terms {
		out = binary.AppendUvarint(out, uint64(len(t)))
		out = append(out, t...)
		list := postings[t]
		out = binary.AppendUvarint(out, uint64(len(list)))
		prev := uint32(0)
		for i, id := range list {
			if i == 0 {
				out = binary.AppendUvarint(out, uint64(id))
			} else {
				out = binary.AppendUvarint(out, uint64(id-prev))
			}
			prev = id
		}
	}
	return out, terms
}
