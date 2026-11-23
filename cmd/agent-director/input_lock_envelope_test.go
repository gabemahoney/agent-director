//go:build linux || darwin

package main_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/mcp"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
	"github.com/gabemahoney/agent-director/pkg/api/errnames"
	"golang.org/x/sys/unix"
)

// b.vxo: real CLI and serve processes preserve the same cause and zero-input
// facts, including pause's detailed ErrInternal, for both admission failures.
func TestVXOInputLockErrorsCLIMCP(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	for _, holder := range []string{"writer", "proof reader"} {
		for _, verb := range []string{"send-keys", "pause"} {
			t.Run(verb+"/"+holder, func(t *testing.T) {
				home, id, _ := seedRowOnSocket(t, store.StateWaiting, apitest.WithAgent("codex"))
				token, _, storeID := launchIdentity(t, home, id)
				dir := filepath.Join(directorDir(home), "input", storeID)
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				suffix := ".intent.lock"
				cause := "another input writer is active or input admission stayed busy for 100ms: " + unix.EWOULDBLOCK.Error()
				if holder == "proof reader" {
					suffix = ".lock"
					cause = "input lock stayed busy for 2s: " + unix.EWOULDBLOCK.Error()
				}
				lock, err := os.OpenFile(filepath.Join(dir, token)+suffix, os.O_CREATE|os.O_RDWR, 0600)
				if err != nil {
					t.Fatal(err)
				}
				defer lock.Close()
				if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
					t.Fatal(err)
				}
				reason := "exclusive input writer or delivery journal unavailable: " + cause
				wantName := "ErrSubmissionUnconfirmed"
				wantDesc := fmt.Sprintf("send-keys: %s: not_started (%s); no input was attempted by this request", id, reason)
				argv := []string{verb, "--instance-id", id}
				params := map[string]any{"instance_id": id}
				if verb == "pause" {
					wantName = "ErrInternal"
					wantDesc = fmt.Sprintf("pause: %s: %s; no input attempted", id, reason)
				} else {
					argv = append(argv, "--text", "must never be typed")
					params["text"] = "must never be typed"
				}
				wantDetails := map[string]any{"outcome": "not_started", "phase": "pre_input", "bytes_attempted": float64(0),
					"enter_may_have_been_sent": false, "reason": reason, "keys_sent": false, "submission_confirmed": false, "row_state": "waiting"}
				stdout, stderr, code := runSpawnCLI(t, home, fakeDir, argv...)
				assertOnlyEnvelope(t, stdout, stderr, code, wantName)
				var cli mcp.ToolErrorData
				if err := json.Unmarshal([]byte(stderr), &cli); err != nil {
					t.Fatal(err)
				}
				if cli.ErrDescription != wantDesc || !reflect.DeepEqual(cli.ErrDetails, wantDetails) {
					t.Errorf("CLI error = %+v; want %s, %v", cli, wantDesc, wantDetails)
				}
				srv := startServe(t, home, fakeTmuxEnv(t, home, fakeDir)...)
				t.Cleanup(srv.kill)
				srv.initialize(t)
				// serveSession's usual reply type omits err_details; decode this
				// response with ToolErrorData to check the complete wire payload.
				if _, err := fmt.Fprintf(srv.in, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":%q,"arguments":%s}}`+"\n", mcp.ToolName(verb), jsonOf(t, params)); err != nil {
					t.Fatal(err)
				}
				select {
				case raw := <-srv.lines:
					var reply struct {
						Result json.RawMessage `json:"result"`
						Error  *struct {
							Code    int               `json:"code"`
							Message string            `json:"message"`
							Data    mcp.ToolErrorData `json:"data"`
						} `json:"error"`
					}
					if err := json.Unmarshal([]byte(raw), &reply); err != nil {
						t.Fatal(err)
					}
					if reply.Error == nil || reply.Result != nil {
						t.Fatalf("MCP reply = %s; want error only", raw)
					}
					if reply.Error.Message != reply.Error.Data.ErrDescription {
						t.Errorf("MCP message and data description differ: %s", raw)
					}
					reply.Error.Data.ErrDescription = errnames.TrimNamePrefix(reply.Error.Data.ErrName, reply.Error.Data.ErrDescription)
					if reply.Error.Code != -32000 || reply.Error.Data.ErrName != wantName ||
						reply.Error.Data.ErrDescription != wantDesc || !reflect.DeepEqual(reply.Error.Data.ErrDetails, wantDetails) ||
						!reflect.DeepEqual(reply.Error.Data, cli) {
						t.Errorf("MCP error = %s; want CLI parity and cause-bearing details", raw)
					}
				case <-time.After(surfaceDeadline):
					t.Fatal("MCP input-lock refusal timed out")
				}
				srv.stop(t)
				assertInvocationKinds(t, home)
			})
		}
	}
}
