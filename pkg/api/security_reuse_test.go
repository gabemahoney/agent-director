package api_test

// security_reuse_test.go is reuse's part of SR-15's per-verb table
// (security_test.go; Epic 17), run by TestSecurityReuse: spawn with the reuse
// opt-in on the finished target row, beside the planted SECRET=xyz sessions,
// at the new-name pre-check (a no-id, another row's or another store's
// holder), the old-row lookup (a leftover, conflicting labels), a successful
// reuse, and the re-lookup after "duplicate session" meeting each holder
// (SR-10.2, SR-10.4, SR-10.8, SR-14). The new launch token the reset writes
// is forbidden as well as the old one (securityScene.forbid).

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// TestSecurityReuse checks SR-15 for reuse's rows, as
// TestSecuritySecretAndOtherRowID does for every other verb's.
func TestSecurityReuse(t *testing.T) { runSecurityVerbs(t, securityReuseVerbs) }

// securityReuseVerbs are reuse's rows: the refusals before anything changes
// (no call event), a successful reuse (ad.spawn.reused) and the held name
// after "duplicate session" (ad.launch.name_held); the harness checks a
// launched row's one create as a launch's, though it acts on target's own id.
var securityReuseVerbs = []securityVerb{
	{verb: "spawn Reuse", call: securityReuseCall, cases: securityReuseCases},
	{verb: "spawn Reuse, launched", event: "ad.spawn.reused", launch: true, call: securityReuseLaunchCall,
		cases: securityReuseLaunchCases},
	{verb: "spawn Reuse, duplicate session", event: "ad.launch.name_held", launch: true, call: securityReuseHeldCall,
		record: rsSecHeldRecord, cases: securityReuseHeldCases},
}

// securityReuseCall reuses target, requesting its recorded name, from a fresh
// HOME with an empty .claude.json for pre-trust and no caller instance id; the
// token the call's create carries goes into s.forbid.
func securityReuseCall(t *testing.T, c *api.Client, s *securityScene) (any, error) {
	home := t.TempDir() // the trail is pinned by TestMain
	t.Setenv("HOME", home)
	t.Setenv("AGENT_DIRECTOR_INSTANCE_ID", "")
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte("{}"), 0o600); err != nil {
		t.Fatalf("write .claude.json: %v", err)
	}
	s.e.rec.AfterCall(tmux.CallCreate, func(sc tmuxfix.SocketCall, _ error) {
		if sc.InstanceID == s.target.ID && sc.Token != "" {
			s.forbid = append(s.forbid, sc.Token)
		}
	})
	return c.Spawn(api.SpawnParams{ClaudeInstanceID: s.target.ID, ReuseFinished: true,
		TmuxSessionName: s.target.Name, CWD: t.TempDir()})
}

// securityReuseLaunchCall is securityReuseCall acting on target, not a launch's fresh id.
func securityReuseLaunchCall(t *testing.T, c *api.Client, s *securityScene) (any, error) {
	s.subject = s.target.ID
	return securityReuseCall(t, c, s)
}

// securityReuseHeldCall is securityReuseLaunchCall with the create answering
// "duplicate session" (arrangeHeld, placing no holder of its own: the case's
// arrange places the planted ones as the create returns).
func securityReuseHeldCall(t *testing.T, c *api.Client, s *securityScene) (any, error) {
	s.e.arrangeHeld(t, resumeRow{killRow: s.target}, heldSpec{Holder: holderVanished})
	return securityReuseLaunchCall(t, c, s)
}

