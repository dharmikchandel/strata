package segment

import "sort"

// Tag is one key=value pair.
type Tag struct{ Key, Value string }

// DistinctTags returns the distinct tag pairs in entries, sorted, at most
// limit of them (the catalogue of tag values is a convenience, so a segment
// full of unique tag values is not worth recording in full).
func DistinctTags(entries []Entry, limit int) []Tag {
	seen := map[Tag]bool{}
	for _, e := range entries {
		for k, v := range e.Tags {
			if len(seen) >= limit {
				break
			}
			seen[Tag{k, v}] = true
		}
	}
	out := make([]Tag, 0, len(seen))
	for t := range seen {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Key != out[j].Key {
			return out[i].Key < out[j].Key
		}
		return out[i].Value < out[j].Value
	})
	return out
}
