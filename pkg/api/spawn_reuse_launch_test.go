package api_test

// spawn_reuse_launch_test.go covers reuse's launch after its reset (SR-3.5,
// SR-3.8, SR-10.4, SR-13.2; AC-LKP-17, AC-REUSE-08, AC-REUSE-16): the identity
// write and its guard, the label step, the create argv through test/fake-tmux
// (labels chained, by id for a $ name, a # doubled, equal to a plain spawn's),
// and the longest paths' virtual time; and the Droid lane's refusals and
// checked Rule Set on its fresh spawns and reuses.

import (
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/spawn"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/droidfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/faketmuxfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// TestPaneOnlyReuseValidationWritesNothing: each request refusal of a lane
// reuse, from the call or an inherited template (SR-1.4's --settings and
// permission entries included), comes before the Rule Set step, so a missing
// Rule Set File never answers instead, and writes nothing.
func TestPaneOnlyReuseValidationWritesNothing(t *testing.T) {
	// Serial: refusal snapshots check the shared trail and temporary HOME.
	for _, agent := range []string{"droid", "opencode"} {
		type refusal struct{ name, want, key string }
		cases := []refusal{
			{"custom model", "ErrSpawnDeniedFlag", ""},
			{"settings", "ErrSpawnDeniedFlag", ""},
			{"inherited settings", "ErrSpawnDeniedFlag", ""},
			{"inherited permissions", "ErrPermissionsUnsupported", ""},
			{"relay", "ErrAgentCapabilityUnsupported", ""},
		}
		keys := []string{"AWS_BEARER_TOKEN_BEDROCK"}
		if agent == "opencode" {
			keys = []string{"OPENROUTER_API_KEY", "OPENCODE_CONFIG", "OPENCODE_CONFIG_DIR", "OPENCODE_CONFIG_CONTENT", "OPENCODE_AUTH_CONTENT", "OPENCODE_PERMISSION"}
		}
		for _, key := range keys {
			cases = append(cases, refusal{"inherited environment", "ErrReservedEnvKey", key})
		}
		for _, tc := range cases {
			t.Run(agent+"/"+tc.name, func(t *testing.T) {
				home := withTempHome(t)
				e := newKillEnv(t)
				r := e.seedReusable(t, agentGone, reuseRowSpec{Opts: []apitest.SpawnOption{apitest.WithAgent(agent)}})
				seedLaneOverlay(t, e.storeID, r.Token)
				c, _ := e.configClient(t, droidRulesConfig(t, filepath.Join(t.TempDir(), "missing-rules.json"), "", agent))
				t.Cleanup(spawn.SetDroidGuardProgramForTest(func() (string, error) { return "", errors.New("validation must precede Guard selection") }))
				p := api.SpawnParams{Agent: agent, CWD: r.CWD, InstanceID: r.ID, ReuseFinished: true, AgentArgs: paneOnlyArgs(agent, "model")}
				settings := append(paneOnlyArgs(agent, "model"), "--settings", "private-lane-sentinel")
				var tmpl *apitest.TemplateFields
				switch tc.name {
				case "custom model":
					p.AgentArgs = []string{"--model", "custom:private-lane-sentinel"}
				case "settings":
					p.AgentArgs = settings
				case "relay":
					p.RelayMode = "on"
				case "inherited environment":
					tmpl = &apitest.TemplateFields{Agent: agent, ExtraEnv: map[string]string{tc.key: "private-lane-sentinel"}}
				case "inherited settings":
					p.AgentArgs, tmpl = nil, &apitest.TemplateFields{Agent: agent, AgentArgs: settings}
				case "inherited permissions":
					tmpl = &apitest.TemplateFields{Agent: agent, Permissions: &config.TemplatePermissions{Ask: []string{"Bash(private-lane-sentinel)"}}}
				}
				if tmpl != nil {
					p.Template = "lane"
					if _, err := apitest.SeedTemplate(filepath.Join(adConfigDir(home), "templates"), p.Template, apitest.TemplateBody(*tmpl)); err != nil {
						t.Fatal(err)
					}
				}
				roots := []string{filepath.Join(adConfigDir(home), "launch"), filepath.Join(adConfigDir(home), "templates")}
				files, before := snapshotTrees(t, roots), e.snapshotReuse(t, r)
				_, err := c.Spawn(p)
				assertOneName(t, err, tc.want)
				if strings.Contains(errText(err), "private-lane-sentinel") {
					t.Errorf("refusal exposed a rejected value: %v", err)
				}
				e.assertWroteNothing(t, before)
				assertNoTmuxCalls(t, e.rec)
				if len(readAPITrailLines(t)) != before.mark {
					t.Error("validation refusal wrote trail")
				}
				if diff := files.diff(snapshotTrees(t, roots)); len(diff) != 0 {
					t.Errorf("refusal changed launch/template files: %v", diff)
				}
			})
		}
	}
}

// droidRulesRefusalEnd closes every ErrPermissionRulesRefused description.
const droidRulesRefusalEnd = "; an operator must fix the Rule Set File [launch] droid_rules_path names, or the droid program (see docs/experimental.md#droid-rules); no row, session, trust entry or launch file was written and nothing was launched"

// launcherRuleSet is a Rule Set File agent-director refuses at rules[1], an
// allow rule for a versioned interpreter, holding sentinel as its id, test
// and reason.
func launcherRuleSet(sentinel string) string {
	return `{"version":1,"rules":[{"id":"tail","decision":"allow","match":{"prefix":["tail"]},"tests":{"match":["tail x"],"noMatch":["head x"]}},` +
		`{"id":"` + sentinel + `","decision":"allow","match":{"prefix":["/usr/bin/python3.11"]},"tests":{"match":["` + sentinel + `"],"noMatch":["x"]},"reason":"` + sentinel + `"}]}`
}

// TestDroidRulesRefusalWritesNothing (SR-2.1, SR-2.5): a Rule Set refused by
// agent-director's own check or by Droid's refuses a fresh Droid spawn and a
// finished-ID reuse with ErrPermissionRulesRefused before any launch-resource
// write: no row or reset, launch file, trust entry or session. The shared tmux
// socket directory is outside this guarantee. It also checks no trail record
// (reuse's lookup read aside); the reused row's history, requests and old
// overlay stay; and neither the error nor the log holds the rules or Droid's output.
func TestDroidRulesRefusalWritesNothing(t *testing.T) {
	// Serial: it sets HOME and checks every record written to the shared trail since its mark.
	const sentinel = "private-rules-sentinel"
	for _, operation := range []string{"spawn", "reuse"} {
		for _, failure := range []string{"own check", "Droid's check"} {
			t.Run(operation+"/"+failure, func(t *testing.T) {
				fake := droidfix.New(t, droidfix.Pass)
				rules, kind, calls := launcherRuleSet(sentinel), "rules[1] is an allow rule whose program is not tail, head or an absolute path to a program other than a shell, interpreter or command runner", 0
				if failure == "Droid's check" {
					fake.SetMode(t, droidfix.Fail)
					rules, kind, calls = droidfix.RuleSet(sentinel), "Droid's rules check failed with a nonzero exit status (a rule is invalid or one of its tests fails)", 1
				}
				rulesPath := filepath.Join(t.TempDir(), "rules.json")
				if err := os.WriteFile(rulesPath, []byte(rules), 0600); err != nil {
					t.Fatal(err)
				}
				body := droidRulesConfig(t, rulesPath, fake.Path, "droid")
				var err error
				if operation == "spawn" {
					e := newSpawnEnv(t)
					// The launch resolves tmux's per-user socket directory before the
					// Rule Set step; as after any earlier launch, it already exists.
					if err := os.MkdirAll(filepath.Dir(e.socket), 0700); err != nil {
						t.Fatal(err)
					}
					c := agentClientWithConfig(t, e, body)
					err = e.checkCreationWrites(t, func() error {
						_, err := c.Spawn(api.SpawnParams{Agent: "droid", CWD: t.TempDir(), AgentArgs: paneOnlyArgs("droid", "model")})
						return err
					})
				} else {
					home := withTempHome(t)
					e := newKillEnv(t)
					r := e.seedReusable(t, agentGone, reuseRowSpec{Opts: []apitest.SpawnOption{apitest.WithAgent("droid")}})
					old := seedLaneOverlay(t, e.storeID, r.Token)
					c, logs := e.configClient(t, body)
					p := reuseParams(t, r, reuseRequest{Args: paneOnlyArgs("droid", "model")})
					p.Agent = "droid"
					roots := []string{filepath.Join(adConfigDir(home), "launch"), filepath.Join(adConfigDir(home), "templates")}
					files, before := snapshotTrees(t, roots), e.snapshotReuse(t, r)
					_, err = c.Spawn(p)
					e.assertWroteNothing(t, before)
					if diff := files.diff(snapshotTrees(t, roots)); len(diff) != 0 {
						t.Errorf("refusal changed launch/template files: %v", diff)
					}
					assertLaneOverlay(t, old, true)
					if strings.Contains(logs.String(), sentinel) || strings.Contains(logs.String(), droidfix.FailOutput) {
						t.Errorf("log holds the rules or Droid's output: %q", logs.String())
					}
				}
				assertOneName(t, err, "ErrPermissionRulesRefused")
				if want := "ErrPermissionRulesRefused: Droid Rule Set refused: " + kind + droidRulesRefusalEnd; errText(err) != want {
					t.Errorf("refusal = %q\nwant %q", errText(err), want)
				}
				if n := len(fake.Calls(t)); n != calls {
					t.Errorf("Droid's check ran %d times; want %d", n, calls)
				}
			})
		}
	}
}

// TestDroidRulesReadAtEachLaunch (SR-1.2 as MI decision PM-1 reads it,
// SR-2.4, SR-3.2): one long-lived Client reads the Rule Set File afresh at
// each Droid launch. A fresh spawn and a finished-ID reuse each run the
// checked program with an overlay holding byte for byte what Droid's check
// received, its IDs under that launch's own token; a change to the file
// after the check has read it does not reach that launch.
func TestDroidRulesReadAtEachLaunch(t *testing.T) {
	// Serial: it sets HOME with t.Setenv.
	withTempHome(t)
	e := newKillEnv(t)
	fake := droidfix.New(t, droidfix.Pass)
	rulesPath := filepath.Join(t.TempDir(), "rules.json")
	writeRules := func(content string) {
		t.Helper()
		if err := os.WriteFile(rulesPath, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	c, _ := e.configClient(t, droidRulesConfig(t, rulesPath, fake.Path, "droid"))
	tokens := map[string]bool{}
	// launched checks launch n of row id, whose operator rule id is operatorID.
	launched := func(id string, n int, operatorID string) {
		t.Helper()
		token, _ := e.columns(t, id).LaunchToken.(string)
		calls, creates := fake.Calls(t), e.rec.SocketCallsOf(tmux.CallCreate)
		if len(calls) != n+1 || len(creates) != n+1 {
			t.Fatalf("launch %d: %d checks, %d creates", n, len(calls), len(creates))
		}
		input, command := string(calls[n].Input), creates[n].Command
		if len(command) != 3 || command[0] != fake.Path || command[1] != "--settings" {
			t.Fatalf("launch %d command %q; want the checked program with its overlay", n, command)
		}
		overlay, err := os.ReadFile(command[2])
		if err != nil {
			t.Fatal(err)
		}
		assertDroidOverlay(t, overlay, "model", input)
		if !strings.Contains(input, `"id":"agent-director/`+token+`/`+operatorID+`"`) || tokens[token] {
			t.Fatalf("launch %d checked %s; want rule %s under its own new token %s", n, input, operatorID, token)
		}
		tokens[token] = true
	}

	writeRules(droidfix.RuleSet("first"))
	p := api.SpawnParams{Agent: "droid", CWD: t.TempDir(), AgentArgs: paneOnlyArgs("droid", "model")}
	res, err := c.Spawn(p)
	if err != nil {
		t.Fatal(err)
	}
	launched(res.InstanceID, 0, "first")

	first, err := e.st.GetSpawn(res.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	for _, session := range e.rec.Sessions(first.Identity.Socket) {
		if err := e.rec.KillSessionID(first.Identity.Socket, session.ID); err != nil {
			t.Fatal(err)
		}
	}
	if _, applied, err := e.st.MarkMissingIfSameLife(first.InstanceID, first.Snapshot); err != nil || applied != store.CondApplied {
		t.Fatalf("finish first launch: %v, %v", applied, err)
	}
	writeRules(droidfix.RuleSet("second"))
	p.InstanceID, p.ReuseFinished = res.InstanceID, true
	if _, err := c.Spawn(p); err != nil {
		t.Fatal(err)
	}
	launched(res.InstanceID, 1, "second")

	// The check holds while the file changes to one agent-director would refuse.
	writeRules(droidfix.RuleSet("third"))
	fake.SetMode(t, droidfix.Hold)
	type outcome struct {
		res api.SpawnResult
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		res, err := c.Spawn(api.SpawnParams{Agent: "droid", CWD: p.CWD, AgentArgs: p.AgentArgs})
		done <- outcome{res, err}
	}()
	select {
	case <-fake.Held():
	case o := <-done:
		t.Fatalf("spawn ended before its check held: %+v, %v", o.res, o.err)
	}
	writeRules("not a rules object")
	fake.Release(t)
	third := <-done
	if third.err != nil {
		t.Fatal(third.err)
	}
	launched(third.res.InstanceID, 2, "third")
}

func TestPaneOnlyFilesBeforeStoreWrite(t *testing.T) {
	// Serial: it owns HOME and observes writes on the fixture clock.
	for _, agent := range []string{"droid"} {
		for _, operation := range []string{"insert", "insert collision", "insert preparation error", "reset", "reset changed", "reset absent", "reset error", "reset preparation error"} {
			t.Run(agent+"/"+operation, func(t *testing.T) {
				t.Setenv("HOME", t.TempDir())
				e := newReuseEnv(t)
				c, _ := e.paneOnlyClient(t, agent)
				p := api.SpawnParams{Agent: agent, InstanceID: "lane-" + uuid.NewString()[:8], CWD: t.TempDir(), AgentArgs: []string{"--model", "model"}}
				w := &hookedReuseStore{st: e.st}
				var before apitest.SpawnColumns
				oldPath := ""
				isReset := strings.HasPrefix(operation, "reset")
				if isReset {
					r := e.seedReusable(t, agentGone, reuseRowSpec{Age: time.Hour, Bare: true, Opts: agentRowOpts(agent)})
					p.InstanceID, p.ReuseFinished = r.ID, true
					before = e.columns(t, r.ID)
					oldPath = seedLaneOverlay(t, e.storeID, r.Token)
				}
				if strings.HasSuffix(operation, "preparation error") {
					dir := filepath.Join(os.Getenv("HOME"), ".config", "agent-director", "launch", e.storeID)
					if err := os.MkdirAll(dir, 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.Chmod(dir, 0500); err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = os.Chmod(dir, 0700) })
					_, err := api.SpawnWithReuseStore(c, w, p)
					assertOneName(t, err, "ErrTmuxSessionCreate")
					if isReset {
						e.assertRowUnchanged(t, p.InstanceID, before)
						assertLaneOverlay(t, oldPath, true)
					} else if _, err := e.st.GetSpawn(p.InstanceID); !errors.Is(err, store.ErrSpawnNotFound) {
						t.Fatalf("preparation failure wrote row: %v", err)
					}
					if len(e.rec.SocketCallsOf(tmux.CallCreate)) != 0 {
						t.Fatal("failed preparation created session")
					}
					return
				}
				newPath := ""
				observe := func() {
					files, err := filepath.Glob(filepath.Join(os.Getenv("HOME"), ".config", "agent-director", "launch", e.storeID, "*", "droid-settings.json"))
					if err != nil {
						t.Fatal(err)
					}
					for _, path := range files {
						if path != oldPath {
							newPath = path
						}
					}
					if newPath == "" {
						t.Fatal("overlay absent before store write")
					}
					if isReset {
						e.assertRowUnchanged(t, p.InstanceID, before)
					} else if _, err := e.st.GetSpawn(p.InstanceID); !errors.Is(err, store.ErrSpawnNotFound) {
						t.Fatalf("row existed before insert: %v", err)
					}
					if len(e.rec.SocketCallsOf(tmux.CallCreate)) != 0 {
						t.Fatal("create preceded preparation/store write")
					}
					switch operation {
					case "insert collision":
						if _, err := apitest.SeedSpawn(e.dbPath, p.InstanceID, store.StateWaiting, p.CWD, "off", "", false); err != nil {
							t.Fatal(err)
						}
					case "reset changed":
						if err := e.st.SetParentID(p.InstanceID, ""); err != nil {
							t.Fatal(err)
						}
					case "reset absent":
						if err := e.st.DeleteSpawn(p.InstanceID); err != nil {
							t.Fatal(err)
						}
					case "reset error":
						if err := e.st.Close(); err != nil {
							t.Fatal(err)
						}
					}
				}
				if isReset {
					w.beforeReset(observe)
				} else {
					api.SetClockForTest(c, func() time.Time {
						if newPath == "" {
							observe()
						}
						return e.clock.Now()
					})
				}
				_, err := api.SpawnWithReuseStore(c, w, p)
				ok := operation == "insert" || operation == "reset"
				if operation == "reset error" {
					assertOneName(t, err, "ErrInternal")
				} else if ok && err != nil || !ok && !errors.Is(err, spawn.ErrInstanceIdCollision) {
					t.Fatalf("Spawn=%v", err)
				}
				if newPath == "" {
					t.Fatal("store boundary not reached")
				}
				assertLaneOverlay(t, newPath, ok)
				if oldPath != "" {
					assertLaneOverlay(t, oldPath, !ok)
				}
				if !ok && len(e.rec.SocketCallsOf(tmux.CallCreate)) != 0 {
					t.Fatal("failed write created session")
				}
			})
		}
	}
}

func TestPaneOnlyReuseReclaimGuards(t *testing.T) {
	// Serial: each case owns HOME for its per-launch files.
	for _, agent := range []string{"droid"} {
		for _, mode := range []string{"gone", "unknown", "alive", "ours", "leftover", "changed to alive", "changed to unknown", "reclaim failure"} {
			t.Run(agent+"/"+mode, func(t *testing.T) {
				t.Setenv("HOME", t.TempDir())
				e := newReuseEnv(t)
				a := agentGone
				if mode == "unknown" {
					a = agentUnreadable
				}
				if mode == "alive" {
					a = agentAlive
				}
				r := e.seedReusable(t, a, reuseRowSpec{Age: time.Hour, Bare: true, Opts: agentRowOpts(agent)})
				path := seedLaneOverlay(t, e.storeID, r.Token)
				switch mode {
				case "ours":
					e.seedSession(t, &r.killRow, e.createdBefore(time.Hour))
				case "leftover":
					e.seedSession(t, &r.killRow, tmuxfix.WithRowSessionLabel(r.old(), true))
				case "changed to alive", "changed to unknown":
					e.rec.AfterCall(tmux.CallCreate, func(tmuxfix.SocketCall, error) {
						p := procfix.Unreadable()
						if mode == "changed to alive" {
							p = procfix.Alive(r.AgentStart)
						}
						e.pc.Set(r.AgentPID, p)
					})
				case "reclaim failure":
					if err := os.Chmod(filepath.Dir(path), 0500); err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = os.Chmod(filepath.Dir(path), 0700) })
				}
				c, logs := e.paneOnlyClient(t, agent)
				_, err := c.Spawn(api.SpawnParams{Agent: agent, InstanceID: r.ID, ReuseFinished: true, CWD: r.CWD, AgentArgs: []string{"--model", "model"}})
				refused := mode == "ours" || mode == "leftover" || mode == "alive"
				if refused {
					assertOneName(t, err, "ErrTmuxSessionConflict")
				} else if err != nil {
					t.Fatal(err)
				}
				assertLaneOverlay(t, path, mode != "gone")
				if mode == "reclaim failure" && !strings.Contains(logs.String(), "WARN") {
					t.Fatalf("no reclaim warning: %q", logs.String())
				}
			})
		}
	}
}

// rlNewPanesAlive puts the panes of every session a create leaves under its
// name in e's fake, alive with apitest.DarwinProcStarttime.
func rlNewPanesAlive(e *killEnv) {
	e.rec.AfterCall(tmux.CallCreate, func(c tmuxfix.SocketCall, _ error) {
		for _, s := range e.rec.Sessions(c.Socket) {
			if s.Name != storedFormOf(c.Target) {
				continue
			}
			for _, p := range s.Panes {
				e.pc.Set(p.PID, procfix.Alive(apitest.DarwinProcStarttime))
			}
		}
	})
}

// rlOneCreate returns the one recorded create, failing unless the calls were
// exactly want and every one was on socket.
func rlOneCreate(t *testing.T, e *killEnv, socket string, want ...tmux.Call) tmuxfix.SocketCall {
	t.Helper()
	if got := callKinds(e.rec); !reflect.DeepEqual(got, want) || len(e.rec.Calls()) != 0 {
		t.Fatalf("tmux calls = %v (+%d name-based); want %v", got, len(e.rec.Calls()), want)
	}
	for _, c := range e.rec.SocketCalls() {
		if c.Socket != socket {
			t.Errorf("%v on socket %q; want the row's %q", c.Call, c.Socket, socket)
		}
	}
	return e.rec.SocketCallsOf(tmux.CallCreate)[0]
}

// rlAssertNewLife fails unless cols is the reset's new launch in the retained life
// from launchStart with token tok: pending, the retained life, a new token, no session
// id, pid or ended_at.
func rlAssertNewLife(t *testing.T, r reuseRow, cols apitest.SpawnColumns, tok string, launchStart time.Time) {
	t.Helper()
	if !spawnTokenRE.MatchString(tok) || tok == r.Token {
		t.Errorf("create token %q; want a new 16-hex token (old %q)", tok, r.Token)
	}
	if cols.State != store.StatePending || cols.LifeNumber != reuseLife || cols.LaunchToken != tok ||
		cols.LaunchStartedAt != launchStart.UnixMilli() || cols.SessionID != nil || cols.PID != nil || cols.EndedAt != nil {
		t.Errorf("row {state %v, life %v, token %v, launch start %v, session %v, pid %v, ended_at %v}; want pending, %d, %s, %d, all NULL",
			cols.State, cols.LifeNumber, cols.LaunchToken, cols.LaunchStartedAt, cols.SessionID, cols.PID, cols.EndedAt,
			reuseLife, tok, launchStart.UnixMilli())
	}
}

// TestSpawnReuseIdentityWrite: the labelled create (session and pane labels)
// is recorded by the identity write, the reset's version plus one, only while
// the row is still this launch's; another versioned write first leaves it
// writing nothing, and a hook first is ignored (SR-22.9).
func TestSpawnReuseIdentityWrite(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		setup    func(t *testing.T, e *killEnv, r reuseRow) (parent any)
		recorded bool
	}{
		{"labelled create", func(*testing.T, *killEnv, reuseRow) any { return nil }, true},
		{"another write between the create and the identity write", func(t *testing.T, e *killEnv, r reuseRow) any {
			e.rec.AfterCall(tmux.CallCreate, func(tmuxfix.SocketCall, error) {
				if err := e.st.SetParentID(r.ID, r.ParentID); err != nil {
					t.Errorf("SetParentID: %v", err)
				}
			})
			return r.ParentID
		}, false},
		// The reset row records no pane until the identity write, so the
		// store's gated hook writes are ignored and the write applies.
		{"hooks between the create and the identity write ignored", func(t *testing.T, e *killEnv, r reuseRow) any {
			e.rec.AfterCall(tmux.CallCreate, func(tmuxfix.SocketCall, error) {
				for _, ev := range []string{"Stop", "SessionStart"} {
					got := apitest.ApplyAgentHook(t, e.dbPath, r.ID, ev, "sess-new-"+uuid.NewString()[:8])
					if got != (store.HookApplied{Reason: store.HookReasonNoPaneRecorded}) {
						t.Errorf("%s before the identity write = %+v; want ignored, %s", ev, got, store.HookReasonNoPaneRecorded)
					}
				}
			})
			return nil
		}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			r := e.seedReusable(t, agentGone, reuseRowSpec{Age: time.Hour})
			rlNewPanesAlive(e)
			wantParent := tc.setup(t, e, r)
			before, start := e.columns(t, r.ID), e.clock.Now().Add(boundQ)

			if _, logs, err := e.reuse(t, reuseParams(t, r, reuseRequest{})); err != nil || strings.Contains(logs, "WARN") ||
				(tc.recorded && logs != "") {
				t.Fatalf("reuse = %v (log %q); want success, no WARN, no log at all once recorded", err, logs)
			}
			c := rlOneCreate(t, e, r.Socket, tmux.CallLookup, tmux.CallCreate)
			s := rplSessionNamed(t, e.rec, r.Socket, r.Name)
			if s.Label != tmuxfix.Valid(c.Token, r.ID, e.storeID) || s.Panes[0].AdPane != c.Token {
				t.Errorf("session label %+v, pane label %q; want ad1 %s <$N> %s <store id>, pane %s", s.Label, s.Panes[0].AdPane,
					c.Token, r.ID, c.Token)
			}
			cols := e.columns(t, r.ID)
			rlAssertNewLife(t, r, cols, c.Token, start)
			want := noIdentity
			if tc.recorded {
				srv, _ := e.rec.Server(r.Socket)
				want = []any{int64(srv.PID), srv.Start, srv.ProcStart, s.Panes[0].ID, int64(s.Panes[0].PID), apitest.DarwinProcStarttime}
				if cols.RowVersion != before.RowVersion.(int64)+2 {
					t.Errorf("row_version %v; want the reset's and the identity write's, %d", cols.RowVersion, before.RowVersion.(int64)+2)
				}
			}
			if got := identityCols(cols); !reflect.DeepEqual(got, want) {
				t.Errorf("identity columns = %#v; want %#v", got, want)
			}
			if cols.ParentID != wantParent {
				t.Errorf("parent_id = %v; want %v", cols.ParentID, wantParent)
			}
		})
	}
}

