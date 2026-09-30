package tmux

import "testing"

// Direct table for escapeFinalSemicolon, the text-call escape of
// SendKeysPane: only a final ";" gets one backslash before it.

// TestEscapeFinalSemicolon pins the escaped text for texts ending in ";" and
// the pass-through of every other text.
func TestEscapeFinalSemicolon(t *testing.T) {
	cases := []struct{ text, want string }{
		{`;`, `\;`},
		{`a;`, `a\;`},
		{`a ;`, `a \;`},
		{`a\;`, `a\\;`},
		{`-x;`, `-x\;`},
		{`;;`, `;\;`},
		{`;a`, `;a`},
		{`a;b`, `a;b`},
		{`a; `, `a; `},
		{`a\`, `a\`},
		{`hello`, `hello`},
		{``, ``},
	}
	for _, c := range cases {
		if got := escapeFinalSemicolon(c.text); got != c.want {
			t.Errorf("escapeFinalSemicolon(%q) = %q, want %q", c.text, got, c.want)
		}
	}
}
