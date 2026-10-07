package api_test

// security_resume_test.go is resume's part of SR-15's per-verb table
// (security_test.go; Epic 16), and the pre-launch and "duplicate session"
// cases reuse's part (security_reuse_test.go) shares: a resumable target row
// whose agent process is gone meets the planted SECRET=xyz sessions at the
// pre-launch check (SR-8.2), refused before any write with no call event, and
// at the re-lookup after "duplicate session" (SR-8.5, SR-14), placed as the
// create returns.

import (
	"testing"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// securityResumeVerbs are resume's rows of securityVerbs: the pre-launch
// refusals, and the held name after "duplicate session", whose one create the
// harness checks as a launch's (launch), though it acts on target's own id.
var securityResumeVerbs = []securityVerb{
	{verb: "resume", call: securityResumeCall, cases: rsSecPreLaunchCases(), serial: securityMovesHome},
	{verb: "resume, duplicate session", event: "ad.launch.name_held", launch: true, call: securityResumeHeldCall,
		record: securityNameHeldRecord, serial: securityMovesHome, cases: rsSecHeldCases("resume", "ad_resume", "resume",
			apitest.ResumeRestore{Outcome: apitest.RestoreApplied, PriorState: store.StateEnded})},
}

// securityResumeCall resumes the subject through the Client, its transcript
// seeded, under a fresh HOME (secMoveHome).
func securityResumeCall(t *testing.T, c *api.Client, s *securityScene) (any, error) {
	secMoveHome(t)
	apitest.SeedJsonl(t, s.target.Spawn.CWD, s.target.Spawn.ClaudeSessionID)
	return c.Resume(api.ResumeParams{ClaudeInstanceID: s.subject})
}

// securityResumeHeldCall resumes target (not a launch's fresh id) with its
// create answering "duplicate session" (arrangeHeld, placing no holder of its
// own: the case's arrange places the planted ones as the create returns).
func securityResumeHeldCall(t *testing.T, c *api.Client, s *securityScene) (any, error) {
	s.subject = s.target.ID
	s.e.arrangeHeld(t, resumeRow{killRow: s.target}, heldSpec{Holder: holderVanished})
	return securityResumeCall(t, c, s)
}

// rsSecTarget is expire's finished target with no session (exSecTarget), a
// session id and a cwd, so resume and reuse pass their guards and reach the
// lookup; it records no socket, so they resolve the fixture's per-user one.
func rsSecTarget() killRowSpec {
	spec := exSecTarget(true)
	spec.SessionID, spec.CWD = "sess-security", "/security/resume"
	spec.Opts = append(spec.Opts, apitest.WithTmuxSocket(""))
	return spec
}

// rsSecPreLaunchCases meet the planted sessions on resume's and reuse's
// refusals before anything changes (SR-8.2, SR-10.2, SR-10.8, SR-3.10): the
// recorded name's holders, a Leftover and conflicting labels.
func rsSecPreLaunchCases() []securityCase {
	held := func(s *securityScene, sessionID string) apitest.HeldName {
		return apitest.HeldName{Name: s.target.Name, SessionID: sessionID, BeforeLaunch: true}
	}
	conflict := api.ErrTmuxSessionConflict
	return append(secHeldBy(securityCase{target: rsSecTarget(), wantErr: conflict},
		func(s *securityScene) apitest.DescCase { return apitest.DescHeldNoValidID(held(s, s.noIDSess)) },
		func(s *securityScene) apitest.DescCase { return apitest.DescHeldDifferentID(held(s, s.otherSess)) }),
		securityCase{
			name: "name held by another store's session with this row's token and id", target: rsSecTarget(),
			arrange: rpSecOtherStore(func(s *securityScene) string { return s.target.Name },
				func(s *securityScene) string { return s.target.Token }),
			wantErr: conflict,
			desc: func(s *securityScene) apitest.DescCase {
				return apitest.DescHeldOtherStore(held(s, s.extraSess.ID), s.e.storeID)
			},
		},
		securityCase{
			name: "leftover", target: rsSecTarget(), wantErr: conflict,
			arrange: func(t *testing.T, s *securityScene) { s.extraSess = s.e.seedHolder(t, s.target, holderOld) },
			desc: func(s *securityScene) apitest.DescCase {
				return apitest.DescPreLaunchLeftover(s.target.ID, []apitest.DescSession{{Name: s.extraSess.Name, ID: s.extraSess.ID}})
			},
		},
		securityCase{
			name: "conflicting labels", target: rsSecTarget(), wantErr: conflict, desc: secConflict, disagree: true,
			arrange: func(t *testing.T, s *securityScene) {
				s.target.Session = s.e.seedOther(t, s.target.Socket, tmuxfix.SeedSession{Name: s.target.Name, Label: s.target.current()})
				secDuplicate(t, s)
			},
		},
	)
}

// rsSecAfterCreate runs place once, when the call's create on target's socket returns.
func rsSecAfterCreate(place func(t *testing.T, s *securityScene)) func(*testing.T, *securityScene) {
	return func(t *testing.T, s *securityScene) {
		placed := false
		s.e.rec.AfterCall(tmux.CallCreate, func(c tmuxfix.SocketCall, _ error) {
			if !placed && c.Socket == s.target.Socket {
				placed = true
				place(t, s)
			}
		})
	}
}

// rsSecPlant seeds a session named name with label on target's socket, its
// one pane's process alive with SECRET=xyz in its environment, and returns it as stored.
func rsSecPlant(t *testing.T, s *securityScene, name string, label tmux.Label) tmuxfix.SeedSession {
	t.Helper()
	pid := s.e.newPID()
	s.e.pc.Set(pid, procfix.Alive(apitest.LinuxProcStarttime).WithEnv(securityEnv()))
	pane := tmuxfix.SeedPane{PID: pid}
	if label.Kind == tmux.LabelValid {
		pane.AdPane = label.Token
	}
	return s.e.seedOther(t, s.target.Socket, tmuxfix.SeedSession{Name: name, Label: label, Panes: []tmuxfix.SeedPane{pane}})
}

// rsSecHeldCases meet SECRET=xyz holders, placed as the create returns, at the
// re-lookup after "duplicate session" (SR-8.5, SR-10.4, SR-14): the other
// row's, an unlabelled one, another store's with the row's (pre-reset) token
// and id, and conflicting labels. Each restores the row (restore); verb,
// source and launch are its records' fields.
func rsSecHeldCases(verb, source, launch string, restore apitest.ResumeRestore) []securityCase {
	restored := func(s *securityScene, sessionID string) apitest.HeldName {
		return apitest.HeldName{Name: s.target.Name, SessionID: sessionID, Restore: restore}
	}
	nameHeld := map[string]any{"source": source, "launch": launch, "row_result": "restored", "carries_this_id": false}
	holder := func(name string, label func(*securityScene) tmux.Label, desc func(*securityScene, apitest.HeldName) apitest.DescCase) securityCase {
		return securityCase{name: name, target: rsSecTarget(), wantErr: api.ErrTmuxSessionConflict, fields: nameHeld,
			arrange: rsSecAfterCreate(func(t *testing.T, s *securityScene) { s.extraSess = rsSecPlant(t, s, s.target.Name, label(s)) }),
			desc:    func(s *securityScene) apitest.DescCase { return desc(s, restored(s, s.extraSess.ID)) }}
	}
	return []securityCase{
		holder("held by the other row's session", func(s *securityScene) tmux.Label { return s.other.current() },
			func(_ *securityScene, p apitest.HeldName) apitest.DescCase { return apitest.DescHeldDifferentID(p) }),
		holder("held by a session with no valid label", func(*securityScene) tmux.Label { return tmux.Label{} },
			func(_ *securityScene, p apitest.HeldName) apitest.DescCase { return apitest.DescHeldNoValidID(p) }),
		holder("held by another store's session with this row's token and id",
			func(s *securityScene) tmux.Label { return s.target.otherStore(s.target.Token) },
			func(s *securityScene, p apitest.HeldName) apitest.DescCase {
				return apitest.DescHeldOtherStore(p, s.e.storeID)
			}),
		{
			name: "conflicting labels", target: rsSecTarget(), wantErr: api.ErrTmuxSessionConflict,
			arrange: rsSecAfterCreate(func(t *testing.T, s *securityScene) {
				s.target.Session = rsSecPlant(t, s, s.target.Name, s.target.current())
				s.extraSess = rsSecPlant(t, s, "dup-"+uuid.NewString()[:8], s.target.current())
			}),
			desc: func(s *securityScene) apitest.DescCase {
				return secConflict(s).AfterHeldName(restored(s, s.target.Session.ID))
			},
			event: "ad.provenance.disagree",
			fields: map[string]any{"verb": verb, "source": source, "reason": "duplicate_label",
				"verdict": "provenance_conflict", "action": "restored"},
		},
	}
}
