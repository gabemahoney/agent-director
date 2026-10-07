package main_test

// The hook verb through the built CLI: its own config load, store open and
// production parent-process reader wired to internal/hook.Handle, fail-open
// on every path (SRD §3.2). The per-event transitions and the ad.hook.fired
// shapes are internal/hook's classify_test.go and TestTrailEmitHookFired.

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"

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
	got := []any{cols.State, cols.ClaudeSessionID, fmt.Sprint(cols.PID), cols.ProcStarttime, cols.JSONLPath}
	want := []any{store.StateWaiting, "session-e2e-uuid", strconv.Itoa(pid), start, transcript}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("state, session id, pid, proc_starttime, jsonl_path = %v; want %v", got, want)
	}
	row := assertOneHookFired(t, home)
	if row["claude_instance_id"] != id || row["event_name"] != "SessionStart" || row["session_id"] != "session-e2e-uuid" {
		t.Errorf("ad.hook.fired = %v; want %s's SessionStart with its session id", row, id)
	}
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
		if row := assertOneHookFired(t, home); row["claude_instance_id"] != nil {
			t.Errorf("claude_instance_id = %v; want null on the early-exit path", row["claude_instance_id"])
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
