//go:build linux

package api_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
	"golang.org/x/sys/unix"

	"github.com/gabemahoney/agent-director/internal/hostenv"
	"github.com/gabemahoney/agent-director/internal/inputdelivery"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/cursorfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/faketmuxfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// injectLaunchSizeAmbientBudget leaves one byte above the filtered ambient
// charge. Setting the soft stack limit injects that exact Linux exec budget;
// the original process limit and environment are restored by test cleanup.
func injectLaunchSizeAmbientBudget(t *testing.T) uint64 {
	t.Helper()
	t.Setenv("XHH_AMBIENT_OVERRIDE", strings.Repeat("x", 100000))
	budget := uint64(32768 + 1)
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if !strings.HasPrefix(key, "AGENT_DIRECTOR_") && !hostenv.IsCredential(key) {
			budget += uint64(len(entry) + 1 + strconv.IntSize/8)
		}
	}
	if budget < 128*1024 || budget > 6*1024*1024 {
		t.Fatal("synthetic ambient budget is outside Linux's unclamped stack/4 range")
	}
	var original unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_STACK, &original); err != nil {
		t.Fatal(err)
	}
	limited := original
	limited.Cur = budget * 4
	if err := unix.Setrlimit(unix.RLIMIT_STACK, &limited); err != nil {
		t.Fatalf("inject stack-derived exec budget: %v", err)
	}
	t.Cleanup(func() {
		if err := unix.Setrlimit(unix.RLIMIT_STACK, &original); err != nil {
			t.Errorf("restore stack limit: %v", err)
		}
	})
	return budget
}

// b.xhh CR1: overriding a large ambient entry frees pane budget, but does not
// free tmux client budget. Its argv must be charged before any launch effects.
func TestLaunchSizeAmbientOverridePreWrite(t *testing.T) {
	// Serial: private HOME, ambient override and injected process stack limit.
	for _, path := range []string{"spawn", "reuse", "resume"} {
		t.Run(path, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			e := newResumeEnv(t)
			trust := seedTrustConfig(t, t.TempDir(), trustLacksEntry)
			args := []string{"--append-system-prompt", "short"}
			if inputdelivery.SafeArgs(args) {
				t.Fatal("CR1 requires native preparation disabled")
			}
			env := trust.extraEnv()
			env["XHH_AMBIENT_OVERRIDE"] = "small"
			p := api.SpawnParams{Agent: store.AgentClaude, CWD: t.TempDir(), AgentArgs: args, ExtraEnv: env}
			var id string
			var before reuseRowState
			reset := &hookedReuseStore{st: e.st}
			resetCalled, moveCalled := false, false
			reset.beforeReset(func() { resetCalled = true })
			e.store.afterMove(func() { moveCalled = true })
			switch path {
			case "reuse":
				id, before = seedFinishedRow(t, e.dbPath, store.StateEnded, apitest.WithTmuxSocket(e.socket))
				p.InstanceID, p.ReuseFinished = id, true
			case "resume":
				raw, err := json.Marshal(args)
				if err != nil {
					t.Fatal(err)
				}
				r := e.seedResumable(t, store.StateEnded, apitest.WithRawAgentArgs(string(raw)), apitest.WithExtraEnv(env))
				id = r.ID
				before = readReuseRowState(t, e.dbPath, id)
			}
			budget := injectLaunchSizeAmbientBudget(t)
			var err error
			if path == "resume" {
				_, err = e.resume(id)
				assertOneSentinel(t, err, tmux.ErrTmuxSessionCreate)
			} else {
				_, err = api.SpawnWithReuseStore(e.c, reset, p)
				assertOneSentinel(t, err, api.ErrInvalidFlags)
			}
			var size *tmux.LaunchSizeError
			if !errors.As(err, &size) || size.Component != "calculated total including reserved overhead" || size.Limit != budget || size.Size < budget {
				t.Errorf("want client total refusal at injected budget %d, got %v", budget, err)
			}
			if resetCalled || moveCalled || e.store.moveToken != "" {
				t.Error("ambient client size refusal called reset or move-to-pending")
			}
			if path == "spawn" {
				assertNoTmuxCalls(t, e.rec)
			} else {
				// Finished-row launches check ownership before preflight. Only
				// that read-only lookup is allowed; create and mutations are not.
				if got := callKinds(e.rec); !slices.Equal(got, []tmux.Call{tmux.CallLookup}) {
					t.Errorf("tmux calls = %v; want only the pre-launch lookup", got)
				}
				if n := len(e.rec.Calls()); n != 0 {
					t.Errorf("name-based tmux calls = %d; want none", n)
				}
			}
			if path == "spawn" {
				rows, readErr := e.st.ListSpawns(store.ListFilters{})
				if readErr != nil || len(rows) != 0 {
					t.Errorf("ambient client size refusal inserted a row: count=%d err=%v", len(rows), readErr)
				}
			} else if after := readReuseRowState(t, e.dbPath, id); !reflect.DeepEqual(after, before) {
				t.Error("ambient client size refusal changed row, history, identity or permissions")
			}
			trust.check(t, "", false, "after ambient client size refusal")
			assertNoLaunchFiles(t)
		})
	}
}

