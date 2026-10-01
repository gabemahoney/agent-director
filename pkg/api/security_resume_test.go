package api_test

// security_resume_test.go is resume's part of SR-15's per-verb table
// (security_test.go; Epic 16): a resumable target row whose agent process is
// gone meets the planted SECRET=xyz sessions at resume's pre-launch check
// (SR-8.2): its recorded name held by the other row's session, by the no-id
// session or by another store's session, a Leftover, and conflicting labels.
// Each is refused before any write, with no call event.

import (
	"testing"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// securityResumeCall resumes the subject through the Client, its transcript
// seeded under a fresh HOME (the trail is pinned by TestMain).
func securityResumeCall(t *testing.T, c *api.Client, s *securityScene) (any, error) {
	t.Setenv("HOME", t.TempDir())
	apitest.SeedJsonl(t, s.target.Spawn.CWD, s.target.Spawn.ClaudeSessionID)
	return c.Resume(api.ResumeParams{ClaudeInstanceID: s.subject})
}

// rsSecTarget is expire's finished target with no session (exSecTarget), a
// session id and a cwd, so resume passes its guards and reaches the lookup;
// it records no socket, so resume resolves the fixture's per-user one
// (killRow.Socket), and each case seeds its sessions there itself.
func rsSecTarget() killRowSpec {
	spec := exSecTarget(true)
	spec.SessionID, spec.CWD = "sess-security", "/security/resume"
	spec.Opts = append(spec.Opts, apitest.WithTmuxSocket(""))
	return spec
}

// rsSecHeld is the pre-launch holder parameter for the target's name held by sessionID.
func rsSecHeld(s *securityScene, sessionID string) apitest.HeldName {
	return apitest.HeldName{Name: s.target.Name, SessionID: sessionID, BeforeLaunch: true}
}

// securityResumeCases meet the planted sessions on resume's name-holder,
// Leftover and conflicting-labels refusals (SR-8.2, SR-3.10).
var securityResumeCases = []securityCase{
	{
		name:    "gone, name held by the other row's session",
		target:  rsSecTarget(),
		holder:  securityHolderOther,
		wantErr: api.ErrTmuxSessionConflict,
		desc:    func(s *securityScene) apitest.DescCase { return apitest.DescHeldDifferentID(rsSecHeld(s, s.otherSess)) },
	},
	{
		name:    "gone, name held by the no-id session",
		target:  rsSecTarget(),
		holder:  securityHolderNoID,
		wantErr: api.ErrTmuxSessionConflict,
		desc:    func(s *securityScene) apitest.DescCase { return apitest.DescHeldNoValidID(rsSecHeld(s, s.noIDSess)) },
	},
	{
		name:   "gone, name held by another store's session with this launch's token and id",
		target: rsSecTarget(),
		arrange: rpSecOtherStore(func(s *securityScene) string { return s.target.Name },
			func(s *securityScene) string { return s.target.Token }),
		wantErr: api.ErrTmuxSessionConflict,
		desc: func(s *securityScene) apitest.DescCase {
			return apitest.DescHeldOtherStore(rsSecHeld(s, s.extraSess.ID), s.e.storeID)
		},
	},
	{
		name:    "leftover",
		target:  rsSecTarget(),
		arrange: func(t *testing.T, s *securityScene) { s.extraSess = s.e.seedHolder(t, s.target, holderOld) },
		wantErr: api.ErrTmuxSessionConflict,
		desc: func(s *securityScene) apitest.DescCase {
			return apitest.DescPreLaunchLeftover(s.target.ID, []apitest.DescSession{{Name: s.extraSess.Name, ID: s.extraSess.ID}})
		},
	},
	{
		name:   "conflicting labels",
		target: rsSecTarget(),
		arrange: func(t *testing.T, s *securityScene) {
			s.target.Session = s.e.seedOther(t, s.target.Socket, tmuxfix.SeedSession{Name: s.target.Name, Label: s.target.current()})
			s.extraSess = s.e.seedOther(t, s.target.Socket,
				tmuxfix.SeedSession{Name: "dup-" + uuid.NewString()[:8], Label: s.target.current()})
		},
		wantErr: api.ErrTmuxSessionConflict,
		desc: func(s *securityScene) apitest.DescCase {
			return apitest.DescConflictingLabels(apitest.ConflictingLabels{InstanceID: s.target.ID, NothingWasDone: true,
				Sessions: []apitest.DescSession{
					{Name: s.target.Session.Name, ID: s.target.Session.ID},
					{Name: s.extraSess.Name, ID: s.extraSess.ID},
				}})
		},
		disagree: true,
	},
}
