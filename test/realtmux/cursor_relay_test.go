package realtmux_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/cursorfix"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

func relayFrame(id any, method string, params json.RawMessage) json.RawMessage {
	raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	return raw
}

func relayRequests(t *testing.T, f *spawnFix, id string, count int) []store.PermissionRow {
	t.Helper()
	var rows []store.PermissionRow
	waitFor(t, "public gateway request admission", func() bool {
		s, e := store.Open(f.DBPath)
		if e != nil {
			t.Fatal(e)
		}
		defer s.Close()
		rows, e = s.PermissionRequestsForSpawn(id)
		return e == nil && len(rows) == count
	}, func() string { return fmt.Sprint(rows) })
	return rows
}

func rawInput(t *testing.T, r cursorfix.Runtime) []byte {
	t.Helper()
	b, e := os.ReadFile(r.Raw)
	if os.IsNotExist(e) {
		return nil
	}
	if e != nil {
		t.Fatal(e)
	}
	return b
}

func relayReplies(t *testing.T, r cursorfix.Runtime) []map[string]json.RawMessage {
	t.Helper()
	var out []map[string]json.RawMessage
	for _, f := range r.Frames(t) {
		if len(f["method"]) == 0 {
			out = append(out, f)
		}
	}
	return out
}

// The observer opens an independent SQLite connection before readline parses any response.
func observeRelayCommit(t *testing.T, r cursorfix.Runtime, dbPath string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "commits.jsonl")
	script := fmt.Sprintf("#!/bin/sh\nsqlite3 -readonly -json '%s' 'SELECT request_token,decision,delivered_at FROM permission_requests ORDER BY request_id;' | jq -c --argjson bytes \"$(wc -c < '%s')\" '{bytes:$bytes,rows:.}' >> '%s'\n", dbPath, r.Raw, path)
	if e := os.WriteFile(r.Observer, []byte(script), 0700); e != nil {
		t.Fatal(e)
	}
	return path
}

