//go:build linux

package cursorhost

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/cursormcp"
	"github.com/gabemahoney/agent-director/internal/testsupport/faketmuxfix"
)

// restartRegistration reuses the protected runtime fixture and the real scoped CLI.
func restartRegistration(t *testing.T, e *requestEnv, setup bool) (cursormcp.Stdio, *cursormcp.Observation, time.Time) {
	t.Helper()
	bin := guidanceCLI(t)
	enableGuidance(t, e)
	path := filepath.Join(os.Getenv("HOME"), ".config", "agent-director", "config.toml")
	if err := os.WriteFile(path, []byte("[defaults]\ninject_help_hook = true\n[tmux]\nquery_timeout_ms = 300\npipe_close_wait_ms = 50\n"), 0600); err != nil {
		t.Fatal(err)
	}
	row, err := e.s.GetSpawn(e.binding.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	until := time.Now().Add(25 * time.Millisecond)
	if setup {
		until = time.Now().Add(time.Second)
	}
	observation, registration, err := cursormcp.New(bin, os.Getenv("HOME"), e.s, row, e.binding, until)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(observation.Close)
	if !setup {
		time.Sleep(time.Until(until) + time.Millisecond)
	}
	return registration, observation, until
}

func startRestartMCP(t *testing.T, registration cursormcp.Stdio) (*ownedProcess, *protocol) {
	t.Helper()
	env := os.Environ()
	for _, variable := range registration.Env {
		env = append(env, variable.Name+"="+variable.Value)
	}
	child, err := startProcess(registration.Command, registration.Args, os.Getenv("HOME"), env)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { child.terminate(); child.input.Close(); child.output.Close() })
	rpc := newProtocol(child.input, child.output)
	t.Cleanup(rpc.close)
	if _, err := rpc.request("initialize", map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{}, "clientInfo": map[string]string{"name": "restart-regression", "version": "1"}}); err != nil {
		t.Fatal(err)
	}
	return child, rpc
}

func restartReply(t *testing.T, rpc *protocol, accepted bool) {
	t.Helper()
	select {
	case response := <-rpc.frames:
		if accepted {
			if response.err != nil || len(response.frame.Error) != 0 || len(response.frame.Result) == 0 {
				t.Fatalf("scoped MCP refused: %+v", response)
			}
			if _, err := rpc.response(response.frame); err != nil {
				t.Fatal(err)
			}
		} else if response.err == nil {
			t.Fatalf("retired scoped MCP produced a response: %+v", response.frame)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("scoped MCP verification did not finish within its bounded allowance")
	}
}

func waitRestartFile(t *testing.T, path string) {
	t.Helper()
	until := time.Now().Add(time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(until) {
			t.Fatalf("query did not enter fixture: %s", path)
		}
		time.Sleep(time.Millisecond)
	}
}

func restartUsefulGet(t *testing.T, rpc *protocol, id string) {
	t.Helper()
	if err := rpc.notify("notifications/initialized", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	if _, err := rpc.request("tools/call", map[string]any{"name": "get", "arguments": map[string]string{"instance_id": id}}); err != nil {
		t.Fatal(err)
	}
	select {
	case response := <-rpc.frames:
		if response.err != nil || len(response.frame.Error) != 0 || !bytes.Contains(response.frame.Result, []byte(id)) || bytes.Contains(response.frame.Result, []byte(`"isError":true`)) {
			t.Fatalf("replacement did not remain useful: %+v", response)
		}
		if _, err := rpc.response(response.frame); err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("replacement tool did not complete")
	}
}

func TestCursorGuidanceExpiredReplacementActualCutRecovery(t *testing.T) {
	// Serial: protected runtime and exec pipe holder own HOME/PATH.
	for _, query := range []string{"list-sessions", "list-panes"} {
		for _, partial := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/partial=%v", query, partial), func(t *testing.T) {
				e := newRequestEnv(t, "off", "published-session")
				registration, observation, _ := restartRegistration(t, e, false)
				marker := heldGuidanceQuery(t, query, partial)
				_, rpc := startRestartMCP(t, registration)
				waitRestartFile(t, filepath.Join(filepath.Dir(marker), "pids"))
				// The real exec collector cuts the held pipe after 50 ms.
				time.Sleep(90 * time.Millisecond)
				select {
				case response := <-rpc.frames:
					t.Fatalf("incomplete held evidence completed verification: %+v", response)
				default:
				}
				if err := os.Remove(marker); err != nil {
					t.Fatal(err)
				}
				restartReply(t, rpc, true)
				restartUsefulGet(t, rpc, e.binding.InstanceID)
				if err := observation.Wait(); err == nil {
					t.Fatal("expired replacement revived the original acknowledgement")
				}
			})
		}
	}
}

