package tmux_test

import (
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
)

// The unusable-recorded-name guard of SR-3.2 (F.2 Unusable): each kind, the
// catalogue's N1/N4/N5/N6 names, and the order empty, control, rewritten.

// unusableCase is one name and the kind the guard must give it.
type unusableCase struct {
	name string
	raw  string
	want tmux.UnusableKind
}

// placed puts fault first, in the middle and last of a plain name, one case each.
func placed(label, fault string, want tmux.UnusableKind) []unusableCase {
	return []unusableCase{
		{label + "/first", fault + "ab", want},
		{label + "/middle", "a" + fault + "b", want},
		{label + "/last", "ab" + fault, want},
	}
}

// mixed puts faults a and b apart in a plain name in both orders and at both ends.
func mixed(label, a, b string, want tmux.UnusableKind) []unusableCase {
	return []unusableCase{
		{label + "/a-then-b", "x" + a + "y" + b + "z", want},
		{label + "/b-then-a", "x" + b + "y" + a + "z", want},
		{label + "/at-ends", a + "xy" + b, want},
		{label + "/at-ends-reversed", b + "xy" + a, want},
		{label + "/adjacent", "x" + a + b + "z", want},
		{label + "/adjacent-reversed", "x" + b + a + "z", want},
	}
}

// catalogueKind is the kind a catalogue name must get, by its Appendix E section.
func catalogueKind(n tmuxfix.StoredName) tmux.UnusableKind {
	switch {
	case strings.Contains(n.Source, "N4"):
		return tmux.UnusableControl
	case strings.Contains(n.Source, "N6"):
		return tmux.UnusableRewritten
	}
	return tmux.UnusableNone
}

// runUnusable asserts the guard's kind for every case.
func runUnusable(t *testing.T, cases []unusableCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tmux.Unusable(tc.raw); got != tc.want {
				t.Errorf("Unusable(%q) = %d, want %d", tc.raw, got, tc.want)
			}
		})
	}
}

// Invalid UTF-8 byte sequences: a lone 0xff, a truncated three-byte sequence
// (the first two bytes of U+20AC) and an overlong encoding of '/'.
const (
	loneFF    = "\xff"
	truncated = "\xe2\x82"
	overlong  = "\xc0\xaf"
)

// TestUnusableKinds covers each kind on names built from byte-level factories.
func TestUnusableKinds(t *testing.T) {
	cases := []unusableCase{
		{"empty", "", tmux.UnusableEmpty},
		{"usable/default-style", "my_proj-1a2b3c4d", tmux.UnusableNone},
		{"usable/space", "a b", tmux.UnusableNone},
		{"usable/hash", "a#b", tmux.UnusableNone},
		{"usable/two-byte-utf8", "café", tmux.UnusableNone},
		{"usable/three-byte-utf8", "日本", tmux.UnusableNone},
		{"usable/four-byte-utf8", "a\U0001F600b", tmux.UnusableNone},
		{"usable/c1-next-line", "a\u0085b", tmux.UnusableNone},
		{"usable/c1-csi", "a\u009bb", tmux.UnusableNone},
		{"usable/nbsp", "a\u00a0b", tmux.UnusableNone},
		{"control/only", "\x01", tmux.UnusableControl},
		{"rewritten/only-dot", ".", tmux.UnusableRewritten},
	}
	for _, c := range []struct{ label, fault string }{
		{"control/nul", "\x00"}, {"control/soh", "\x01"}, {"control/tab", "\t"}, {"control/newline", "\n"},
		{"control/esc", "\x1b"}, {"control/unit-separator", "\x1f"}, {"control/del", "\x7f"},
	} {
		cases = append(cases, placed(c.label, c.fault, tmux.UnusableControl)...)
	}
	for _, c := range []struct{ label, fault string }{
		{"rewritten/dot", "."}, {"rewritten/colon", ":"}, {"rewritten/lone-ff", loneFF},
		{"rewritten/truncated-multibyte", truncated}, {"rewritten/overlong", overlong},
	} {
		cases = append(cases, placed(c.label, c.fault, tmux.UnusableRewritten)...)
	}
	runUnusable(t, cases)
}

