package tmux

import "testing"

func TestStripANSI(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "SGR red wrapper stripped",
			in:   "\x1b[31mhello\x1b[0m world",
			want: "hello world",
		},
		{
			name: "256-color SGR stripped",
			in:   "\x1b[38;5;208mERROR\x1b[0m: boom",
			want: "ERROR: boom",
		},
		{
			name: "cursor-home stripped",
			in:   "\x1b[Hredrawn",
			want: "redrawn",
		},
		{
			name: "no escape sequences passes through verbatim",
			in:   "plain ASCII line",
			want: "plain ASCII line",
		},
		{
			name: "unicode prompt glyph preserved",
			// ❯ (U+276F) carries Claude's idle-prompt signal — never
			// strip even after escape codes around it are removed.
			in:   "\x1b[36m❯\x1b[0m command",
			want: "❯ command",
		},
		{
			name: "continuation glyph preserved",
			// ⎿ (U+23BF) flags multi-line follow-up output.
			in:   "  ⎿ Done in 9s",
			want: "  ⎿ Done in 9s",
		},
		{
			name: "bee emoji preserved",
			// 🐝 (U+1F41D) is Claude's branding spinner glyph; multi-
			// byte UTF-8, must not be touched by a byte-oriented regex.
			in:   "🐝 Scouting…",
			want: "🐝 Scouting…",
		},
		{
			name: "box-drawing preserved",
			// ╭ ╮ ╰ ╯ ─ │ are part of the input box border.
			in:   "╭─────╮\n│ ❯   │\n╰─────╯",
			want: "╭─────╮\n│ ❯   │\n╰─────╯",
		},
		{
			name: "real capture-pane sample with SGR + glyphs + newlines",
			in: "\x1b[2J\x1b[H" + // clear screen + cursor home
				"╭───────────╮\n" +
				"\x1b[1m❯ \x1b[0mwhat is 2+2?\n" +
				"╰───────────╯\n" +
				"\x1b[38;5;208m🐝 Scouting…\x1b[0m\n",
			want: "╭───────────╮\n" +
				"❯ what is 2+2?\n" +
				"╰───────────╯\n" +
				"🐝 Scouting…\n",
		},
		{
			name: "empty string round-trips empty",
			in:   "",
			want: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := StripANSI(tc.in)
			if got != tc.want {
				t.Fatalf("StripANSI(%q) = %q; want %q", tc.in, got, tc.want)
			}
		})
	}
}
