package api_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/codexfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
	"github.com/google/uuid"
)

// TestSpawnResumeArgumentPolicy crosses validation with reuse eligibility;
// the complete flag/arity matrix belongs to internal/spawn.
func TestSpawnResumeArgumentPolicy(t *testing.T) {
	// Serial: refusal snapshots check every shared trail record since their mark.
	paths := []string{"plain", "absent", "same", "live", "read-failed", "switch"}
	cases := []struct {
		name string
		args []string
		flag string
		all  bool
	}{
		{"attached short", []string{"-rS1"}, "-r", false},
		{"bare short", []string{"-r"}, "-r", false},
		{"detached short", []string{"-r", "S1"}, "-r", false},
		{"continue short", []string{"-c"}, "-c", false},
		{"continue cluster", []string{"-cd"}, "-c", false},
		{"resume long", []string{"--resume", "S1"}, "--resume", false},
		{"fork session", []string{"--fork-session"}, "--fork-session", false},
		{"from pr", []string{"--from-pr", "42"}, "--from-pr", false},
		{"teleport", []string{"--teleport", "S1"}, "--teleport", false},
		{"hidden selector", []string{"--resume-session-at=turn"}, "--resume-session-at", false},
		{"control before cloud", []string{"--ref", "main", "--cloud", "S1"}, "--ref", false},
		{"control before remote", []string{"--on-branch", "work", "--remote", "S1"}, "--on-branch", false},
		{"forward control before cloud", []string{"--forward-home-settings", "false", "--cloud", "S1"}, "--forward-home-settings", false},
		{"context keeps controls selector refused", []string{"--ref", "main", "--environment", "fresh", "--cloud", "S1"}, "--cloud", false},
		{"print after resume", []string{"--resume", "S1", "-p"}, "-p", true},
		{"print cluster after resume", []string{"--resume=S1", "-pc"}, "-p", true},
		{"long print after resume", []string{"--resume", "S1", "--print"}, "--print", true},
		{"owned required long", []string{"--append-system-prompt", "--resume", "foo"}, "--resume", true},
		{"owned required equals long", []string{"--model", "--continue=true"}, "--continue", true},
		{"owned list long", []string{"--tools", "--resume", "Read", "-c"}, "--resume", true},
		{"owned print", []string{"--model", "--print=x", "-c"}, "--print", true},
		{"denied surviving settings", []string{"--settings=x", "--resume", "S1"}, "--settings", true},
		{"owned denied settings", []string{"--model", "--settings=x", "-c"}, "--settings", true},
	}
	for _, path := range paths {
		for _, tc := range cases {
			if path == "switch" && !tc.all {
				continue
			}
			t.Run(path+"/"+tc.name, func(t *testing.T) {
				e := newKillEnv(t)
				oldAgent, state := "claude", store.StateEnded
				if path == "switch" || path == "live" {
					oldAgent = "codex"
				}
				if path == "live" {
					state = store.StatePending
				}
				r := e.seedReusable(t, agentGone, reuseRowSpec{State: state, Opts: []apitest.SpawnOption{apitest.WithAgent(oldAgent)}})
				p := reuseParams(t, r, reuseRequest{Args: tc.args, Env: map[string]string{"HOME": "/reserved"}})
				if path == "plain" {
					p.ReuseFinished = false
				}
				if path == "absent" {
					p.InstanceID = "absent-" + uuid.NewString()
				}
				w := &hookedReuseStore{st: e.st}
				if path == "read-failed" {
					w.failRead(nil)
				}
				before := e.snapshotReuse(t, r)
				_, _, err := e.reuseWith(t, w, p)
				assertOneName(t, err, "ErrSpawnDeniedFlag")
				if want := "ErrSpawnDeniedFlag: " + tc.flag; err == nil || err.Error() != want {
					t.Fatalf("error = %v; want %q", err, want)
				}
				reads := 1
				if path == "plain" {
					reads = 0
				}
				if got := w.readCount(); got != reads {
					t.Errorf("precheck reads=%d; want %d", got, reads)
				}
				e.assertWroteNothing(t, before)
				if len(e.rec.SocketCalls()) != before.calls {
					t.Error("validation refusal called tmux")
				}
				if path == "absent" {
					if _, err := e.st.GetSpawn(p.InstanceID); !errors.Is(err, store.ErrSpawnNotFound) {
						t.Errorf("absent request row: %v", err)
					}
				}
			})
		}
	}
}

