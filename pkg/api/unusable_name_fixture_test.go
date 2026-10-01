package api_test

// unusable_name_fixture_test.go is the one table of unusable recorded tmux
// session names (SR-3.2) the Epic 19 tests share: each fixture's raw name,
// the guard kind tmux.Unusable gives it and the ErrInternal description case
// a verb's refusal must match. The call table builds a column per fixture
// (lookup_calltable_unusable_test.go); find-missing's notes and expire's kept
// reasons extend this table rather than keeping their own. It holds no tests.

import (
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// unusableNameFixture is one unusable recorded name: label (subtest and
// column name), raw (the recorded name as seeded), kind (tmux.Unusable's
// verdict, the first fault in SR-3.2's order) and desc (its ErrInternal case).
type unusableNameFixture struct {
	label string
	raw   string
	kind  tmux.UnusableKind
	desc  apitest.DescCase
}

// preGqeDefaultName is a default name made before the b.gqe fix (commit
// dab3a80): the cwd base, a hyphen and id b.18k-fix's first eight
// characters unsanitised, so it keeps the id's '.'.
const preGqeDefaultName = "proj-b.18k-fi"

// unusableNameFixtures returns the fixtures: empty, a newline, ESC, the
// pre-b.gqe default name ('.'), ':', invalid UTF-8, and a '.' with a newline,
// whose control character alone is described (killControlOnly).
func unusableNameFixtures() []unusableNameFixture {
	rewritten := func(label, raw string, which apitest.RewrittenChars) unusableNameFixture {
		return unusableNameFixture{label, raw, tmux.UnusableRewritten, apitest.DescUnusableNameRewritten(raw, which)}
	}
	control := func(label, raw string) unusableNameFixture {
		return unusableNameFixture{label, raw, tmux.UnusableControl, apitest.DescUnusableNameControlChar(raw)}
	}
	return []unusableNameFixture{
		{"empty", "", tmux.UnusableEmpty, apitest.DescUnusableNameEmpty()},
		control("newline", "ad\nx"),
		control("escape", "ad\x1bx"),
		rewritten("pre-b.gqe default name", preGqeDefaultName, apitest.RewrittenChars{Dot: true}),
		rewritten("colon", "ad:x", apitest.RewrittenChars{Colon: true}),
		rewritten("invalid UTF-8", "bad\xffx", apitest.RewrittenChars{InvalidUTF8: true}),
		{"dot and newline", "a.b\n", tmux.UnusableControl, killControlOnly("a.b\n")},
	}
}
