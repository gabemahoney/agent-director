package inputdelivery

import (
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
