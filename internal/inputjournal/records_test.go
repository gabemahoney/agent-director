package inputjournal

import (
	"slices"
	"strings"
	"testing"
)

// TestSplitAdvancesInRuneAlignedPieces (b.vr5): Split, which Chunks and paced
// Claude typing share, cuts text into pieces of at most 512 bytes that join
// to it, ending before a rune start, or at 512 bytes where there is none (not
// UTF-8): the cut points Chunks made before it. Chunks gives valid ordinary
// text exactly Split's pieces, the cut points proof matching relies on.
func TestSplitAdvancesInRuneAlignedPieces(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		text string
		lens []int
	}{
		{"512 bytes", strings.Repeat("a", 512), []int{512}},
		{"513 bytes", strings.Repeat("a", 513), []int{512, 1}},
		{"rune across 512", strings.Repeat("a", 511) + "界" + strings.Repeat("b", 600), []int{511, 512, 91}},
		{"not UTF-8", strings.Repeat("\x80", 600), []int{512, 88}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			pieces := Split(tc.text)
			var lens []int
			for _, p := range pieces {
				lens = append(lens, len(p))
			}
			if !slices.Equal(lens, tc.lens) || strings.Join(pieces, "") != tc.text {
				t.Errorf("Split gave pieces of %v bytes (joined equal: %v); want %v joining to the text",
					lens, strings.Join(pieces, "") == tc.text, tc.lens)
			}
		})
	}
	t.Run("Chunks cuts as Split", func(t *testing.T) {
		t.Parallel()
		text := strings.Repeat("a", 510) + "🌍\n" + strings.Repeat("界", 300)
		chunks, err := Chunks(text)
		if err != nil || !slices.Equal(chunks, Split(text)) || len(chunks) != 3 {
			t.Errorf("Chunks = %d pieces, %v; want Split's 3 pieces", len(chunks), err)
		}
	})
}

func pasted(id, text string) string {
	return "\n\n<pasted_content id=\"" + id + "\">\n" + text + "\n</pasted_content id=\"" + id + "\">\n"
}

// TestHistoricalClaudeExactConsumesAllKnownSegments is historical-record
// coverage. A covered Claude Launch writes no native attempt, but a store from
// an earlier release can hold submitted Claude attempts, and every read and
// legacy import re-validates their proof through MatchRecord's ClaudeExact.
// Claude wraps pasted text, so those wrapped records must keep validating.
func TestHistoricalClaudeExactConsumesAllKnownSegments(t *testing.T) {
	t.Parallel()
	payload := strings.Repeat("a", 512) + "🌍\nlast"
	first, last := payload[:512], payload[512:]
	wrapped := pasted("ab12", first) + pasted("ab12", last)
	for _, tc := range []struct {
		name, text string
		want       bool
	}{
		{"literal", payload, true},
		{"whole wrapper", pasted("abc", payload), true},
		{"adjacent repeated wrapper ids", wrapped, true},
		{"mixed literal wrapper", first + pasted("1", last), true},
		{"prefix", "x" + wrapped, false},
		{"suffix", wrapped + "x", false},
		{"mismatched ids", strings.Replace(wrapped, "</pasted_content id=\"ab12\">", "</pasted_content id=\"ab13\">", 1), false},
		{"changed text", strings.Replace(wrapped, "last", "lost", 1), false},
		{"extra separator", pasted("1", first) + "\n" + pasted("2", last), false},
		{"nonhex id", pasted("zz", payload), false},
		{"unpaired wrapper", strings.TrimSuffix(wrapped, "\n"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClaudeExact(tc.text, payload); got != tc.want {
				t.Fatalf("exact = %v, want %v", got, tc.want)
			}
		})
	}
}
