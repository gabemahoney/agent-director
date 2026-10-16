//go:build linux

package api_test

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/cursorfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
	"github.com/google/uuid"
)

// Synthetic gateway-only history: native layout/recall provenance is not established here.
func seedCursorResume(t *testing.T, e *resumeEnv, state string) (string, cursorfix.GatewayHistory) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	installCursorFixtureGateway(t)
	id := "resume-cursor-" + uuid.NewString()
	h := cursorfix.NewGatewayHistory(t, id, 3, "latest-cursor", "older-cursor")
	_, err := apitest.SeedSpawn(e.dbPath, id, state, t.TempDir(), "off", "latest-cursor", false,
		apitest.WithAgent(store.AgentCursor), apitest.WithJsonlPath(h.Latest), apitest.WithLifeNumber(3),
		apitest.WithTmuxSocket(e.socket), apitest.WithNoPreTrust(),
		apitest.WithRawAgentArgs(`["--model","saved-model","saved initial"]`),
		apitest.WithExtraEnv(map[string]string{"SAVED_CURSOR_ENV": "retained", "AGENT_DIRECTOR_GROUP": "saved-group"}),
		apitest.WithSessionHistory(apitest.SessionHistorySeed{SessionID: "older-cursor", JSONLPath: h.Older, Life: 3}))
	if err != nil {
		t.Fatal(err)
	}
	return id, h
}

func TestCursorResumeActualStoreOverride(t *testing.T) {
	// Serial: isolated protected HOME and gateway installation.
	for _, surface := range []string{"Client", "Path forwarding wrapper"} {
		t.Run(surface, func(t *testing.T) {
			e := newResumeEnv(t)
			id, _ := seedCursorResume(t, e, store.StateEnded)
			defaultPath := filepath.Join(os.Getenv("HOME"), ".config", "agent-director", "state.db")
			if _, err := apitest.InitStore(defaultPath); err != nil {
				t.Fatal(err)
			}
			before := e.columns(t, id)
			var err error
			if surface == "Client" {
				_, err = e.c.Resume(api.ResumeParams{InstanceID: id})
			} else {
				_, err = e.resume(id)
			}
			if err != nil {
				t.Fatal(err)
			}
			calls := e.rec.SocketCallsOf(tmux.CallCreate)
			if len(calls) != 1 || calls[0].Envs["AGENT_DIRECTOR_CURSOR_STORE_PATH"] != e.dbPath || e.dbPath == defaultPath {
				t.Fatalf("actual override lost: %+v default=%s", calls, defaultPath)
			}
			after := e.columns(t, id)
			if after.State != store.StatePending || after.ExtraEnv != before.ExtraEnv {
				t.Fatalf("saved configuration changed: %+v", after)
			}
			db, err := store.Open(defaultPath)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if row, err := db.GetSpawn(id); err == nil || row.InstanceID != "" {
				t.Fatalf("default store touched: %+v %v", row, err)
			}
		})
	}
}

func TestCursorResumeUnavailableGatewayRefusesBeforeMutation(t *testing.T) {
	// Serial: private HOME and gateway file permissions.
	for _, kind := range []string{"missing", "nonexecutable", "dangling symlink", "directory"} {
		t.Run(kind, func(t *testing.T) {
			e := newResumeEnv(t)
			id, _ := seedCursorResume(t, e, store.StateEnded)
			path := filepath.Join(os.Getenv("HOME"), ".config", "agent-director", "bin", "agent-director")
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "nonexecutable":
				if err := os.WriteFile(path, []byte("fixture"), 0600); err != nil {
					t.Fatal(err)
				}
			case "dangling symlink":
				if err := os.Symlink(path+"-missing", path); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			}
			before := e.columns(t, id)
			if _, err := e.c.Resume(api.ResumeParams{InstanceID: id}); err == nil {
				t.Fatal("unavailable gateway accepted")
			}
			if !reflect.DeepEqual(before, e.columns(t, id)) || len(e.rec.SocketCallsOf(tmux.CallCreate)) != 0 {
				t.Fatal("refusal mutated or launched")
			}
		})
	}
}

