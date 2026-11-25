package api_test

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/codexfix"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// erroredReadRow selects the requested row through each public read's JSON.
// Using JSON keeps these regressions buildable before b.cqr adds the field.
func erroredReadRow(t *testing.T, c *api.Client, verb, id string) map[string]any {
	t.Helper()
	var result any
	var err error
	switch verb {
	case "get":
		result, err = c.Get(id)
	case "get-many":
		result, err = c.GetMany([]string{id})
	case "list":
		result, err = c.List(api.ListParams{})
	default:
		t.Fatalf("unknown read %q", verb)
	}
	if err != nil {
		t.Fatalf("%s: %v", verb, err)
	}
	body, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(body, &wire); err != nil {
		t.Fatal(err)
	}
	if verb == "get" {
		if wire["instance_id"] != id {
			t.Fatalf("get instance_id = %v; want %s", wire["instance_id"], id)
		}
		return wire
	}
	rows, ok := wire["spawns"].([]any)
	if !ok {
		t.Fatalf("%s spawns = %v; want array", verb, wire["spawns"])
	}
	for _, value := range rows {
		row, ok := value.(map[string]any)
		if ok && row["instance_id"] == id {
			return row
		}
	}
	t.Fatalf("%s omitted row %s", verb, id)
	return nil
}

func assertLastTurnErrored(t *testing.T, row map[string]any, want any) {
	t.Helper()
	got, present := row["last_turn_errored"]
	if !present {
		t.Errorf("last_turn_errored missing; want explicit JSON %v", want)
	} else if got != want {
		t.Errorf("last_turn_errored = %#v; want %#v", got, want)
	}
}

// b.cqr: every read reports the error on an already-waiting row, and on
// both the first recovering read and a later read after working -> waiting.
func TestCodexTurnErroredReadSurfaces(t *testing.T) {
	t.Parallel()
	for _, verb := range []string{"get", "get-many", "list"} {
		for _, state := range []string{store.StateWaiting, store.StateWorking} {
			t.Run(verb+"/"+state, func(t *testing.T) {
				t.Parallel()
				e := newKillEnv(t)
				turn := codexfix.ErroredTurn
				path := codexfix.TempRolloutPath(t, turn.SessionID, true, turn.Started())
				id := uuid.NewString()
				e.seedRow(t, killRowSpec{ID: id, State: store.StateWaiting, SessionID: turn.SessionID,
					Opts: codexRowOpts(apitest.WithJsonlPath(path))})
				if state == store.StateWorking {
					codexHook(t, e.st, e.dbPath, id, "UserPromptSubmit", turn.TurnID, store.StateWorking)
				}
				if err := os.WriteFile(path, turn.Ended(), 0o600); err != nil {
					t.Fatal(err)
				}
				c, _ := e.client(t)
				for read := 1; read <= 2; read++ {
					row := erroredReadRow(t, c, verb, id)
					assertLastTurnErrored(t, row, true)
					if row["state"] != store.StateWaiting {
						t.Errorf("read %d state = %v; want waiting", read, row["state"])
					}
					if raw := mustGetSpawnState(t, e.st, id); raw != store.StateWaiting {
						t.Fatalf("after read %d raw state = %s; want waiting", read, raw)
					}
				}
			})
		}
	}
}

