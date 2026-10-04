package main_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/probe"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/faketmuxfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// seedKillRow seeds one row under a fresh HOME on the per-test socket the CLI
// child resolves (SR-20.3); it returns the HOME, the row's id and its socket.
func seedKillRow(t *testing.T, state string, opts ...apitest.SpawnOption) (home, id, socket string) {
	t.Helper()
	home = t.TempDir()
	socket = spawnSocket(t, home)
	opts = append([]apitest.SpawnOption{apitest.WithTmuxSocket(socket)}, opts...)
	id, err := apitest.SeedSpawn(stateDB(home), "", state, "", "", "", true, opts...)
	if err != nil {
		t.Fatalf("SeedSpawn: %v", err)
	}
	return home, id, socket
}

// killTable is a fake table whose server is this test process holding sessions.
func killTable(sessions ...faketmuxfix.Session) faketmuxfix.Table {
	now := time.Now().Unix()
	return faketmuxfix.Table{Server: &faketmuxfix.Server{PID: os.Getpid(), Start: now}, Sessions: sessions}
}

// rowColumns reads id's row straight from the store under home.
func rowColumns(t *testing.T, home, id string) apitest.SpawnColumns {
	t.Helper()
	cols, err := apitest.ReadSpawnColumns(stateDB(home), id)
	if err != nil {
		t.Fatalf("ReadSpawnColumns: %v", err)
	}
	return cols
}

// assertKillSent checks stdout is exactly kill's result with kill_sent want.
func assertKillSent(t *testing.T, stdout string, want bool) {
	t.Helper()
	var res map[string]any
	if err := json.Unmarshal([]byte(stdout), &res); err != nil {
		t.Fatalf("parse stdout %q: %v", stdout, err)
	}
	if len(res) != 1 || res["kill_sent"] != want {
		t.Errorf("stdout = %s; want exactly {\"kill_sent\":%v}", stdout, want)
	}
}

// TestKillCLIHappyPath: the row's own labelled session is killed by pane id and
// session id on the row's socket, never by name; kill_sent is true (SR-6.1).
func TestKillCLIHappyPath(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	home, id, socket := seedKillRow(t, store.StateWaiting)
	token, _, storeID := launchIdentity(t, home, id)
	name, _ := rowColumns(t, home, id).TmuxSessionName.(string)
	const sessionID = "$3"
	faketmuxfix.Tables{}.Write(t, socket, killTable(faketmuxfix.Session{
		ID: sessionID, Created: time.Now().Unix(), Name: name, Label: tmuxfix.LabelValue(token, sessionID, id, storeID),
		Panes: []faketmuxfix.Pane{{ID: apitest.TestPaneID, PID: apitest.TestPanePID}},
	}))

	stdout, stderr, code := runSpawnCLI(t, home, fakeDir, "kill", "--claude-instance-id", id)
	if code != 0 || stderr != "" {
		t.Fatalf("kill exit = %d, stderr = %q; want 0 and empty", code, stderr)
	}
	assertKillSent(t, stdout, true)

	invs := assertInvocationKinds(t, home, "list-sessions", "list-panes", "kill-pane", "kill-session")
	wantKills := [][]string{
		{"-u", "-S", socket, "kill-pane", "-t", apitest.TestPaneID},
		{"-u", "-S", socket, "kill-session", "-t", sessionID},
	}
	for i, want := range wantKills {
		if got := invs[2+i]; !slices.Equal(got, want) {
			t.Errorf("kill invocation %d = %q; want %q", i, got, want)
		}
	}
	for _, argv := range invs {
		for _, a := range argv {
			if strings.Contains(a, name) {
				t.Errorf("invocation %q names the session %q; want ids only", argv, name)
			}
		}
	}
	if left := (faketmuxfix.Tables{}).Read(t, socket).Sessions; len(left) != 0 {
		t.Errorf("sessions after kill = %+v; want none", left)
	}
	if st := rowColumns(t, home, id).State; st != store.StateWaiting {
		t.Errorf("row state after kill = %v; want %s unchanged", st, store.StateWaiting)
	}
}

