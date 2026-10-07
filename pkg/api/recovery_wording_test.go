package api_test

// recovery_wording_test.go checks that no text an agent reads prescribes
// delete-then-spawn, says only a live row collides or presents reuse as
// reattaching history (SR-18.4, SR-18.9, SR-18.16; AC-DOC-03, AC-DOC-08,
// AC-DOC-13), and pins the TypeScript README's SR-18.4 and SR-18.9 rows. The
// Go doc sites of SR-18.4 are pinned in pkg/api/manifest, beside the Go-doc
// reader (manifest_recovery_wording_test.go).

import (
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// The package READMEs, as seen from pkg/api/.
const (
	mdAPIREADME = "README.md"
	mdTSREADME  = "../ts-bun-client/README.md"
)

// recoveryWordingSites are sources the scan must reach, or it is vacuous.
var recoveryWordingSites = []string{
	"pkg/api/errors.go", "pkg/api/resume.go", "pkg/api/spawn.go", "internal/store/spawns.go",
	"internal/spawn/params.go", "internal/spawn/errors.go", "manifest kill Description",
	"manifest spawn param claude_instance_id", mdAPIREADME, mdTSREADME,
}

// TestRecoveryWordingScan: no manifest text, non-test Go source (the helper's
// own pkg/api/apitest excepted) or package README carries a forbidden form.
func TestRecoveryWordingScan(t *testing.T) {
	t.Parallel()
	seen := map[string]bool{}
	scan := func(source, text string) {
		seen[source] = true
		apitest.AssertMustNot(t, source, text, apitest.DescReuseDocsForbidden())
	}
	eachManifestText(scan)
	walkGoSources(t, func(rel, text string) {
		if !strings.HasPrefix(rel, "pkg/api/apitest/") {
			scan(rel, text)
		}
	})
	for _, path := range []string{mdAPIREADME, mdTSREADME} {
		scan(path, strings.Join(readMD(t, path).lines, "\n"))
	}
	for _, site := range recoveryWordingSites {
		if !seen[site] {
			t.Errorf("the scan did not reach %s", site)
		}
	}
}

// TestRecoveryTSREADMERows pins the TypeScript README's ErrNoSessionId and
// ErrJsonlMissing rows (SR-18.4) and its ErrInstanceIdCollision row (SR-18.9).
func TestRecoveryTSREADMERows(t *testing.T) {
	t.Parallel()
	d := readMD(t, mdTSREADME)
	for name, c := range map[string]apitest.DescCase{
		"ErrNoSessionId":         apitest.DescReuseRecourse(apitest.RecourseTSREADME),
		"ErrJsonlMissing":        apitest.DescReuseRecourse(apitest.RecourseTSREADME),
		"ErrInstanceIdCollision": apitest.DescInstanceIDCollision(apitest.CollisionSite{Full: true, Code: "`"}),
	} {
		row, ok := d.tableRow(name)
		if !ok {
			t.Errorf("%s has no %s row", d.path, name)
			continue
		}
		apitest.AssertAgentTextCase(t, d.path+" "+name+" row", row, c)
	}
}

// tableRow returns the Markdown table row headed by the code span `name`.
func (d mdDoc) tableRow(name string) (string, bool) {
	for _, line := range d.lines {
		if strings.HasPrefix(line, "| `"+name+"` |") {
			return line, true
		}
	}
	return "", false
}
