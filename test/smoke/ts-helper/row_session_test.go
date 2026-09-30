package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/probe"
	"github.com/gabemahoney/agent-director/internal/testsupport/faketmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// rowSessionText is the capture text seed-row-session scripts for the pane,
// with no escape sequences, so read-pane's default stripping keeps it whole.
const rowSessionText = "seeded pane line one\nseeded pane line two\n"

// seedRowOnSocket seeds row id at state on a private socket through
// seed-spawn and returns the store path and the socket.
func seedRowOnSocket(t *testing.T, id, state string) (dbPath, socket string) {
	t.Helper()
	dir := t.TempDir()
	dbPath = filepath.Join(dir, "state.db")
	socket = filepath.Join(dir, "tmux", "default")
	args := []string{"seed-spawn", "--store", dbPath, "--id", id, "--state", state,
		"--create-store", "--socket", socket}
	var stdout, stderr bytes.Buffer
	if code := dispatch(args, &stdout, &stderr); code != 0 {
		t.Fatalf("seed-spawn exit %d; stderr: %s", code, stderr.String())
	}
	return dbPath, socket
}

// runSeedRowSession runs seed-row-session on the row and returns its exit
// code, parsed result (nil on failure), stdout and stderr.
func runSeedRowSession(t *testing.T, args ...string) (int, map[string]string, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := dispatch(append([]string{"seed-row-session"}, args...), &stdout, &stderr)
	if code != 0 {
		return code, nil, stdout.String(), stderr.String()
	}
	var res map[string]string
	if err := json.Unmarshal([]byte(strings.TrimRight(stdout.String(), "\n")), &res); err != nil {
		t.Fatalf("stdout not single-line JSON: %v; stdout %q", err, stdout.String())
	}
	return code, res, stdout.String(), stderr.String()
}

// lookupRow makes the production lookup for the row against the fake.
func lookupRow(t *testing.T, dbPath, id, socket string) tmux.Result {
	t.Helper()
	cols, err := apitest.ReadSpawnColumns(dbPath, id)
	if err != nil {
		t.Fatalf("ReadSpawnColumns: %v", err)
	}
	storeID, err := apitest.ReadStoreID(dbPath)
	if err != nil {
		t.Fatalf("ReadStoreID: %v", err)
	}
	token, _ := cols.LaunchToken.(string)
	d := config.Tmux{}
	c := tmux.New(faketmuxfix.Binary(t), tmux.Timeouts{Query: d.EffectiveQueryTimeout(),
		Action: d.EffectiveActionTimeout(), Create: d.EffectiveCreateTimeout(), WaitDelay: d.EffectivePipeCloseWait()})
	return tmux.Lookup(c, probe.NewProcChecker(), tmux.Launch{InstanceID: id, Token: token, StoreID: storeID, Socket: socket}, "")
}

// readPane runs the read-pane verb on the row through api.New against the fake.
func readPane(t *testing.T, dbPath, id string) (api.ReadPaneResult, error) {
	t.Helper()
	cfgPath := filepath.Join(t.TempDir(), "config.toml")
	apitest.WriteTmuxConfig(t, cfgPath)
	client, err := api.New(api.Options{ConfigPath: cfgPath, StorePath: dbPath, TmuxCommand: faketmuxfix.Binary(t)})
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	defer client.Close() //nolint:errcheck
	return client.ReadPane(api.ReadPaneParams{ClaudeInstanceID: id})
}

// TestSeedRowSession: the table written holds the row's own session, so the
// fake answers the lookup with Ours and read-pane returns the scripted text.
func TestSeedRowSession(t *testing.T) {
	for _, tc := range []struct {
		name     string
		state    string
		envTable bool // tables in a FAKE_TMUX_TABLES directory, not beside the socket
	}{
		{"live row, recorded pane", "working", false},
		{"finished row, no recorded pane, found by its pane label", "ended", false},
		{"tables directory from the environment", "waiting", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const id = "ts-helper-row-session"
			dbPath, socket := seedRowOnSocket(t, id, tc.state)
			tables := faketmuxfix.Tables{}
			if tc.envTable {
				tables.Dir = t.TempDir()
				t.Setenv(faketmuxfix.EnvTables, tables.Dir)
			}

			code, res, _, stderr := runSeedRowSession(t, "--store", dbPath, "--id", id, "--capture", rowSessionText)
			if code != 0 || stderr != "" {
				t.Fatalf("seed-row-session exit %d, stderr %q; want 0 and empty", code, stderr)
			}
			want := map[string]string{"socket": socket, "session_id": "$0", "pane_id": apitest.TestPaneID, "table": tables.Path(socket)}
			for k, v := range want {
				if res[k] != v {
					t.Errorf("result[%q] = %q, want %q (result %v)", k, res[k], v, res)
				}
			}
			if got := tables.Read(t, socket).Sessions; len(got) != 1 {
				t.Fatalf("table sessions = %+v, want the row's one session", got)
			}

			lr := lookupRow(t, dbPath, id, socket)
			if lr.Verdict != tmux.Ours || lr.Session.ID != res["session_id"] {
				t.Fatalf("lookup = %s (session %q), want ours on %s", lr.Token(), lr.Session.ID, res["session_id"])
			}
			got, err := readPane(t, dbPath, id)
			if err != nil {
				t.Fatalf("ReadPane: %v", err)
			}
			if got.Pane != rowSessionText {
				t.Errorf("pane = %q, want the scripted %q", got.Pane, rowSessionText)
			}
		})
	}
}

