package tmux

import "testing"

// TestStripANSI: escape sequences go; text, multi-byte glyphs (Claude's ❯
// prompt, ⎿, 🐝, box drawing) and newlines stay byte for byte.
func TestStripANSI(t *testing.T) {
	cases := []struct{ in, want string }{
		{"\x1b[31mhello\x1b[0m world", "hello world"},
		{"\x1b[38;5;208mERROR\x1b[0m: boom", "ERROR: boom"},
		{"\x1b[Hredrawn", "redrawn"},
		{"plain ASCII line", "plain ASCII line"},
		{"", ""},
		{"\x1b[2J\x1b[H╭───╮\n\x1b[1m❯ \x1b[0mwhat is 2+2?\n  ⎿ Done in 9s\n\x1b[38;5;208m🐝 Scouting…\x1b[0m\n",
			"╭───╮\n❯ what is 2+2?\n  ⎿ Done in 9s\n🐝 Scouting…\n"},
	}
	for _, tc := range cases {
		if got := StripANSI(tc.in); got != tc.want {
			t.Errorf("StripANSI(%q) = %q; want %q", tc.in, got, tc.want)
		}
	}
}