func TestKillCLIErrSpawnNotFound(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	home := t.TempDir()
	bootstrapDB(t, home)

	_, stderr, code := runSpawnCLI(t, home, fakeDir,
		"kill", "--claude-instance-id", "absent")
	if code == 0 {
		t.Fatalf("expected non-zero exit; got 0 (stderr=%s)", stderr)
	}
	env := parseEnvelope(t, stderr)
	if env.ErrName != "ErrSpawnNotFound" {
		t.Errorf("err_name = %q; want ErrSpawnNotFound", env.ErrName)
	}
	assertInvocationKinds(t, home)
}

// liveChild starts a real child process of the test, stopped at cleanup, and
// returns its pid and start time as the production reader reads them.
func liveChild(t *testing.T) (int, string) {
	t.Helper()
	child := exec.Command("sleep", "300")
	if err := child.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}
	t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })
	pid := child.Process.Pid
	start, alive, known := probe.NewProcChecker().StartTime(pid)
	if !known || !alive || start == "" {
		t.Fatalf("StartTime(child %d) = (%q, alive=%v, known=%v); want the live start time", pid, start, alive, known)
	}
	return pid, start
}

// TestKillCLIRefusals: a leftover beside the live row and a Gone lookup with the
// agent alive and no pane each exit 1 with only the envelope and send no kill.
func TestKillCLIRefusals(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	cases := []struct {
		name string
		// arrange seeds the row and the fake; it returns home, id, the
		// description case and the values the description must not carry.
		arrange   func(t *testing.T) (home, id string, desc apitest.DescCase, forbid []string)
		wantErr   string
		wantCalls []string
	}{
		{
			name: "leftover",
			arrange: func(t *testing.T) (string, string, apitest.DescCase, []string) {
				home, id, socket := seedKillRow(t, store.StateWaiting)
				token, _, storeID := launchIdentity(t, home, id)
				leftover := apitest.DescSession{Name: "kill-leftover", ID: "$4"}
				label := tmuxfix.LabelValue(tmuxfix.OtherToken, leftover.ID, id, storeID)
				faketmuxfix.Tables{}.Write(t, socket, killTable(faketmuxfix.Session{
					ID: leftover.ID, Created: time.Now().Unix(), Name: leftover.Name, Label: label,
					Panes: []faketmuxfix.Pane{{ID: "%4", PID: apitest.TestPanePID}},
				}))
				desc := apitest.DescKillLeftover([]apitest.DescSession{leftover})
				return home, id, desc, []string{token, tmuxfix.OtherToken, storeID, label}
			},
			wantErr:   "ErrTmuxSessionConflict",
			wantCalls: []string{"list-sessions"},
		},
		{
			name: "gone with the agent alive and no pane",
			arrange: func(t *testing.T) (string, string, apitest.DescCase, []string) {
				pid, start := liveChild(t)
				home := t.TempDir()
				socket := spawnSocket(t, home)
				const token = "5eed0000000000c3"
				id, err := apitest.SeedSpawn(stateDB(home), "", store.StateWaiting, "", "", "", true,
					apitest.WithLaunchIdentity(store.LaunchIdentity{
						Token: token, Socket: socket, PaneID: apitest.TestPaneID, PanePID: pid, PaneStarttime: start,
					}))
				if err != nil {
					t.Fatalf("SeedSpawn: %v", err)
				}
				t.Cleanup(func() {
					if _, alive, _ := probe.NewProcChecker().StartTime(pid); !alive {
						t.Errorf("agent process %d is gone after kill; kill must never signal it", pid)
					}
				})
				name, _ := rowColumns(t, home, id).TmuxSessionName.(string)
				return home, id, apitest.DescKillNoPane(id, name, pid), []string{token}
			},
			wantErr:   "ErrTmuxKillFailed",
			wantCalls: []string{"list-sessions", "list-panes"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home, id, desc, forbid := tc.arrange(t)
			stdout, stderr, code := runSpawnCLI(t, home, fakeDir, "kill", "--claude-instance-id", id)
			if code != 1 || stdout != "" {
				t.Fatalf("kill exit = %d, stdout = %q; want 1 and empty (stderr=%q)", code, stdout, stderr)
			}
			env := parseEnvelope(t, stderr)
			if env.ErrName != tc.wantErr {
				t.Errorf("err_name = %q; want %q", env.ErrName, tc.wantErr)
			}
			apitest.AssertDescription(t, env.ErrDescription, desc, forbid...)
			assertInvocationKinds(t, home, tc.wantCalls...)
			if st := rowColumns(t, home, id).State; st != store.StateWaiting {
				t.Errorf("row state after kill = %v; want %s unchanged", st, store.StateWaiting)
			}
		})
	}
}

