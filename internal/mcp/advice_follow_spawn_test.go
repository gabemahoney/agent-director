package mcp_test

// advice_follow_spawn_test.go copies, over MCP and literally, the reuse
// opt-in name that advice an MCP caller sees gives (b.fji I4, b.c4u): the
// tools/list descriptions of spawn, kill (live-row step 6, inventory C11) and
// delete (F1) and the held-name ErrTmuxUnresponsive (A4) each say
// "reuse_finished (--reuse-finished on the CLI)", and the name before the
// parenthesis is the spawn tool's parameter.

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

// advReuseCLIParen is what follows the reuse opt-in's MCP name in shared advice.
const advReuseCLIParen = " (--reuse-finished on the CLI)"

// advOptInName is the name the advice gives the reuse opt-in: the word just
// before advReuseCLIParen.
func advOptInName(t *testing.T, advice string) string {
	t.Helper()
	i := strings.Index(advice, advReuseCLIParen)
	if i < 0 {
		t.Fatalf("advice %q names no opt-in before %q", advice, advReuseCLIParen)
	}
	words := strings.Fields(advice[:i])
	return words[len(words)-1]
}

// TestAdviceFollow_I4_MCPLiteralReuseSpelling: I4 spawn's "spawn the id again with reuse_finished (--reuse-finished on the CLI)",
// kill's (C11 step 6) "otherwise spawn with reuse_finished (--reuse-finished on the CLI)", delete's (F1) "respawn with spawn
// reuse_finished (--reuse-finished on the CLI)" and A4's "the reuse opt-in reuse_finished (--reuse-finished on the CLI)", the name
// before the parenthesis copied literally into an MCP spawn's arguments once the held name is free and the row ended (no session id).
func TestAdviceFollow_I4_MCPLiteralReuseSpelling(t *testing.T) {
	cases := []struct {
		name, phrase    string
		relookupTimeout bool   // the re-lookup times out (A4's ErrTmuxUnresponsive); else the no-valid-id conflict
		wantErr         string // the held-name refusal
		tool            string // the tool whose tools/list description carries phrase; "" for the error's description
	}{
		{"spawn description",
			"then, if the refusal was for a held name, spawn the id again with reuse_finished (--reuse-finished on the CLI)",
			false, "ErrTmuxSessionConflict", "spawn"},
		{"kill description's live-row step 6",
			"6. Then resume the row if it has a session id and the caller wants the conversation back; otherwise spawn with " +
				"reuse_finished (--reuse-finished on the CLI); callers whose ids agent-director mints spawn fresh.",
			false, "ErrTmuxSessionConflict", "kill"},
		{"delete description", "respawn with spawn reuse_finished (--reuse-finished on the CLI)",
			false, "ErrTmuxSessionConflict", "delete"},
		{"held-name error",
			"a retry with this id uses the reuse opt-in reuse_finished (--reuse-finished on the CLI) once the name is free",
			true, "ErrTmuxUnresponsive", ""},
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
				t.Fatalf("advice %q\nwant it to carry %q", advice, tc.phrase)
			}
			key := advOptInName(t, advice)
			if cols := readColumns(t, e.storePath, id); cols.State != store.StateEnded {
				t.Fatalf("row state = %v; want ended", cols.State)
			}
			if err := e.rec.KillSessionID(e.socket, holder); err != nil { // a human ends it, or it exits: the name is free
				t.Fatalf("KillSessionID(%s): %v", holder, err)
			}

			resp := callTool(t, e.d, "spawn", spawnArgs(t, cwd, c, map[string]any{"claude_instance_id": id, key: true}))

			if resp == nil || resp.Error != nil {
				got := "no response"
				if resp != nil {
					got = resp.Error.Message
				}
				t.Fatalf("spawn with %q: true = %s; want the reuse to launch", key, got)
			}
			if cols := readColumns(t, e.storePath, id); cols.State != store.StatePending {
				t.Errorf("row state = %v; want pending", cols.State)
			}
		})
	}
}
