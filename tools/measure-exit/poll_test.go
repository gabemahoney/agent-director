package main

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// scripted answers polls in order: "present", "gone" or "error"; the last
// answer repeats.
func scripted(answers ...string) (goneProbe, *int) {
	n := 0
	return func() (bool, error) {
		a := answers[min(n, len(answers)-1)]
		n++
		switch a {
		case "gone":
			return true, nil
		case "error":
			return false, errors.New("unreadable")
		}
		return false, nil
	}, &n
}

func TestPollIntervalIs100ms(t *testing.T) {
	if pollInterval != 100*time.Millisecond {
		t.Fatalf("pollInterval = %s", pollInterval)
	}
}

func TestPollUntilGone(t *testing.T) {
	for _, tc := range []struct {
		name    string
		answers []string
		outcome outcome
		elapsed time.Duration
		polls   int
		errs    int
	}{
		{"gone on the first poll", []string{"gone"}, outcomeCompleted, 0, 1, 0},
		{"gone after three polls", []string{"present", "present", "present", "gone"}, outcomeCompleted, 300 * time.Millisecond, 4, 0},
		{"never gone", []string{"present"}, outcomeDidNotExit, time.Second, 11, 0},
		{"errors then gone", []string{"error", "error", "gone"}, outcomeCompleted, 200 * time.Millisecond, 3, 2},
		{"errors at the ceiling", []string{"present", "error"}, outcomeProbeError, time.Second, 11, 10},
		{"an early error, present at the ceiling", []string{"error", "present"}, outcomeDidNotExit, time.Second, 11, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clk := &virtualClock{now: clockStart}
			p, _ := scripted(tc.answers...)
			r := pollUntilGone(clk, clockStart, time.Second, p)
			if r.Outcome != tc.outcome || r.Elapsed != tc.elapsed || r.Polls != tc.polls || r.ProbeErrors != tc.errs {
				t.Fatalf("got %+v", r)
			}
			if !r.ObservedAt.Equal(clockStart.Add(tc.elapsed)) {
				t.Errorf("observed at %s", r.ObservedAt)
			}
		})
	}
}

func TestProcessGone(t *testing.T) {
	for _, tc := range []struct {
		name               string
		start              string
		alive, known, gone bool
		err                bool
	}{
		{"alive with its start time", "s1", true, true, false, false},
		{"exited or a zombie", "", false, true, true, false},
		{"pid reused by another process", "s2", true, true, true, false},
		{"unreadable", "", false, false, false, true},
	} {
		procs := fakeProcs(func(int) (string, bool, bool) { return tc.start, tc.alive, tc.known })
		gone, err := processGone(procs, 42, "s1")()
		if gone != tc.gone || (err != nil) != tc.err {
			t.Errorf("%s: gone=%t err=%v", tc.name, gone, err)
		}
	}
}

func TestSessionGone(t *testing.T) {
	for _, tc := range []struct {
		name         string
		stdout, errs string
		code         int
		gone, err    bool
	}{
		{"still listed", "$1\n$7\n", "", 0, false, false},
		{"no longer listed", "$1\n", "", 0, true, false},
		{"no server running", "", "no server running on /x/default\n", 1, true, false},
		{"another listing failure", "", "lost server\n", 1, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t, modeDry)
			r.ex.reply = func([]string) (string, string, int) { return tc.stdout, tc.errs, tc.code }
			a := agentRef{Socket: privateSocket(r.h.iso.TmuxTmpdir), TmuxSessionID: "$7"}
			gone, err := r.h.sessionGone("rn2.pause", a)()
			if gone != tc.gone || (err != nil) != tc.err {
				t.Fatalf("gone=%t err=%v", gone, err)
			}
			if r.ex.count("tmux", "-S", a.Socket, "list-sessions") != 1 {
				t.Errorf("calls %q", r.ex.calls)
			}
		})
	}
}

func TestRunLogRecordsArgvNeverEnvironment(t *testing.T) {
	r := newRig(t, modeDry)
	key := credentialSentinels["ANTHROPIC_API_KEY"]
	r.h.inv.scr = newScrubber(fakeEnv(map[string]string{"ANTHROPIC_API_KEY": key}, "/h"))
	r.h.inv.log = newRunLog(r.log, r.h.inv.scr)
	r.h.inv.env = []string{"ANTHROPIC_API_KEY=" + key, "MX_ENV_ONLY=env-only-sentinel-value"}
	r.ex.reply = func([]string) (string, string, int) { return "", "failed with " + key, 1 }
	_, err := r.h.inv.agentDirectorCall("rn6.idle", actionRead, "get", "--claude-instance-id", "id-"+key)
	if err == nil {
		t.Fatal("want the call's non-zero exit as an error")
	}
	logged := r.log.String()
	var entry runLogEntry
	if err := json.Unmarshal([]byte(logged), &entry); err != nil {
		t.Fatalf("run log line %q: %v", logged, err)
	}
	wantArgv := []string{"agent-director", "get", "--claude-instance-id", "id-<redacted>"}
	if entry.Case != "rn6.idle" || entry.Kind != actionRead || entry.ExitCode != 1 || !reflect.DeepEqual(entry.Argv, wantArgv) {
		t.Errorf("run log entry %+v", entry)
	}
	assertAbsent(t, "run log", logged, key, "env-only-sentinel-value")
	assertAbsent(t, "call error", err.Error(), key)
	if _, err := r.h.inv.tmuxCall("x", actionRead, "", "list-sessions"); err == nil || len(r.ex.calls) != 1 {
		t.Error("a tmux call without a socket was not refused before running")
	}
}

func TestWaitStateNotReadyNamesTrailReasons(t *testing.T) {
	r := newRig(t, modeDry)
	r.ex.reply = func([]string) (string, string, int) { return `{"state":"spawning"}`, "", 0 }
	writeFile(t, filepath.Join(r.h.iso.Home, ".agent-director", "ad-trail.jsonl"), strings.Join([]string{
		`{"event":"ad.hook.ignored","claude_instance_id":"id-1","reason":"no_exec_form"}`,
		`{"event":"ad.hook.ignored","claude_instance_id":"id-2","reason":"pid_mismatch"}`,
		`{"event":"ad.hook.ignored","claude_instance_id":"id-1","reason":"no_exec_form"}`,
		`not json`,
	}, "\n")+"\n")
	_, err := r.h.waitState("rn6.idle", "id-1", stateWaiting)
	if !errors.Is(err, errNotReady) || !strings.Contains(err.Error(), "ignored hooks: no_exec_form") || strings.Contains(err.Error(), "pid_mismatch") {
		t.Fatalf("got %v", err)
	}
	if got := r.clk.now.Sub(clockStart); got != r.h.cfg.readyTimeout {
		t.Errorf("waited %s of virtual time, want the ready timeout %s", got, r.h.cfg.readyTimeout)
	}
	r.ex.reply = func([]string) (string, string, int) { return `{"state":"ended"}`, "", 0 }
	if _, err := r.h.waitState("rn6.idle", "id-1", stateWaiting); !errors.Is(err, errNotReady) || !strings.Contains(err.Error(), "became ended first") {
		t.Errorf("an ended row: %v", err)
	}
}