// b.xhh: API inputs reach the preflight without hitting the outer CLI's exec
// limit first. Linux caps the aggregate at 6 MiB even with a larger stack.
func launchSizeInput(agent, kind string) ([]string, map[string]string) {
	env := map[string]string{}
	args := []string{"--model", "test-model"}
	switch kind {
	case "argument":
		// This model survives resume filtering for all three agents. Cursor's
		// JSON-encoded runtime argv is larger still and must be measured too.
		args[1] = strings.Repeat("private-launch-value-", 6554)
	case "environment":
		// KEY, '=' and NUL count: the value alone is below MAX_ARG_STRLEN.
		env["XHH_PRIVATE"] = strings.Repeat("v", 131072-len("XHH_PRIVATE")-1)
	case "aggregate":
		for i := range 80 {
			env[fmt.Sprintf("XHH_%03d", i)] = strings.Repeat("private-launch-value-", 5000)
		}
	case "ordinary":
		if agent == store.AgentCodex {
			args = []string{"--config=developer_instructions=" + strings.Repeat("x", 30000)}
		} else {
			args[1] = strings.Repeat("x", 30000)
		}
	}
	return args, env
}

func assertLaunchSizeDescription(t *testing.T, err error, kind string) {
	t.Helper()
	if err == nil {
		t.Error("launch size refusal = nil; want a pre-write size error")
		return
	}
	text := err.Error()
	component := map[string]string{"argument": "argument", "environment": "environment", "aggregate": "total"}[kind]
	if !strings.Contains(strings.ToLower(text), component) || len(regexp.MustCompile(`[0-9]+`).FindAllString(text, -1)) < 2 {
		t.Errorf("size error must name %s, its size and its limit: %s", component, text)
	}
	if kind != "aggregate" && !strings.Contains(text, "131072") {
		t.Errorf("size error does not name the 131072-byte entry limit: %s", text)
	}
	if kind == "aggregate" && !strings.Contains(strings.ToLower(text), "reserve") {
		t.Errorf("total size error omits its reserve: %s", text)
	}
	for _, secret := range []string{"private-launch-value-", strings.Repeat("v", 64)} {
		if strings.Contains(text, secret) {
			t.Error("size error discloses the refused value")
		}
	}
}

