//go:build linux

package api_test

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/cursorfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
	"github.com/google/uuid"
)

const advCursorHome = "keep HOME unchanged; for Cursor configuration/data with the existing personal login, set absolute CURSOR_CONFIG_DIR and CURSOR_DATA_DIR in extra_env; these directories do not isolate account credentials"

// A14 and E6 Task1: follow the directory advice only for the caller's intended
// personal-login configuration. The recorder proves composition, not native auth.
func TestAdviceFollow_A14_CursorHomeAndLiteralDirectories(t *testing.T) {
	// Serial: newSpawnEnv owns HOME and the process environment.
	for _, operation := range []string{"spawn", "template"} {
		for _, key := range []string{"HOME", "CURSOR_CONFIG_DIR", "CURSOR_DATA_DIR"} {
			t.Run(operation+"/"+key, func(t *testing.T) {
				e := newSpawnEnv(t)
				root := t.TempDir()
				selected := map[string]string{
					"CURSOR_CONFIG_DIR": filepath.Join(root, "literal $CONFIG ~ config"),
					"CURSOR_DATA_DIR":   filepath.Join(root, "literal $DATA ~ data"),
				}
				// Each directory is independently optional, as the final prose says.
				if key == "CURSOR_CONFIG_DIR" {
					delete(selected, "CURSOR_DATA_DIR")
				} else if key == "CURSOR_DATA_DIR" {
					delete(selected, "CURSOR_CONFIG_DIR")
				}
				invalid := map[string]string{key: "relative/../selection"}
				p := api.SpawnParams{Agent: store.AgentCursor, CWD: t.TempDir(), NoPreTrust: true, ExtraEnv: invalid}
				var err error
				if operation == "template" {
					_, err = e.c.MakeTemplate(api.MakeTemplateParams{Name: "literal-selection", Agent: store.AgentCursor, ExtraEnv: invalid})
				} else {
					_, err = e.c.Spawn(p)
				}
				assertOneName(t, err, "ErrReservedEnvKey")
				if key == "HOME" {
					adviceAssertPhrase(t, err, `remove "HOME" from extra_env, and `+advCursorHome)
				} else {
					adviceAssertPhrase(t, err, fmt.Sprintf("extra_env key %s requires a nonempty absolute, lexically clean directory path without control bytes; supply its literal path without expansion", key))
				}
				assertNoTmuxCalls(t, e.rec)
				if len(listIDs(t, e.c)) != 0 {
					t.Fatal("refusal created a row")
				}
				if operation == "template" {
					if _, err := os.Stat(filepath.Join(adConfigDir(e.home), "templates", "literal-selection.toml")); !os.IsNotExist(err) {
						t.Fatalf("refused template written: %v", err)
					}
					if _, err := e.c.MakeTemplate(api.MakeTemplateParams{Name: "literal-selection", Agent: store.AgentCursor, ExtraEnv: selected}); err != nil {
						t.Fatal(err)
					}
					p.Template, p.ExtraEnv = "literal-selection", nil
				} else {
					p.ExtraEnv = selected
				}
				fresh, err := e.c.Spawn(p)
				if err != nil || fresh.InstanceID == "" {
					t.Fatalf("literal correction: %+v %v", fresh, err)
				}
				adviceCursorSelection(t, e, selected)
			})
		}
	}
}

func adviceCursorSelection(t *testing.T, e spawnEnv, selected map[string]string) {
	t.Helper()
	calls := e.rec.SocketCallsOf(tmux.CallCreate)
	if len(calls) != 1 {
		t.Fatalf("creates = %d; want one accepted launch", len(calls))
	}
	for _, key := range []string{"CURSOR_CONFIG_DIR", "CURSOR_DATA_DIR", "CURSOR_API_KEY", "AGENT_CLI_CREDENTIAL_STORE"} {
		got, present := calls[0].Envs[key]
		want, supplied := selected[key]
		if got != want || present != supplied {
			t.Fatalf("%s = %q, present %v; want literal %q, supplied %v", key, got, present, want, supplied)
		}
		if supplied {
			if _, err := os.Stat(want); !os.IsNotExist(err) {
				t.Fatalf("selection created a directory: %q: %v", want, err)
			}
		}
	}
	if os.Getenv("HOME") != e.home {
		t.Fatal("correction changed HOME")
	}
	if home, present := calls[0].Envs["HOME"]; present && home != e.home {
		t.Fatalf("launch changed HOME to %q", home)
	}
}