func TestCursorGuidanceExpiredReplacementRequiresCurrentPublication(t *testing.T) {
	// Serial: the protected runtime fixture owns HOME/PATH.
	for _, change := range []string{"unpublished new", "unpublished saved load", "disagreement", "retired", "published pending", "current confirmed session"} {
		t.Run(change, func(t *testing.T) {
			e := newRequestEnv(t, "off", "published-session")
			registration, observation, _ := restartRegistration(t, e, false)
			switch change {
			case "unpublished new":
				e.exec(t, "UPDATE spawns SET state='pending',session_id='' WHERE instance_id=?", e.binding.InstanceID)
				e.exec(t, "UPDATE cursor_runtimes SET bound_session_id='' WHERE bound_instance_id=?", e.binding.InstanceID)
			case "unpublished saved load":
				e.exec(t, "UPDATE spawns SET state='pending' WHERE instance_id=?", e.binding.InstanceID)
				e.exec(t, "UPDATE cursor_runtimes SET bound_session_id='' WHERE bound_instance_id=?", e.binding.InstanceID)
			case "disagreement":
				e.exec(t, "UPDATE spawns SET session_id='different-session' WHERE instance_id=?", e.binding.InstanceID)
			case "retired":
				if ok, err := e.s.MarkCursorRuntimeGone(e.binding); err != nil || !ok {
					t.Fatalf("retire: %v %v", ok, err)
				}
			case "published pending":
				e.exec(t, "UPDATE spawns SET state='pending' WHERE instance_id=?", e.binding.InstanceID)
			}
			_, rpc := startRestartMCP(t, registration)
			accepted := change == "published pending" || change == "current confirmed session"
			restartReply(t, rpc, accepted)
			if accepted {
				if change == "current confirmed session" {
					e.exec(t, "UPDATE spawns SET session_id='current-session' WHERE instance_id=?", e.binding.InstanceID)
					e.exec(t, "UPDATE cursor_runtimes SET bound_session_id='current-session' WHERE bound_instance_id=?", e.binding.InstanceID)
				}
				restartUsefulGet(t, rpc, e.binding.InstanceID)
			}
			if observation.Wait() == nil {
				t.Fatal("expired observation accepted a replacement receipt")
			}
		})
	}
}