// TestSpawnReuseSavedPrecheckOutcome pins error precedence and the one saved
// read; lookup races and provenance rereads remain covered by lookup tests.
func TestSpawnReuseSavedPrecheckOutcome(t *testing.T) {
	// Serial: refusal snapshots check every shared trail record since their mark.
	cases := []struct {
		name, state, agent string
		args               []string
		reserved, failed   bool
		want               string
	}{
		{name: "live selector before collision", state: store.StatePending, agent: "codex", args: []string{"--resume", "S1"}, want: "ErrSpawnDeniedFlag"},
		{name: "live saved collision", state: store.StatePending, agent: "codex", want: "ErrInstanceIdCollision"},
		{name: "read failure selector before internal", state: store.StateEnded, agent: "codex", args: []string{"--resume", "S1"}, failed: true, want: "ErrSpawnDeniedFlag"},
		{name: "saved read failure", state: store.StateEnded, agent: "codex", failed: true, want: "ErrInternal"},
		{name: "finished switch reserved env after drop", state: store.StateEnded, agent: "codex", args: []string{"--resume", "S1"}, reserved: true, want: "ErrReservedEnvKey"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			r := e.seedReusable(t, agentGone, reuseRowSpec{State: tc.state, Opts: []apitest.SpawnOption{apitest.WithAgent(tc.agent)}})
			p := reuseParams(t, r, reuseRequest{Args: tc.args})
			if tc.reserved {
				p.ExtraEnv["HOME"] = "/reserved"
			}
			w := &hookedReuseStore{st: e.st}
			if tc.failed {
				w.failRead(nil)
			}
			before := e.snapshotReuse(t, r)
			_, _, err := e.reuseWith(t, w, p)
			assertOneName(t, err, tc.want)
			if tc.want == "ErrSpawnDeniedFlag" && err.Error() != "ErrSpawnDeniedFlag: --resume" {
				t.Errorf("detail = %v", err)
			}
			if w.readCount() != 1 {
				t.Errorf("reads=%d; want one saved precheck", w.readCount())
			}
			e.assertWroteNothing(t, before)
			if len(e.rec.SocketCalls()) != before.calls {
				t.Error("refusal called tmux")
			}
		})
	}
}

// spawnArgsCreate returns the latest recorded create for this test's id.
func spawnArgsCreate(t *testing.T, e *killEnv, id string) []string {
	t.Helper()
	calls := e.rec.SocketCalls()
	for i := len(calls) - 1; i >= 0; i-- {
		if calls[i].Call == tmux.CallCreate && calls[i].InstanceID == id {
			return calls[i].Command
		}
	}
	t.Fatal("no recorded create")
	return nil
}

// spawnArgsResult checks the public JSON and original dropped tokens together.
func spawnArgsResult(t *testing.T, res api.SpawnResult, want []string) {
	t.Helper()
	if !slices.Equal(res.DroppedArgs, want) {
		t.Errorf("DroppedArgs=%q; want original %q", res.DroppedArgs, want)
	}
	b, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	expected := []string{"instance_id", "pre_trust"}
	if len(want) > 0 {
		expected = append(expected, "dropped_args")
		var drops []string
		if err := json.Unmarshal(got["dropped_args"], &drops); err != nil || !slices.Equal(drops, want) {
			t.Errorf("JSON drops=%s (%v); want %q", got["dropped_args"], err, want)
		}
	}
	slices.Sort(expected)
	if !slices.Equal(rchgJSONKeys(t, res), expected) {
		t.Errorf("result JSON=%s; want keys %q", b, expected)
	}
}

// spawnArgsTrail checks the applied-change event, including null empty drops.
func spawnArgsTrail(t *testing.T, mark int, id, prior string, want []string) {
	t.Helper()
	recs := ptRecords(t, mark, rutReused, id)
	if len(recs) != 1 {
		t.Fatalf("reuse events=%v; want one", recs)
	}
	var dropped any
	if len(want) > 0 {
		values := make([]any, len(want))
		for i, v := range want {
			values[i] = v
		}
		dropped = values
	}
	assertTrailRecord(t, recs[0], rutReusedKeys, map[string]any{"instance_id": id, "prior_state": prior, "archived_session_id": nil, "lookup_outcome": "gone", "source": "ad_spawn"})
	if got := recs[0]["dropped_args"]; !reflect.DeepEqual(got, dropped) {
		t.Errorf("trail dropped_args=%v; want original %v", got, dropped)
	}
}

