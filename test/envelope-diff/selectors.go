// selectors.go implements the path-selector grammar used by structuralDiff's
// ignore list.
//
// # Grammar
//
// A selector is a dot-separated sequence of field names and optional array
// accessors:
//
//	selector = segment ("." segment)*
//	segment  = field_name | array_accessor
//	array_accessor = "[" index "]"
//	index    = "*" | decimal_integer
//
// Examples:
//
//	"version"                         → top-level field
//	"spawns[*].claude_instance_id"    → any element's field
//	"spawns[0].claude_instance_id"    → first element's field
//	"ids[*]"                          → any element of the top-level ids array
//
// JSON paths in structuralDiff use the same segment grammar but are rooted at
// the empty string ""; a top-level field "foo" has path ".foo" and a
// first-array-element path is ".arr[0]". The leading dot is stripped before
// matching so that "version" matches ".version".
package envelope_diff

import "strings"

// pathMatchesSelector returns true if jsonPath (e.g. ".spawns[0].claude_instance_id")
// matches selector (e.g. "spawns[*].claude_instance_id").
//
// Matching rules:
//   - Leading dot is stripped from jsonPath before comparison.
//   - "[*]" in selector matches any "[N]" segment in jsonPath.
//   - All other segments are matched by exact string equality.
//   - Both path and selector must be fully consumed for a match (no suffix matching).
func pathMatchesSelector(jsonPath, selector string) bool {
	// Strip leading dot from path so both sides use the same format.
	p := strings.TrimPrefix(jsonPath, ".")
	s := strings.TrimPrefix(selector, ".") // tolerate a leading dot in selectors too
	return segmentsMatch(p, s)
}

// segmentsMatch recursively checks whether the remaining path p matches the
// remaining selector s, one segment at a time.
func segmentsMatch(p, s string) bool {
	// Both exhausted → full match.
	if p == "" && s == "" {
		return true
	}
	// One exhausted but not the other → no match.
	if p == "" || s == "" {
		return false
	}

	pSeg, pRem := consumeSegment(p)
	sSeg, sRem := consumeSegment(s)

	if !segmentEq(pSeg, sSeg) {
		return false
	}
	return segmentsMatch(pRem, sRem)
}

// consumeSegment returns the first path/selector segment and the remainder of
// the string after it. There are two kinds of segment:
//
//   - Field name: a run of non-".[" characters, e.g. "foo" from "foo.bar" or "foo[0]".
//   - Array accessor: the entire "[…]" token, e.g. "[0]" or "[*]".
//
// The separator dot between two field-name segments is consumed as part of the
// remainder (not returned in the segment).
func consumeSegment(s string) (seg, rest string) {
	if strings.HasPrefix(s, "[") {
		// Array accessor: consume through the closing "]".
		end := strings.Index(s, "]")
		if end < 0 {
			// Malformed; consume the whole string.
			return s, ""
		}
		seg = s[:end+1]
		rest = s[end+1:]
		// Consume a trailing dot so the next call sees the field name directly.
		rest = strings.TrimPrefix(rest, ".")
		return seg, rest
	}

	// Field name: run up to "." or "[".
	for i, c := range s {
		switch c {
		case '.':
			// Consume the dot as separator.
			return s[:i], s[i+1:]
		case '[':
			// Leave "[" for the next call to handle as an array accessor.
			return s[:i], s[i:]
		}
	}
	return s, ""
}

// segmentEq returns true if pathSeg (from the JSON path) equals selSeg (from
// the selector), respecting the "[*]" wildcard.
func segmentEq(pathSeg, selSeg string) bool {
	if selSeg == "[*]" {
		// Wildcard: matches any "[N]" accessor in the path.
		return strings.HasPrefix(pathSeg, "[") && strings.HasSuffix(pathSeg, "]")
	}
	return pathSeg == selSeg
}
