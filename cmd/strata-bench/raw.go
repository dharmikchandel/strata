package main

import (
	"bufio"
	"os"
)

// readRaw calls fn for each line of the file (up to maxLines if > 0).
func readRaw(path string, maxLines int, fn func(string)) (lines int, bytes int64, err error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		if maxLines > 0 && lines >= maxLines {
			break
		}
		fn(sc.Text())
		lines++
		bytes += int64(len(sc.Bytes()))
	}
	return lines, bytes, sc.Err()
}
