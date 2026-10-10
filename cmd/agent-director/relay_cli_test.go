package main_test

// relay_cli_test.go — the relay's delivery facts through the built CLI (b.146
// rules 14 and 15): get-permission, get and list print every delivery fact
// and judge a recorded relay hook with the real /proc in the CLI's own pid
// namespace: a running or stopped process is alive, an exited or zombie one
// gone.

import (
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/probe"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// realIdentity is pid's identity as a relay hook records it: its start time
// and this process's pid namespace (its children share it).
func realIdentity(t *testing.T, pid int) store.ProcessIdentity {
	t.Helper()
	ns, ok := probe.SelfPIDNamespace()
	if !ok {
		t.Fatal("SelfPIDNamespace unreadable; want this test's pid namespace")
	}
	return store.ProcessIdentity{PID: pid, Starttime: liveStart(t, pid), PIDNamespace: ns}
}

// startChild starts argv as a child and returns it with its identity, read
// while it runs; cleanup kills and reaps it.
func startChild(t *testing.T, argv ...string) (*exec.Cmd, store.ProcessIdentity) {
	t.Helper()
	cmd := exec.Command(argv[0], argv[1:]...) //nolint:gosec // fixed test argv
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %v: %v", argv, err)
	}
	id := realIdentity(t, cmd.Process.Pid)
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	return cmd, id
}

// TestCLIDeliveryFactsJudgeRealProcesses (rules 14, 15): a request whose relay
// hook is this process, or a stopped child (state T), reads not_confirmed with
// hook_alive true; one whose hook exited (reaped) or is a zombie reads
// fallen_back with hook_alive false. get-permission, get's and list's
// permission_requests print every delivery fact.
func TestCLIDeliveryFactsJudgeRealProcesses(t *testing.T) {
	cases := []struct {
		name  string
		hook  func(t *testing.T) store.ProcessIdentity
		want  string
		alive bool
	}{
		{"this process, running", func(t *testing.T) store.ProcessIdentity { return realIdentity(t, os.Getpid()) }, "not_confirmed", true},
		{"a stopped child", func(t *testing.T) store.ProcessIdentity {
			cmd, id := startChild(t, "sleep", "30")
			if err := cmd.Process.Signal(syscall.SIGSTOP); err != nil {
				t.Fatalf("SIGSTOP: %v", err)
			}
			return id
		}, "not_confirmed", true},
		{"an exited child, reaped", func(t *testing.T) store.ProcessIdentity {
			cmd, id := startChild(t, "sleep", "30")
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			return id
		}, "fallen_back", false},
		{"a zombie child", func(t *testing.T) store.ProcessIdentity {
			cmd, id := startChild(t, "sleep", "30")
			_ = cmd.Process.Kill() // not reaped until cleanup: a zombie
			deadline := time.Now().Add(5 * time.Second)
			for {
				if _, alive, _ := probe.NewProcChecker().StartTime(cmd.Process.Pid); !alive {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("child still alive 5s after SIGKILL")
				}
				time.Sleep(10 * time.Millisecond)
			}
			return id
		}, "fallen_back", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			id, err := apitest.SeedSpawn(stateDB(home), "", store.StateCheckPermission, "", "on", "", true)
			if err != nil {
				t.Fatalf("SeedSpawn: %v", err)
			}
			s := openDBForRead(t, home)
			storefix.RegisterStorePath(t, s, stateDB(home))
			storefix.SeedRelayRequest(t, s, id, store.RelayRequest{RequestToken: storefix.TestRequestTokenA, ToolName: "Bash",
				ToolInput: `{}`, ToolUseID: "toolu_cli", Hook: tc.hook(t), SettledAt: time.Now().Add(time.Hour)})

			check := func(what string, m map[string]any) {
				t.Helper()
				assertDeliveryKeys(t, what, m)
				if m["delivery"] != tc.want || m["hook_alive"] != tc.alive || m["tool_use_id"] != "toolu_cli" {
					t.Errorf("%s = delivery %v, hook_alive %v, tool_use_id %v; want %s, %v, toolu_cli",
						what, m["delivery"], m["hook_alive"], m["tool_use_id"], tc.want, tc.alive)
				}
			}
			check("get-permission", cliJSON(t, home, "get-permission", "--request-token", storefix.TestRequestTokenA))
			check("get's request", onlyRequestOf(t, "get", cliJSON(t, home, "get", "--claude-instance-id", id)))
			list := cliJSON(t, home, "list")
			spawns, _ := list["spawns"].([]any)
			if len(spawns) != 1 {
				t.Fatalf("list spawns = %v; want the one row", list["spawns"])
			}
			row, _ := spawns[0].(map[string]any)
			check("list's request", onlyRequestOf(t, "list", row))
		})
	}
}