func TestCursorGuidanceReplacementChecksHaveIndependentBounds(t *testing.T) {
	// Serial: scoped CLI and executable pipe holder own HOME/PATH.
	for _, mode := range []string{"delayed initialized", "setup expires", "persistent cut", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			e := newRequestEnv(t, "off", "published-session")
			registration, observation, until := restartRegistration(t, e, mode == "setup expires")
			marker := heldGuidanceQuery(t, "list-panes", false)
			if mode != "persistent cut" {
				if err := os.Remove(marker); err != nil {
					t.Fatal(err)
				}
			}
			started := time.Now()
			child, rpc := startRestartMCP(t, registration)
			if mode == "persistent cut" {
				restartReply(t, rpc, false)
				elapsed := time.Since(started)
				if elapsed < 650*time.Millisecond || elapsed > 1500*time.Millisecond {
					t.Fatalf("persistent cuts did not honor one 700ms allowance: %v", elapsed)
				}
			} else {
				restartReply(t, rpc, true)
				if mode == "setup expires" {
					time.Sleep(time.Until(until) + time.Millisecond)
				} else if mode == "delayed initialized" {
					time.Sleep(750 * time.Millisecond)
				}
				if err := os.WriteFile(marker, nil, 0600); err != nil {
					t.Fatal(err)
				}
				if err := rpc.notify("notifications/initialized", map[string]any{}); err != nil {
					t.Fatal(err)
				}
				if mode != "setup expires" {
					waitRestartFile(t, filepath.Join(filepath.Dir(marker), "pids"))
				}
				if mode == "canceled" {
					if err := syscall.Kill(child.pid, syscall.SIGTERM); err != nil {
						t.Fatal(err)
					}
				}
				if mode == "delayed initialized" {
					time.Sleep(90 * time.Millisecond)
				}
				if err := os.Remove(marker); err != nil {
					t.Fatal(err)
				}
				if _, err := rpc.request("tools/call", map[string]any{"name": "get", "arguments": map[string]string{"instance_id": e.binding.InstanceID}}); err != nil {
					t.Fatal(err)
				}
				restartReply(t, rpc, mode == "delayed initialized")
			}
			if observation.Wait() == nil {
				t.Fatal("verification revived expired setup acknowledgement")
			}
			binding, err := e.s.ReadCursorRuntime(e.binding.InstanceID)
			if err != nil || binding != e.binding {
				t.Fatalf("read-only replacement verification changed publication: %+v %v", binding, err)
			}
		})
	}
}

func TestCursorGuidanceReplacementRechecksPublicationAfterPane(t *testing.T) {
	// Serial: protected runtime and gated complete pane query own HOME/PATH.
	for _, change := range []string{"disagreement", "unpublished", "retired"} {
		t.Run(change, func(t *testing.T) {
			e := newRequestEnv(t, "off", "published-session")
			registration, observation, _ := restartRegistration(t, e, false)
			dir := t.TempDir()
			entered := filepath.Join(dir, "entered")
			release := filepath.Join(dir, "release")
			complete := filepath.Join(dir, "complete")
			evidence := filepath.Join(dir, "pane-evidence")
			script := fmt.Sprintf(`#!/bin/sh
set -eu
case " $* " in
 *" list-panes "*)
  touch %q
  while [ ! -f %q ]; do sleep .001; done
  %q "$@" > %q
  cat %q
  printf 'exit=0\n' > %q.pending
  mv %q.pending %q
  exit 0;;
esac
exec %q "$@"
`, entered, release, faketmuxfix.Binary(t), evidence, evidence, complete, complete, complete, faketmuxfix.Binary(t))
			if err := os.WriteFile(filepath.Join(dir, "tmux"), []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
			_, rpc := startRestartMCP(t, registration)
			waitRestartFile(t, entered)
			switch change {
			case "disagreement":
				e.exec(t, "UPDATE spawns SET session_id='other' WHERE instance_id=?", e.binding.InstanceID)
			case "unpublished":
				e.exec(t, "UPDATE cursor_runtimes SET bound_session_id='' WHERE bound_instance_id=?", e.binding.InstanceID)
			case "retired":
				if ok, err := e.s.MarkCursorRuntimeGone(e.binding); err != nil || !ok {
					t.Fatalf("retire: %v %v", ok, err)
				}
			}
			if err := os.WriteFile(release, nil, 0600); err != nil {
				t.Fatal(err)
			}
			waitRestartFile(t, complete)
			status, err := os.ReadFile(complete)
			if err != nil || string(status) != "exit=0\n" {
				t.Fatalf("gated pane query did not complete successfully: %q %v", status, err)
			}
			output, err := os.ReadFile(evidence)
			want := fmt.Sprintf("$0\t0\t0\t%%0\t%d\t%s %%0\n", os.Getpid(), e.binding.Token)
			if err != nil || string(output) != want {
				t.Fatalf("gated query did not emit complete matching pane evidence: %q, want %q: %v", output, want, err)
			}
			restartReply(t, rpc, false)
			if observation.Wait() == nil {
				t.Fatal("late pane result acknowledged retired publication")
			}
		})
	}
}
