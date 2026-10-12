package main_test

// The hook verb through the built CLI: its own config load, store open and
// production parent-process reader wired to internal/hook.Handle, fail-open
// on every path (SRD §3.2). The per-event transitions and the ad.hook.fired
// shapes are internal/hook's classify_test.go and TestTrailEmitHookFired.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/probe"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// tsRe is the SR-A-7.9 trail timestamp form.
var tsRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3,}Z$`)

// runHook pipes payload into the hook verb under home with env.
func runHook(t *testing.T, home string, env map[string]string, payload string) (string, string, int) {
	t.Helper()
	stdout, stderr, code, timedOut := runBounded(t, home, env, payload, false, surfaceDeadline, "hook")
	if timedOut {
		t.Fatalf("hook still running after %v; stderr=%q", surfaceDeadline, stderr)
	}
	return stdout, stderr, code
}

// assertOneHookFired returns the one ad.hook.fired line of home's trail,
// checking its SR-A-2.1 shape: ts, source ad_hook, string relay_mode and
// session_id, an array matcher, and no tool_input.
func assertOneHookFired(t *testing.T, home string) map[string]any {
	t.Helper()
	fired := eventsOf(readTrailLines(t, home), "ad.hook.fired")
	if len(fired) != 1 {
		t.Fatalf("ad.hook.fired lines = %v; want exactly one", fired)
	}
	row := fired[0]
	ts, _ := row["ts"].(string)
	_, relay := row["relay_mode"].(string)
	_, session := row["session_id"].(string)
	_, matcher := row["matcher"].([]any)
	_, toolInput := row["tool_input"]
	if !tsRe.MatchString(ts) || row["source"] != "ad_hook" || !relay || !session || !matcher || toolInput {
		t.Errorf("ad.hook.fired = %v; want an SR-A-7.9 ts, source ad_hook, string relay_mode and session_id, "+
			"an array matcher and no tool_input", row)
	}
	return row
}

// TestHookCLISessionStartRecordsIdentityAndTranscript: a SessionStart from
// the row's pane process (this test process, the hook's parent, SR-22.9)
// moves the pending row to waiting and records the session id, pid and
// proc_starttime of the parent (read by the production reader) and the
// payload's existing transcript path (b.v2c), with one ad.hook.fired line.
func TestHookCLISessionStartRecordsIdentityAndTranscript(t *testing.T) {
	home := t.TempDir()
	const id = "id-e2e-1"
	if _, err := apitest.SeedSpawn(stateDB(home), id, store.StatePending, "/tmp", "off", "", true, withTestProcessPane(t)); err != nil {
		t.Fatalf("SeedSpawn: %v", err)
	}
	transcript := filepath.Join(home, ".claude", "projects", "e2e", "session-e2e-uuid.jsonl")
	if err := os.MkdirAll(filepath.Dir(transcript), 0o700); err != nil {
		t.Fatalf("mkdir transcript parent: %v", err)
	}
	if err := os.WriteFile(transcript, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	stdout, stderr, code := runHook(t, home, map[string]string{"AGENT_DIRECTOR_INSTANCE_ID": id},
		`{"hook_event_name":"SessionStart","transcript_path":"`+transcript+`"}`)

	if code != 0 || stdout != "" {
		t.Fatalf("hook exit = %d, stdout = %q; want 0 and empty\nstderr=%s", code, stdout, stderr)
	}
	pid := os.Getpid()
	start, _, _ := probe.NewProcChecker().StartTime(pid)
	cols := rowColumns(t, home, id)
	got := []any{cols.State, cols.SessionID, fmt.Sprint(cols.PID), cols.ProcStarttime, cols.JSONLPath}
	want := []any{store.StateWaiting, "session-e2e-uuid", strconv.Itoa(pid), start, transcript}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("state, session id, pid, proc_starttime, jsonl_path = %v; want %v", got, want)
	}
	row := assertOneHookFired(t, home)
	if row["instance_id"] != id || row["event_name"] != "SessionStart" || row["session_id"] != "session-e2e-uuid" {
		t.Errorf("ad.hook.fired = %v; want %s's SessionStart with its session id", row, id)
	}
}

// TestHookCLIOpensTheVerbsStore: for each kind of [store] db_path, a hook run
// from another cwd records its SessionStart in the store `list` reads, logs
// nothing and writes nothing to its cwd (b.8up: "" left the hook storeless).
func TestHookCLIOpensTheVerbsStore(t *testing.T) {
	for _, tc := range []struct {
		name, dbPath string // the config's db_path, "<HOME>" replaced; "-" writes no config
		store        string // the store both open, under HOME
	}{
		{"no config", "-", ".config/agent-director/state.db"},
		{"empty", "", ".config/agent-director/state.db"},
		{"tilde", "~/t/s.db", "t/s.db"},
		{"relative", "r/s.db", ".config/agent-director/r/s.db"},
		{"absolute", "<HOME>/a/s.db", "a/s.db"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, cwd := t.TempDir(), t.TempDir()
			if tc.dbPath != "-" {
				cfg := fmt.Sprintf("[store]\ndb_path = %q\n", strings.Replace(tc.dbPath, "<HOME>", home, 1))
				if err := os.MkdirAll(directorDir(home), 0o700); err != nil {
					t.Fatalf("mkdir: %v", err)
				}
				if err := os.WriteFile(filepath.Join(directorDir(home), "config.toml"), []byte(cfg), 0o600); err != nil {
					t.Fatalf("write config: %v", err)
				}
			}
			const id = "id-b8up-1"
			if _, err := apitest.SeedSpawn(filepath.Join(home, tc.store), id, store.StatePending, "/tmp", "off", "", true,
				withTestProcessPane(t)); err != nil {
				t.Fatalf("SeedSpawn: %v", err)
			}

			stdout, stderr, code := mustRun(t, cliOpts{dir: cwd, env: homeEnv(home, map[string]string{"AGENT_DIRECTOR_INSTANCE_ID": id}),
				stdin: `{"hook_event_name":"SessionStart","transcript_path":"/x/b8up.jsonl"}`, deadline: surfaceDeadline}, "hook")
			if code != 0 || stdout != "" || stderr != "" {
				t.Fatalf("hook exit = %d, stdout = %q, stderr = %q; want 0 and both empty", code, stdout, stderr)
			}
			if log, err := os.ReadFile(filepath.Join(directorDir(home), "errors.log")); len(log) != 0 {
				t.Errorf("errors.log = %q (err %v); want nothing logged", log, err)
			}
			stdout, stderr, code = mustRun(t, cliOpts{dir: cwd, env: homeEnv(home, nil)}, "list")
			var res struct {
				Spawns []struct {
					ID    string `json:"instance_id"`
					State string `json:"state"`
				} `json:"spawns"`
			}
			if code != 0 || json.Unmarshal([]byte(stdout), &res) != nil {
				t.Fatalf("list exit = %d, stdout = %q; want 0 and a list (stderr=%q)", code, stdout, stderr)
			}
			if len(res.Spawns) != 1 || res.Spawns[0].ID != id || res.Spawns[0].State != store.StateWaiting {
				t.Errorf("list = %+v; want only %s, moved to %s by the hook", res.Spawns, id, store.StateWaiting)
			}
			assertHomeTree(t, cwd)
			if row := assertOneHookFired(t, home); row["instance_id"] != id {
				t.Errorf("ad.hook.fired = %v; want %s's", row, id)
			}
		})
	}
}

// TestHookCLIUsesConfiguredBusyTimeout (b.c7f; b.xjn SR-15.1, SR-15.3, SR-18):
// on one store, a SessionStart, a SubagentStart and a SubagentStop each run
// behind their own held write lock. With 1 ms each gives up at once, fail-open
// (exit 0, nothing on stdout or stderr, the row and its entries untouched, a
// locked-store line in errors.log); with 60000 ms each waits for the release and
// applies. A SubagentStart's and SubagentStop's soft refresh and entry write are
// one transaction.
func TestHookCLIUsesConfiguredBusyTimeout(t *testing.T) {
	hookFixture := func(name string) string {
		b, err := os.ReadFile(filepath.Join("..", "..", "internal", "hook", "testdata", "hook-payloads", name))
		if err != nil {
			t.Fatalf("read fixture: %v", err)
		}
		return string(b)
	}
	steps := []struct {
		name    string
		payload string
		// applied reports whether the hook's write is in the store.
		applied func(t *testing.T, home, id string) bool
	}{
		{"SessionStart", `{"hook_event_name":"SessionStart","transcript_path":"/x/c7f.jsonl"}`,
			func(t *testing.T, home, id string) bool { return rowColumns(t, home, id).State == store.StateWaiting }},
		{"SubagentStart", hookFixture("subagent-start.json"), func(t *testing.T, home, id string) bool {
			rows := subagentRows(t, home, id)
			return len(rows.Entries) == 1 && rows.Entries[0].State == store.SubagentStateRunning
		}},
		{"SubagentStop", hookFixture("subagent-stop.json"), func(t *testing.T, home, id string) bool {
			rows := subagentRows(t, home, id)
			return len(rows.Entries) == 1 && rows.Entries[0].State == store.SubagentStateStopped
		}},
	}
	for _, tc := range []struct {
		name    string
		ms      int
		hold    time.Duration // each lock's release, unless the hook returns first
		applied bool
	}{
		{"1 ms gives up", 1, 5 * time.Second, false},
		{"60000 ms waits for the release", 60000, 300 * time.Millisecond, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			const id = "id-c7f-1"
			if _, err := apitest.SeedSpawn(stateDB(home), id, store.StatePending, "/tmp", "off", "", true,
				withTestProcessPane(t)); err != nil {
				t.Fatalf("SeedSpawn: %v", err)
			}
			cfg := fmt.Sprintf("[store]\nbusy_timeout_ms = %d\n", tc.ms)
			if err := os.WriteFile(filepath.Join(directorDir(home), "config.toml"), []byte(cfg), 0o600); err != nil {
				t.Fatalf("write config: %v", err)
			}
			locked := func() int {
				log, _ := os.ReadFile(filepath.Join(directorDir(home), "errors.log"))
				return strings.Count(string(log), "database is locked")
			}

			for _, step := range steps {
				rowBefore, lockedBefore := rowColumns(t, home, id), locked()

				release := apitest.HoldWriteLock(t, stateDB(home), tc.hold)
				stdout, stderr, code := runHook(t, home, map[string]string{"AGENT_DIRECTOR_INSTANCE_ID": id}, step.payload)
				release()

				if code != 0 || stdout != "" || stderr != "" {
					t.Fatalf("%s: hook exit = %d, stdout = %q, stderr = %q; want 0 and both empty", step.name, code, stdout, stderr)
				}
				if got := step.applied(t, home, id); got != tc.applied {
					t.Errorf("%s: applied = %v; want %v", step.name, got, tc.applied)
				}
				if rowAfter := rowColumns(t, home, id); (rowAfter.RowVersion != rowBefore.RowVersion) != tc.applied {
					t.Errorf("%s: row_version %v -> %v; want it moved exactly when the hook applied (%v)",
						step.name, rowBefore.RowVersion, rowAfter.RowVersion, tc.applied)
				}
				if gaveUp := locked() > lockedBefore; gaveUp == tc.applied {
					t.Errorf("%s: errors.log gained a locked-store line = %v; want one exactly when the write gave up", step.name, gaveUp)
				}
			}
		})
	}
}

// subagentRows reads id's subagent entries and claimable launches from home's store.
func subagentRows(t *testing.T, home, id string) apitest.SubagentRows {
	t.Helper()
	rows, err := apitest.ReadSubagentRows(stateDB(home), id)
	if err != nil {
		t.Fatalf("ReadSubagentRows: %v", err)
	}
	return rows
}

// TestHookCLIFailOpen: with no AGENT_DIRECTOR_INSTANCE_ID the hook exits 0
// with no stdout and still writes its ad.hook.fired line (instance id null);
// with the trail unwritable (an empty 0400 ad-trail.jsonl) it still exits 0
// and moves the row to waiting, writing nothing to the trail (SR-A-7).
func TestHookCLIFailOpen(t *testing.T) {
	t.Run("no instance id", func(t *testing.T) {
		home := t.TempDir()
		bootstrapDB(t, home)
		stdout, stderr, code := runHook(t, home, nil, `{"hook_event_name":"PreToolUse","tool_name":"Bash"}`)
		if code != 0 || stdout != "" {
			t.Fatalf("hook exit = %d, stdout = %q; want 0 and empty (stderr=%q)", code, stdout, stderr)
		}
		if row := assertOneHookFired(t, home); row["instance_id"] != nil {
			t.Errorf("instance_id = %v; want null on the early-exit path", row["instance_id"])
		}
	})
	t.Run("trail unwritable", func(t *testing.T) {
		home := t.TempDir()
		const id = "id-twf-1"
		if _, err := apitest.SeedSpawn(stateDB(home), id, store.StatePending, "/tmp", "off", "", true, withTestProcessPane(t)); err != nil {
			t.Fatalf("SeedSpawn: %v", err)
		}
		trailPath := filepath.Join(directorDir(home), "ad-trail.jsonl")
		if err := os.WriteFile(trailPath, nil, 0o400); err != nil {
			t.Fatalf("write read-only trail: %v", err)
		}
		stdout, stderr, code := runHook(t, home, map[string]string{"AGENT_DIRECTOR_INSTANCE_ID": id},
			`{"hook_event_name":"SessionStart","transcript_path":"/x/abc.jsonl"}`)
		if code != 0 || stdout != "" {
			t.Fatalf("hook exit = %d, stdout = %q; want 0 and empty (stderr=%q)", code, stdout, stderr)
		}
		if state := rowColumns(t, home, id).State; state != store.StateWaiting || len(readTrailLines(t, home)) != 0 {
			t.Errorf("state = %s, trail = %v; want %s and the trail still empty", state, readTrailLines(t, home), store.StateWaiting)
		}
	})
}
