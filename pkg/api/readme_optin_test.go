package api_test

// readme_optin_test.go checks that kill's operator-only finished-row opt-in is
// documented only in the README's "Operator actions" and in
// agent-director-admin's generated docs/admin-reference.md (SR-6.8, SR-18.15,
// SR-18.17, b.vqr), on readme_sections_test.go's shared mdDoc parser.

import (
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// optInRe matches any spelling of the opt-in: the former flag, Go field and
// TypeScript field, and the trail field.
var optInRe = regexp.MustCompile(`(?i)include.?finished`)

// adminKillVerbRe matches agent-director-admin's verb that runs the opt-in.
var adminKillVerbRe = regexp.MustCompile(`(?i)kill.?finished`)

// mdAdminReference is agent-director-admin's generated reference, the one doc
// besides the README's "Operator actions" that names the opt-in (b.vqr).
const mdAdminReference = "../../docs/admin-reference.md"

// sectionLines returns the 0-based line range [from, to) of h's body. Later
// README tests use it, never their own line counting.
func sectionLines(d mdDoc, h mdHeading) (from, to int) {
	from = h.line + 1
	return from, from + strings.Count(d.body(h), "\n") + 1
}

// operatorActions returns the README's "Operator actions" heading, failing
// the test when it is missing. Later README tests find the section with it,
// never their own heading search.
func operatorActions(t *testing.T, d mdDoc) mdHeading {
	t.Helper()
	hs := d.titled(apitest.OperatorActionsTitle)
	if len(hs) == 0 {
		t.Fatalf("%s has no heading titled %q", d.path, apitest.OperatorActionsTitle)
	}
	return hs[0]
}

// TestREADMEOptInOnlyInOperatorActions checks every spelling of the opt-in, and
// of agent-director-admin's kill-finished, in the README lies inside "Operator
// actions", which names one of them and the opt-in's key facts.
func TestREADMEOptInOnlyInOperatorActions(t *testing.T) {
	d := readMD(t, mdTopREADME)
	h := operatorActions(t, d)
	from, to := sectionLines(d, h)
	inside := 0
	for i, line := range d.lines {
		if !optInRe.MatchString(line) && !adminKillVerbRe.MatchString(line) {
			continue
		}
		if i >= from && i < to {
			inside++
			continue
		}
		t.Errorf("%s:%d names the opt-in outside %q: %s", d.path, i+1, apitest.OperatorActionsTitle, strings.TrimSpace(line))
	}
	if inside == 0 {
		t.Errorf("%s %q never names the opt-in (include-finished or kill-finished)", d.path, apitest.OperatorActionsTitle)
	}

	body := strings.Join(strings.Fields(d.body(h)), " ")
	for _, want := range []string{"never reported in", "ErrSpawnNotResumable", "kill_sent"} {
		if !strings.Contains(body, want) {
			t.Errorf("%s %q lacks %q, a fact of the opt-in item", d.path, apitest.OperatorActionsTitle, want)
		}
	}
}

// TestREADMEOptInAbsentFromOtherDocs checks no doc under docs/ or either package
// README names the opt-in, bar the architecture doc's ad.kill.called trail field
// and docs/admin-reference.md, the admin binary's own reference (b.vqr).
func TestREADMEOptInAbsentFromOtherDocs(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join(mdRepoRoot, "docs", "*.md"))
	if err != nil {
		t.Fatalf("glob docs: %v", err)
	}
	if !slices.Contains(paths, mdArchitecture) {
		t.Fatalf("docs glob %v misses %s; the scan is vacuous", paths, mdArchitecture)
	}
	if !slices.Contains(paths, mdAdminReference) {
		t.Fatalf("docs glob %v misses %s; the exemption below names no file", paths, mdAdminReference)
	}
	paths = append(paths, "README.md", filepath.Join(mdRepoRoot, "pkg", "ts-bun-client", "README.md"))

	for _, path := range paths {
		if path == mdAdminReference {
			continue
		}
		for i, line := range readMD(t, path).lines {
			for _, m := range optInRe.FindAllStringIndex(line, -1) {
				if path == mdArchitecture && isKillTrailField(line, m) {
					continue
				}
				t.Errorf("%s:%d names the opt-in outside the README's %q: %s", path, i+1, apitest.OperatorActionsTitle, snippet(line, m))
			}
		}
	}
}

// isKillTrailField reports whether match m on line is the backticked trail
// field `include_finished` in the trail-event table's ad.kill.called row.
func isKillTrailField(line string, m []int) bool {
	return strings.HasPrefix(line, "| `ad.kill.called` |") &&
		line[m[0]:m[1]] == "include_finished" &&
		m[0] > 0 && line[m[0]-1] == '`' && m[1] < len(line) && line[m[1]] == '`'
}

// snippet returns match m on line with up to 60 bytes of context each side.
func snippet(line string, m []int) string {
	from, to := max(m[0]-60, 0), min(m[1]+60, len(line))
	return fmt.Sprintf("…%s…", line[from:to])
}
