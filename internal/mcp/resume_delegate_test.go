package mcp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/installversion"
	"github.com/gabemahoney/agent-director/internal/layout"
	"github.com/gabemahoney/agent-director/internal/mcp"
	"github.com/gabemahoney/agent-director/internal/resumedelegate"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// b.kii: one dispatcher/Client and one MCP server handle resume then version.
// The covering executable is a real staged pair, validated before ready. Its
// tiny protocol stand-in does no provider work; API owns the selection matrix.
func TestResumeMCPForwarding(t *testing.T) {
	e := newEnv(t)
	e.seed(t, "mcp-delegated", store.StateEnded, apitest.WithAgent("codex"))
	if err := apitest.SeedBusySendCoverage(e.storePath, "mcp-delegated", store.BusySendCovered, store.BusySendCovered); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	reply, received := filepath.Join(dir, "reply"), filepath.Join(dir, "received")
	core, digest := store.BusySendCoreContractIdentity()
	facts := installversion.Facts{FormatVersion: 1, Version: "mcp-covering", Commit: "fixture", CoveredAgents: []string{"claude", "codex"}, CoreVersion: core, CoreDeclarationsSHA256: digest}
	encoded, err := json.Marshal(facts)
	if err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\ncase \"$1\" in\n--install-version-facts|version-facts) printf '%s\\n' '" + string(encoded) + "';;\n--resume-delegation-job) cat >> '" + received + "'; cat '" + reply + "';;\n*) exit 71;;\nesac\n"
	runtime, admin := filepath.Join(dir, "runtime"), filepath.Join(dir, "admin")
	for _, path := range []string{runtime, admin} {
		if err := os.WriteFile(path, []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := (installversion.Engine{Root: layout.Dir(os.Getenv("HOME"))}).Publish(context.Background(), runtime, admin, facts); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, field, payload string
		failure              bool
	}{
		{"success", "result", `{"instance_id":"mcp-delegated","pre_trust":"skipped","future":false,"nullable":null,"large":9007199254740993}`, false},
		{"cover unavailable", "error", `{"err_name":"ErrSpawnNotResumable","err_description":"no covering runtime","err_details":{"cause":"covering_version_unavailable","provider":"codex"}}`, true},
		{"unknown name", "error", `{"err_name":"ErrFutureResume","err_description":"future refusal","err_details":{"future":false,"nullable":null,"large":9007199254740993}}`, true},
		{"unknown outcome", "error", `{"err_name":"ErrInternal","err_description":"outcome unknown; check get"}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(reply, []byte(`{"response_format":1,"`+tc.field+`":`+tc.payload+`}`), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(received, nil, 0600); err != nil {
				t.Fatal(err)
			}
			in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"resume","arguments":{"instance_id":"mcp-delegated","confirmed_delivery":false,"confirm_from_transcripts":false}}}` + "\n" + `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"version","arguments":{}}}` + "\n")
			var output bytes.Buffer
			if err := mcp.New(e.d, nil).Serve(context.Background(), in, &output); err != nil && err != io.EOF {
				t.Fatal(err)
			}
			// Retain raw error data so large integers are not rounded by any's
			// float64 decoding in the convenience helper.
			decoder := json.NewDecoder(&output)
			var raw struct {
				Result json.RawMessage
				Error  *struct{ Data json.RawMessage }
			}
			if err := decoder.Decode(&raw); err != nil {
				t.Fatal(err)
			}
			if tc.failure {
				if raw.Error == nil || string(raw.Error.Data) != tc.payload {
					t.Fatalf("MCP error %+v; want %s", raw, tc.payload)
				}
			} else {
				var response mcp.Response
				if err := json.Unmarshal(raw.Result, &response.Result); err != nil {
					t.Fatal(err)
				}
				if got := toolText(t, &response); got != tc.payload {
					t.Fatalf("MCP result %s; want %s", got, tc.payload)
				}
			}
			var next mcp.Response
			if err := decoder.Decode(&next); err != nil {
				t.Fatalf("server did not survive resume: %v", err)
			}
			if got := toolResult(t, &next); len(got["version"]) == 0 {
				t.Fatalf("subsequent version %+v", got)
			}
			if err := decoder.Decode(new(any)); err != io.EOF {
				t.Fatalf("extra output: %v", err)
			}
			calls, err := os.ReadFile(received)
			if err != nil {
				t.Fatal(err)
			}
			request, err := resumedelegate.DecodeRequest(bytes.NewReader(calls))
			if err != nil {
				t.Fatalf("want exactly one delegation: %v", err)
			}
			var params map[string]json.RawMessage
			if err := json.Unmarshal(request.Params, &params); err != nil {
				t.Fatal(err)
			}
			if string(params["confirmed_delivery"]) != "false" || string(params["confirm_from_transcripts"]) != "false" {
				t.Fatalf("explicit false lost: %s", request.Params)
			}
		})
	}
}
