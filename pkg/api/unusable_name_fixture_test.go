package api_test

// unusable_name_fixture_test.go is the one table of unusable recorded tmux
// session names (SR-3.2) the Epic 19 tests share: each fixture's raw name,
// the guard kind tmux.Unusable gives it, the ErrInternal description case a
// verb's refusal must match, find-missing's note (SR-11.3) and expire's kept
// reason (SR-12.2). The call table builds a column per fixture
// (lookup_calltable_unusable_test.go). It also holds the usable control
// fixture. It holds no tests. Later tests add a name here, never a second
// table.

import (
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// unusableNameFixture is one recorded name: label (subtest and column name),
// raw (the recorded name as seeded), kind (tmux.Unusable's verdict, the first
// fault in SR-3.2's order), desc (its ErrInternal case), note (find-missing's
// liveness note) and kept (expire's kept reason). note and kept are "" for
// the usable control.
type unusableNameFixture struct {
	label string
	raw   string
	kind  tmux.UnusableKind
	desc  apitest.DescCase
	note  string
	kept  string
}

// unusableNameToken is one unusable kind's find-missing note and expire kept
// reason.
type unusableNameToken struct {
	kind tmux.UnusableKind
	note string
	kept string
}

// unusableNameTokens is the three unusable kinds in SR-3.2's order with their
// note and kept reason: the one place the six tokens are spelled, since the
// source's constants are unexported.
func unusableNameTokens() []unusableNameToken {
	return []unusableNameToken{
		{tmux.UnusableEmpty, "tmux_session_name_empty", "empty_session_name"},
		{tmux.UnusableControl, "tmux_session_name_control_char", "control_char_session_name"},
		{tmux.UnusableRewritten, "tmux_session_name_rewritten", "rewritten_session_name"},
	}
}

// unusableNameTokenOf is kind's entry of unusableNameTokens; the zero value
// for UnusableNone.
func unusableNameTokenOf(kind tmux.UnusableKind) unusableNameToken {
	for _, tok := range unusableNameTokens() {
		if tok.kind == kind {
			return tok
		}
	}
	return unusableNameToken{}
}

// preGqeDefaultName is a default name made before the b.gqe fix (commit
// dab3a80): the cwd base, a hyphen and id b.18k-fix's first eight
// characters unsanitised, so it keeps the id's '.'.
const preGqeDefaultName = "proj-b.18k-fi"

// usableDefaultName is the default name the b.gqe fix makes for the same cwd
// base and id: the id's '.' sanitised to '-', so the name is usable.
const usableDefaultName = "proj-b-18k-fi"

// newNameFixture builds a fixture of kind, taking its note and kept reason
// from unusableNameTokens.
func newNameFixture(label, raw string, kind tmux.UnusableKind, desc apitest.DescCase) unusableNameFixture {
	tok := unusableNameTokenOf(kind)
	return unusableNameFixture{label: label, raw: raw, kind: kind, desc: desc, note: tok.note, kept: tok.kept}
}

// unusableNameFixtures returns the fixtures: empty, a newline, DEL, ESC, the
// pre-b.gqe default name ('.'), ':', invalid UTF-8, and a '.' with a newline,
// whose control character alone decides (killControlOnly; the control note
// and kept reason).
func unusableNameFixtures() []unusableNameFixture {
	rewritten := func(label, raw string, which apitest.RewrittenChars) unusableNameFixture {
		return newNameFixture(label, raw, tmux.UnusableRewritten, apitest.DescUnusableNameRewritten(raw, which))
	}
	control := func(label, raw string) unusableNameFixture {
		return newNameFixture(label, raw, tmux.UnusableControl, apitest.DescUnusableNameControlChar(raw))
	}
	return []unusableNameFixture{
		newNameFixture("empty", "", tmux.UnusableEmpty, apitest.DescUnusableNameEmpty()),
		control("newline", "ad\nx"),
		control("DEL", "ad\x7fx"),
		control("escape", "ad\x1bx"),
		rewritten("pre-b.gqe default name", preGqeDefaultName, apitest.RewrittenChars{Dot: true}),
		rewritten("colon", "ad:x", apitest.RewrittenChars{Colon: true}),
		rewritten("invalid UTF-8", "bad\xffx", apitest.RewrittenChars{InvalidUTF8: true}),
		newNameFixture("dot and newline", "a.b\n", tmux.UnusableControl, killControlOnly("a.b\n")),
	}
}

// usableNameFixture is the usable control, usableDefaultName: kind
// UnusableNone, no note and no kept reason, so the same row is looked up.
func usableNameFixture() unusableNameFixture {
	return newNameFixture("usable default name", usableDefaultName, tmux.UnusableNone, apitest.DescCase{})
}