// TestSpawnReuseLabelStep: a failed chained label is relabelled by id and the reuse succeeds; when the
// relabel fails too, the session is killed by id (a failed kill leaves it), ErrTmuxSessionCreate says so,
// and the restore runs (a successful kill's: TestSpawnReuseRestoreAfterEachLaunchFailure).
func TestSpawnReuseLabelStep(t *testing.T) {
	// Serial: installs the shared HOME's gateway before entering the parallel phase.
	installCursorFixtureGateway(t) // Before parallel execution: composition fixture only.
	t.Parallel()
	lookup, create, label, kill := tmux.CallLookup, tmux.CallCreate, tmux.CallSetLabel, tmux.CallKillSession
	cases := []struct {
		name                string
		agent               string
		labelFail, killFail bool
		want                []tmux.Call
	}{
		{"relabel by id succeeds", "claude", false, false, []tmux.Call{lookup, create, label}},
		{"relabel fails, kill by id fails", "claude", true, true, []tmux.Call{lookup, create, label, kill}},
		{"Cursor relabel by id succeeds", "cursor", false, false, []tmux.Call{lookup, create, label}},
		{"Cursor relabel fails, kill by id fails", "cursor", true, true, []tmux.Call{lookup, create, label, kill}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newKillEnv(t)
			r := e.seedReusable(t, agentGone, reuseRowSpec{Age: time.Hour})
			e.rec.Script(r.Socket, tmuxfix.Script{Failure: tmux.FailLabel, Times: 1}, create)
			if tc.labelFail {
				e.rec.Script(r.Socket, tmuxfix.Script{Failure: tmux.FailUnrecognized, ExitStatus: 1}, label)
			}
			if tc.killFail {
				e.rec.Script(r.Socket, tmuxfix.Script{Failure: tmux.FailUnrecognized, ExitStatus: 1}, kill)
			}
			rlNewPanesAlive(e)
			var created tmuxfix.SeedSession
			e.rec.AfterCall(create, func(c tmuxfix.SocketCall, _ error) { created = rplSessionNamed(t, e.rec, c.Socket, r.Name) })
			before := e.columns(t, r.ID)

			p := reuseParams(t, r, reuseRequest{})
			p.Agent = tc.agent
			_, _, err := e.reuse(t, p)
			c := rlOneCreate(t, e, r.Socket, tc.want...)
			l := e.rec.SocketCallsOf(label)[0]
			if l.Target != created.ID || l.PaneID != created.Panes[0].ID || l.Token != c.Token || l.InstanceID != r.ID || l.StoreID != e.storeID {
				t.Errorf("label by id = %+v; want session %s pane %s with token %s, id %s, store %s",
					l, created.ID, created.Panes[0].ID, c.Token, r.ID, e.storeID)
			}
			cols := e.columns(t, r.ID)
			restored := pendTrail(t, rutRestored, r.ID)
			if !tc.labelFail {
				if err != nil {
					t.Fatalf("reuse: %v", err)
				}
				s := rplSessionNamed(t, e.rec, r.Socket, r.Name)
				if s.Label != tmuxfix.Valid(c.Token, r.ID, e.storeID) || s.Panes[0].AdPane != c.Token {
					t.Errorf("session %+v; want ad1 %s %s %s <store id> with pane label", s, c.Token, created.ID, r.ID)
				}
				if cols.State != store.StatePending || cols.PaneID != created.Panes[0].ID ||
					cols.RowVersion != before.RowVersion.(int64)+2 || len(restored) != 0 {
					t.Errorf("row {state %v, pane %v, row_version %v}, restores %v; want pending, %s, reset + identity write, none",
						cols.State, cols.PaneID, cols.RowVersion, restored, created.Panes[0].ID)
				}
				return
			}
			assertLaunchSentinel(t, err, tmux.ErrTmuxSessionCreate)
			if k := e.rec.SocketCallsOf(kill)[0]; k.Target != created.ID {
				t.Errorf("kill targets %q; want the session id %q", k.Target, created.ID)
			}
			present := slices.ContainsFunc(e.rec.Sessions(r.Socket), func(s tmuxfix.SeedSession) bool { return s.ID == created.ID })
			if present != tc.killFail {
				t.Errorf("unlabelled session %s present %v; want present only when its kill failed", created.ID, present)
			}
			apitest.AssertDescription(t, err.Error(), apitest.DescUnlabelledSession(apitest.UnlabelledSession{
				Name: r.Name, SessionID: created.ID, Ended: !tc.killFail,
				Restore: apitest.ResumeRestore{Outcome: apitest.RestoreApplied, PriorState: store.StateEnded, Launch: apitest.LaunchReuse},
			}), c.Token, e.storeID, tmuxfix.LabelValue(c.Token, created.ID, r.ID, e.storeID))
			if cols.State != store.StateEnded || len(restored) != 1 {
				t.Errorf("state %v, %s %v; want the restore run once", cols.State, rutRestored, restored)
			}
		})
	}
}

