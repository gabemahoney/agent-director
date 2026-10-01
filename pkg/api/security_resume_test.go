package api_test

// security_resume_test.go is resume's part of SR-15's per-verb table
// (security_test.go; Epic 16): a resumable target row whose agent process is
// gone meets the planted SECRET=xyz sessions at resume's pre-launch check
// (SR-8.2): its recorded name held by the other row's session, by the no-id
// session or by another store's session, a Leftover, and conflicting labels,
// each refused before any write, with no call event; and at its re-lookup
// after "duplicate session" (SR-8.5, SR-14): sessions with SECRET=xyz in
// their pane processes' environment, placed as the create returns.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"

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
	{verb: "resume", call: securityResumeCall, cases: securityResumeCases},
	{verb: "resume, duplicate session", event: "ad.launch.name_held", launch: true, call: securityResumeHeldCall,
		record: rsSecHeldRecord, cases: securityResumeHeldCases},
}

// securityResumeCall resumes the subject through the Client, its transcript
// and an empty .claude.json for pre-trust seeded under a fresh HOME (the
// trail is pinned by TestMain).
func securityResumeCall(t *testing.T, c *api.Client, s *securityScene) (any, error) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte("{}"), 0o600); err != nil {
		t.Fatalf("write .claude.json: %v", err)
	}
	apitest.SeedJsonl(t, s.target.Spawn.CWD, s.target.Spawn.ClaudeSessionID)
	return c.Resume(api.ResumeParams{ClaudeInstanceID: s.subject})
}

// rsSecTarget is expire's finished target with no session (exSecTarget), a
// session id and a cwd, so resume passes its guards and reaches the lookup;
// it records no socket (a launch verb's scene records the per-user default),
// so resume resolves the fixture's per-user one (killRow.Socket), and each
// case seeds its sessions there itself.
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

// securityResumeHeldCall resumes target (not a launch's fresh id) with its
// create answering "duplicate session" (arrangeHeld, placing no holder of its
// own: the case's arrange places the planted ones as the create returns).
func securityResumeHeldCall(t *testing.T, c *api.Client, s *securityScene) (any, error) {
	s.subject = s.target.ID
	s.e.arrangeHeld(t, resumeRow{killRow: s.target}, heldSpec{Holder: holderVanished})
	return securityResumeCall(t, c, s)
}

// rsSecHeldRecord checks an ad.launch.name_held record as spawn's
// (securityNameHeldRecord); a case pinning its disagree record skips it.
func rsSecHeldRecord(t *testing.T, s *securityScene, rec map[string]any, texts map[string]string) {
	t.Helper()
	if rec["event"] == "ad.launch.name_held" {
		securityNameHeldRecord(t, s, rec, texts)
	}
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

// rsSecHolder places a holder of target's name labelled by label(s).
func rsSecHolder(label func(s *securityScene) tmux.Label) func(*testing.T, *securityScene) {
	return rsSecAfterCreate(func(t *testing.T, s *securityScene) {
		s.extraSess = rsSecPlant(t, s, s.target.Name, label(s))
	})
}

// rsSecRestored is the held-name description parameter for target's name held
// by sessionID, after the restore put the row back to ended.
func rsSecRestored(s *securityScene, sessionID string) apitest.HeldName {
	return apitest.HeldName{Name: s.target.Name, SessionID: sessionID,
		Restore: apitest.ResumeRestore{Outcome: apitest.RestoreApplied, PriorState: "ended"}}
}

// rsSecNameHeld is the ad.launch.name_held fields of a restored resume whose
// holder carries no label of this row.
var rsSecNameHeld = map[string]any{"source": "ad_resume", "launch": "resume", "row_result": "restored",
	"carries_this_id": false}

// securityResumeHeldCases meet SECRET=xyz holders at resume's re-lookup after
// "duplicate session": the other row's, an unlabelled, another store's (with
// target's own token and id), and conflicting labels (SR-8.5, SR-14).
var securityResumeHeldCases = []securityCase{
	{
		name:    "held by the other row's session",
		target:  rsSecTarget(),
		arrange: rsSecHolder(func(s *securityScene) tmux.Label { return s.other.current() }),
		wantErr: api.ErrTmuxSessionConflict,
		desc: func(s *securityScene) apitest.DescCase {
			return apitest.DescHeldDifferentID(rsSecRestored(s, s.extraSess.ID))
		},
		fields: rsSecNameHeld,
	},
	{
		name:    "held by a session with no valid label",
		target:  rsSecTarget(),
		arrange: rsSecHolder(func(*securityScene) tmux.Label { return tmux.Label{} }),
		wantErr: api.ErrTmuxSessionConflict,
		desc: func(s *securityScene) apitest.DescCase {
			return apitest.DescHeldNoValidID(rsSecRestored(s, s.extraSess.ID))
		},
		fields: rsSecNameHeld,
	},
	{
		name:    "held by another store's session with this row's token and id",
		target:  rsSecTarget(),
		arrange: rsSecHolder(func(s *securityScene) tmux.Label { return s.target.otherStore(s.target.Token) }),
		wantErr: api.ErrTmuxSessionConflict,
		desc: func(s *securityScene) apitest.DescCase {
			return apitest.DescHeldOtherStore(rsSecRestored(s, s.extraSess.ID), s.e.storeID)
		},
		fields: rsSecNameHeld,
	},
	{
		name:   "conflicting labels",
		target: rsSecTarget(),
		arrange: rsSecAfterCreate(func(t *testing.T, s *securityScene) {
			s.target.Session = rsSecPlant(t, s, s.target.Name, s.target.current())
			s.extraSess = rsSecPlant(t, s, "dup-"+uuid.NewString()[:8], s.target.current())
		}),
		wantErr: api.ErrTmuxSessionConflict,
		desc: func(s *securityScene) apitest.DescCase {
			return apitest.DescConflictingLabels(apitest.ConflictingLabels{InstanceID: s.target.ID, NothingWasDone: true,
				Sessions: []apitest.DescSession{
					{Name: s.target.Session.Name, ID: s.target.Session.ID},
					{Name: s.extraSess.Name, ID: s.extraSess.ID},
				}}).AfterHeldName(rsSecRestored(s, s.target.Session.ID))
		},
		event: "ad.provenance.disagree",
		fields: map[string]any{"verb": "resume", "source": "ad_resume", "reason": "duplicate_label",
			"verdict": "provenance_conflict", "action": "restored"},
	},
}
