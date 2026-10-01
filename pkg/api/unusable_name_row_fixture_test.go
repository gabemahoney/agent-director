package api_test

// unusable_name_row_fixture_test.go seeds a pane verb's row recording one of
// unusable_name_fixture_test.go's names (SR-3.2), its own session up under a
// usable name, so only the name guard stops a read, send or /exit. It holds no tests.

import (
	"testing"

	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// unusableFixture is unusableNameFixtures' entry labelled label. Later tests
// pick a fixture with it, never their own lookup.
func unusableFixture(t *testing.T, label string) unusableNameFixture {
	t.Helper()
	for _, f := range unusableNameFixtures() {
		if f.label == label {
			return f
		}
	}
	t.Fatalf("no unusable-name fixture %q", label)
	return unusableNameFixture{}
}

// seedUnusableRow seeds spec's row recording f's raw name, with its
// current-labelled session up as "renamed-<id>" and every pane's capture text set.
// Later pane-verb tests seed such a row with it, never their own seeder.
func (e *killEnv) seedUnusableRow(t *testing.T, spec killRowSpec, f unusableNameFixture) killRow {
	t.Helper()
	spec.NoSession = true
	spec.Opts = append(spec.Opts, apitest.WithTmuxSessionName(f.raw))
	r := e.seedRow(t, spec)
	e.seedSession(t, &r, tmuxfix.WithRowSessionName("renamed-"+r.ID))
	e.setPaneTexts(r.Socket)
	return r
}