// b.cqr: only a later rollout boundary supersedes the error, including a
// turn started without UserPromptSubmit; a clean completion stays false.
func TestCodexTurnErroredReadBoundaries(t *testing.T) {
	t.Parallel()
	for _, verb := range []string{"get", "get-many", "list"} {
		t.Run(verb, func(t *testing.T) {
			t.Parallel()
			e := newKillEnv(t)
			turn := codexfix.ErroredTurn
			path := codexfix.TempRolloutPath(t, turn.SessionID, true, turn.Ended())
			id := uuid.NewString()
			e.seedRow(t, killRowSpec{ID: id, State: store.StateWaiting, SessionID: turn.SessionID,
				Opts: codexRowOpts(apitest.WithJsonlPath(path))})
			codexHook(t, e.st, e.dbPath, id, "UserPromptSubmit", turn.TurnID, store.StateWorking)
			c, _ := e.client(t)
			file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			thirdTurn := uuid.NewString()
			stages := []struct {
				name   string
				append []byte
				want   bool
			}{
				{"errored completion", nil, true},
				{"next start without hook", nextTaskStarted(t), false},
				{"clean completion", taskComplete(t, func(_, p map[string]any) {
					p["turn_id"] = nextTurn
					delete(p, "error")
				}), false},
				{"later errored turn", cat(
					turnEvent(t, "task_started", func(_, p map[string]any) {
						p["turn_id"], p["root_turn_id"] = thirdTurn, thirdTurn
					}),
					taskComplete(t, func(_, p map[string]any) { p["turn_id"] = thirdTurn }),
				), true},
			}
			for _, stage := range stages {
				t.Run(stage.name, func(t *testing.T) {
					if _, err := file.Write(stage.append); err != nil {
						t.Fatal(err)
					}
					assertLastTurnErrored(t, erroredReadRow(t, c, verb, id), stage.want)
				})
			}
		})
	}
}

// b.cqr: payload.error's presence decides the mark, independently of its
// message, codex_error_info, or value type; codex_error_info alone is no error.
func TestCodexTurnErroredReadPresence(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		edit func(record, payload map[string]any)
		want bool
	}{
		{"changed message", func(_, p map[string]any) {
			p["error"].(map[string]any)["message"] = "different provider message"
		}, true},
		{"changed codex_error_info", func(_, p map[string]any) {
			p["error"].(map[string]any)["codex_error_info"] = "different_error_kind"
		}, true},
		{"null error", func(_, p map[string]any) { p["error"] = nil }, true},
		{"empty error", func(_, p map[string]any) { p["error"] = map[string]any{} }, true},
		{"scalar error", func(_, p map[string]any) { p["error"] = false }, true},
		{"codex_error_info alone", func(_, p map[string]any) {
			p["codex_error_info"] = p["error"].(map[string]any)["codex_error_info"]
			delete(p, "error")
		}, false},
		{"error outside payload", func(r, p map[string]any) {
			r["error"] = p["error"]
			delete(p, "error")
		}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newKillEnv(t)
			turn := codexfix.ErroredTurn
			path := codexfix.TempRolloutPath(t, turn.SessionID, true,
				cat(turn.Started(), taskComplete(t, tc.edit)))
			id := uuid.NewString()
			e.seedRow(t, killRowSpec{ID: id, State: store.StateWaiting, SessionID: turn.SessionID,
				Opts: codexRowOpts(apitest.WithJsonlPath(path))})
			c, _ := e.client(t)
			assertLastTurnErrored(t, erroredReadRow(t, c, "get", id), tc.want)
		})
	}
}

// b.cqr: unsupported agents and unavailable Codex evidence carry explicit
// null on every read surface; removing a rollout supersedes an observed error.
func TestCodexTurnErroredReadUnknown(t *testing.T) {
	t.Parallel()
	for _, agent := range []string{"claude", "codex"} {
		t.Run(agent, func(t *testing.T) {
			t.Parallel()
			e := newKillEnv(t)
			id := uuid.NewString()
			spec := killRowSpec{ID: id, State: store.StateWaiting}
			var rollout string
			if agent == "codex" {
				turn := codexfix.ErroredTurn
				rollout = codexfix.TempRolloutPath(t, turn.SessionID, true, turn.Ended())
				spec.SessionID, spec.Opts = turn.SessionID, codexRowOpts(apitest.WithJsonlPath(rollout))
			}
			e.seedRow(t, spec)
			c, _ := e.client(t)
			if agent == "codex" {
				for _, verb := range []string{"get", "get-many", "list"} {
					assertLastTurnErrored(t, erroredReadRow(t, c, verb, id), true)
				}
				if err := os.Remove(rollout); err != nil {
					t.Fatal(err)
				}
			}
			for _, verb := range []string{"get", "get-many", "list"} {
				t.Run(verb, func(t *testing.T) {
					assertLastTurnErrored(t, erroredReadRow(t, c, verb, id), nil)
				})
			}
		})
	}
}