// rlFakeServer gives r's socket in test/fake-tmux the row's recorded server
// holding one unlabelled bystander session, as seedReusable leaves the
// Recorder.
func (e *killEnv) rlFakeServer(t *testing.T, r reuseRow) {
	t.Helper()
	srv, _ := e.rec.Server(r.Socket)
	(faketmuxfix.Tables{}).Write(t, r.Socket, faketmuxfix.Table{Server: &faketmuxfix.Server{PID: srv.PID, Start: srv.Start},
		Sessions: []faketmuxfix.Session{{ID: "$0", Created: srv.Start, Name: "bystander",
			Panes: []faketmuxfix.Pane{{ID: "%0", PID: e.newPID()}}}}})
}

// rlFakeTmuxClient is a Client on e's store driving test/fake-tmux, r's
// socket holding its server (rlFakeServer), with e's clock and process fake;
// it returns the fake's argv log.
func (e *killEnv) rlFakeTmuxClient(t *testing.T, r reuseRow) (*api.Client, string) {
	t.Helper()
	e.rlFakeServer(t, r)
	logPath := filepath.Join(t.TempDir(), "fake-tmux.log")
	t.Setenv(faketmuxfix.EnvLog, logPath)
	cfgPath := filepath.Join(t.TempDir(), "config.toml")
	apitest.WriteTmuxConfig(t, cfgPath)
	c, err := api.New(api.Options{StorePath: e.dbPath, ConfigPath: cfgPath, TmuxCommand: faketmuxfix.Binary(t),
		Logger: log.New(io.Discard, "", 0)})
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	api.SetClockForTest(c, e.clock.Now)
	api.SetProcCheckerForTest(c, e.pc)
	return c, logPath
}

