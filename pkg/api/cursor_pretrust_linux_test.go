//go:build linux

package api_test

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
	"github.com/google/uuid"
)

func checkCursorPreTrust(t *testing.T, result any, warning string, off bool, command []string) {
	t.Helper()
	want := "failed"
	wantWarning := "agent-director: Cursor ACP pre-trust is unsupported; no trust grant was made\n"
	if off {
		want = "skipped"
		wantWarning = ""
	}
	checkPreTrustJSON(t, result, want)
	if warning != wantWarning {
		t.Fatalf("warning=%q want=%q", warning, wantWarning)
	}
	for _, flag := range []string{"--trust", "--force", "--yolo", "--dangerously-skip-permissions", "--dangerously-bypass-approvals-and-sandbox"} {
		if slices.Contains(command, flag) {
			t.Fatalf("trust outcome bypasses runtime policy: %q", command)
		}
	}
}

func cursorTrustSentinel(t *testing.T) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cursor-settings.json")
	raw := []byte(`{"workspaceTrust":false,"token":"synthetic-only"}`)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CURSOR_CONFIG_DIR", filepath.Dir(path))
	t.Cleanup(func() {
		got, err := os.ReadFile(path)
		if err != nil || !reflect.DeepEqual(got, raw) {
			t.Errorf("Cursor config changed: %q %v", got, err)
		}
		entries, err := os.ReadDir(filepath.Dir(path))
		if err != nil || len(entries) != 1 || entries[0].Name() != filepath.Base(path) {
			t.Errorf("Cursor config directory changed: %v %v", entries, err)
		}
	})
}

func TestCursorSpawnPreTrust(t *testing.T) {
	// Serial: fresh spawn factory sets isolated HOME and tmux environment; captures stderr.
	for _, off := range []bool{false, true} {
		t.Run(map[bool]string{false: "unsupported", true: "opted out"}[off], func(t *testing.T) {
			e := newSpawnEnv(t)
			cursorTrustSentinel(t)
			var result api.SpawnResult
			var err error
			warning := captureStderr(t, func() {
				result, err = e.c.Spawn(api.SpawnParams{Agent: "cursor", InstanceID: "cursor-trust-" + uuid.NewString(), CWD: t.TempDir(), NoPreTrust: off})
			})
			if err != nil {
				t.Fatal(err)
			}
			calls := e.rec.SocketCallsOf(tmux.CallCreate)
			if len(calls) != 1 {
				t.Fatalf("creates=%d", len(calls))
			}
			checkCursorPreTrust(t, result, warning, off, calls[0].Command)
			row, err := apitest.ReadSpawnColumns(e.dbPath, result.InstanceID)
			want := int64(0)
			if off {
				want = 1
			}
			if err != nil || row.NoPreTrust != want || row.State != store.StatePending || row.Agent != "cursor" {
				t.Fatalf("row=%+v err=%v", row, err)
			}
			if calls[0].Envs["AGENT_DIRECTOR_CURSOR_STORE_PATH"] != e.dbPath {
				t.Fatal("trust opt-out disabled mandatory reporting")
			}
		})
	}
}

func TestCursorReusePreTrustReplacesSavedChoice(t *testing.T) {
	// Serial: private HOME/gateway, reusable factory environment and stderr capture.
	for _, off := range []bool{false, true} {
		for _, fail := range []bool{false, true} {
			t.Run(strings.Join([]string{map[bool]string{false: "attempt", true: "skip"}[off], map[bool]string{false: "success", true: "restore"}[fail]}, "/"), func(t *testing.T) {
				t.Setenv("HOME", t.TempDir())
				installCursorFixtureGateway(t)
				cursorTrustSentinel(t)
				e := newReuseEnv(t)
				r := e.seedReusable(t, agentGone, reuseRowSpec{Bare: true, NoPreTrust: !off, Opts: []apitest.SpawnOption{apitest.WithAgent("cursor"), apitest.WithRawAgentArgs(`[]`)}})
				before := e.columns(t, r.ID)
				p := reuseParams(t, r, reuseRequest{NoPreTrust: off})
				p.Agent = "cursor"
				if fail {
					e.rec.Script(tmuxfix.AnySocket, tmuxfix.Script{Failure: tmux.FailUnrecognized, ExitStatus: 1, Times: 1}, tmux.CallCreate)
				}
				var result api.SpawnResult
				var err error
				warning := captureStderr(t, func() { result, _, err = e.reuse(t, p) })
				if fail {
					if err == nil {
						t.Fatal("create failure ignored")
					}
					after := e.columns(t, r.ID)
					if after.NoPreTrust != before.NoPreTrust || after.State != before.State {
						t.Fatalf("failed reuse lost old choice: before=%+v after=%+v", before, after)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				calls := e.rec.SocketCallsOf(tmux.CallCreate)
				if len(calls) != 1 {
					t.Fatalf("creates=%d", len(calls))
				}
				checkCursorPreTrust(t, result, warning, off, calls[0].Command)
				want := int64(0)
				if off {
					want = 1
				}
				if row := e.columns(t, r.ID); row.NoPreTrust != want || row.State != store.StatePending {
					t.Fatalf("replacement choice=%+v", row)
				}
				if calls[0].Envs["AGENT_DIRECTOR_CURSOR_STORE_PATH"] != e.dbPath {
					t.Fatal("reuse trust choice disabled reporting")
				}
			})
		}
	}
}

func TestCursorResumePreTrustRetainsSavedChoice(t *testing.T) {
	// Serial: protected-history fixture sets HOME; captures stderr and changes current default.
	for _, off := range []bool{false, true} {
		for _, retry := range []bool{false, true} {
			t.Run(strings.Join([]string{map[bool]string{false: "attempt", true: "skip"}[off], map[bool]string{false: "success", true: "restore retry"}[retry]}, "/"), func(t *testing.T) {
				e := newResumeEnv(t)
				id, _ := seedCursorResume(t, e, store.StateEnded)
				cursorTrustSentinel(t)
				choice := 0
				if off {
					choice = 1
				}
				cursorReadSQL(t, e.dbPath, "UPDATE spawns SET no_pre_trust=? WHERE instance_id=?", choice, id)
				before := e.columns(t, id)
				if retry {
					e.rec.Script(tmuxfix.AnySocket, tmuxfix.Script{Failure: tmux.FailUnrecognized, ExitStatus: 1, Times: 1}, tmux.CallCreate)
					var err error
					captureStderr(t, func() { _, err = e.resume(id) })
					if err == nil {
						t.Fatal("create failure ignored")
					}
					row := e.columns(t, id)
					if row.NoPreTrust != before.NoPreTrust || row.State != before.State {
						t.Fatalf("restore lost choice: %+v", row)
					}
				}
				var result api.ResumeResult
				var err error
				warning := captureStderr(t, func() { result, err = e.resume(id) })
				if err != nil {
					t.Fatal(err)
				}
				calls := e.rec.SocketCallsOf(tmux.CallCreate)
				checkCursorPreTrust(t, result, warning, off, calls[len(calls)-1].Command)
				row := e.columns(t, id)
				if row.NoPreTrust != before.NoPreTrust || row.State != store.StatePending {
					t.Fatalf("resume replaced saved choice: %+v", row)
				}
				if calls[len(calls)-1].Envs["AGENT_DIRECTOR_CURSOR_STORE_PATH"] != e.dbPath {
					t.Fatal("resume trust choice disabled reporting")
				}
			})
		}
	}
}