// TestSpawnReuseKeyCaseClaudeCodexClaudeResumeStartsFresh is SR-6.8.9's
// named key case, including a report-in and subsequent resume of the new session.
func TestSpawnReuseKeyCaseClaudeCodexClaudeResumeStartsFresh(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name              string
		args, want, drops []string
	}{
		{"resume detached", []string{"--resume", "S1"}, nil, []string{"--resume", "S1"}},
		{"resume bare", []string{"--resume"}, nil, []string{"--resume"}},
		{"resume equals", []string{"--resume=S1"}, nil, []string{"--resume=S1"}},
		{"short bare", []string{"-r"}, nil, []string{"-r"}},
		{"short detached", []string{"-r", "S1"}, nil, []string{"-r", "S1"}},
		{"short attached", []string{"-rS1"}, nil, []string{"-rS1"}},
		{"short equals", []string{"-r=S1"}, nil, []string{"-r=S1"}},
		{"continue", []string{"-c"}, nil, []string{"-c"}},
		{"continue long", []string{"--continue"}, nil, []string{"--continue"}},
		{"continue debug cluster", []string{"-cd"}, []string{"-d"}, []string{"-cd"}},
		{"fork", []string{"--fork-session"}, nil, []string{"--fork-session"}},
		{"from pr", []string{"--from-pr", "42"}, nil, []string{"--from-pr", "42"}},
		{"from pr bare", []string{"--from-pr"}, nil, []string{"--from-pr"}},
		{"from pr equals", []string{"--from-pr=42"}, nil, []string{"--from-pr=42"}},
		{"teleport", []string{"--teleport", "S1"}, nil, []string{"--teleport", "S1"}},
		{"teleport bare", []string{"--teleport"}, nil, []string{"--teleport"}},
		{"teleport equals", []string{"--teleport=S1"}, nil, []string{"--teleport=S1"}},
		{"hidden at", []string{"--resume-session-at", "turn"}, nil, []string{"--resume-session-at", "turn"}},
		{"hidden drops", []string{"--resume-drops-turn=turn"}, nil, []string{"--resume-drops-turn=turn"}},
		{"rewind", []string{"--rewind-files", "turn"}, nil, []string{"--rewind-files", "turn"}},
		{"reply", []string{"--reply-on-resume"}, nil, []string{"--reply-on-resume"}},
		{"attach serve", []string{"--attach-serve", "S1"}, nil, []string{"--attach-serve", "S1"}},
		{"benign values", []string{"-dc", "-nrocket", "--model", "-c", "--append-system-prompt", "-concise answers", "--resume", "S1"}, []string{"-dc", "-nrocket", "--model", "-c", "--append-system-prompt", "-concise answers"}, []string{"--resume", "S1"}},
		{"cloud orphan controls", []string{"--ref", "main", "--cloud", "S1", "--forward-home-settings", "false", "--on-branch=work"}, nil, []string{"--ref", "main", "--cloud", "S1", "--forward-home-settings", "false", "--on-branch=work"}},
		{"remote environment context", []string{"--remote", "S1", "--environment", "fresh", "--forward-home-settings", "false", "--ref", "main"}, []string{"--environment", "fresh", "--forward-home-settings", "false", "--ref", "main"}, []string{"--remote", "S1"}},
		{"cloud pool context", []string{"--cloud=S1", "--pool=fresh", "--on-branch", "work"}, []string{"--pool=fresh", "--on-branch", "work"}, []string{"--cloud=S1"}},
		{"prompt move original metadata", []string{"--debug", "--resume", "S1", "fix this", "--model=sonnet", "--continue"}, []string{"--debug", "--model=sonnet", "--", "fix this"}, []string{"--resume", "S1", "--continue"}},
		{"cluster render original metadata", []string{"-cd2e"}, []string{"--debug=2e"}, []string{"-cd2e"}},
		{"incomplete upstream rejection retained", []string{"--debug", "--resume", "S1", "prompt", "--model"}, []string{"--debug", "prompt", "--model"}, []string{"--resume", "S1"}},
		{"no selector", []string{"-dc", "-nrocket"}, []string{"-dc", "-nrocket"}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newReuseEnv(t)
			r := e.seedReusable(t, agentGone, reuseRowSpec{Bare: true, Opts: []apitest.SpawnOption{apitest.WithLifeNumber(0)}})
			old := r.Spawn.SessionID
			if old == "" {
				t.Fatal("old Claude session not seeded")
			}
			apitest.SeedJsonlUnder(t, r.Trust.dir, r.CWD, old)
			p := reuseParams(t, r, reuseRequest{})
			p.Agent = "codex"
			if _, _, err := e.reuse(t, p); err != nil {
				t.Fatalf("Claude to Codex: %v", err)
			}
			rlvAssertLife(t, e, r.ID, 1)
			rlfEndLife(t, e, r.ID)
			mark := trailMark(t)
			args, drops := slices.Clone(tc.args), slices.Clone(tc.drops)
			for i, v := range args {
				args[i] = strings.ReplaceAll(v, "S1", old)
			}
			for i, v := range drops {
				drops[i] = strings.ReplaceAll(v, "S1", old)
			}
			p = reuseParams(t, r, reuseRequest{Args: args})
			w := &hookedReuseStore{st: e.st}
			res, _, err := e.reuseWith(t, w, p)
			if err != nil {
				t.Fatalf("Codex to Claude: %v", err)
			}
			if w.readCount() != 1 {
				t.Errorf("reads=%d; want one", w.readCount())
			}
			row := rlfRow(t, e, r.ID)
			if row.Agent != "claude" || row.State != store.StatePending || row.SessionID != "" || !slices.Equal(row.AgentArgs, tc.want) {
				t.Fatalf("fresh row=%+v; want pending Claude, no session, args %q", row, tc.want)
			}
			rlvAssertLife(t, e, r.ID, 2)
			view := rlfGet(t, e, r.ID)
			if len(view.PriorSessions) != 0 || view.TranscriptStatus != "no_session" {
				t.Errorf("fresh view=%+v", view)
			}
			argv := spawnArgsCreate(t, e, r.ID)
			if len(argv) < 3 || argv[0] != "claude" || argv[1] != "--settings" || !slices.Equal(argv[3:], tc.want) {
				t.Errorf("fresh launch=%q; want Claude settings and %q", argv, tc.want)
			}
			spawnArgsResult(t, res, drops)
			spawnArgsTrail(t, mark, r.ID, store.StateEnded, drops)
			latest := uuid.NewString()
			rlfReportIn(t, e, r.ID, latest, true)
			rlfEndLife(t, e, r.ID)
			if _, err := e.resume(r.ID); err != nil {
				t.Fatalf("resume new Claude session: %v", err)
			}
			if got, ok := resumedSession(t, spawnArgsCreate(t, e, r.ID), false); !ok || got != latest || got == old {
				t.Errorf("resumed %q (%v); want new session %q, never old %q", got, ok, latest, old)
			}
		})
	}
}

