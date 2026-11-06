package tmux

import "testing"

// TestLastPaneLines (b.7y8): the last n rows of a capture, after dropping the
// blank rows (whitespace only once ANSI is stripped) below the last row with
// content; blank rows between content rows count, each row keeps its
// newline, and an all-blank capture is "".
func TestLastPaneLines(t *testing.T) {
	cases := []struct {
		name, text string
		n          int
		want       string
	}{
		{"last rows", "a\nb\nc\nd\n", 2, "c\nd\n"},
		{"one row", "a\nb\nc\n", 1, "c\n"},
		{"trailing blank rows dropped first", "a\nb\n\n   \n\t\n\n", 1, "b\n"},
		{"blank rows between content count", "a\n\nb\n\n\n", 2, "\nb\n"},
		{"n larger than the rows, trailing blanks", "a\nb\n\n\n", 10, "a\nb\n"},
		{"no final newline", "a\nb", 1, "b"},
		{"all blank", "\n  \n\n", 5, ""},
		{"n zero", "a\nb\n", 0, ""},
		{"ansi rows", "\x1b[1ma\x1b[0m\nb\n\x1b[7mc\x1b[0m\n", 2, "b\n\x1b[7mc\x1b[0m\n"},
		{"ansi-only trailing rows are blank", "a\n\x1b[1mb\x1b[0m\n\x1b[0m\n\x1b[7m  \x1b[0m\n", 1, "\x1b[1mb\x1b[0m\n"},
		{"ansi all blank", "\x1b[0m\n\x1b[1m \x1b[0m\n", 2, ""},
		{"glyphs kept", "╭───╮\n❯ hi\n🐝 Brewed\n", 2, "❯ hi\n🐝 Brewed\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := LastPaneLines(tc.text, tc.n); got != tc.want {
				t.Errorf("LastPaneLines(%q, %d) = %q; want %q", tc.text, tc.n, got, tc.want)
			}
		})
	}
}

// TestLastPaneLinesSameRowsWithANSI: an -e capture and tmux's plain capture
// of the same pane (no escapes, styled blank rows empty) select the same
// rows, so read-pane's ansi read and its default read show one region of
// the pane.
func TestLastPaneLinesSameRowsWithANSI(t *testing.T) {
	raw := "\x1b[1mtop\x1b[0m\n\nmid \x1b[31mred\x1b[0m\n\x1b[2mbottom\x1b[0m\n\x1b[0m\n\x1b[7m \x1b[0m\n\n\x1b[4:3m   \x1b[0m\n"
	plain := "top\n\nmid red\nbottom\n\n\n\n\n"
	for n := 0; n <= 6; n++ {
		if got, want := StripANSI(LastPaneLines(raw, n)), LastPaneLines(plain, n); got != want {
			t.Errorf("n=%d: stripped rows of the -e capture = %q; rows of the plain capture = %q", n, got, want)
		}
	}
}