// B15: "spawn --agent cursor --cwd \"$PWD\"" with an automatically generated unique instance_id.
func TestAdviceFollow_B15_CursorResumeFreshUniqueInstance(t *testing.T) {
	// Serial: newSpawnEnv owns HOME and process environment.
	for _, kind := range []string{"pending no session", "finished no session", "HOME", "invalid env", "missing transcript", "CURSOR_CONFIG_DIR", "CURSOR_DATA_DIR", "CURSOR_API_KEY", "AGENT_CLI_CREDENTIAL_STORE"} {
		t.Run(kind, func(t *testing.T) {
			e := newSpawnEnv(t)
			id := uuid.NewString()
			h := cursorfix.NewGatewayHistory(t, id, 1, "latest", "older")
			state, session := store.StateEnded, "latest"
			var env map[string]string
			switch kind {
			case "pending no session":
				state, session = store.StatePending, ""
			case "finished no session":
				session = ""
			case "HOME":
				env = map[string]string{"HOME": "/saved-invalid-home"}
			case "invalid env":
				env = map[string]string{"BROKEN=NAME": "value"}
			case "CURSOR_CONFIG_DIR", "CURSOR_DATA_DIR":
				env = map[string]string{kind: "relative/../selection"}
			case "CURSOR_API_KEY", "AGENT_CLI_CREDENTIAL_STORE":
				env = map[string]string{kind: ""}
			case "missing transcript":
				if err := os.Remove(h.Latest); err != nil {
					t.Fatal(err)
				}
			}
			_, err := apitest.SeedSpawn(e.dbPath, id, state, t.TempDir(), "off", session, false, apitest.WithAgent(store.AgentCursor), apitest.WithJsonlPath(h.Latest), apitest.WithExtraEnv(env),
				apitest.WithSessionHistory(apitest.SessionHistorySeed{SessionID: "older", JSONLPath: h.Older, Life: 1}))
			if err != nil {
				t.Fatal(err)
			}
			before, err := apitest.ReadSpawnColumns(e.dbPath, id)
			if err != nil {
				t.Fatal(err)
			}
			history, err := apitest.ReadSessionHistoryAllLives(e.dbPath, id)
			if err != nil {
				t.Fatal(err)
			}
			mark := trailMark(t)
			_, err = e.c.Resume(api.ResumeParams{InstanceID: id})
			phrase := `from the intended project directory run agent-director spawn --agent cursor --cwd "$PWD" for a fresh conversation with an automatically generated unique instance_id`
			selectionRefusal := kind == "CURSOR_CONFIG_DIR" || kind == "CURSOR_DATA_DIR" || kind == "CURSOR_API_KEY" || kind == "AGENT_CLI_CREDENTIAL_STORE"
			if selectionRefusal {
				adviceAssertPhrase(t, err, "nothing was written and nothing was launched; resume cannot change the saved environment; start a new instance with a supported explicit selection only if it matches the intended account and configuration")
				if kind == "CURSOR_API_KEY" || kind == "AGENT_CLI_CREDENTIAL_STORE" {
					adviceAssertPhrase(t, err, fmt.Sprintf("extra_env key %s selects an unreleased Cursor credential profile; this account-selection intent cannot be launched", kind))
				} else {
					adviceAssertPhrase(t, err, fmt.Sprintf("extra_env key %s requires a nonempty absolute, lexically clean directory path without control bytes; supply its literal path without expansion", kind))
				}
			} else {
				adviceAssertPhrase(t, err, phrase)
			}
			if kind == "HOME" {
				adviceAssertPhrase(t, err, advCursorHome)
			}
			want := "ErrJsonlMissing"
			switch kind {
			case "pending no session":
				want = "ErrSpawnNotResumable"
			case "finished no session":
				want = "ErrNoSessionId"
			case "HOME", "invalid env", "CURSOR_CONFIG_DIR", "CURSOR_DATA_DIR", "CURSOR_API_KEY", "AGENT_CLI_CREDENTIAL_STORE":
				want = "ErrReservedEnvKey"
			}
			assertOneName(t, err, want)
			assertNoTmuxCalls(t, e.rec)
			assertNoTrailSince(t, mark, id)
			// Pending advice requires the old launch to be reconciled before fresh recovery.
			if kind == "pending no session" {
				cursorReadSQL(t, e.dbPath, `UPDATE spawns SET state='missing' WHERE instance_id=?`, id)
				before, err = apitest.ReadSpawnColumns(e.dbPath, id)
				if err != nil {
					t.Fatal(err)
				}
			}
			cwd := t.TempDir()
			// Credential intent follows STOP: never erase it and personally retry.
			// A human must choose any different intent; this fixture cannot authenticate.
			if kind != "CURSOR_API_KEY" && kind != "AGENT_CLI_CREDENTIAL_STORE" {
				var selected map[string]string
				if kind == "HOME" || kind == "CURSOR_CONFIG_DIR" || kind == "CURSOR_DATA_DIR" {
					root := t.TempDir()
					selected = map[string]string{"CURSOR_CONFIG_DIR": filepath.Join(root, "intended config"), "CURSOR_DATA_DIR": filepath.Join(root, "intended data")}
				}
				fresh, err := e.c.Spawn(api.SpawnParams{Agent: store.AgentCursor, CWD: cwd, ExtraEnv: selected, NoPreTrust: true})
				if err != nil || fresh.InstanceID == id || fresh.InstanceID == "" {
					t.Fatalf("literal fresh remedy: %+v %v", fresh, err)
				}
				if selected != nil {
					adviceCursorSelection(t, e, selected)
				}
			} else if ids := listIDs(t, e.c); len(ids) != 1 || ids[0] != id {
				t.Fatalf("STOP branch added or replaced rows: %v", ids)
			}
			after, err := apitest.ReadSpawnColumns(e.dbPath, id)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("fresh recovery changed original row")
			}
			afterHistory, err := apitest.ReadSessionHistoryAllLives(e.dbPath, id)
			if err != nil || !reflect.DeepEqual(history, afterHistory) {
				t.Fatal("recovery changed original session history")
			}
			got, err := os.ReadFile(h.Older)
			if err != nil || !bytes.Equal(got, h.OlderBytes) {
				t.Fatal("fresh recovery changed retained artifact")
			}
			if kind != "missing transcript" {
				got, err = os.ReadFile(h.Latest)
				if err != nil || !bytes.Equal(got, h.LatestBytes) {
					t.Fatal("fresh recovery changed latest artifact")
				}
			}
		})
	}
}
