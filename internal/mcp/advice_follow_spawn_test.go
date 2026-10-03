package mcp_test

// advice_follow_spawn_test.go copies, over MCP and literally, the reuse
// opt-in spelling that advice an MCP caller sees names (b.fji I4): the
// tools/list descriptions of spawn, kill (live-row step 6, inventory C11) and
// delete (F1) say "--reuse-finished" and the held-name ErrTmuxUnresponsive
// (A4) says "reuse_finished", while the spawn tool's parameter is
// reuse-finished.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/mcp"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
)

// advSpawnDir is the base name of the spawn's cwd, so the default session
// name its create asks for is known: <advSpawnDir>-<id[:8]>.
const advSpawnDir = "advwork"

// advToolDescription is tool's description as tools/list shows it.
func advToolDescription(t *testing.T, d mcp.Dispatcher, tool string) string {
	t.Helper()
	resp := runOne(t, d, mcp.Request{JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "tools/list"})
	if resp == nil || resp.Error != nil {
		t.Fatalf("tools/list failed: %+v", resp)
	}
	var got struct {
		Tools []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		} `json:"tools"`
	}
	body, _ := json.Marshal(resp.Result)
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("parse tools/list: %v", err)
	}
	for _, tt := range got.Tools {
		if tt.Name == tool {
			return tt.Description
		}
	}
	t.Fatalf("tools/list has no %s tool: %s", tool, body)
	return ""
}

// TestAdviceFollow_I4_MCPLiteralReuseSpelling: I4 spawn's "spawn the id again with --reuse-finished", kill's (C11 step 6)
// "otherwise spawn with --reuse-finished", delete's (F1) "respawn with spawn --reuse-finished" and A4's "(reuse_finished)",
// copied literally into an MCP spawn's arguments once the held name is free and the row ended (no session id).
func TestAdviceFollow_I4_MCPLiteralReuseSpelling(t *testing.T) {
	cases := []struct {
		name, key, phrase string
		relookupTimeout   bool   // the re-lookup times out (A4's ErrTmuxUnresponsive); else the no-valid-id conflict
		wantErr           string // the held-name refusal
		tool              string // the tool whose tools/list description carries phrase; "" for the error's description
	}{
		{"spawn description's --reuse-finished", "--reuse-finished",
			"then, if the refusal was for a held name, spawn the id again with --reuse-finished", false, "ErrTmuxSessionConflict", "spawn"},
		{"kill description's live-row step 6 --reuse-finished", "--reuse-finished",
			"6. Then resume the row if it has a session id and the caller wants the conversation back; otherwise spawn with --reuse-finished",
			false, "ErrTmuxSessionConflict", "kill"},
		{"delete description's --reuse-finished", "--reuse-finished",
			"respawn with spawn --reuse-finished", false, "ErrTmuxSessionConflict", "delete"},
		{"held-name error's reuse_finished", "reuse_finished",
			"a retry with this id uses the reuse opt-in (reuse_finished) once the name is free", true, "ErrTmuxUnresponsive", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, c := newReuseParamEnv(t)
			id := "i4-" + uuid.NewString()[:8]
			cwd := filepath.Join(t.TempDir(), advSpawnDir)
			if err := os.Mkdir(cwd, 0o700); err != nil {
				t.Fatalf("mkdir %s: %v", cwd, err)
			}
			const holder = "$4"
			e.rec.SeedSessions(e.socket, tmuxfix.SeedSession{ID: holder, Name: advSpawnDir + "-" + id[:8]})
			if tc.relookupTimeout {
				lookups := 0
				e.rec.AfterCall(tmux.CallLookup, func(tmuxfix.SocketCall, error) {
					if lookups++; lookups == 1 { // the label scan's; the next is the re-lookup
						e.rec.Script(e.socket, tmuxfix.Script{Failure: tmux.FailTimeout, Times: 1}, tmux.CallLookup)
					}
				})
			}

			data := toolErrorData(t, callTool(t, e.d, "spawn", spawnArgs(t, cwd, c, map[string]any{"claude_instance_id": id})))

			if data.ErrName != tc.wantErr {
				t.Fatalf("err_name = %q (%s); want %s", data.ErrName, data.ErrDescription, tc.wantErr)
			}
			advice := data.ErrDescription
			if tc.tool != "" {
				advice = advToolDescription(t, e.d, tc.tool)
			}
			if !strings.Contains(advice, tc.phrase) {
				t.Errorf("advice %q\nwant it to carry %q", advice, tc.phrase)
			}
			if cols := readColumns(t, e.storePath, id); cols.State != store.StateEnded {
				t.Fatalf("row state = %v; want ended", cols.State)
			}
			if err := e.rec.KillSessionID(e.socket, holder); err != nil { // a human ends it, or it exits: the name is free
				t.Fatalf("KillSessionID(%s): %v", holder, err)
			}

			resp := callTool(t, e.d, "spawn", spawnArgs(t, cwd, c, map[string]any{"claude_instance_id": id, tc.key: true}))

			knownBrokenAdvice(t, "I4", "the spawn tool's parameter is reuse-finished; MCP ignores the unknown "+tc.key+
				", so the literal retry is a plain spawn that collides with the ended row (ErrInstanceIdCollision)")
			if resp == nil || resp.Error != nil {
				got := "no response"
				if resp != nil {
					got = resp.Error.Message
				}
				t.Fatalf("spawn with %q: true = %s; want the reuse to launch", tc.key, got)
			}
			if cols := readColumns(t, e.storePath, id); cols.State != store.StatePending {
				t.Errorf("row state = %v; want pending", cols.State)
			}
		})
	}
}
