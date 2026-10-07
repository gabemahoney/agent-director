package api_test

// expire_trail_unusable_test.go covers expire's trail for a row whose recorded
// tmux session name is unusable (SR-3.2, SR-12.5, SR-14, SR-18.17 step 1):
// one ad.expire.kept per run with the fixture's reason and the recorded name
// as the trail's JSON writes it, no ad.provenance.disagree, and no other
// row's id or session-environment value. It reuses expire_trail_test.go's
// world, whose fail-open run includes these cases.

import (
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// xtuTrailName is name as the trail's JSON encoder writes it: each byte that
// is not valid UTF-8 replaced by U+FFFD, every other byte as stored.
func xtuTrailName(name string) string {
	var b strings.Builder
	for _, r := range name {
		b.WriteRune(r) // ranging yields U+FFFD for each invalid byte
	}
	return b.String()
}

// xtuRow seeds row id in x, its agent gone, recording f's raw name plus opts,
// to be kept with f's reason; its expected trail name is xtuTrailName's.
func (x *xtrWorld) xtuRow(t *testing.T, id string, f unusableNameFixture, opts ...apitest.SpawnOption) *killRow {
	t.Helper()
	r := x.rowWith(t, id, agentGone, func(s *killRowSpec) {
		s.Opts = append(append(s.Opts, apitest.WithTmuxSessionName(f.raw)), opts...)
	}, f.kept)
	r.Name = xtuTrailName(f.raw)
	return r
}

// xtrUnusableCases is one world per fixture (its row kept with the fixture's
// reason) and one where a lookup would write a disagree record: the
// SessionStart and pane identities disagree and the server restarted, so the
// usable row beside them gets server_restarted while theirs get none.
func xtrUnusableCases() []xtrCase {
	var cases []xtrCase
	for _, f := range unusableNameFixtures() {
		cases = append(cases, xtrCase{name: f.kept + ", " + f.label, seed: func(t *testing.T, x *xtrWorld) {
			x.xtuRow(t, "r", f)
		}})
	}
	return append(cases, xtrCase{name: "no ad.provenance.disagree though the identities disagree and the server restarted",
		seed: func(t *testing.T, x *xtrWorld) {
			for i, f := range unusableNameFixtures() {
				x.xtuRow(t, fmt.Sprintf("u%d", i), f, apitest.WithPID(x.e.newPID()))
			}
			usable := x.row(t, "usable", agentGone, xtrDeleted, disagreeWant{reason: "server_restarted",
				server: "restarted", verdict: "gone", action: "deleted"})
			x.restart(usable)
		}})
}

// TestExpireTrailUnusableName: each unusable-name row writes exactly one
// ad.expire.kept with its reason and recorded name, and no disagree record.
func TestExpireTrailUnusableName(t *testing.T) {
	// Serial: it checks every record written to the shared trail since its mark.
	xtrRunCases(t, xtrUnusableCases())
}

// TestExpireTrailUnusableNameEveryRun: every fixture's row, kept on each of
// two runs, writes its one ad.expire.kept on each.
func TestExpireTrailUnusableNameEveryRun(t *testing.T) {
	// Serial: it checks every record written to the shared trail since its mark.
	x := newXtrWorld(t, "xtr-"+uuid.NewString()[:8])
	for i, f := range unusableNameFixtures() {
		x.xtuRow(t, fmt.Sprintf("u%d", i), f)
	}
	x.expire(t)
	x.expire(t)
}