func TestCursorResumeLatestGateway(t *testing.T) {
	// Serial: the protected transcript fixture sets HOME.
	for _, tc := range []struct{ state, path string }{
		{store.StateEnded, `/current/"cursor agent" --flag`},
		{store.StateMissing, "/current/second-agent"},
		{store.StateEnded, ""},
	} {
		t.Run(tc.state+"/"+tc.path, func(t *testing.T) {
			e := newResumeEnv(t)
			id, h := seedCursorResume(t, e, tc.state)
			e.cfg.Launch.CursorPath = tc.path
			before := e.columns(t, id)
			history, err := e.st.ListSessionHistory(id, 3)
			if err != nil {
				t.Fatal(err)
			}
			result, err := e.resume(id)
			if err != nil || result.InstanceID != id || result.PreTrust != "skipped" {
				t.Fatalf("resume: %+v %v", result, err)
			}
			after := e.columns(t, id)
			if after.State != store.StatePending || after.SessionID != before.SessionID || after.JSONLPath != before.JSONLPath || after.LifeNumber != before.LifeNumber || after.AgentArgs != before.AgentArgs || after.ExtraEnv != before.ExtraEnv {
				t.Fatalf("saved identity/config changed: before=%+v after=%+v", before, after)
			}
			gotHistory, err := e.st.ListSessionHistory(id, 3)
			if err != nil || !reflect.DeepEqual(history, gotHistory) {
				t.Fatalf("history changed: %+v %v", gotHistory, err)
			}
			creates := e.rec.SocketCallsOf(tmux.CallCreate)
			if len(creates) != 1 || creates[0].Socket != e.socket {
				t.Fatalf("launch count/socket: %+v", creates)
			}
			wantRuntime := tc.path
			if wantRuntime == "" {
				wantRuntime = "agent"
			}
			at := slices.Index(creates[0].Command, "--runtime")
			if at < 0 || at+1 >= len(creates[0].Command) || creates[0].Command[at+1] != wantRuntime {
				t.Fatalf("current whole runtime lost: %q; want %q", creates[0].Command, wantRuntime)
			}
			if creates[0].Envs["SAVED_CURSOR_ENV"] != "retained" || creates[0].Envs["AGENT_DIRECTOR_GROUP"] != "saved-group" || after.CWD != before.CWD {
				t.Fatalf("saved cwd/env/group lost: %+v", creates[0])
			}
			for path, want := range map[string][]byte{h.Latest: h.LatestBytes, h.Older: h.OlderBytes} {
				got, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(got, want) {
					t.Fatalf("artifact changed: %s %v", path, err)
				}
			}
		})
	}
}

func TestCursorResumeProtectedPathRefusesBeforeMutation(t *testing.T) {
	// Serial: the protected transcript fixture sets HOME.
	for _, kind := range []string{"missing", "symlink", "public file", "public directory", "directory"} {
		t.Run(kind, func(t *testing.T) {
			e := newResumeEnv(t)
			id, h := seedCursorResume(t, e, store.StateEnded)
			switch kind {
			case "missing":
				if err := os.Remove(h.Latest); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				target := filepath.Join(t.TempDir(), "preserved.jsonl")
				if err := os.WriteFile(target, h.LatestBytes, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(h.Latest); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, h.Latest); err != nil {
					t.Fatal(err)
				}
			case "public file":
				if err := os.Chmod(h.Latest, 0644); err != nil {
					t.Fatal(err)
				}
			case "public directory":
				if err := os.Chmod(filepath.Dir(h.Latest), 0755); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Remove(h.Latest); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(h.Latest, 0700); err != nil {
					t.Fatal(err)
				}
			}
			before := e.columns(t, id)
			_, err := e.resume(id)
			if err == nil {
				t.Fatal("unsafe/missing current path accepted")
			}
			if !reflect.DeepEqual(before, e.columns(t, id)) {
				t.Fatal("preflight refusal mutated row")
			}
			if len(e.rec.SocketCallsOf(tmux.CallCreate)) != 0 {
				t.Fatal("preflight refusal launched")
			}
			got, err := os.ReadFile(h.Older)
			if err != nil || !bytes.Equal(got, h.OlderBytes) {
				t.Fatal("older artifact changed")
			}
		})
	}
}

