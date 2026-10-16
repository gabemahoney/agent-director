package api_test

import (
	"slices"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
)

// A25/B14: follow spawn's reuse opt-in with the row's agent, or explicitly switch it.
func TestAdviceFollow_A25_B14_CursorReuseFinished(t *testing.T) {
	// Serial: refusal snapshots inspect every append to the shared trail.
	adviceAssertManifest(t, "spawn", "reuse_finished", "Only an agent switch starts a new life; same-agent reuse keeps session history and subagent records but starts a fresh conversation.", "A switch back also starts with no memory of its earlier lives: resume and get never use or show an earlier life's history.")
	installCursorFixtureGateway(t)
	for _, agents := range [][]string{{"cursor", "cursor"}, {"claude", "cursor", "claude"}, {"cursor", "codex", "cursor"}} {
		t.Run(agents[0]+"-"+agents[1], func(t *testing.T) {
			e := newReuseEnv(t)
			r := e.seedReusable(t, agentGone, reuseRowSpec{Child: true, Opts: agentRowOpts(agents[0])})
			original := r
			mark := trailMark(t)
			life := int64(reuseLife)
			for _, target := range agents[1:] {
				prior := rlfRow(t, e, r.ID)
				p := reuseParams(t, r, reuseRequest{})
				p.Agent = target
				if target != prior.Agent {
					life++
				}
				if _, _, err := e.reuse(t, p); err != nil {
					t.Fatalf("%s -> %s: %v", prior.Agent, target, err)
				}
				r = e.reusedAs(t, r)
				row := rlfRow(t, e, r.ID)
				if row.Agent != target || row.SessionID != "" || row.JSONLPath != "" || row.Identity.Token == prior.Identity.Token {
					t.Fatalf("reuse did not create a fresh launch: %+v", row)
				}
				rlvAssertLife(t, e, r.ID, life)
				for _, token := range spawnArgsCreate(t, e, r.ID) {
					if slices.Contains([]string{"--resume", "--continue", "resume", original.Spawn.SessionID}, token) {
						t.Fatalf("fresh composition selects old conversation: %q", spawnArgsCreate(t, e, r.ID))
					}
				}
				view := rlfGet(t, e, r.ID)
				if life == reuseLife {
					rlfAssertListed(t, view, "no_session", rlfRetained(original)...)
				} else {
					rlfAssertListed(t, view, "no_session")
				}
				if child := rlfRow(t, e, r.ChildID); child.ParentID != r.ID {
					t.Fatalf("switch lost AD child: %+v", child)
				}
				if target == "cursor" {
					creates := e.rec.SocketCallsOf(tmux.CallCreate)
					if got := creates[len(creates)-1].Envs["AGENT_DIRECTOR_CURSOR_STORE_PATH"]; got != e.dbPath {
						t.Errorf("reused gateway store override = %q; want actual client store %q", got, e.dbPath)
					}
					subs := listSubs(t, e.st, r.ID)
					if subs.Tracked || subs.Subagents == nil || len(subs.Subagents) != 0 {
						t.Fatalf("Cursor fabricated subagent support: %+v", subs)
					}
				}
				// Never-reported launches cannot recover an earlier life, including after switch back.
				rlfNeverReported(t, e, r.ID)
				msg := rlfResumeRefused(t, e, r.ID, api.ErrNoSessionId)
				rlfAssertEarlierUnused(t, e, original, msg)
			}
			rlfAssertStored(t, e, r.ID, original.Spawn.SessionID, reuseLife)
			for _, h := range original.History {
				rlfAssertStored(t, e, r.ID, h.SessionID, h.Life)
			}
			if got := len(ptRecords(t, mark, rutReused, r.ID)); got != len(agents)-1 {
				t.Fatalf("append-only reuse trail has %d events; want %d", got, len(agents)-1)
			}
			if row := rlfRow(t, e, r.ID); row.State != store.StateMissing {
				t.Fatalf("unreported launch remained live: %+v", row)
			}
		})
	}
}

