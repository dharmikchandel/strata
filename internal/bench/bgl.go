// Package bench holds what the benchmark and demo tools share: a parser for
// the BGL log dataset and small statistics helpers.
//
// BGL is a public dataset of real logs from the BlueGene/L supercomputer at
// Lawrence Livermore National Laboratory (Oliner & Stearley, "What
// Supercomputers Say", DSN 2007), distributed by the LogHub collection. It has
// 4,747,963 lines.
package bench

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	stratav1 "github.com/dharmikchandel/strata/gen/strata/v1"
)

// A BGL line looks like this (fields separated by single spaces):
//
//	KERNDTLB 1117838570 2005.06.03 R02-M1-N0-C:J12-U11 2005-06-03-15.42.50.363779 R02-M1-N0-C:J12-U11 RAS KERNEL FATAL data TLB error interrupt
//	label    epoch      date       node                timestamp                   node               type component level message...
//
// The label is "-" for ordinary lines and an alert category otherwise.
const timestampLayout = "2006-01-02-15.04.05.000000"

// ParseBGLLine converts one dataset line into a log entry.
//
//   - Timestamp: the microsecond-precision timestamp field (the dataset
//     doesn't say which time zone; it is read as UTC, which only shifts every
//     timestamp by the same amount).
//   - Message: the line without its leading label and epoch fields. Everything
//     else stays as it was, so the text is what a real operator would search.
//   - Tags: level (INFO, FATAL, ...), component (KERNEL, APP, ...), and alert
//     (the alert category) for lines the dataset flags as alerts.
func ParseBGLLine(line string) (*stratav1.LogEntry, error) {
	// Split into at most 9 parts: the first 8 are fixed fields, the rest of
	// the line (the message) stays whole.
	f := strings.SplitN(line, " ", 9)
	if len(f) < 8 {
		return nil, fmt.Errorf("bgl: expected at least 8 fields, got %d", len(f))
	}
	ts, err := time.Parse(timestampLayout, f[4])
	if err != nil {
		return nil, fmt.Errorf("bgl: bad timestamp %q: %w", f[4], err)
	}
	tags := map[string]string{"component": f[7]}
	if len(f) > 8 {
		// f[8] starts with the level, then the message.
		level, _, _ := strings.Cut(f[8], " ")
		tags["level"] = level
	}
	if f[0] != "-" {
		tags["alert"] = f[0]
	}
	// Strip "<label> <epoch> " from the front.
	msg := line[len(f[0])+1+len(f[1])+1:]
	return &stratav1.LogEntry{TimestampUnixNano: ts.UnixNano(), Message: msg, Tags: tags}, nil
}

// ReadBGL streams the file in batches, calling fn with each batch. Lines that
// fail to parse are counted and skipped. It stops after maxLines lines
// (0 = the whole file) or when fn returns an error.
func ReadBGL(path string, batchSize int, maxLines int, fn func(batch []*stratav1.LogEntry) error) (lines, skipped int, err error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	return readBGL(f, batchSize, maxLines, fn)
}

func readBGL(r io.Reader, batchSize, maxLines int, fn func([]*stratav1.LogEntry) error) (lines, skipped int, err error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 1<<20) // some lines are long
	batch := make([]*stratav1.LogEntry, 0, batchSize)
	for sc.Scan() {
		if maxLines > 0 && lines >= maxLines {
			break
		}
		e, perr := ParseBGLLine(sc.Text())
		if perr != nil {
			skipped++
			continue
		}
		lines++
		batch = append(batch, e)
		if len(batch) == batchSize {
			if err := fn(batch); err != nil {
				return lines, skipped, err
			}
			batch = make([]*stratav1.LogEntry, 0, batchSize)
		}
	}
	if err := sc.Err(); err != nil {
		return lines, skipped, err
	}
	if len(batch) > 0 {
		if err := fn(batch); err != nil {
			return lines, skipped, err
		}
	}
	return lines, skipped, nil
}