func TestCursorResumeCreateFailureRestoresExaminedRow(t *testing.T) {
	// Serial: the protected transcript fixture sets HOME.
	e := newResumeEnv(t)
	id, _ := seedCursorResume(t, e, store.StateEnded)
	before := e.columns(t, id)
	e.rec.Script(e.socket, tmuxfix.Script{Failure: tmux.FailUnrecognized, ExitStatus: 1, Times: 1}, tmux.CallCreate)
	_, err := e.resume(id)
	if err == nil {
		t.Fatal("injected create failure ignored")
	}
	after := e.columns(t, id)
	if after.RowVersion != before.RowVersion.(int64)+2 {
		t.Fatalf("restore version=%v want %d", after.RowVersion, before.RowVersion.(int64)+2)
	}
	before.RowVersion = after.RowVersion
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("restore changed saved row: before=%+v after=%+v", before, after)
	}
}

func TestCursorResumeLiveRowStillRefuses(t *testing.T) {
	// Serial: the protected transcript fixture sets HOME.
	for _, state := range []string{store.StateWaiting, store.StateWorking, store.StateCheckPermission} {
		t.Run(state, func(t *testing.T) {
			e := newResumeEnv(t)
			id, _ := seedCursorResume(t, e, state)
			before := e.columns(t, id)
			_, err := e.resume(id)
			assertOneName(t, err, "ErrSpawnNotResumable")
			if !reflect.DeepEqual(before, e.columns(t, id)) {
				t.Fatal("live refusal mutated row")
			}
			if len(e.rec.SocketCallsOf(tmux.CallCreate)) != 0 {
				t.Fatal("live refusal launched")
			}
		})
	}
}

func TestCursorResumeNativeArtifactsRemainRuntimeOwned(t *testing.T) {
	// Serial: the protected transcript fixture sets HOME.
	for _, entry := range []struct{ name, state string }{
		{"synthetic graceful stop", store.StateEnded},
		{"synthetic gateway crash", store.StateMissing},
		{"synthetic runtime crash", store.StateMissing},
	} {
		t.Run(entry.name, func(t *testing.T) {
			for _, missing := range []bool{false, true} {
				t.Run(map[bool]string{false: "native retained", true: "latest native removed"}[missing], func(t *testing.T) {
					e := newResumeEnv(t)
					id, gateway := seedCursorResume(t, e, entry.state)
					before := e.columns(t, id)
					native := cursorfix.NewNativeHistory(t, before.CWD.(string), "latest-cursor", "older-cursor")
					// Point a hypothetical native probe at the isolated store. Production preflight owns only the protected transcript.
					t.Setenv("CURSOR_CONFIG_DIR", native.ConfigRoot)
					if missing {
						native.Latest.RemoveDatabase(t)
					}
					result, err := e.resume(id)
					if err != nil || result.InstanceID != id {
						t.Fatalf("resume preflight: %+v %v", result, err)
					}
					after := e.columns(t, id)
					if after.State != store.StatePending || after.SessionID != before.SessionID || after.JSONLPath != before.JSONLPath {
						t.Fatalf("latest identity changed: %+v", after)
					}
					if len(e.rec.SocketCallsOf(tmux.CallCreate)) != 1 {
						t.Fatal("resume did not launch exactly once")
					}
					for path, want := range map[string][]byte{native.Older.Database: native.Older.DatabaseBytes, native.Older.Metadata: native.Older.MetadataBytes, native.Latest.Metadata: native.Latest.MetadataBytes, gateway.Latest: gateway.LatestBytes, gateway.Older: gateway.OlderBytes} {
						got, err := os.ReadFile(path)
						if err != nil || !bytes.Equal(got, want) {
							t.Fatalf("artifact changed: %s %v", path, err)
						}
					}
					if missing {
						if _, err := os.Stat(native.Latest.Database); !os.IsNotExist(err) {
							t.Fatalf("preflight recreated absent native DB: %v", err)
						}
					} else {
						got, err := os.ReadFile(native.Latest.Database)
						if err != nil || !bytes.Equal(got, native.Latest.DatabaseBytes) {
							t.Fatalf("native DB changed: %v", err)
						}
					}
				})
			}
		})
	}
}
