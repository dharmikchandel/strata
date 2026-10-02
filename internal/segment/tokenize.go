package segment

import (
	"strings"
	"unicode"
)

// Tokenize splits a log message into lowercase search terms: maximal runs of
// letters and digits. "Connection RESET by peer: 10.0.0.1" becomes
// [connection reset by peer 10 0 0 1].
//
// The same function must be used when building the index and when parsing a
// query, otherwise a term could be indexed one way and searched another.
// It is exported for that reason.
func Tokenize(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

// TagTerm returns the index term representing an exact key=value tag match.
//
// Tags share the index and bloom filter with message tokens. They cannot
// collide: message tokens only contain letters and digits, while a tag term
// always contains ':' and '='. Encode rejects tag keys containing '=' so
// that (key, value) pairs map to distinct terms.
func TagTerm(key, value string) string {
	return "tag:" + key + "=" + value
}
