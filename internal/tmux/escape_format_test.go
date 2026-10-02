package tmux

import "testing"

// TestEscapeFormat pins escapeFormat (b.dsx): every run of "#" is doubled,
// except a run directly before "[", which tmux copies through as a style.
func TestEscapeFormat(t *testing.T) {
	cases := []struct{ text, want string }{
		{"", ""},
		{"/work", "/work"},
		{"[x]", "[x]"},
		{"#", "##"},
		{"##", "####"},
		{"#lead", "##lead"},
		{"x#(touch m)", "x##(touch m)"},
		{"x#{session_name}", "x##{session_name}"},
		{"x#S", "x##S"},
		{"x#;", "x##;"},
		{"x#,y#}", "x##,y##}"},
		{"x###y", "x######y"},
		{"x# [y", "x## [y"},
		{"#[", "#["},
		{"x#[y", "x#[y"},
		{"x##[y", "x##[y"},
		{"x###[y", "x###[y"},
		{"x[#", "x[##"},
		{"x#[y]#(touch m)", "x#[y]##(touch m)"},
		{"#(a)##[b]#S#", "##(a)##[b]##S##"},
		{"é#ü", "é##ü"},
	}
	for _, c := range cases {
		if got := escapeFormat(c.text); got != c.want {
			t.Errorf("escapeFormat(%q) = %q, want %q", c.text, got, c.want)
		}
	}
}