func TestSpawnReuseCursorSelectorsStartFresh(t *testing.T) {
	// Serial: installs the shared test HOME's inert gateway executable.
	installCursorFixtureGateway(t)
	for _, tc := range []struct {
		name              string
		args, want, drops []string
	}{
		{"all verified forms", []string{"--resume", "old", "--continue", "--new-session-id", "old-id", "resume", "--model", "sonnet", "hello"}, []string{"--model", "sonnet", "hello"}, []string{"--resume", "old", "--continue", "--new-session-id", "old-id", "resume"}},
		{"continue owns no prompt", []string{"--continue", "hello"}, []string{"hello"}, []string{"--continue"}},
		{"required model owns command spelling", []string{"--model", "resume", "--continue"}, []string{"--model", "resume"}, []string{"--continue"}},
		{"explicit empty prompt", []string{"--resume=old", "--", ""}, []string{"--", ""}, []string{"--resume=old"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newReuseEnv(t)
			r := e.seedReusable(t, agentGone, reuseRowSpec{Bare: true, NoSessionID: true})
			p := reuseParams(t, r, reuseRequest{Args: slices.Clone(tc.args)})
			p.Agent = "cursor"
			mark := trailMark(t)
			res, _, err := e.reuse(t, p)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(p.AgentArgs, tc.args) {
				t.Fatal("switch mutated caller argv")
			}
			row := rlfRow(t, e, r.ID)
			if row.Agent != "cursor" || row.SessionID != "" || !slices.Equal(row.AgentArgs, tc.want) {
				t.Fatalf("fresh row: %+v; want args %q", row, tc.want)
			}
			spawnArgsResult(t, res, tc.drops)
			spawnArgsTrail(t, mark, r.ID, store.StateEnded, tc.drops)
			argv := spawnArgsCreate(t, e, r.ID)
			if !slices.Contains(argv, "cursor-host") || slices.Contains(argv, "old") {
				t.Fatalf("reuse did not compose fresh gateway: %q", argv)
			}
		})
	}
}

func TestSpawnReuseCursorSelectorAuthority(t *testing.T) {
	// Serial: no-write snapshots include the shared trail; installs the inert gateway.
	installCursorFixtureGateway(t)
	for _, path := range []string{"plain", "same", "absent", "live", "read-failed", "malformed-switch", "unverified-switch", "raced-switch"} {
		t.Run(path, func(t *testing.T) {
			e := newReuseEnv(t)
			old, state := "claude", store.StateEnded
			if path == "same" {
				old = "cursor"
			}
			if path == "live" {
				state = store.StatePending
			}
			r := e.seedReusable(t, agentGone, reuseRowSpec{State: state, Opts: agentRowOpts(old)})
			p := reuseParams(t, r, reuseRequest{Args: []string{"--resume", "old"}})
			p.Agent = "cursor"
			if path == "plain" {
				p.ReuseFinished = false
			}
			if path == "absent" {
				p.InstanceID += "-absent"
			}
			if path == "malformed-switch" {
				p.AgentArgs = append(p.AgentArgs, "--unknown-survivor")
			}
			if path == "unverified-switch" {
				p.AgentArgs = append(p.AgentArgs, "-r")
			}
			w := &hookedReuseStore{st: e.st}
			if path == "read-failed" {
				w.failRead(nil)
			}
			before := e.snapshotReuse(t, r)
			if path == "raced-switch" {
				w.beforeReset(func() {
					if err := e.st.SetParentID(r.ID, r.ParentID); err != nil {
						t.Fatal(err)
					}
					before = e.snapshotReuse(t, r)
				})
			}
			_, _, err := e.reuseWith(t, w, p)
			wantError := "ErrSpawnDeniedFlag"
			if path == "raced-switch" {
				wantError = "ErrInstanceIdCollision"
			}
			assertOneName(t, err, wantError)
			e.assertWroteNothing(t, before)
			if len(e.rec.SocketCalls()) != before.calls {
				t.Fatal("selector refusal called tmux")
			}
		})
	}
}