func TestSpawnReuseCodexSelectorsStartFresh(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name                      string
		args, want, drops, prompt []string
		missing                   bool
	}{
		{"resume id", []string{"resume", "OLD", "hello"}, []string{"--", "hello"}, []string{"resume", "OLD"}, []string{"hello"}, false},
		{"resume last", []string{"--model", "gpt", "resume", "--last", "--all", "--include-non-interactive", "hello"}, []string{"--model", "gpt", "--", "hello"}, []string{"resume", "--last", "--all", "--include-non-interactive"}, []string{"hello"}, false},
		{"fork id", []string{"fork", "OLD", "hello"}, []string{"--", "hello"}, []string{"fork", "OLD"}, []string{"hello"}, false},
		{"fork last", []string{"fork", "--last", "hello"}, []string{"--", "hello"}, []string{"fork", "--last"}, []string{"hello"}, false},
		{"hello after delimiter", []string{"--", "hello", "resume"}, []string{"--", "hello", "resume"}, nil, []string{"hello", "resume"}, false},
		{"missing row resume id", []string{"resume", "OLD", "hello"}, []string{"--", "hello"}, []string{"resume", "OLD"}, []string{"hello"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newReuseEnv(t)
			prior := store.StateEnded
			if tc.missing {
				prior = store.StateMissing
			}
			r := e.seedReusable(t, agentGone, reuseRowSpec{State: prior, Bare: true, NoSessionID: true})
			p := reuseParams(t, r, reuseRequest{Args: tc.args})
			p.Agent = "codex"
			mark := trailMark(t)
			res, _, err := e.reuse(t, p)
			if err != nil {
				t.Fatal(err)
			}
			row := rlfRow(t, e, r.ID)
			if row.Agent != "codex" || row.State != store.StatePending || row.SessionID != "" || !slices.Equal(row.AgentArgs, tc.want) {
				t.Errorf("fresh row=%+v; want %q", row, tc.want)
			}
			launch := codexfix.Split(t, spawnArgsCreate(t, e, r.ID))
			if _, ok := launch.Resume(); ok {
				t.Error("fresh launch resumes a session")
			}
			if !slices.Equal(launch.AfterDashDashArgs(), tc.prompt) {
				t.Errorf("launch prompt=%q; want %q", launch.AfterDashDashArgs(), tc.prompt)
			}
			spawnArgsResult(t, res, tc.drops)
			spawnArgsTrail(t, mark, r.ID, prior, tc.drops)
		})
	}
}