// seedCLIRelayRequest seeds a relay-on check_permission row under a new home
// with request A recorded by a relay hook with identity hook, and returns the
// home and the row's id.
func seedCLIRelayRequest(t *testing.T, hook store.ProcessIdentity) (home, id string) {
	t.Helper()
	home = t.TempDir()
	id, err := apitest.SeedSpawn(stateDB(home), "", store.StateCheckPermission, "", "on", "", true)
	if err != nil {
		t.Fatalf("SeedSpawn: %v", err)
	}
	s := openDBForRead(t, home)
	storefix.RegisterStorePath(t, s, stateDB(home))
	storefix.SeedRelayRequest(t, s, id, store.RelayRequest{RequestToken: storefix.TestRequestTokenA, ToolName: "Bash",
		ToolInput: `{"command":"ls"}`, ToolUseID: "toolu_cli", Hook: hook, SettledAt: time.Now().Add(time.Hour)})
	return home, id
}

// cliErrorEnvelope runs the CLI under home, fails unless it exited 1 with
// empty stdout, and returns its stderr envelope as raw JSON fields.
func cliErrorEnvelope(t *testing.T, home string, args ...string) map[string]json.RawMessage {
	t.Helper()
	stdout, stderr, code := runCLIWithHome(t, home, args...)
	var env map[string]json.RawMessage
	if code != 1 || stdout != "" || json.Unmarshal([]byte(stderr), &env) != nil {
		t.Fatalf("%v: exit %d, stdout %q, stderr %q; want 1, nothing on stdout and one JSON envelope", args, code, stdout, stderr)
	}
	return env
}

// TestCLIErrDetails (b.146 rule 15, decision 8 A): a refusal that carries
// facts prints them as the stderr envelope's err_details object: decide's and
// plain send-keys' ErrRelayFallenBack on a request whose relay hook exited,
// and record-pane-answer's ErrClaimTooSoon while its hook runs (not_before
// null). An error with none has no err_details key.
func TestCLIErrDetails(t *testing.T) {
	cmd, gone := startChild(t, "sleep", "30")
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	home, id := seedCLIRelayRequest(t, gone)
	for name, argv := range map[string][]string{
		"decide":    decideArgv(id, storefix.TestRequestTokenA),
		"send-keys": {"send-keys", "--claude-instance-id", id, "--text", "1"},
	} {
		env := cliErrorEnvelope(t, home, argv...)
		var details map[string]any
		if string(env["err_name"]) != `"ErrRelayFallenBack"` || json.Unmarshal(env["err_details"], &details) != nil {
			t.Fatalf("%s envelope = %v; want ErrRelayFallenBack with an err_details object", name, env)
		}
		if details["request_token"] != storefix.TestRequestTokenA || details["tool_use_id"] != "toolu_cli" ||
			details["delivery"] != "fallen_back" || details["hook_alive"] != false || details["state"] != store.StateCheckPermission {
			t.Errorf("%s err_details = %v; want request A fallen back, hook_alive false, state check_permission", name, details)
		}
		if reqs, ok := details["open_requests"].([]any); !ok || len(reqs) != 0 {
			t.Errorf("%s err_details.open_requests = %#v; want []", name, details["open_requests"])
		}
	}

	alive, _ := seedCLIRelayRequest(t, realIdentity(t, os.Getpid()))
	env := cliErrorEnvelope(t, alive, "record-pane-answer", "--request-token", storefix.TestRequestTokenA, "--as", "unknown",
		"--expect-pane-sha256", strings.Repeat("0", 64))
	var details map[string]any
	if string(env["err_name"]) != `"ErrClaimTooSoon"` || json.Unmarshal(env["err_details"], &details) != nil ||
		details["request_token"] != storefix.TestRequestTokenA || details["hook_alive"] != true || details["not_before"] != nil {
		t.Errorf("record-pane-answer envelope = %v; want ErrClaimTooSoon, err_details request A, hook_alive true, not_before null", env)
	}

	env = cliErrorEnvelope(t, home, "get-permission", "--request-token", storefix.TestRequestTokenB)
	if _, has := env["err_details"]; has || string(env["err_name"]) != `"ErrPermissionRequestNotFound"` {
		t.Errorf("get-permission of an unknown token envelope = %v; want ErrPermissionRequestNotFound with no err_details", env)
	}
}

// cliJSON runs the CLI under home and returns its stdout parsed as one JSON
// object, failing on a non-zero exit.
func cliJSON(t *testing.T, home string, args ...string) map[string]any {
	t.Helper()
	stdout, stderr, code := runCLIWithHome(t, home, args...)
	var m map[string]any
	if code != 0 || json.Unmarshal([]byte(stdout), &m) != nil {
		t.Fatalf("%v: exit %d, stdout %q, stderr %q; want 0 and a JSON object", args, code, stdout, stderr)
	}
	return m
}

// onlyRequestOf returns the one element of row's permission_requests.
func onlyRequestOf(t *testing.T, what string, row map[string]any) map[string]any {
	t.Helper()
	reqs, _ := row["permission_requests"].([]any)
	if len(reqs) != 1 {
		t.Fatalf("%s permission_requests = %v; want one", what, row["permission_requests"])
	}
	m, _ := reqs[0].(map[string]any)
	return m
}