// securityReuseCases meet the planted sessions on reuse's refusals before
// anything changes: the requested name's holders, a leftover and conflicting
// labels (SR-10.2, SR-10.8, SR-3.10).
var securityReuseCases = []securityCase{
	{
		name:    "pre-check, name held by the no-id session",
		target:  rsSecTarget(),
		holder:  securityHolderNoID,
		wantErr: api.ErrTmuxSessionConflict,
		desc:    func(s *securityScene) apitest.DescCase { return apitest.DescHeldNoValidID(rsSecHeld(s, s.noIDSess)) },
	},
	{
		name:    "pre-check, name held by the other row's session",
		target:  rsSecTarget(),
		holder:  securityHolderOther,
		wantErr: api.ErrTmuxSessionConflict,
		desc:    func(s *securityScene) apitest.DescCase { return apitest.DescHeldDifferentID(rsSecHeld(s, s.otherSess)) },
	},
	{
		name:   "pre-check, name held by another store's session with this row's token and id",
		target: rsSecTarget(),
		arrange: rpSecOtherStore(func(s *securityScene) string { return s.target.Name },
			func(s *securityScene) string { return s.target.Token }),
		wantErr: api.ErrTmuxSessionConflict,
		desc: func(s *securityScene) apitest.DescCase {
			return apitest.DescHeldOtherStore(rsSecHeld(s, s.extraSess.ID), s.e.storeID)
		},
	},
	{
		name:    "old-row lookup, leftover",
		target:  rsSecTarget(),
		arrange: func(t *testing.T, s *securityScene) { s.extraSess = s.e.seedHolder(t, s.target, holderOld) },
		wantErr: api.ErrTmuxSessionConflict,
		desc: func(s *securityScene) apitest.DescCase {
			return apitest.DescPreLaunchLeftover(s.target.ID, []apitest.DescSession{{Name: s.extraSess.Name, ID: s.extraSess.ID}})
		},
	},
	{
		name:   "old-row lookup, conflicting labels",
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

// securityReuseLaunchCases is a successful reuse beside both planted
// sessions, each under its own name: one ad.spawn.reused, the row pending.
var securityReuseLaunchCases = []securityCase{{
	name:   "success beside both",
	target: rsSecTarget(),
	fields: map[string]any{"source": "ad_spawn", "prior_state": store.StateEnded},
	check: func(t *testing.T, s *securityScene, res any) {
		t.Helper()
		if got := res.(api.SpawnResult); got.ClaudeInstanceID != s.target.ID {
			t.Errorf("result id = %q; want %q", got.ClaudeInstanceID, s.target.ID)
		}
		if got := s.e.columns(t, s.target.ID).State; got != store.StatePending {
			t.Errorf("row state = %v; want %s", got, store.StatePending)
		}
	},
}}

// securityReuseRestored is the held-name description parameter for target's
// name held by sessionID, after the restore put the reset row back to ended.
func securityReuseRestored(s *securityScene, sessionID string) apitest.HeldName {
	return apitest.HeldName{Name: s.target.Name, SessionID: sessionID,
		Restore: apitest.ResumeRestore{Outcome: apitest.RestoreApplied, PriorState: store.StateEnded, Launch: apitest.LaunchReuse}}
}

// securityReuseNameHeld is the ad.launch.name_held fields of a restored reuse
// whose holder carries no label of this row.
var securityReuseNameHeld = map[string]any{"source": "ad_spawn", "launch": "reuse", "row_result": "restored",
	"carries_this_id": false}

// securityReuseHeldCases meet SECRET=xyz holders at reuse's re-lookup after
// "duplicate session": the other row's, an unlabelled, another store's (with
// target's pre-reset token and id), and conflicting labels (SR-10.4, SR-14).
var securityReuseHeldCases = []securityCase{
	{
		name:    "held by the other row's session",
		target:  rsSecTarget(),
		arrange: rsSecHolder(func(s *securityScene) tmux.Label { return s.other.current() }),
		wantErr: api.ErrTmuxSessionConflict,
		desc: func(s *securityScene) apitest.DescCase {
			return apitest.DescHeldDifferentID(securityReuseRestored(s, s.extraSess.ID))
		},
		fields: securityReuseNameHeld,
	},
	{
		name:    "held by a session with no valid label",
		target:  rsSecTarget(),
		arrange: rsSecHolder(func(*securityScene) tmux.Label { return tmux.Label{} }),
		wantErr: api.ErrTmuxSessionConflict,
		desc: func(s *securityScene) apitest.DescCase {
			return apitest.DescHeldNoValidID(securityReuseRestored(s, s.extraSess.ID))
		},
		fields: securityReuseNameHeld,
	},
	{
		name:    "held by another store's session with this row's pre-reset token and id",
		target:  rsSecTarget(),
		arrange: rsSecHolder(func(s *securityScene) tmux.Label { return s.target.otherStore(s.target.Token) }),
		wantErr: api.ErrTmuxSessionConflict,
		desc: func(s *securityScene) apitest.DescCase {
			return apitest.DescHeldOtherStore(securityReuseRestored(s, s.extraSess.ID), s.e.storeID)
		},
		fields: securityReuseNameHeld,
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
				}}).AfterHeldName(securityReuseRestored(s, s.target.Session.ID))
		},
		event: "ad.provenance.disagree",
		fields: map[string]any{"verb": "spawn", "source": "ad_spawn", "reason": "duplicate_label",
			"verdict": "provenance_conflict", "action": "restored"},
	},
}
