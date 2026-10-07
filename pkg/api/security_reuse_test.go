package api_test

// security_reuse_test.go is reuse's part of SR-15's per-verb table
// (security_test.go; Epic 17): spawn with the reuse opt-in on the finished
// target row, beside the planted SECRET=xyz sessions, at the pre-check and
// old-row lookup (resume's rsSecPreLaunchCases), a successful reuse, and the
// re-lookup after "duplicate session" (rsSecHeldCases) (SR-10.2, SR-10.4,
// SR-10.8, SR-14). The new launch token the reset writes is forbidden as well
// as the old one (securityScene.forbid).

import (
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// securityReuseVerbs are reuse's rows of securityVerbs: the refusals before
// anything changes (no call event), a successful reuse (ad.spawn.reused) and
// the held name after "duplicate session" (ad.launch.name_held); the harness
// checks a launched row's one create as a launch's, though it acts on
// target's own id.
var securityReuseVerbs = []securityVerb{
	{verb: "spawn Reuse", call: securityReuseCall, cases: rsSecPreLaunchCases(), serial: securityMovesHome},
	{verb: "spawn Reuse, launched", event: "ad.spawn.reused", launch: true, call: securityReuseLaunchCall,
		cases: securityReuseLaunchCases, serial: securityMovesHome},
	{verb: "spawn Reuse, duplicate session", event: "ad.launch.name_held", launch: true, call: securityReuseHeldCall,
		record: securityNameHeldRecord, serial: securityMovesHome, cases: rsSecHeldCases("spawn", "ad_spawn", "reuse",
			apitest.ResumeRestore{Outcome: apitest.RestoreApplied, PriorState: store.StateEnded, Launch: apitest.LaunchReuse})},
}

// securityReuseCall reuses target, requesting its recorded name, from a fresh
// HOME (secMoveHome) with no caller instance id; the token the call's create
// carries goes into s.forbid.
func securityReuseCall(t *testing.T, c *api.Client, s *securityScene) (any, error) {
	secMoveHome(t)
	t.Setenv("AGENT_DIRECTOR_INSTANCE_ID", "")
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
// "duplicate session" (arrangeHeld, placing no holder of its own).
func securityReuseHeldCall(t *testing.T, c *api.Client, s *securityScene) (any, error) {
	s.e.arrangeHeld(t, resumeRow{killRow: s.target}, heldSpec{Holder: holderVanished})
	return securityReuseLaunchCall(t, c, s)
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