// TestUnusableCatalogueNames gives every N4 name control, every N6 name
// rewritten and every other recorded name (N1, N5 $/\ cases, F3, T2a, U1) none.
func TestUnusableCatalogueNames(t *testing.T) {
	var cases []unusableCase
	seen := map[tmux.UnusableKind]int{}
	for _, n := range tmuxfix.StoredNames() {
		want := catalogueKind(n)
		seen[want]++
		cases = append(cases, unusableCase{n.Source + "/" + n.Stored, n.Raw, want})
	}
	for _, k := range []tmux.UnusableKind{tmux.UnusableNone, tmux.UnusableControl, tmux.UnusableRewritten} {
		if seen[k] == 0 {
			t.Fatalf("catalogue has no name of kind %d", k)
		}
	}
	runUnusable(t, cases)
}

// TestUnusablePrecedence holds the SR-3.2 order, empty then control then
// rewritten, wherever the faults sit in the name.
func TestUnusablePrecedence(t *testing.T) {
	var cases []unusableCase
	for _, c := range []struct {
		label, a, b string
		want        tmux.UnusableKind
	}{
		{"control+dot", "\x01", ".", tmux.UnusableControl},
		{"control+colon", "\t", ":", tmux.UnusableControl},
		{"del+dot", "\x7f", ".", tmux.UnusableControl},
		{"control+lone-ff", "\x1b", loneFF, tmux.UnusableControl},
		{"control+truncated", "\n", truncated, tmux.UnusableControl},
		{"control+overlong", "\x00", overlong, tmux.UnusableControl},
		{"control+dot+invalid", "\x1f.", loneFF, tmux.UnusableControl},
		{"dot+lone-ff", ".", loneFF, tmux.UnusableRewritten},
		{"colon+truncated", ":", truncated, tmux.UnusableRewritten},
		{"dot+colon", ".", ":", tmux.UnusableRewritten},
		{"dollar+dot", "$", ".", tmux.UnusableRewritten},
		{"backslash+lone-ff", `\`, loneFF, tmux.UnusableRewritten},
		{"hash+control", "#", "\x01", tmux.UnusableControl},
	} {
		cases = append(cases, mixed(c.label, c.a, c.b, c.want)...)
	}
	cases = append(cases,
		unusableCase{"control-inside-truncated-sequence", "a\xe2\x01b", tmux.UnusableControl},
		unusableCase{"control-after-lead-byte-only", "\xc3\x7f", tmux.UnusableControl},
	)
	runUnusable(t, cases)
}

// TestUnusableZeroValueIsNone: an unset kind reads as usable, and the four
// kinds are distinct so each fault maps to its own kind (F.2).
func TestUnusableZeroValueIsNone(t *testing.T) {
	var zero tmux.UnusableKind
	if zero != tmux.UnusableNone {
		t.Fatalf("zero UnusableKind = %d, want UnusableNone (%d)", zero, tmux.UnusableNone)
	}
	kinds := map[tmux.UnusableKind]bool{}
	for _, k := range []tmux.UnusableKind{tmux.UnusableNone, tmux.UnusableEmpty, tmux.UnusableControl, tmux.UnusableRewritten} {
		kinds[k] = true
	}
	if len(kinds) != 4 {
		t.Fatalf("the four UnusableKind values are not distinct: %v", kinds)
	}
}

// TestUnusableIgnoresEnvironment: the kind depends on the name alone, not on
// the locale or tmux variables (SR-3.2's guard reads no environment).
func TestUnusableIgnoresEnvironment(t *testing.T) {
	names := []string{"", "p\tq", "p.q", "p" + loneFF, "café", "p$q"}
	before := make([]tmux.UnusableKind, len(names))
	for i, n := range names {
		before[i] = tmux.Unusable(n)
	}
	for _, env := range [][2]string{{"LC_ALL", "C"}, {"LANG", "C"}, {"LC_CTYPE", "POSIX"},
		{"TMUX", "/tmp/elsewhere.sock,1,0"}, {"TMUX_TMPDIR", t.TempDir()}} {
		t.Setenv(env[0], env[1])
	}
	for i, n := range names {
		if got := tmux.Unusable(n); got != before[i] {
			t.Errorf("Unusable(%q) = %d under a changed environment, was %d", n, got, before[i])
		}
	}
}