// Captured envelopes use a synthetic peer; each public caller crosses its real dispatch boundary.
func TestCursorRelayPublicCallers(t *testing.T) {
	// Serial: the shared real-pane fixture changes HOME, PATH and tmux environment.
	for _, surface := range []string{"CLI", "MCP", "Go", "Bun"} {
		t.Run(surface, func(t *testing.T) {
			script := relayCapturedScenario(t)
			f, r, launch := newCursorPaneScenario(t, false, "", cursorPaneOptions{Script: &script, Relay: "on", Timeout: 20})
			id := launch.ID
			bin := buildHookCLI(t, t.TempDir())
			if _, e := apitest.SeedSpawn(filepath.Join(os.Getenv("HOME"), ".config", "agent-director", "state.db"), id, store.StateWaiting, f.CWD, "off", "decoy", false, apitest.WithAgent(store.AgentCursor)); e != nil {
				t.Fatal(e)
			}
			decoyPath := filepath.Join(os.Getenv("HOME"), ".config", "agent-director", "state.db")
			decoy := readRow(t, decoyPath, id)
			observed := observeRelayCommit(t, r, f.DBPath)
			if _, e := f.API.SendKeys(api.SendKeysParams{InstanceID: id, Text: "public relay"}); e != nil {
				t.Fatal(e)
			}
			rows := relayRequests(t, f, id, 2)
			for _, row := range rows {
				if row.MainTurnEndEligible || row.AgentID != "" || row.Hook.PID != 0 {
					t.Fatalf("unknown origin invented authority: %+v", row)
				}
			}
			before := readRow(t, f.DBPath, id)
			call := func(verb string, params map[string]any) json.RawMessage {
				t.Helper()
				var raw []byte
				var e error
				switch surface {
				case "CLI":
					args := []string{verb, "--store-path", f.DBPath}
					for _, key := range []string{"instance_id", "request_token", "decision", "max_wait_ms"} {
						if v, ok := params[key]; ok {
							args = append(args, "--"+strings.ReplaceAll(key, "_", "-"), fmt.Sprint(v))
						}
					}
					raw, e = exec.Command(bin, args...).CombinedOutput()
				case "MCP":
					m := startStdioMCP(t, bin, []string{"serve", "--stdio", "--store-path", f.DBPath}, os.Environ(), f.CWD)
					m.request("initialize", map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "relay-test", "version": "1"}})
					raw = []byte(m.callTool(verb, params))
					m.stop()
				case "Go":
					c, err := api.New(api.Options{StorePath: f.DBPath})
					if err != nil {
						t.Fatal(err)
					}
					defer c.Close()
					var v any
					switch verb {
					case "get-permission":
						v, e = c.GetPermission(api.GetPermissionParams{RequestToken: params["request_token"].(string)})
					case "get":
						v, e = c.Get(id)
					case "list":
						v, e = c.List(api.ListParams{})
					case "decide":
						v, e = c.Decide(api.DecideParams{InstanceID: id, RequestToken: params["request_token"].(string), Decision: params["decision"].(string)})
					}
					raw, _ = json.Marshal(v)
				case "Bun":
					clientSource, err := filepath.Abs("../../pkg/ts-bun-client/src/client.ts")
					if err != nil {
						t.Fatal(err)
					}
					encoded, _ := json.Marshal(params)
					method := map[string]string{"get-permission": "getPermission", "get": "get", "list": "list", "decide": "decide"}[verb]
					js := fmt.Sprintf("import {Client} from %q; const c=await Client.create({storePath:%q,home:%q,_cliPath:%q}); try { console.log(JSON.stringify(await c.%s(%s))); } finally {await c.close();}", clientSource, f.DBPath, os.Getenv("HOME"), bin, method, encoded)
					path := filepath.Join(t.TempDir(), "caller.ts")
					if err = os.WriteFile(path, []byte(js), 0600); err != nil {
						t.Fatal(err)
					}
					ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
					defer cancel()
					raw, e = exec.CommandContext(ctx, "bun", path).CombinedOutput()
				}
				if e != nil {
					t.Fatalf("%s %s: %v %s", surface, verb, e, raw)
				}
				if !json.Valid(raw) {
					t.Fatalf("%s non-JSON %s", verb, raw)
				}
				return raw
			}
			for _, verb := range []string{"get", "list"} {
				params := map[string]any{}
				if verb == "get" {
					params["instance_id"] = id
				}
				raw := call(verb, params)
				if !strings.Contains(string(raw), rows[0].RequestToken) || !strings.Contains(string(raw), rows[1].RequestToken) {
					t.Fatalf("%s omitted simultaneous requests: %s", verb, raw)
				}
			}
			for _, row := range rows {
				raw := call("get-permission", map[string]any{"request_token": row.RequestToken})
				var got api.GetPermissionResult
				if e := json.Unmarshal(raw, &got); e != nil {
					t.Fatal(e)
				}
				if got.ToolInput != row.ToolInput || got.HookAlive != nil || got.ConfirmBy.IsZero() || got.Delivery != "not_confirmed" {
					t.Fatalf("raw/evidence %s", raw)
				}
				var supplied struct {
					Tool string `json:"toolCallId"`
				}
				if e := json.Unmarshal([]byte(row.ToolInput), &supplied); e != nil {
					t.Fatal(e)
				}
				requestID := `"permission"`
				if supplied.Tool != "" {
					requestID = `"plan"`
				}
				matched := false
				for _, sent := range r.SentFrames(t) {
					if string(sent["id"]) == requestID {
						matched = true
						if got.ToolInput != string(sent["params"]) {
							t.Fatalf("public raw params differ from actual peer output: %s vs %s", got.ToolInput, sent["params"])
						}
					}
				}
				if !matched {
					t.Fatal("no matching scripted peer request")
				}
				decision := "allow"
				if supplied.Tool != "" {
					decision = "deny"
				}
				call("decide", map[string]any{"instance_id": id, "request_token": row.RequestToken, "decision": decision})
				// Every call above opens a replacement process/client; the deadline never rearms.
				raw = call("get-permission", map[string]any{"request_token": row.RequestToken})
				var after api.GetPermissionResult
				json.Unmarshal(raw, &after)
				if after.ConfirmBy != got.ConfirmBy || after.ToolInput != got.ToolInput || after.Delivery != "delivered" || after.Decision == nil || *after.Decision != decision {
					t.Fatalf("replacement evidence %s", raw)
				}
			}
			waitFor(t, "two correlated ACP answers", func() bool { return len(relayReplies(t, r)) == 2 }, func() string { return fmt.Sprint(relayReplies(t, r)) })
			replies := relayReplies(t, r)
			byID := map[string]string{}
			for _, reply := range replies {
				byID[string(reply["id"])] = string(reply["result"])
			}
			if !strings.Contains(byID[`"permission"`], `"optionId":"allow-once"`) || !strings.Contains(byID[`"plan"`], `"outcome":"rejected"`) {
				t.Fatalf("once/reject mapping: %v", byID)
			}
			expected := map[string]string{}
			for _, row := range rows {
				var p struct {
					Tool string `json:"toolCallId"`
				}
				json.Unmarshal([]byte(row.ToolInput), &p)
				key := `"permission"`
				if p.Tool != "" {
					key = `"plan"`
				}
				expected[key] = row.RequestToken
			}
			assertRelayCommittedAtRawResponse(t, r, observed, expected)

			waitFor(t, "normal turn outcome retains child-safe holds", func() bool {
				raw, e := os.ReadFile(before.JSONLPath.(string))
				return e == nil && strings.Contains(string(raw), `"kind":"turn_outcome"`)
			}, func() string { return fmt.Sprint(readRow(t, f.DBPath, id)) })
			var gateway int
			fmt.Sscan(fmt.Sprint(before.PanePID), &gateway)
			if pidGone(gateway) {
				t.Fatal("gateway died after valid captured answers")
			}
			after := readRow(t, f.DBPath, id)
			if before.SessionID != after.SessionID || before.LaunchToken != after.LaunchToken || before.PanePID != after.PanePID {
				t.Fatal("caller recreation changed launch identity")
			}
			if !reflect.DeepEqual(readRow(t, decoyPath, id), decoy) {
				t.Fatal("public caller changed decoy default store")
			}
			// A complete turn/relay response cannot release unknown-origin child-safe holds.
			assertRelayPromptGuard(t, f, r, id, "must remain held")
		})
	}
}

