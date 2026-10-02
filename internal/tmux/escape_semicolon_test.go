package tmux

import (
	"slices"
	"testing"
)

// Direct tests for escapeFinalSemicolon (only a final ";" gets one backslash
// before it) and commandArgv, which applies it to every element of a tmux
// command list so no element can end its command (b.ukw).

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

// TestCommandArgvEscapesLaterCommands pins that commandArgv escapes elements
// of the second and later commands too. No public method can give a later
// command an element ending in ";", so this is checked directly; the first
// command and the separators are covered through the public methods in
// client_argv_test.go.
func TestCommandArgvEscapesLaterCommands(t *testing.T) {
	cmds := [][]string{{"a;"}, {";", "b"}, {`c\;`}}
	want := []string{`a\;`, ";", `\;`, "b", ";", `c\\;`}
	if got := commandArgv(cmds...); !slices.Equal(got, want) {
		t.Errorf("commandArgv(%q) =\n %q\nwant %q", cmds, got, want)
	}
}