// rlSocketArgvs returns the logged invocations that name a socket, in order.
func rlSocketArgvs(t *testing.T, logPath string) [][]string {
	t.Helper()
	var out [][]string
	for _, a := range fakeTmuxArgvs(t, logPath) {
		if len(a) > 2 && a[1] == "-S" {
			out = append(out, a)
		}
	}
	return out
}

// rlEnvEntries counts argv's elements that start with prefix.
func rlEnvEntries(argv []string, prefix string) int {
	n := 0
	for _, a := range argv {
		if strings.HasPrefix(a, prefix) {
			n++
		}
	}
	return n
}

// TestSpawnReuseCreateArgv: after the lookup on -S <socket>, one create there chains both labels
// (a # in the id doubled); a $ name (as a \ one) gets no chain and one label by id; the id's env entry appears once.
func TestSpawnReuseCreateArgv(t *testing.T) {
	// Serial: it sets test/fake-tmux's log variable, TMUX_TMPDIR with t.Setenv.
	cases := []struct{ name, id, session string }{
		{"plain name", "", ""},
		{"dollar name", "", `a$b`},
		{"# in the id", "reuse#x##" + uuid.NewString()[:8], ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			e.ownSocketDir(t) // test/fake-tmux keeps its table beside the socket
			r := e.seedReusable(t, agentGone, reuseRowSpec{ID: tc.id, Age: time.Hour, Bare: true})
			name := tc.session
			if name == "" {
				name = "argv-" + uuid.NewString()[:8]
			}
			c, logPath := e.rlFakeTmuxClient(t, r)
			if _, err := c.Spawn(reuseParams(t, r, reuseRequest{Name: name})); err != nil {
				t.Fatalf("reuse: %v", err)
			}
			tok, _ := e.columns(t, r.ID).LaunchToken.(string)
			if !spawnTokenRE.MatchString(tok) || tok == r.Token {
				t.Fatalf("launch_token = %q; want a new 16-hex token (old %q)", tok, r.Token)
			}

			argvs, byID := rlSocketArgvs(t, logPath), tmux.NeedsLabelByID(name)
			if want := map[bool]int{false: 2, true: 3}[byID]; len(argvs) != want {
				t.Fatalf("socket invocations = %q; want %d", argvs, want)
			}
			if !isLookupOn(argvs[0], r.Socket) {
				t.Errorf("first socket invocation %q; want the lookup on -S %s", argvs[0], r.Socket)
			}
			create, target := argvs[1], "="+name+":"
			if !containsRun(create, []string{"-u", "-S", r.Socket, "new-session"}) || !containsRun(create, []string{"-s", name}) {
				t.Errorf("create argv %q; want new-session -s %q on -u -S %s", create, name, r.Socket)
			}
			if dash := slices.Index(create, "--"); dash < 0 || len(create)-dash-1 < 2 || create[dash+1] != "claude" {
				t.Errorf("create argv %q: want claude and at least one more element after -- (SR-3.8)", create)
			}
			if n := rlEnvEntries(create, "AGENT_DIRECTOR_INSTANCE_ID="+r.ID); n != 1 || rlEnvEntries(create, "AGENT_DIRECTOR_INSTANCE_ID=") != 1 ||
				strings.Contains(strings.Join(create, "\n"), "AGENT_DIRECTOR_LAUNCH_STARTED_AT") {
				t.Errorf("create argv %q: want AGENT_DIRECTOR_INSTANCE_ID=%s exactly once and no launch start", create, r.ID)
			}
			if !byID {
				for _, seq := range [][]string{
					{";", "set-option", "-F", "-t", target, "@ad_owner", tmuxfix.ChainLabelValue(tok, r.ID, e.storeID)},
					{";", "set-option", "-p", "-F", "-t", target, "@ad_pane", tmuxfix.ChainPaneLabelValue(tok)},
				} {
					if !containsRun(create, seq) {
						t.Errorf("create argv %q lacks the chained %q", create, seq)
					}
				}
				return
			}
			if slices.Contains(create, "@ad_owner") || slices.Contains(create, "@ad_pane") {
				t.Errorf("create argv %q chains a label for %q; want none", create, name)
			}
			label, sid := argvs[2], ""
			if i := slices.Index(label, "-t"); i > 0 && i+1 < len(label) {
				sid = label[i+1]
			}
			var paneID string
			for _, s := range (faketmuxfix.Tables{}).Read(t, r.Socket).Sessions {
				if s.ID == sid && len(s.Panes) > 0 {
					paneID = s.Panes[0].ID
				}
			}
			for _, seq := range [][]string{
				{"-S", r.Socket, "set-option", "-t", sid, "@ad_owner", tmuxfix.LabelValue(tok, sid, r.ID, e.storeID)},
				{"set-option", "-p", "-t", paneID, "@ad_pane", tmuxfix.PaneLabelValue(tok, paneID)},
			} {
				if !strings.HasPrefix(sid, "$") || paneID == "" || !containsRun(label, seq) {
					t.Errorf("label-by-id argv %q lacks %q", label, seq)
				}
			}
		})
	}
}