// A rejected acknowledgement, including timeout denial, must emit no partial response bytes.
func TestCursorRelayFailedAckHasZeroRawBytes(t *testing.T) {
	// Serial: the shared real-pane fixture changes HOME, PATH and tmux environment.
	for _, tc := range []struct {
		decision string
		missing  bool
	}{{"allow", false}, {"deny", false}, {"timeout", false}, {"allow", true}, {"deny", true}, {"timeout", true}} {
		decision := tc.decision
		t.Run(fmt.Sprintf("%s-missing-%v", decision, tc.missing), func(t *testing.T) {
			script := cursorfix.Scenario{SessionID: "relay-fault-session", Requests: []json.RawMessage{relayFrame("fault", "session/request_permission", cursorfix.Permission("relay-fault-session", "fault-tool", "execute"))}}
			f, r, launch := newCursorPaneScenario(t, false, "", cursorPaneOptions{Script: &script, Relay: "on", Timeout: 1})
			if _, e := f.API.SendKeys(api.SendKeysParams{InstanceID: launch.ID, Text: "ack fault"}); e != nil {
				t.Fatal(e)
			}
			rows := relayRequests(t, f, launch.ID, 1)
			row := rows[0]
			db, e := openRelayFaultDB(f.DBPath, tc.missing)
			if e != nil {
				t.Fatal(e)
			}
			defer db.Close()
			before := string(rawInput(t, r))
			initial, e := f.API.GetPermission(api.GetPermissionParams{RequestToken: row.RequestToken})
			if e != nil {
				t.Fatal(e)
			}
			if decision != "timeout" {
				zero := int64(0)
				if _, e = f.API.Decide(api.DecideParams{InstanceID: launch.ID, RequestToken: row.RequestToken, Decision: decision, MaxWaitMs: &zero}); e != nil {
					t.Fatal(e)
				}
			}
			waitFor(t, "finite failed-ack settlement", func() bool {
				got, e := f.API.GetPermission(api.GetPermissionParams{RequestToken: row.RequestToken})
				return e == nil && got.Delivery == "fallen_back"
			}, func() string { return fmt.Sprint(relayReplies(t, r)) })
			if string(rawInput(t, r)) != before {
				t.Fatalf("failed %s ack emitted raw response bytes", decision)
			}
			got, e := f.API.GetPermission(api.GetPermissionParams{RequestToken: row.RequestToken})
			if e != nil {
				t.Fatal(e)
			}
			persisted := relayRequests(t, f, launch.ID, 1)[0]
			if !persisted.DeliveredAt.IsZero() || got.HookAlive != nil || got.ConfirmBy != initial.ConfirmBy {
				t.Fatalf("fault claimed ack or rearmed: %+v", got)
			}
			if got.Decision != nil && decision == "timeout" {
				t.Fatal("failed timeout transaction committed a verdict")
			}
		})
	}
}