func TestPauseCLIEndedRowIsNoop(t *testing.T) {
	// pause on an ended row exits 0 without touching tmux. This is the
	// only pause CLI path that does not require a transition simulator,
	// so it's the cheap CLI smoke test. The full state-flip behavior is
	// covered by the internal/api unit tests.
	fakeDir := buildFakeTmux(t)
	home := t.TempDir()
	bootstrapDB(t, home)
	dbPath := filepath.Join(home, ".agent-director", "state.db")
	seedSpawnRow(t, dbPath, "id-pause-1", "cd-pause-1", "ended", "off")

	stdout, stderr, code := runSpawnCLI(t, home, fakeDir,
		"pause", "--claude-instance-id", "id-pause-1")
	if code != 0 {
		t.Fatalf("pause on ended row exit = %d; stderr=%s", code, stderr)
	}
	if strings.TrimSpace(stdout) != "{}" {
		t.Errorf("stdout = %q; want \"{}\"", stdout)
	}
	logBytes, _ := os.ReadFile(filepath.Join(home, "fake-tmux.log"))
	if strings.Contains(string(logBytes), "send-keys") {
		t.Errorf("ended row should not trigger send-keys: %s", string(logBytes))
	}
}

// TestPauseCLIClearsInputLineBeforeExit (b.9o4): on a waiting row pause sends
// C-u to the agent's pane by id as a key (send-keys -t <pane id> C-u), then
// /exit literally and Enter; nothing ends the row, so its 1 s wait times out.
func TestPauseCLIClearsInputLineBeforeExit(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	home, id, socket := seedKillRow(t, store.StateWaiting)
	faketmuxfix.Tables{}.Write(t, socket, killTable(ownSession(t, home, id)))
	if err := os.WriteFile(filepath.Join(directorDir(home), "config.toml"), []byte("[pause]\ntimeout_seconds = 1\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	stdout, stderr, code := runSpawnCLI(t, home, fakeDir, "pause", "--claude-instance-id", id)

	if code == 0 || stdout != "" {
		t.Fatalf("pause exit = %d, stdout = %q; want 1 and empty, the wait timed out (stderr=%q)", code, stdout, stderr)
	}
	if env := parseEnvelope(t, stderr); env.ErrName != "ErrPauseTimeout" {
		t.Errorf("err_name = %q (%s); want ErrPauseTimeout", env.ErrName, env.ErrDescription)
	}
	invs := assertInvocationKinds(t, home, "list-sessions", "list-panes", "send-keys", "send-keys", "send-keys")
	target := []string{"-u", "-S", socket, "send-keys", "-t", apitest.TestPaneID}
	for i, want := range [][]string{append(slices.Clone(target), "C-u"), append(slices.Clone(target), "-l", "--", "/exit"),
		append(slices.Clone(target), "Enter")} {
		if !slices.Equal(invs[2+i], want) {
			t.Errorf("invocation %d = %q; want %q", 2+i, invs[2+i], want)
		}
	}
}

// TestPauseCLILineClearFails (b.9o4): a failed C-u key send ends pause with
// ErrTmuxUnresponsive naming the key send, "nothing was done; retry later";
// no /exit or Enter follows and the row stays waiting.
func TestPauseCLILineClearFails(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	home, id, socket := seedKillRow(t, store.StateWaiting)
	token, _, storeID := launchIdentity(t, home, id)
	faketmuxfix.Tables{}.Write(t, socket, killTable(ownSession(t, home, id)))
	noPane := tmuxfix.Find(tmuxfix.Replies(socket), "reply/cant-find-pane")
	faketmuxfix.Tables{}.Inject(t, socket, faketmuxfix.Reply(tmux.CallSendKey, noPane))

	stdout, stderr, code := runSpawnCLI(t, home, fakeDir, "pause", "--claude-instance-id", id)

	if code != 1 || stdout != "" {
		t.Fatalf("pause exit = %d, stdout = %q; want 1 and empty (stderr=%q)", code, stdout, stderr)
	}
	env := parseEnvelope(t, stderr)
	if env.ErrName != "ErrTmuxUnresponsive" {
		t.Errorf("err_name = %q (%s); want ErrTmuxUnresponsive", env.ErrName, env.ErrDescription)
	}
	desc := apitest.DescUnrecognisedReply(tmux.CallSendKey, noPane.FirstLine).AfterTextFailed()
	desc.Require = append(desc.Require, "tmux key send failed")
	apitest.AssertDescription(t, env.ErrDescription, desc, token, storeID)
	invs := assertInvocationKinds(t, home, "list-sessions", "list-panes", "send-keys", "list-sessions")
	if want := []string{"-u", "-S", socket, "send-keys", "-t", apitest.TestPaneID, "C-u"}; !slices.Equal(invs[2], want) {
		t.Errorf("key send invocation = %q; want %q", invs[2], want)
	}
	if st := rowColumns(t, home, id).State; st != store.StateWaiting {
		t.Errorf("row state after pause = %v; want %s unchanged", st, store.StateWaiting)
	}
}

func TestPauseCLIWorkingStateRejected(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	home := t.TempDir()
	bootstrapDB(t, home)
	dbPath := filepath.Join(home, ".agent-director", "state.db")
	seedSpawnRow(t, dbPath, "id-pause-2", "cd-pause-2", "working", "off")

	_, stderr, code := runSpawnCLI(t, home, fakeDir,
		"pause", "--claude-instance-id", "id-pause-2")
	if code == 0 {
		t.Fatalf("expected non-zero exit; got 0 (stderr=%s)", stderr)
	}
	env := parseEnvelope(t, stderr)
	if env.ErrName != "ErrSpawnNotPausable" {
		t.Errorf("err_name = %q; want ErrSpawnNotPausable", env.ErrName)
	}
}

func TestPauseCLIErrSpawnNotFound(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	home := t.TempDir()
	bootstrapDB(t, home)

	_, stderr, code := runSpawnCLI(t, home, fakeDir,
		"pause", "--claude-instance-id", "absent")
	if code == 0 {
		t.Fatalf("expected non-zero exit; got 0 (stderr=%s)", stderr)
	}
	env := parseEnvelope(t, stderr)
	if env.ErrName != "ErrSpawnNotFound" {
		t.Errorf("err_name = %q; want ErrSpawnNotFound", env.ErrName)
	}
}

func TestPauseCLIMissingInstanceID(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	home := t.TempDir()
	bootstrapDB(t, home)

	_, stderr, code := runSpawnCLI(t, home, fakeDir, "pause")
	if code == 0 {
		t.Fatalf("expected non-zero exit; got 0 (stderr=%s)", stderr)
	}
	env := parseEnvelope(t, stderr)
	if env.ErrName != "ErrInvalidFlags" {
		t.Errorf("err_name = %q; want ErrInvalidFlags", env.ErrName)
	}
}

func TestKillCLIMissingInstanceID(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	home := t.TempDir()
	bootstrapDB(t, home)

	_, stderr, code := runSpawnCLI(t, home, fakeDir, "kill")
	if code == 0 {
		t.Fatalf("expected non-zero exit; got 0 (stderr=%s)", stderr)
	}
	env := parseEnvelope(t, stderr)
	if env.ErrName != "ErrInvalidFlags" {
		t.Errorf("err_name = %q; want ErrInvalidFlags", env.ErrName)
	}
}