// TestSpawnReuseCreateMatchesPlainSpawn: reuse's create argv (settings, environment and command) equals a
// plain spawn's for the same request, but for the id and token, with AGENT_DIRECTOR_INSTANCE_ID once.
func TestSpawnReuseCreateMatchesPlainSpawn(t *testing.T) {
	// Serial: it sets test/fake-tmux's log variable, TMUX_TMPDIR with t.Setenv.
	e := newKillEnv(t)
	e.ownSocketDir(t) // test/fake-tmux keeps its table beside the socket
	r := e.seedReusable(t, agentGone, reuseRowSpec{Age: time.Hour})
	c, logPath := e.rlFakeTmuxClient(t, r)
	q := reuseRequest{Name: "parity-" + uuid.NewString()[:8], Args: []string{"--model", "sonnet"},
		Env: map[string]string{"PARITY_VAR": "on"}, Labels: map[string]string{"team": "parity"}}
	plain := reuseParams(t, r, q)
	plain.ReuseFinished, plain.InstanceID = false, "plain-"+uuid.NewString()[:8]
	if _, err := c.Spawn(plain); err != nil {
		t.Fatalf("plain spawn: %v", err)
	}
	e.rlFakeServer(t, r) // the plain spawn's session gone
	if _, err := c.Spawn(reuseParams(t, r, q)); err != nil {
		t.Fatalf("reuse: %v", err)
	}

	var creates [][]string
	for _, a := range rlSocketArgvs(t, logPath) {
		if slices.Contains(a, "new-session") {
			creates = append(creates, a)
		}
	}
	if len(creates) != 2 {
		t.Fatalf("creates = %q; want the plain spawn's, then the reuse's", creates)
	}
	// normalised is argv with id's and its launch token's text replaced.
	normalised := func(argv []string, id string) []string {
		tok, _ := e.columns(t, id).LaunchToken.(string)
		out := make([]string, len(argv))
		for i, a := range argv {
			out[i] = strings.NewReplacer(id, "<id>", tok, "<token>").Replace(a)
		}
		return out
	}
	if got, want := normalised(creates[1], r.ID), normalised(creates[0], plain.InstanceID); !slices.Equal(got, want) {
		t.Errorf("reuse's create argv\n  %q\nwant the plain spawn's\n  %q", got, want)
	}
}

