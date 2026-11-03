package inputdelivery

import (
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestChunksPreservesOrdinaryText(t *testing.T) {
	t.Parallel()
	for _, text := range []string{"hello\nworld", strings.Repeat("a", 511) + "🌍\n" + strings.Repeat("界", 400), strings.Repeat("x", 16384)} {
		t.Run(string([]rune(text)[:1]), func(t *testing.T) {
			parts, err := Chunks(text)
			if err != nil || strings.Join(parts, "") != text {
				t.Fatalf("chunks changed payload: %v", err)
			}
			for _, part := range parts {
				if len(part) == 0 || len(part) > 512 || !utf8.ValidString(part) {
					t.Fatalf("unsafe chunk: %d bytes, UTF-8=%v", len(part), utf8.ValidString(part))
				}
			}
		})
	}
}

// TestSplitAdvancesInRuneAlignedPieces (b.vr5): Split, which Chunks and paced
// Claude typing share, cuts text into pieces of at most 512 bytes that join
// to it, ending before a rune start, or at 512 bytes where there is none (not
// UTF-8): the cut points Chunks made before it.
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
}

func TestChunksRejectsUnsafeFraming(t *testing.T) {
	t.Parallel()
	for _, text := range []string{"", " \n", "/exit", "\n !command", "a\r\nb", "a\tb", "a\x00b", "a\x1b[A", "a\u0085b", "a\u2028b", "a\u2029b", "a\xff"} {
		t.Run(text, func(t *testing.T) {
			if parts, err := Chunks(text); err == nil || len(parts) != 0 {
				t.Fatalf("unsafe input accepted: %q, %v", parts, err)
			}
		})
	}
}

func TestSafeArgsRequiresFreshOrdinaryComposer(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		args []string
		want bool
	}{
		{"defaults", nil, true},
		{"known options", []string{"--model", "test", "--effort", "high", "--no-alt-screen"}, true},
		{"resume", []string{"resume"}, false},
		{"positional prompt", []string{"hello"}, false},
		{"binding override", []string{"-c", "keybindings=custom"}, false},
		{"missing value", []string{"--model"}, false},
		{"option as value", []string{"-m", "--resume"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := SafeArgs(tc.args); got != tc.want {
				t.Fatalf("SafeArgs(%q) = %v, want %v", tc.args, got, tc.want)
			}
		})
	}
}

func pasted(id, text string) string {
	return "\n\n<pasted_content id=\"" + id + "\">\n" + text + "\n</pasted_content id=\"" + id + "\">\n"
}

func TestClaudeExactConsumesAllKnownSegments(t *testing.T) {
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
