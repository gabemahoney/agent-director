package mcp_test

// spawn_reuse_param_test.go pins the MCP side of spawn's reuse_finished opt-in
// (SR-10.1, AC-REUSE-13; b.c4u's name): without it a finished row's id
// collides with the Go client's error envelope. The opt-in's behaviour matrix
// is pkg/api's: TestSpawnExistingRowCollides (an ended or missing row without
// it, a live one with it), TestSpawnAcceptsEmptyAndPrintableInstanceID (no id
// with it mints one), TestSpawnRejectsControlCharacterInstanceID and
// spawn_reuse_test.go's applied reset; its MCP spelling and a reuse it launches
// are TestAdviceFollow_I4_MCPLiteralReuseSpelling's; its schema and decode are
// TestMCPParamToolsListNames', TestMCPParamTypesAgree's and TestMCPParamParity's.

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/spawn"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	api "github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
	"github.com/gabemahoney/agent-director/pkg/api/errnames"
)

// TestSpawnReuseMCPCollisionMatchesGoClient: without the opt-in (absent or
// false) an ended row's id collides: the MCP error carries the Go client's
// err_name and description (code -32000, message = description), the row is
// unchanged and no session is created.
func TestSpawnReuseMCPCollisionMatchesGoClient(t *testing.T) {
	const id = "mcp-reuse-collision-row"
	for name, extra := range map[string]map[string]any{
		"absent": {"instance_id": id},
		"false":  {"instance_id": id, "reuse_finished": false},
	} {
		t.Run(name, func(t *testing.T) {
			e, c := newReuseParamEnv(t)
			before := seedFinished(t, e, id, store.StateEnded)
			cwd := t.TempDir()

			resp := callTool(t, e.d, "spawn", spawnArgs(t, cwd, c, extra))
			data := toolErrorData(t, resp)

			goErr := goClientSpawn(t, e.storePath, api.SpawnParams{CWD: cwd, Agent: "claude", ExtraEnv: c.env(), InstanceID: id})
			if !errors.Is(goErr, spawn.ErrInstanceIdCollision) {
				t.Fatalf("Go client Spawn err = %v; want ErrInstanceIdCollision", goErr)
			}
			errName, desc := errnames.Classify(goErr)
			if data.ErrName != errName || data.ErrDescription != desc || resp.Error.Code != -32000 || resp.Error.Message != desc || resp.Result != nil {
				t.Errorf("MCP error = %+v %+v, result %v; want code -32000 and the Go client's {%s %s}", resp.Error, data, resp.Result, errName, desc)
			}
			if got := readColumns(t, e.storePath, id); !reflect.DeepEqual(got, before) {
				t.Errorf("row changed:\n got %+v\nwant %+v", got, before)
			}
			if n := len(e.rec.SocketCallsOf(tmux.CallCreate)); n != 0 {
				t.Errorf("creates = %d; want 0", n)
			}
		})
	}
}

// goClientSpawn runs p through a Go client on the store at dbPath, with a
// Recorder of its own, and returns the error.
func goClientSpawn(t *testing.T, dbPath string, p api.SpawnParams) error {
	t.Helper()
	client, err := api.New(api.Options{StorePath: dbPath, ConfigPath: filepath.Join(t.TempDir(), "config.toml"),
		TmuxClient: tmuxfix.NewRecorder()})
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	defer client.Close() //nolint:errcheck
	_, err = client.Spawn(p)
	return err
}

func TestSpawnReuseMCPDroppedArgs(t *testing.T) {
	for _, args := range [][]string{{"--resume", "S1"}, nil} {
		wantJSON, err := json.Marshal(args)
		if err != nil {
			t.Fatal(err)
		}
		t.Run(string(wantJSON), func(t *testing.T) {
			e, c := newReuseParamEnv(t)
			id := uuid.NewString()
			seedFinished(t, e, id, store.StateEnded, apitest.WithAgent("codex"))
			obj := callToolText(t, e.d, "spawn", spawnArgs(t, t.TempDir(), c, map[string]any{"instance_id": id, "reuse_finished": true, "agent_args": args}))
			if len(args) > 0 {
				if string(obj["dropped_args"]) != string(wantJSON) {
					t.Errorf("drops=%s", obj["dropped_args"])
				}
			} else if _, present := obj["dropped_args"]; present {
				t.Errorf("empty drops present: %s", obj["dropped_args"])
			}
			var gotID string
			if err := json.Unmarshal(obj["instance_id"], &gotID); err != nil || gotID != id {
				t.Fatalf("id=%s", obj["instance_id"])
			}
			assertCreates(t, e.rec)
			if row := readColumns(t, e.storePath, id); row.Agent != "claude" || row.AgentArgs != "[]" {
				t.Errorf("row=%+v", row)
			}
		})
	}
}

func TestSpawnReuseMCPPolicyRefusal(t *testing.T) {
	e, c := newReuseParamEnv(t)
	id := uuid.NewString()
	before := seedFinished(t, e, id, store.StateEnded, apitest.WithAgent("codex"))
	resp := callTool(t, e.d, "spawn", spawnArgs(t, t.TempDir(), c, map[string]any{"instance_id": id, "reuse_finished": true, "agent_args": []string{"-pc"}}))
	data := toolErrorData(t, resp)
	if data.ErrName != "ErrSpawnDeniedFlag" || data.ErrDescription != "ErrSpawnDeniedFlag: -p" || resp.Error.Code != -32000 || resp.Error.Message != data.ErrDescription || resp.Result != nil {
		t.Errorf("refusal=%+v data=%+v", resp, data)
	}
	if !reflect.DeepEqual(readColumns(t, e.storePath, id), before) {
		t.Error("refusal changed row")
	}
	if len(e.rec.SocketCallsOf(tmux.CallCreate)) != 0 {
		t.Error("refusal launched")
	}
}