// TestSpawnReuseCeilingPaths (SR-13.2; AC-REUSE-16): in virtual time with no
// pipe-close wait, path (i) (lookup, a create whose chained label fails,
// then the label and the kill by id failing) charges Q + C + 2A, 10.5 s at
// the defaults; path (ii) (lookup, create answering "duplicate session",
// re-lookup) charges 2Q + C, 8 s, the longer once Q is raised above 2A.
func TestSpawnReuseCeilingPaths(t *testing.T) {
	t.Parallel()
	q, a, _ := ceilDefaults()
	c := config.Default().Tmux.EffectiveCreateTimeout()
	if got := q + c + 2*a; got != 10500*time.Millisecond || got > boundCap {
		t.Fatalf("Q + C + 2A at the defaults = %v; the Epic says 10.5 s, within %v", got, boundCap)
	}
	if got := 2*q + c; got != 8*time.Second || got > boundCap {
		t.Fatalf("2Q + C at the defaults = %v; the Epic says 8 s, within %v", got, boundCap)
	}
	raised := config.Tmux{QueryTimeoutMs: 2*config.DefaultActionTimeoutMs + config.DefaultQueryTimeoutMs}
	rq, written := raised.EffectiveQueryTimeout(), []apitest.TmuxSetting{
		apitest.TmuxInt(config.TmuxQueryTimeoutMs, raised.QueryTimeoutMs)}
	if rq <= 2*a || 2*rq+c <= rq+c+2*a {
		t.Fatalf("raised Q = %v; want above 2A = %v, making path (ii) the longer", rq, 2*a)
	}
	cases := []struct {
		name   string
		q      time.Duration
		config []apitest.TmuxSetting // also written for the Client
		held   bool                  // path (ii); false: path (i)
	}{
		{"default Q/path i", q, nil, false},
		{"default Q/path ii", q, nil, true},
		{"Q above 2A/path i", rq, written, false},
		{"Q above 2A/path ii", rq, written, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newKillEnv(t)
			e.rec.WithVirtualTime(e.clock, tmux.Timeouts{Query: tc.q})
			r := e.seedReusable(t, agentGone, reuseRowSpec{Age: rlkSettled(e), Held: tc.held})
			want, wantErr := tc.q+c+2*a, api.ErrTmuxSessionCreate
			calls := []tmux.Call{tmux.CallLookup, tmux.CallCreate, tmux.CallSetLabel, tmux.CallKillSession}
			if tc.held {
				want, wantErr, calls = 2*tc.q+c, api.ErrTmuxSessionConflict, []tmux.Call{tmux.CallLookup, tmux.CallCreate, tmux.CallLookup}
				e.arrangeHeld(t, r.resumeRow, heldSpec{Holder: holderForeign})
			} else {
				e.rec.Script(r.Socket, tmuxfix.Script{Failure: tmux.FailLabel, Times: 1}, tmux.CallCreate).
					Script(r.Socket, tmuxfix.Script{Failure: tmux.FailUnrecognized, ExitStatus: 1}, tmux.CallSetLabel, tmux.CallKillSession)
			}
			start := e.clock.Now()

			_, _, err := e.reuse(t, reuseParams(t, r, reuseRequest{}), tc.config...)

			if elapsed := e.clock.Now().Sub(start); elapsed != want {
				t.Errorf("virtual time = %v; want %v", elapsed, want)
			}
			if !errors.Is(err, wantErr) {
				t.Fatalf("err = %v; want %v", err, wantErr)
			}
			e.assertKillCalls(t, calls...)
			if st := e.columns(t, r.ID).State; st != store.StateEnded {
				t.Errorf("state = %v; want ended again (restored)", st)
			}
		})
	}
}

