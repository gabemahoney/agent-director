//go:build linux

package api_test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

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
	for _, kind := range []string{"pending no session", "finished no session", "known directories", "known template", "protected reconstruction", "HOME", "invalid env", "missing transcript", "CURSOR_CONFIG_DIR", "CURSOR_DATA_DIR", "CURSOR_API_KEY", "AGENT_CLI_CREDENTIAL_STORE"} {
		t.Run(kind, func(t *testing.T) {
			e := newSpawnEnv(t)
			id := uuid.NewString()
			h := cursorfix.NewGatewayHistory(t, id, 1, "latest", "older")
			state, session := store.StateEnded, "latest"
			var env map[string]string
			known := map[string]string{"CURSOR_CONFIG_DIR": filepath.Join(t.TempDir(), "intended config"), "CURSOR_DATA_DIR": filepath.Join(t.TempDir(), "intended data")}
			switch kind {
			case "pending no session":
				state, session = store.StatePending, ""
			case "finished no session":
				session = ""
			case "known directories", "known template", "protected reconstruction":
				session, env = "", known
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
				apitest.WithLaunchStartedAt(e.clock.Now().UnixMilli()), apitest.WithSessionHistory(apitest.SessionHistorySeed{SessionID: "older", JSONLPath: h.Older, Life: 1}))
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
			refusal := err
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
			case "finished no session", "known directories", "known template", "protected reconstruction":
				want = "ErrNoSessionId"
			case "HOME", "invalid env", "CURSOR_CONFIG_DIR", "CURSOR_DATA_DIR", "CURSOR_API_KEY", "AGENT_CLI_CREDENTIAL_STORE":
				want = "ErrReservedEnvKey"
			}
			assertOneName(t, err, want)
			assertNoTmuxCalls(t, e.rec)
			assertNoTrailSince(t, mark, id)
			// Pending advice requires the old launch to be reconciled before fresh recovery.
			if kind == "pending no session" {
				row, getErr := e.c.Get(id)
				if getErr != nil {
					t.Fatal(getErr)
				}
				if row.State != store.StateEnded && row.State != store.StateMissing {
					if _, killErr := e.c.Kill(api.KillParams{InstanceID: id}); killErr != nil {
						t.Fatal(killErr)
					}
					e.clock.Advance(fmGrace + time.Second)
					if _, findErr := e.c.FindMissing(context.Background()); findErr != nil {
						t.Fatal(findErr)
					}
					row, getErr = e.c.Get(id)
				}
				if getErr != nil || (row.State != store.StateEnded && row.State != store.StateMissing) {
					t.Fatalf("public recovery did not finish: %+v %v", row, getErr)
				}
				before, err = apitest.ReadSpawnColumns(e.dbPath, id)
				if err != nil {
					t.Fatal(err)
				}
			}
			cwd := t.TempDir()
			// Credential intent follows STOP: never erase it and personally retry.
			// A human must choose any different intent; this fixture cannot authenticate.
			if kind != "CURSOR_API_KEY" && kind != "AGENT_CLI_CREDENTIAL_STORE" && kind != "protected reconstruction" {
				var selected map[string]string
				if kind == "HOME" || kind == "CURSOR_CONFIG_DIR" || kind == "CURSOR_DATA_DIR" {
					root := t.TempDir()
					selected = map[string]string{"CURSOR_CONFIG_DIR": filepath.Join(root, "intended config"), "CURSOR_DATA_DIR": filepath.Join(root, "intended data")}
				}
				p := api.SpawnParams{Agent: store.AgentCursor, CWD: cwd, ExtraEnv: selected, NoPreTrust: true}
				if kind == "known directories" || kind == "known template" {
					// Follow the offered command's selection instruction. A bare fresh
					// command loses known intent; the received launch catches that defect.
					selected = known
					if strings.Contains(err.Error(), "add caller-known supported extra_env") {
						p.ExtraEnv = known
						if kind == "known template" {
							if _, makeErr := e.c.MakeTemplate(api.MakeTemplateParams{Name: "recovery-intent", Agent: "cursor", ExtraEnv: known}); makeErr != nil {
								t.Fatal(makeErr)
							}
							p.Template, p.ExtraEnv = "recovery-intent", nil
						}
					}
				}
				fresh, err := e.c.Spawn(p)
				if err != nil || fresh.InstanceID == id || fresh.InstanceID == "" {
					t.Fatalf("literal fresh remedy: %+v %v", fresh, err)
				}
				adviceCursorSelection(t, e, selected)
			} else if ids := listIDs(t, e.c); len(ids) != 1 || ids[0] != id {
				t.Fatalf("STOP branch added or replaced rows: %v", ids)
			}
			if kind == "CURSOR_API_KEY" || kind == "AGENT_CLI_CREDENTIAL_STORE" || kind == "protected reconstruction" {
				adviceAssertPhrase(t, err, "if intent is unavailable or reconstructing it requires protected data, STOP and ask a human; do not read or edit the protected saved environment or drop credential intent to retry personally")
				_, repeated := e.c.Resume(api.ResumeParams{InstanceID: id})
				if errText(repeated) != errText(err) {
					t.Fatalf("STOP refusal changed: %v / %v", err, repeated)
				}
				assertNoTmuxCalls(t, e.rec)
			}
			adviceAssertPhrase(t, refusal, "use agent-director get --instance-id <id> and, while the row is live, follow agent-director kill --instance-id <id> and its live-row advice")
			adviceAssertPhrase(t, refusal, "missing state or a finished-row kill no-op is not proof of exit; if public operations cannot resolve survivors, STOP and ask a human to follow docs/operator-actions.md#inspect-and-end-a-session-by-hand before any fresh launch")
			adviceAssertPhrase(t, refusal, "no selection is valid only for intended personal login without custom configuration/data; directories do not isolate account credentials")
			adviceAssertPhrase(t, refusal, "before running, add caller-known supported extra_env (CLI: repeat --extra-env KEY=VALUE) or --template NAME carrying that intent, keeping HOME unchanged")
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

func TestAdviceFollow_B15_CursorFinishedSurvivorStopsForHuman(t *testing.T) {
	t.Parallel()
	e := newKillEnv(t)
	e.rowAgent = store.AgentCursor
	r := e.seedRow(t, killRowSpec{State: store.StateEnded, CWD: t.TempDir()})
	c, _ := e.client(t)
	before := e.columns(t, r.ID)
	sessions := e.rec.Sessions(r.Socket)
	for attempt := 0; attempt < 2; attempt++ {
		_, err := c.Resume(api.ResumeParams{InstanceID: r.ID})
		assertOneName(t, err, "ErrNoSessionId")
		adviceAssertPhrase(t, err, "missing state or a finished-row kill no-op is not proof of exit; if public operations cannot resolve survivors, STOP and ask a human to follow docs/operator-actions.md#inspect-and-end-a-session-by-hand before any fresh launch")
		adviceAssertManifest(t, "resume", "", "Fresh: kill's live-row advice; unresolved survivors: STOP, human: docs/operator-actions.md#inspect-and-end-a-session-by-hand", "same HOME, known supported extra_env/template intent", "Unavailable intent/needs protected data: STOP, human; no saved-env edit/drop credential intent")
		row, getErr := c.Get(r.ID)
		if getErr != nil || row.State != store.StateEnded {
			t.Fatalf("finished read: %+v %v", row, getErr)
		}
		result, killErr := c.Kill(api.KillParams{InstanceID: r.ID})
		if killErr != nil || result.KillSent {
			t.Fatalf("finished kill must not promise survivor death: %+v %v", result, killErr)
		}
		if !reflect.DeepEqual(sessions, e.rec.Sessions(r.Socket)) {
			t.Fatal("finished kill disposed of unresolved survivor")
		}
		if len(e.rec.SocketCallsOf(tmux.CallCreate)) != 0 {
			t.Fatal("human STOP auto-launched")
		}
		e.assertRowUnchanged(t, r.ID, before)
	}
}
