package hook_test

// b.hib: late SessionStart must preserve same-session activity. D1 violations
// before the launch identity write remain a residual (docs/hooks.md and
// docs/architecture.md); these tests use the real store and injected clocks.

import (
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// reproIdentity is the launch's identity write recording p as the pane process.
func reproIdentity(t *testing.T, st *store.Store, id string, p hookParent) {
	t.Helper()
	sp := mustGetSpawn(t, st, id)
	res, err := st.RecordLaunchIdentity(id, sp.RowVersion, sp.Identity.Token,
		store.LaunchIdentity{PaneID: apitest.TestPaneID, PanePID: p.PID, PaneStarttime: p.Start})
	if err != nil || res != store.CondApplied {
		t.Fatalf("RecordLaunchIdentity(%q) = %v, %v; want applied", id, res, err)
	}
}

// reproPendingNoPane seeds a fresh spawn's pending row: launch start now, no
// pane recorded (InsertPending's shape before RecordLaunchIdentity).
func reproPendingNoPane(t *testing.T, id, agent string) *store.Store {
	t.Helper()
	st, _ := ssgSeed(t, id, store.StatePending, "", apitest.WithAgent(agent),
		apitest.WithLaunchIdentity(swNoPane), apitest.WithLaunchStartedAt(time.Now().UnixMilli()))
	return st
}

type reproAgent struct {
	name             string
	agent            string
	sessionStart     func(t *testing.T, source string) ([]byte, string)
	userPromptSubmit func(t *testing.T, sessionID string) []byte
	parent           func(hookParent) hookParent
}

var reproAgents = []reproAgent{
	{"claude", store.AgentClaude,
		func(t *testing.T, source string) ([]byte, string) {
			p, sid, _ := ssgPayload(t, source)
			return []byte(p), sid
		},
		func(t *testing.T, sid string) []byte {
			extra := map[string]any{}
			if sid != "" {
				extra["session_id"], extra["transcript_path"] = sid, writeTranscript(t, sid)
			}
			raw, _, _ := subPayload(t, "user-prompt-submit.json", extra)
			return raw
		},
		func(p hookParent) hookParent { return p }},
	{"codex", store.AgentCodex,
		func(t *testing.T, source string) ([]byte, string) {
			raw, sid, _ := codexPayload(t, "session-start-"+source+".json", true, nil)
			return raw, sid
		},
		func(t *testing.T, sid string) []byte {
			extra := map[string]any{}
			if sid != "" {
				extra["session_id"] = sid
			}
			raw, _, _ := codexPayload(t, "user-prompt-submit.json", true, extra)
			return raw
		},
		codexParent},
}

// TestStartupResidualD1PromptBeforeIdentity: steps 1-4 of the Bug, in order:
// UserPromptSubmit on the pane-less pending row, then the identity write, then
// SessionStart (startup). Accepted residual: the row ends waiting.
func TestStartupResidualD1PromptBeforeIdentity(t *testing.T) {
	for _, ag := range reproAgents {
		t.Run(ag.name, func(t *testing.T) {
			id := "repro-c1-" + ag.name
			st := reproPendingNoPane(t, id, ag.agent)
			p := ag.parent(agentParent(t, st, id)) // the pane process the identity write will record
			before := len(readTrailLines(t, trailFile()))

			ssgHandle(t, st, id, p, string(ag.userPromptSubmit(t, "")), nil)
			afterUPS := mustGetSpawn(t, st, id)
			ignored := hookIgnoredAfter(t, before, id)
			t.Logf("OBSERVED after UserPromptSubmit: state=%s ad.hook.ignored=%v", afterUPS.State, ignored)

			reproIdentity(t, st, id, p)
			ss, _ := ag.sessionStart(t, "startup")
			ssgHandle(t, st, id, p, string(ss), nil)
			final := mustGetSpawn(t, st, id)
			t.Logf("OBSERVED after identity write + SessionStart(startup): state=%s", final.State)

			if afterUPS.State != store.StatePending || final.State != store.StateWaiting {
				t.Errorf("D1 residual: prompt state/final = %q/%q; want pending/waiting", afterUPS.State, final.State)
			}
			if len(ignored) != 1 {
				t.Fatalf("ignored = %v; want one dropped prompt", ignored)
			}
			assertStr(t, ignored[0], "reason", store.HookReasonNoPaneRecorded)
			assertStr(t, ignored[0], "hook_event", "UserPromptSubmit")
		})
	}
}

// TestStartupResidualD1PromptDuringSessionStartWait: the race the Bug describes:
// SessionStart is waiting for the identity write (Claude runs it in the
// background), a UserPromptSubmit fires meanwhile, then the identity lands.
func TestStartupResidualD1PromptDuringSessionStartWait(t *testing.T) {
	for _, ag := range reproAgents {
		t.Run(ag.name, func(t *testing.T) {
			id := "repro-c1w-" + ag.name
			st := reproPendingNoPane(t, id, ag.agent)
			p := ag.parent(agentParent(t, st, id))
			hc := hookConfig(envHook(id, ""), p)
			ups := ag.userPromptSubmit(t, "")
			var upsState string
			before := len(readTrailLines(t, trailFile()))
			atSleep(t, hc, 2, func() {
				ssgHandle(t, st, id, p, string(ups), nil) // the prompt typed while SessionStart waits
				upsState = mustGetSpawn(t, st, id).State
				reproIdentity(t, st, id, p) // then the launch's identity write lands
			})
			ss, _ := ag.sessionStart(t, "startup")
			ssgHandleWith(t, st, hc, string(ss), nil)
			final := mustGetSpawn(t, st, id)
			t.Logf("OBSERVED: SessionStart slept %d times; state after UserPromptSubmit=%s; final=%s",
				len(hookClock(t, hc).Sleeps()), upsState, final.State)
			if upsState != store.StatePending || final.State != store.StateWaiting {
				t.Errorf("D1 residual: prompt state/final = %q/%q; want pending/waiting", upsState, final.State)
			}
			ignored := hookIgnoredAfter(t, before, id)
			if len(ignored) != 1 {
				t.Fatalf("ignored = %v; want one dropped prompt", ignored)
			}
			assertStr(t, ignored[0], "reason", store.HookReasonNoPaneRecorded)
			assertStr(t, ignored[0], "hook_event", "UserPromptSubmit")
		})
	}
}

// TestReproCase2LateSessionStartOverwritesWorking: the second case: a row
// moved to working by an applied UserPromptSubmit, then a SessionStart of the
// same life. Expected: the row stays working.
func TestReproCase2LateSessionStartOverwritesWorking(t *testing.T) {
	for _, ag := range reproAgents {
		for _, source := range []string{"startup", "resume"} {
			t.Run(ag.name+"/"+source, func(t *testing.T) {
				id := "repro-c2-" + ag.name + "-" + source
				st, _ := ssgSeed(t, id, store.StatePending, "", apitest.WithAgent(ag.agent),
					apitest.WithLaunchIdentity(ssgFreshPane))
				p := ag.parent(agentParent(t, st, id))
				ss, sid := ag.sessionStart(t, source)
				ssgHandle(t, st, id, p, string(ag.userPromptSubmit(t, sid)), nil)
				mid := mustGetSpawn(t, st, id)
				if mid.State != store.StateWorking || mid.SessionID != sid {
					t.Fatalf("setup: UserPromptSubmit left %q; want working", mid.State)
				}
				ssgHandle(t, st, id, p, string(ss), nil)
				final := mustGetSpawn(t, st, id)
				t.Logf("OBSERVED: after UserPromptSubmit=%s; after late SessionStart(%s)=%s", mid.State, source, final.State)
				if final.State != store.StateWorking {
					t.Errorf("BUG case 2: late SessionStart(%s) moved working to %q; want working", source, final.State)
				}
			})
		}
	}
}

// TestReproCase2PromptBetweenReadAndWrite: SessionStart retries once on the
// changed snapshot and must preserve the intervening UserPromptSubmit.
func TestReproCase2PromptBetweenReadAndWrite(t *testing.T) {
	for _, ag := range reproAgents {
		t.Run(ag.name, func(t *testing.T) {
			id := "repro-c2r-" + ag.name
			st, _ := ssgSeed(t, id, store.StatePending, "", apitest.WithAgent(ag.agent),
				apitest.WithLaunchIdentity(ssgFreshPane))
			p := ag.parent(agentParent(t, st, id))
			ss, sid := ag.sessionStart(t, "startup")
			ups := ag.userPromptSubmit(t, sid)
			var upsState string
			rs := &ssgBumpingStore{Store: st, t: t, bumps: 1, afterRead: func() {
				ssgHandle(t, st, id, p, string(ups), nil)
				upsState = mustGetSpawn(t, st, id).State
			}}
			ssgHandle(t, rs, id, p, string(ss), nil)
			final := mustGetSpawn(t, st, id)
			t.Logf("OBSERVED: SessionStart row reads=%d; state after UserPromptSubmit=%s; final=%s", rs.reads, upsState, final.State)
			if upsState != store.StateWorking || final.State != store.StateWorking {
				t.Errorf("BUG case 2 (prompt between read and write): row = %q; want working", final.State)
			}
		})
	}
}