func openRelayFaultDB(path string, missing ...bool) (*sql.DB, error) {
	db, e := sql.Open("sqlite", path)
	if e != nil {
		return nil, e
	}
	action := "RAISE(ABORT,'injected ack failure')"
	if len(missing) > 0 && missing[0] {
		action = "RAISE(IGNORE)"
	}
	_, e = db.Exec(`CREATE TRIGGER reject_ack BEFORE UPDATE OF delivered_at ON permission_requests WHEN NEW.delivered_at IS NOT NULL BEGIN SELECT ` + action + `; END`)
	if e != nil {
		db.Close()
		return nil, e
	}
	return db, nil
}

func TestCursorRelayTimeoutDenyCommittedBeforeResponse(t *testing.T) {
	// Serial: the shared real-pane fixture changes HOME, PATH and tmux environment.
	script := cursorfix.Scenario{SessionID: "timeout-session", Requests: []json.RawMessage{relayFrame("timeout", "session/request_permission", cursorfix.Permission("timeout-session", "timeout-tool", "execute"))}}
	f, r, launch := newCursorPaneScenario(t, false, "", cursorPaneOptions{Script: &script, Relay: "on", Timeout: 1})
	observed := observeRelayCommit(t, r, f.DBPath)
	if _, e := f.API.SendKeys(api.SendKeysParams{InstanceID: launch.ID, Text: "timeout deny"}); e != nil {
		t.Fatal(e)
	}
	relayRequests(t, f, launch.ID, 1)
	waitFor(t, "timeout denial response", func() bool { return len(relayReplies(t, r)) == 1 }, func() string { return fmt.Sprint(relayReplies(t, r)) })
	row := relayRequests(t, f, launch.ID, 1)[0]
	assertRelayCommittedAtRawResponse(t, r, observed, map[string]string{`"timeout"`: row.RequestToken})
	if row.Decision != "deny" {
		t.Fatal("timeout response lacks committed deny")
	}

	if !strings.Contains(string(relayReplies(t, r)[0]["result"]), `"optionId":"reject-once"`) {
		t.Fatal("timeout did not select supported rejection")
	}
}

// Byte offsets keep partial or coalesced chunks distinct from complete frame counts.
func assertRelayCommittedAtRawResponse(t *testing.T, r cursorfix.Runtime, path string, expected map[string]string) {
	t.Helper()
	raw, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	type committed struct {
		Token     string  `json:"request_token"`
		Decision  *string `json:"decision"`
		Delivered *int64  `json:"delivered_at"`
	}
	var observations []struct {
		Bytes int         `json:"bytes"`
		Rows  []committed `json:"rows"`
	}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var item struct {
			Bytes int         `json:"bytes"`
			Rows  []committed `json:"rows"`
		}
		if e = json.Unmarshal([]byte(line), &item); e != nil {
			t.Fatal(e)
		}
		observations = append(observations, item)
	}
	offset := 0
	seen := map[string]bool{}
	for _, line := range strings.Split(string(rawInput(t, r)), "\n") {
		if line == "" {
			continue
		}
		var f map[string]json.RawMessage
		if e = json.Unmarshal([]byte(line), &f); e != nil {
			t.Fatal(e)
		}
		if len(f["method"]) == 0 {
			id := string(f["id"])
			token, ok := expected[id]
			if !ok {
				t.Fatalf("unexpected response ID %s", id)
			}
			found := false
			for _, obs := range observations {
				if obs.Bytes <= offset {
					continue
				}
				for _, row := range obs.Rows {
					if row.Token == token && row.Decision != nil && row.Delivered != nil {
						found = true
					}
				}
				break
			}
			if !found {
				t.Fatalf("first raw response byte at %d lacked committed ack for %s: %s", offset, id, raw)
			}
			if seen[id] {
				t.Fatalf("duplicate response %s", id)
			}
			seen[id] = true
		}
		offset += len(line) + 1
	}
	if len(seen) != len(expected) {
		t.Fatalf("response IDs %v, expected %v", seen, expected)
	}
}

func relayCapturedScenario(t *testing.T) cursorfix.Scenario {
	t.Helper()
	captures := cursorfix.Authorizations(t)
	var permission, plan cursorfix.Authorization
	for _, c := range captures {
		if c.Name == "execute" {
			permission = c
		}
		if c.Name == "plan" {
			plan = c
		}
	}
	var p struct {
		Session string `json:"sessionId"`
	}
	if e := json.Unmarshal(permission.Params, &p); e != nil {
		t.Fatal(e)
	}
	// Marshal only the outer frame; preserve the actual params bytes sent by the peer.
	script := cursorfix.Scenario{SessionID: p.Session, Requests: []json.RawMessage{relayFrame("permission", "session/request_permission", permission.Params), relayFrame("plan", "cursor/create_plan", plan.Params)}}
	return script
}
