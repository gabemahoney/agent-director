package mcp_test

// kill_optin_absent_test.go pins SR-6.8 and SR-6.6 over MCP: kill has no
// finished-row opt-in, so a call sending it in any spelling behaves exactly
// as the same call without it.

import (
	"bufio"
	"encoding/json"
	"os"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/mcp"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// optInKeys are the opt-in's spellings a caller might send; Go's decoder
// matches keys case-insensitively, so includefinished is covered too.
var optInKeys = []string{"include-finished", "include_finished", "IncludeFinished", "includefinished"}

// mcpKillCall is what one kill tool call, on a fresh fixture, showed.
type mcpKillCall struct {
	r         *mcp.Response
	resp      []byte // r marshalled whole
	calls     []tmuxfix.SocketCall
	nameCalls int
	sessions  []tmuxfix.SeedSession
	trail     []map[string]any // the ad.kill.called lines this call wrote
}

// callKillOnce builds tc's fixture, sends kill (with key set to true unless
// key is ""), and fails unless the row is unchanged afterwards.
func callKillOnce(t *testing.T, tc killMCPCase, key string) mcpKillCall {
	t.Helper()
	var dbPath string
	seed := tc.seed
	tc.seed = func(t *testing.T, rec *tmuxfix.Recorder, p string) {
		dbPath = p
		if seed != nil {
			seed(t, rec, p)
		}
	}
	d, rec := newKillMCPServer(t, tc)
	before := readColumns(t, dbPath, killMCPID)
	args := map[string]any{"claude_instance_id": killMCPID}
	if key != "" {
		args[key] = true
	}
	body, _ := json.Marshal(args)
	seen := len(killCalledLines(t))

	resp := callTool(t, d, "kill", string(body))

	if after := readColumns(t, dbPath, killMCPID); !reflect.DeepEqual(after, before) {
		t.Errorf("kill %s changed the row:\n got %+v\nwant %+v", body, after, before)
	}
	raw, _ := json.Marshal(resp)
	return mcpKillCall{r: resp, resp: raw, calls: rec.SocketCalls(), nameCalls: len(rec.Calls()),
		sessions: rec.Sessions(apitest.TestSocket), trail: killCalledLines(t)[seen:]}
}

// killCalledLines returns killMCPID's ad.kill.called trail lines, in order.
func killCalledLines(t *testing.T) []map[string]any {
	t.Helper()
	f, err := os.Open(mcpTrailPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("open trail: %v", err)
	}
	defer f.Close() //nolint:errcheck // read-only
	var out []map[string]any
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("parse trail line %q: %v", sc.Text(), err)
		}
		if m["event"] == "ad.kill.called" && m["claude_instance_id"] == killMCPID {
			out = append(out, m)
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan trail: %v", err)
	}
	return out
}

// seedUnrelated starts the socket's server with one unrelated session, so
// kill's lookup answers Gone.
func seedUnrelated(_ *testing.T, rec *tmuxfix.Recorder, _ string) {
	rec.SeedSessions(apitest.TestSocket, tmuxfix.SeedSession{Name: "unrelated"})
}

// TestMCPKillIgnoresOptInKeys: on a finished row whose own reported-in old
// session still runs (one the CLI opt-in would end) and on a live row, a kill
// sending any opt-in spelling gives the same response, tmux calls and trail as
// without it, and never the opt-in's live-row refusal (SR-6.8, SR-6.6).
func TestMCPKillIgnoresOptInKeys(t *testing.T) {
	window := time.Duration(config.DefaultStoppingWindowSeconds) * time.Second
	bound := time.Duration(config.DefaultStartingSessionSeconds) * time.Second
	endedAt := time.Now().Add(-window - time.Minute)
	created := endedAt.Add(-bound - time.Minute).Unix()
	ownOldSession := func(t *testing.T, rec *tmuxfix.Recorder, p string) {
		rec.SeedRowSession(t, p, killMCPID, tmuxfix.WithRowSessionCreated(created))
	}
	reportedIn := []apitest.SpawnOption{apitest.WithPID(apitest.TestPanePID), apitest.WithEndedAt(endedAt)}

	cases := []struct {
		tc       killMCPCase
		finished bool
	}{
		{killMCPCase{name: "ended row", state: store.StateEnded, seed: ownOldSession, opts: reportedIn}, true},
		{killMCPCase{name: "missing row", state: store.StateMissing, seed: ownOldSession, opts: reportedIn}, true},
		{killMCPCase{name: "pending row", state: store.StatePending, seed: seedUnrelated}, false},
		{killMCPCase{name: "waiting row", state: store.StateWaiting, seed: seedUnrelated}, false},
	}
	for _, c := range cases {
		t.Run(c.tc.name, func(t *testing.T) {
			var base mcpKillCall
			for _, key := range append([]string{""}, optInKeys...) {
				got := callKillOnce(t, c.tc, key)
				assertKillSent(t, got.r, false)
				if c.finished {
					if got.nameCalls != 0 || len(got.calls) != 0 {
						t.Errorf("finished row, key %q: tmux calls = %d name-based, %+v; want none", key, got.nameCalls, got.calls)
					}
					if len(got.sessions) != 1 {
						t.Errorf("finished row, key %q: sessions = %+v; want the row's own old session still there", key, got.sessions)
					}
				} else if !slices.ContainsFunc(got.calls, func(c tmuxfix.SocketCall) bool { return c.Call == tmux.CallLookup }) {
					t.Errorf("live row, key %q: tmux calls = %+v; want the lookup (the opt-in's refusal makes none)", key, got.calls)
				}
				if len(got.trail) != 1 || got.trail[0]["include_finished"] != false {
					t.Errorf("SR-6.8: kill with %q wrote ad.kill.called %+v; want one line with include_finished false", key, got.trail)
				}
				if key == "" {
					base = got
					continue
				}
				if string(got.resp) != string(base.resp) {
					t.Errorf("SR-6.8: kill with %q = %s; want %s, as without it", key, got.resp, base.resp)
				}
				if !reflect.DeepEqual(got.calls, base.calls) || got.nameCalls != base.nameCalls {
					t.Errorf("SR-6.8: kill with %q made tmux calls %+v (%d name-based); want %+v (%d), as without it",
						key, got.calls, got.nameCalls, base.calls, base.nameCalls)
				}
			}
		})
	}
}