func assertNoLaunchFiles(t *testing.T) {
	t.Helper()
	root := filepath.Join(os.Getenv("HOME"), ".config", "agent-director", "launch")
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			t.Errorf("refused launch wrote launch file %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// b.xhh: refusal must precede both the insert and reuse's reset, including
// history archival, launch identity replacement, pre-trust and preparation.
func TestLaunchSizeSpawnPreWrite(t *testing.T) {
	// Serial: the shared spawn fixture owns HOME and the tmux environment.
	for _, agent := range []string{store.AgentClaude, store.AgentCodex, store.AgentCursor} {
		for _, reuse := range []bool{false, true} {
			for _, kind := range []string{"argument", "environment", "aggregate"} {
				t.Run(fmt.Sprintf("%s/reuse=%t/%s", agent, reuse, kind), func(t *testing.T) {
					e := newSpawnEnv(t)
					trust := seedTrustConfig(t, t.TempDir(), trustLacksEntry)
					args, env := launchSizeInput(agent, kind)
					env["CLAUDE_CONFIG_DIR"] = trust.dir
					p := api.SpawnParams{Agent: agent, CWD: t.TempDir(), AgentArgs: args, ExtraEnv: env}
					var before reuseRowState
					resetCalled := false
					var err error
					if reuse {
						p.InstanceID, before = seedFinishedRow(t, e.dbPath, store.StateEnded,
							apitest.WithAgent(agent), apitest.WithTmuxSocket(e.socket))
						p.ReuseFinished = true
						st, openErr := store.Open(e.dbPath)
						if openErr != nil {
							t.Fatal(openErr)
						}
						defer st.Close()
						rs := &hookedReuseStore{st: st}
						rs.beforeReset(func() { resetCalled = true })
						_, err = api.SpawnWithReuseStore(e.c, rs, p)
					} else {
						_, err = e.c.Spawn(p)
					}
					assertOneSentinel(t, err, api.ErrInvalidFlags)
					assertLaunchSizeDescription(t, err, kind)
					if resetCalled {
						t.Error("size refusal called ResetForReuse")
					}
					if reuse {
						if after := readReuseRowState(t, e.dbPath, p.InstanceID); !reflect.DeepEqual(after, before) {
							t.Error("size refusal changed reused row, launch identity, history or permissions")
						}
					} else if ids := listIDs(t, e.c); len(ids) != 0 {
						t.Errorf("size refusal inserted %d rows", len(ids))
					}
					if n := len(e.rec.SocketCallsOf(tmux.CallCreate)); n != 0 {
						t.Errorf("size refusal made %d create calls", n)
					}
					trust.check(t, p.CWD, false, "after size refusal")
					assertNoLaunchFiles(t)
				})
			}
		}
	}
}

// b.xhh: saved model options survive resume's removal of the initial prompt;
// an over-limit saved request must not enter even a subsequently restored move.
func TestLaunchSizeResumePreWrite(t *testing.T) {
	// Serial: private HOME, Cursor gateway installation and trust configuration.
	for _, agent := range []string{store.AgentClaude, store.AgentCodex, store.AgentCursor} {
		for _, kind := range []string{"argument", "environment", "aggregate"} {
			t.Run(agent+"/"+kind, func(t *testing.T) {
				t.Setenv("HOME", t.TempDir())
				installCursorFixtureGateway(t)
				e := newResumeEnv(t)
				trust := seedTrustConfig(t, t.TempDir(), trustLacksEntry)
				args, env := launchSizeInput(agent, kind)
				env["CLAUDE_CONFIG_DIR"] = trust.dir
				raw, err := json.Marshal(args)
				if err != nil {
					t.Fatal(err)
				}
				id := "size-resume-" + uuid.NewString()
				opts := []apitest.SpawnOption{apitest.WithAgent(agent), apitest.WithRawAgentArgs(string(raw)), apitest.WithExtraEnv(env)}
				spec := resumableSpec{ID: id, Opts: opts}
				if agent == store.AgentCursor {
					h := cursorfix.NewGatewayHistory(t, id, resumableLife, "latest-cursor", "older-cursor")
					spec.SessionID = "latest-cursor"
					spec.Opts = append(spec.Opts, apitest.WithJsonlPath(h.Latest))
				}
				e.seedRow(t, spec)
				before := readReuseRowState(t, e.dbPath, id)
				moved := false
				e.store.afterMove(func() { moved = true })
				_, err = e.resume(id)
				assertOneSentinel(t, err, tmux.ErrTmuxSessionCreate)
				assertLaunchSizeDescription(t, err, kind)
				if moved || e.store.moveToken != "" {
					t.Error("size refusal called the move-to-pending seam")
				}
				if after := readReuseRowState(t, e.dbPath, id); !reflect.DeepEqual(after, before) {
					t.Error("size refusal changed resumed row, launch identity, history or permissions")
				}
				if n := len(e.rec.SocketCallsOf(tmux.CallCreate)); n != 0 {
					t.Errorf("size refusal made %d create calls", n)
				}
				trust.check(t, "", false, "after size refusal")
				assertNoLaunchFiles(t)
			})
		}
	}
}

// b.xhh: the transport threshold is not a caller limit. A normal 30 KB
// request still writes its row and reaches create with its bytes intact.
func TestLaunchSizeThirtyKBAccepted(t *testing.T) {
	// Serial: newSpawnEnv isolates HOME and the tmux environment.
	for _, agent := range []string{store.AgentClaude, store.AgentCodex, store.AgentCursor} {
		t.Run(agent, func(t *testing.T) {
			e := newSpawnEnv(t)
			args, env := launchSizeInput(agent, "ordinary")
			res, err := e.c.Spawn(api.SpawnParams{Agent: agent, CWD: t.TempDir(), AgentArgs: args, ExtraEnv: env, NoPreTrust: true})
			if err != nil {
				t.Fatal(err)
			}
			cols, err := apitest.ReadSpawnColumns(e.dbPath, res.InstanceID)
			if err != nil || !slices.Equal(storedAgentArgs(t, cols), args) {
				t.Fatal("accepted launch did not preserve its request in the row")
			}
			calls := e.rec.SocketCallsOf(tmux.CallCreate)
			if len(calls) != 1 {
				t.Fatalf("create calls = %d; want 1", len(calls))
			}
			want := args[len(args)-1]
			if agent == store.AgentCursor {
				encoded, marshalErr := json.Marshal(args)
				if marshalErr != nil {
					t.Fatal(marshalErr)
				}
				want = string(encoded)
			}
			if !slices.Contains(calls[0].Command, want) {
				t.Error("accepted launch changed or dropped its 30 KB argument")
			}
		})
	}
}

// b.xhh: unlike the Recorder positive control above, this exercises the
// production client and fake-tmux's real command-message limit on all Start paths.
func TestLaunchSizeThirtyKBAgainstFake(t *testing.T) {
	// Serial: private HOME, TMUX_TMPDIR and fake-tmux argv log.
	for _, path := range []string{"spawn", "reuse", "resume"} {
		t.Run(path, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			e := newResumeEnv(t)
			e.socket = filepath.Join(userSocketDir(useOwnTmuxTmpdir(t)), "default")
			logPath := filepath.Join(t.TempDir(), "tmux.log")
			t.Setenv(faketmuxfix.EnvLog, logPath)
			tc := newFakeTmuxClient(t)
			c, err := api.New(api.Options{StorePath: e.dbPath,
				ConfigPath: filepath.Join(filepath.Dir(e.dbPath), "config.toml"), TmuxClient: tc})
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			api.SetClockForTest(c, e.clock.Now)
			api.SetProcCheckerForTest(c, e.pc)
			args, _ := launchSizeInput(store.AgentCodex, "ordinary")
			id := "long-api-" + uuid.NewString()
			p := api.SpawnParams{Agent: store.AgentCodex, InstanceID: id, CWD: t.TempDir(), AgentArgs: args, NoPreTrust: true}
			if path == "reuse" {
				p.InstanceID, _ = seedFinishedRow(t, e.dbPath, store.StateEnded,
					apitest.WithAgent(store.AgentCodex), apitest.WithTmuxSocket(e.socket), apitest.WithNoPreTrust())
				id, p.ReuseFinished = p.InstanceID, true
			}
			if path == "resume" {
				raw, marshalErr := json.Marshal(args)
				if marshalErr != nil {
					t.Fatal(marshalErr)
				}
				r := e.seedResumable(t, store.StateEnded, apitest.WithAgent(store.AgentCodex),
					apitest.WithRawAgentArgs(string(raw)), apitest.WithNoPreTrust())
				id = r.ID
				_, err = c.Resume(api.ResumeParams{InstanceID: id})
			} else {
				_, err = c.Spawn(p)
			}
			if err != nil {
				t.Fatalf("30 KB %s must succeed through tmux.Client/fake: %v", path, err)
			}
			cols, readErr := apitest.ReadSpawnColumns(e.dbPath, id)
			if readErr != nil || cols.State != store.StatePending || !slices.Equal(storedAgentArgs(t, cols), args) {
				t.Fatal("successful long launch did not retain its pending row and exact saved argument")
			}
			table := (faketmuxfix.Tables{}).Read(t, e.socket)
			if len(table.Sessions) != 1 || len(table.Sessions[0].Panes) != 1 {
				t.Fatal("long launch did not create exactly one session and pane")
			}
			session := table.Sessions[0]
			token, _ := cols.LaunchToken.(string)
			if session.Label != tmuxfix.LabelValue(token, session.ID, id, e.storeID) ||
				session.Panes[0].AdPane != tmuxfix.PaneLabelValue(token, session.Panes[0].ID) {
				t.Fatal("long launch lost its session or pane label")
			}
			bootstraps := 0
			for _, argv := range fakeTmuxArgvs(t, logPath) {
				if containsRun(argv, []string{"start-server", ";", "source-file", "-"}) {
					bootstraps++
				}
			}
			if bootstraps != 1 {
				t.Errorf("stdin bootstrap calls = %d; want exactly 1", bootstraps)
			}
		})
	}
}