// TestSeedRowSessionKeepsOtherSessions: the row's session is added beside a
// table's existing session and server, under the next session id.
func TestSeedRowSessionKeepsOtherSessions(t *testing.T) {
	const id = "row-beside"
	dbPath, socket := seedRowOnSocket(t, id, "working")
	other := faketmuxfix.Session{ID: "$4", Created: 1, Name: "other", Panes: []faketmuxfix.Pane{{ID: "%1", PID: 1}}}
	server := &faketmuxfix.Server{PID: 1, Start: 1}
	faketmuxfix.Tables{}.Write(t, socket, faketmuxfix.Table{Server: server, Sessions: []faketmuxfix.Session{other}})

	code, res, _, stderr := runSeedRowSession(t, "--store", dbPath, "--id", id)
	if code != 0 {
		t.Fatalf("seed-row-session exit %d; stderr %q", code, stderr)
	}
	tb := faketmuxfix.Tables{}.Read(t, socket)
	if len(tb.Sessions) != 2 || tb.Sessions[0].ID != other.ID || tb.Sessions[1].ID != "$5" || res["session_id"] != "$5" {
		t.Errorf("sessions = %+v, result %v; want %s kept and the row's added as $5", tb.Sessions, res, other.ID)
	}
	if tb.Server == nil || *tb.Server != *server {
		t.Errorf("server = %+v, want the table's %+v kept", tb.Server, *server)
	}
	if lr := lookupRow(t, dbPath, id, socket); lr.Verdict != tmux.Ours || lr.Session.ID != "$5" {
		t.Errorf("lookup = %s (session %q), want ours on $5", lr.Token(), lr.Session.ID)
	}
}

// TestSeedRowSessionPaneTaken: a row whose pane is already in the socket's
// table is refused, and the table is left as it was.
func TestSeedRowSessionPaneTaken(t *testing.T) {
	dbPath, socket := seedRowOnSocket(t, "row-a", "working")
	var stdout, stderr bytes.Buffer
	if code := dispatch([]string{"seed-spawn", "--store", dbPath, "--id", "row-b", "--state", "pending",
		"--socket", socket}, &stdout, &stderr); code != 0 {
		t.Fatalf("seed-spawn row-b exit %d; stderr: %s", code, stderr.String())
	}
	if code, _, _, stderr := runSeedRowSession(t, "--store", dbPath, "--id", "row-a"); code != 0 {
		t.Fatalf("seed-row-session row-a exit %d; stderr %q", code, stderr)
	}
	// row-b records the same default pane as row-a.
	code, _, stdoutB, stderrB := runSeedRowSession(t, "--store", dbPath, "--id", "row-b")
	if code != 1 || stdoutB != "" || !strings.Contains(stderrB, "already in session $0") {
		t.Fatalf("row-b exit %d, stdout %q, stderr %q; want 1, empty, naming session $0", code, stdoutB, stderrB)
	}
	if got := (faketmuxfix.Tables{}).Read(t, socket).Sessions; len(got) != 1 || got[0].ID != "$0" {
		t.Errorf("table sessions after the refusal = %+v; want only row-a's $0", got)
	}
}

// TestSeedRowSessionRefusals: a missing flag or a row the helper cannot label
// fails with exit 1, empty stdout and the reason on stderr.
func TestSeedRowSessionRefusals(t *testing.T) {
	for _, tc := range []struct {
		name       string
		seed       bool // seed a row first
		args       func(dbPath string) []string
		wantStderr string
	}{
		{"missing --id", true, func(db string) []string { return []string{"--store", db} }, "--id"},
		{"missing --store", false, func(string) []string { return []string{"--id", "x"} }, "--store"},
		{"unknown row", true, func(db string) []string { return []string{"--store", db, "--id", "no-such-row"} }, "no-such-row"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dbPath := filepath.Join(t.TempDir(), "state.db")
			if tc.seed {
				dbPath, _ = seedRowOnSocket(t, "seeded-row", "working")
			}
			code, _, stdout, stderr := runSeedRowSession(t, tc.args(dbPath)...)
			if code != 1 || stdout != "" || !strings.Contains(stderr, tc.wantStderr) {
				t.Errorf("exit %d, stdout %q, stderr %q; want 1, empty, naming %q", code, stdout, stderr, tc.wantStderr)
			}
		})
	}
}

// TestSeedRowSessionNoLaunchToken: a row from before the release (no token,
// no socket) is refused, since no label can name it.
func TestSeedRowSessionNoLaunchToken(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.db")
	var stdout, stderr bytes.Buffer
	if code := dispatch([]string{"seed-spawn", "--store", dbPath, "--id", "old-row", "--state", "working",
		"--create-store", "--no-launch-identity"}, &stdout, &stderr); code != 0 {
		t.Fatalf("seed-spawn exit %d; stderr: %s", code, stderr.String())
	}
	code, _, out, errOut := runSeedRowSession(t, "--store", dbPath, "--id", "old-row")
	if code != 1 || out != "" || !strings.Contains(errOut, "no launch token") {
		t.Errorf("exit %d, stdout %q, stderr %q; want 1, empty, naming the missing launch token", code, out, errOut)
	}
}