const droidGuardRefusalEnd = "; an operator must fix the Path List File named by [launch] droid_secret_paths_file or the Guard installation and environment (see docs/experimental.md#droid-secret-paths); no row, session, trust entry or launch file was written and nothing was launched"

// SR-2.3: both real launch entry points retain their no-write contract for
// every distinct pre-write refusal; the private short-deadline path is tested
// in spawn.TestDroidGuardSelfCheck and TestDroidGuardCancelledProbeStopsProgram.
func TestDroidGuardRefusalWritesNothing(t *testing.T) {
	// Serial: it sets HOME/PATH, replaces the Guard selector, and snapshots the shared trail.
	cases := []struct {
		name, kind string
		line       int
	}{
		{"unconfigured", "no Path List File is configured", 0},
		{"missing", "the Path List File is not a readable regular file of at most 1 MiB", 0},
		{"unreadable", "the Path List File is not a readable regular file of at most 1 MiB", 0},
		{"directory", "the Path List File is not a readable regular file of at most 1 MiB", 0},
		{"FIFO", "the Path List File is not a readable regular file of at most 1 MiB", 0},
		{"oversize", "the Path List File is not a readable regular file of at most 1 MiB", 0},
		{"encoding", "the Path List File is not valid UTF-8", 0},
		{"empty", "the Path List has no entries", 0},
		{"relative", "the entry is not an absolute or ~/ path", 3},
		{"whitespace", "the entry has leading or trailing whitespace", 3},
		{"control", "the entry has a control character", 3},
		{"HOME", "HOME is not a usable absolute path", 0},
		{"selection", "the Guard program is unavailable", 0},
		{"length", "the Guard command exceeds the single-argument length limit", 0},
		{"missing sh", "sh is unavailable on agent-director's PATH", 0},
		{"missing program", "the Guard self-check failed", 0},
		{"wrong exit", "the Guard self-check failed", 0},
		{"output", "the Guard self-check output failed its bounds", 0},
	}
	for _, operation := range []string{"spawn", "reuse"} {
		for _, tc := range cases {
			t.Run(operation+"/"+tc.name, func(t *testing.T) {
				g := passingDroidGuard(t)
				list := g.List
				contents := "/invented/PRIVATE_GUARD_ENTRY\n"
				switch tc.name {
				case "unconfigured":
					list = ""
				case "encoding":
					contents = "/invented/PRIVATE_GUARD_ENTRY\xff"
				case "empty":
					contents = "# ignored\n \t\n"
				case "relative":
					contents = "# ignored\n\nPRIVATE_GUARD_ENTRY\n"
				case "whitespace":
					contents = "# ignored\n\n /invented/PRIVATE_GUARD_ENTRY\n"
				case "control":
					contents = "# ignored\n\n/invented/PRIVATE\x00GUARD_ENTRY\n"
				case "oversize":
					contents = strings.Repeat("#", (1<<20)+1)
				case "length":
					contents = "/" + strings.Repeat("'", 50000)
				case "selection":
					t.Cleanup(spawn.SetDroidGuardProgramForTest(func() (string, error) { return "", errors.New("PRIVATE_SELECTION_OUTPUT") }))
				case "wrong exit":
					if err := g.Set(droidfix.Pass); err != nil {
						t.Fatal(err)
					}
				case "output":
					if err := g.Set(droidfix.StderrFlood); err != nil {
						t.Fatal(err)
					}
				case "missing program":
					if err := os.Remove(g.Path); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.WriteFile(g.List, []byte(contents), 0600); err != nil {
					t.Fatal(err)
				}
				switch tc.name {
				case "missing", "directory", "FIFO":
					if err := os.Remove(g.List); err != nil {
						t.Fatal(err)
					}
					if tc.name == "directory" {
						if err := os.Mkdir(g.List, 0700); err != nil {
							t.Fatal(err)
						}
					}
					if tc.name == "FIFO" {
						if err := syscall.Mkfifo(g.List, 0600); err != nil {
							t.Fatal(err)
						}
					}
				case "unreadable":
					if err := os.Chmod(g.List, 0); err != nil {
						t.Fatal(err)
					}
				}
				body := "[experimental]\ndroid = true\n[launch]\ndroid_secret_paths_file = " + strconv.Quote(list) + "\ndroid_rules_path = " + strconv.Quote(refusedRulesFile(t)) + "\n"
				invoke := func(c *api.Client, p api.SpawnParams) error {
					if tc.name == "HOME" {
						old := os.Getenv("HOME")
						t.Setenv("HOME", "relative-home")
						defer t.Setenv("HOME", old)
					}
					if tc.name == "missing sh" {
						old := os.Getenv("PATH")
						t.Setenv("PATH", t.TempDir())
						defer t.Setenv("PATH", old)
					}
					_, err := c.Spawn(p)
					return err
				}
				var err error
				var logs string
				if operation == "spawn" {
					e := newSpawnEnv(t)
					if err := os.MkdirAll(filepath.Dir(e.socket), 0700); err != nil {
						t.Fatal(err)
					}
					c := agentClientWithConfig(t, e, body)
					err = e.checkCreationWrites(t, func() error {
						return invoke(c, api.SpawnParams{Agent: "droid", CWD: t.TempDir(), AgentArgs: paneOnlyArgs("droid", "model")})
					})
					logs = e.logs.String()
				} else {
					home := withTempHome(t)
					e := newKillEnv(t)
					r := e.seedReusable(t, agentGone, reuseRowSpec{Opts: []apitest.SpawnOption{apitest.WithAgent("droid")}})
					old := seedLaneOverlay(t, e.storeID, r.Token)
					c, log := e.configClient(t, body)
					p := reuseParams(t, r, reuseRequest{Args: paneOnlyArgs("droid", "model")})
					p.Agent = "droid"
					roots := []string{filepath.Join(adConfigDir(home), "launch"), filepath.Join(adConfigDir(home), "templates")}
					files, before := snapshotTrees(t, roots), e.snapshotReuse(t, r)
					err = invoke(c, p)
					e.assertWroteNothing(t, before)
					if diff := files.diff(snapshotTrees(t, roots)); len(diff) != 0 {
						t.Errorf("Guard refusal changed files: %v", diff)
					}
					assertLaneOverlay(t, old, true)
					logs = log.String()
				}
				assertOneName(t, err, "ErrLaunchGuardRefused")
				where := ""
				if tc.line > 0 {
					where = fmt.Sprintf("line %d: ", tc.line)
				}
				want := "ErrLaunchGuardRefused: Droid launch Guard refused: " + where + tc.kind + droidGuardRefusalEnd
				if errText(err) != want {
					t.Errorf("error=%q; want %q", errText(err), want)
				}
				if strings.Contains(logs, contents) {
					t.Error("refusal log leaked Path List contents")
				}
				for _, private := range []string{"PRIVATE_GUARD_ENTRY", "PRIVATE_SELECTION_OUTPUT", g.List, g.Path, droidfix.FailOutput} {
					if strings.Contains(logs, private) || strings.Contains(errText(err), private) {
						t.Errorf("refusal leaked %q", private)
					}
				}
			})
		}
	}
}

// SR-1.2/SR-2.1: one Client observes edited bytes at both entry points. A
// pre-feature retained model-only overlay is replaced by a checked Guard.
func TestDroidGuardReadAtEachLaunch(t *testing.T) {
	// Serial: it owns HOME and replaces the Guard selector.
	withTempHome(t)
	e := newKillEnv(t)
	g := passingDroidGuard(t)
	c, _ := e.configClient(t, "[experimental]\ndroid = true\n[launch]\n"+g.ConfigLine())
	old := e.seedReusable(t, agentGone, reuseRowSpec{Opts: []apitest.SpawnOption{apitest.WithAgent("droid")}})
	retained := seedLaneOverlay(t, e.storeID, old.Token)
	for i, operation := range []string{"spawn", "spawn", "reuse"} {
		entry := fmt.Sprintf("/invented/vault-%d", i)
		if err := os.WriteFile(g.List, []byte(entry+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		p := api.SpawnParams{Agent: "droid", CWD: t.TempDir(), AgentArgs: paneOnlyArgs("droid", "model")}
		if operation == "reuse" {
			p.InstanceID, p.ReuseFinished = old.ID, true
		}
		if _, err := c.Spawn(p); err != nil {
			t.Fatal(err)
		}
		creates, calls := e.rec.SocketCallsOf(tmux.CallCreate), g.Calls(t)
		if len(creates) != i+1 || len(calls) != 2*(i+1) {
			t.Fatalf("creates=%d checks=%d", len(creates), len(calls))
		}
		for _, call := range calls[2*i:] {
			if !reflect.DeepEqual(call.Args, []string{"tool-guard", "--", entry}) {
				t.Fatalf("stale check: %q", call.Args)
			}
		}
		argv := creates[i].Command
		if len(argv) != 3 || argv[1] != "--settings" {
			t.Fatalf("argv=%q", argv)
		}
		data, err := os.ReadFile(argv[2])
		if err != nil {
			t.Fatal(err)
		}
		command := assertDroidOverlay(t, data, "model", "")
		if !strings.Contains(command, "'"+entry+"'") {
			t.Fatalf("stale overlay command: %s", command)
		}
	}
	assertLaneOverlay(t, retained, false)
}