func TestSpawnResultDroppedArgsJSON(t *testing.T) {
	t.Parallel()
	for _, drops := range [][]string{nil, {}, {"-r=S1", "--continue", "OLD token"}} {
		t.Run(string(mustJSON(t, drops)), func(t *testing.T) {
			spawnArgsResult(t, api.SpawnResult{InstanceID: "id", PreTrust: "skipped", DroppedArgs: drops}, drops)
		})
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestSpawnBenignArgumentsRetainMeaning verifies ordinary paths preserve
// option-owned short spellings and do not perform conditional orphan cleanup.
func TestSpawnBenignArgumentsRetainMeaning(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		args []string
	}{
		{"short values", []string{"-dc", "-nrocket", "--model", "-c", "--append-system-prompt", "-concise answers"}},
		{"controls without removed cloud selector", []string{"--ref", "main", "--forward-home-settings", "false"}},
	}
	for _, path := range []string{"plain", "absent", "same"} {
		for _, tc := range cases {
			t.Run(path+"/"+tc.name, func(t *testing.T) {
				t.Parallel()
				e := newReuseEnv(t)
				r := e.seedReusable(t, agentGone, reuseRowSpec{Bare: true, NoSessionID: true})
				p := reuseParams(t, r, reuseRequest{Args: tc.args})
				if path != "same" {
					p.InstanceID = "fresh-" + uuid.NewString()
					p.TmuxSessionName = p.InstanceID
				}
				if path == "plain" {
					p.ReuseFinished = false
				}
				mark := trailMark(t)
				w := &hookedReuseStore{st: e.st}
				res, _, err := e.reuseWith(t, w, p)
				if err != nil {
					t.Fatal(err)
				}
				row := rlfRow(t, e, p.InstanceID)
				if !slices.Equal(row.AgentArgs, tc.args) {
					t.Errorf("stored args=%q; want %q", row.AgentArgs, tc.args)
				}
				argv := spawnArgsCreate(t, e, p.InstanceID)
				if len(argv) < 3 || argv[0] != "claude" || argv[1] != "--settings" || !slices.Equal(argv[3:], tc.args) {
					t.Errorf("launch=%q; want original args %q", argv, tc.args)
				}
				spawnArgsResult(t, res, nil)
				reads := 1
				if path == "plain" {
					reads = 0
				}
				if w.readCount() != reads {
					t.Errorf("reads=%d; want %d", w.readCount(), reads)
				}
				if path == "same" {
					spawnArgsTrail(t, mark, r.ID, store.StateEnded, nil)
				} else if len(ptRecords(t, mark, rutReused, p.InstanceID)) != 0 {
					t.Error("fresh spawn wrote reuse trail event")
				}
			})
		}
	}
}
