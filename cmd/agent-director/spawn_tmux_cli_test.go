package main_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/testsupport/faketmuxfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// The spawn CLI's tmux side (SR-20.3): one labelled create on the socket under
// the test's private TMUX_TMPDIR, the bounded create, tmux unavailable, and
// plain spawn's label scan, all against test/fake-tmux.

const (
	// shortCreateTimeout is the create timeout the hung-create case configures.
	shortCreateTimeout = 300 * time.Millisecond
	// fakeHangBound caps the hung fake's own wait, so no fake outlives a test.
	fakeHangBound = 10 * time.Second
)

// useSpawnTmuxEnv gives this test process the environment the CLI child gets
// from runSpawnCLIEnv (home's TMUX_TMPDIR, no TMUX), so in-process resolution
// finds the child's socket.
func useSpawnTmuxEnv(t *testing.T, home string) {
	t.Helper()
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_TMPDIR", spawnTmuxTmpdir(t, home))
}

// spawnSocket returns the socket a spawn under home creates its session on,
// making its per-user directory as the launch does.
func spawnSocket(t *testing.T, home string) string {
	t.Helper()
	useSpawnTmuxEnv(t, home)
	socket, err := tmux.ResolveSocket(true)
	if err != nil {
		t.Fatalf("resolve socket: %v", err)
	}
	return socket
}

// fakeTmuxInvocations returns each argv the fake logged under home, without
// argv[0], in call order; nil when the fake was never run.
func fakeTmuxInvocations(t *testing.T, home string) [][]string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(home, "fake-tmux.log"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatalf("read fake-tmux log: %v", err)
	}
	var out [][]string
	for _, rec := range strings.Split(string(raw), "---\n") {
		if lines := strings.Split(strings.TrimSuffix(rec, "\n"), "\n"); len(lines) > 1 {
			out = append(out, lines[1:])
		}
	}
	return out
}

// assertInvocationKinds fails unless the fake's invocations under home are,
// in order, the named commands (list-sessions for the lookup, new-session for
// the create) and no invocation holds a create it was not expected to.
func assertInvocationKinds(t *testing.T, home string, want ...string) [][]string {
	t.Helper()
	invs := fakeTmuxInvocations(t, home)
	if len(invs) != len(want) {
		t.Fatalf("fake-tmux invocations = %d; want %d (%v): %q", len(invs), len(want), want, invs)
	}
	for i, argv := range invs {
		if !slices.Contains(argv, want[i]) {
			t.Errorf("invocation %d = %q; want a %s", i, argv, want[i])
		}
		if want[i] != "new-session" && slices.Contains(argv, "new-session") {
			t.Errorf("invocation %d is a create; want only a %s: %q", i, want[i], argv)
		}
	}
	return invs
}

// spawnRowIDs returns the instance ids `list` shows under home.
func spawnRowIDs(t *testing.T, home, fakeDir string) []string {
	t.Helper()
	stdout, stderr, code := runSpawnCLI(t, home, fakeDir, "list")
	if code != 0 {
		t.Fatalf("list exit = %d; stderr=%q", code, stderr)
	}
	var res listResult
	if err := json.Unmarshal([]byte(stdout), &res); err != nil {
		t.Fatalf("parse list %q: %v", stdout, err)
	}
	var ids []string
	for _, sp := range res.Spawns {
		ids = append(ids, sp.ClaudeInstanceID)
	}
	return ids
}

// statusOf runs `status` for id and returns its state, or its err_name when
// the verb fails.
func statusOf(t *testing.T, home, fakeDir, id string) string {
	t.Helper()
	stdout, stderr, code := runSpawnCLI(t, home, fakeDir, "status", "--claude-instance-id", id)
	if code != 0 {
		return parseEnvelope(t, lastJSONLine(stderr)).ErrName
	}
	var st struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal([]byte(stdout), &st); err != nil {
		t.Fatalf("parse status %q: %v", stdout, err)
	}
	return st.State
}

// launchIdentity reads id's launch token and socket, and the store's id.
func launchIdentity(t *testing.T, home, id string) (token, socket, storeID string) {
	t.Helper()
	cols, err := apitest.ReadSpawnColumns(stateDB(home), id)
	if err != nil {
		t.Fatalf("ReadSpawnColumns: %v", err)
	}
	storeID, err = apitest.ReadStoreID(stateDB(home))
	if err != nil {
		t.Fatalf("ReadStoreID: %v", err)
	}
	token, _ = cols.LaunchToken.(string)
	socket, _ = cols.TmuxSocket.(string)
	return token, socket, storeID
}

// TestSpawnCLICreatesOneLabelledInvocation: a minted-id spawn makes exactly
// one tmux invocation, the create with both chained labels (SR-2.1, SR-3.5).
func TestSpawnCLICreatesOneLabelledInvocation(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	home := t.TempDir()
	const name = "w1-labelled"
	stdout, stderr, code := runSpawnCLI(t, home, fakeDir,
		"spawn", "--cwd", t.TempDir(), "--tmux-session-name", name, "--no-pre-trust")
	if code != 0 {
		t.Fatalf("exit = %d; stderr=%s", code, stderr)
	}
	var res spawnResult
	if err := json.Unmarshal([]byte(stdout), &res); err != nil {
		t.Fatalf("parse stdout %q: %v", stdout, err)
	}
	id := res.ClaudeInstanceID
	socket := spawnSocket(t, home)
	token, rowSocket, storeID := launchIdentity(t, home, id)
	if len(token) != 16 || rowSocket != socket {
		t.Errorf("row token = %q, socket = %q; want a 16-hex token on %q", token, rowSocket, socket)
	}
	tmpdir, err := filepath.EvalSymlinks(spawnTmuxTmpdir(t, home))
	if err != nil || !strings.HasPrefix(socket, tmpdir+string(filepath.Separator)) {
		t.Errorf("socket %q is not under the test's TMUX_TMPDIR %q (%v)", socket, tmpdir, err)
	}

	argv := assertInvocationKinds(t, home, "new-session")[0]
	if len(argv) < 4 || !slices.Equal(argv[:4], []string{"-u", "-S", socket, "new-session"}) {
		t.Errorf("create argv head = %q; want [-u -S %s new-session]", argv[:min(4, len(argv))], socket)
	}
	target := "=" + name + ":"
	chain := []string{
		";", "set-option", "-F", "-t", target, "@ad_owner", tmuxfix.ChainLabelValue(token, id, storeID),
		";", "set-option", "-p", "-F", "-t", target, "@ad_pane", tmuxfix.ChainPaneLabelValue(token),
	}
	if len(argv) < len(chain) || !slices.Equal(argv[len(argv)-len(chain):], chain) {
		t.Errorf("create argv = %q; want it to end with the chain %q", argv, chain)
	}
	idEnv, launchEnv := 0, 0
	for _, a := range argv {
		if a == "AGENT_DIRECTOR_INSTANCE_ID="+id {
			idEnv++
		}
		if strings.Contains(a, "AGENT_DIRECTOR_LAUNCH_STARTED_AT") {
			launchEnv++
		}
	}
	if idEnv != 1 || launchEnv != 0 {
		t.Errorf("create argv has %d instance-id and %d launch-start entries; want 1 and 0: %q", idEnv, launchEnv, argv)
	}

	sessions := faketmuxfix.Tables{}.Read(t, socket).Sessions
	if len(sessions) != 1 || len(sessions[0].Panes) != 1 {
		t.Fatalf("fake sessions = %+v; want one session with one pane", sessions)
	}
	s, p := sessions[0], sessions[0].Panes[0]
	if s.Label != tmuxfix.LabelValue(token, s.ID, id, storeID) || p.AdPane != tmuxfix.PaneLabelValue(token, p.ID) {
		t.Errorf("session label %q, pane label %q; want this launch's labels", s.Label, p.AdPane)
	}
}

// TestSpawnCLICreateFailures: a hung create, a permission reply on the create
// and an unsafe per-user socket directory, each as its catalogued error.
func TestSpawnCLICreateFailures(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	cases := []struct {
		name    string
		arrange func(t *testing.T, home string) func(id string) apitest.DescCase
		wantErr string
		wantRow bool // the row stays pending after one create invocation
	}{
		{
			name: "hung create",
			arrange: func(t *testing.T, home string) func(string) apitest.DescCase {
				apitest.WriteTmuxConfig(t, filepath.Join(directorDir(home), "config.toml"),
					apitest.TmuxInt(config.TmuxCreateTimeoutMs, shortCreateTimeout.Milliseconds()))
				socket := spawnSocket(t, home)
				faketmuxfix.Tables{}.Inject(t, socket, faketmuxfix.Hang(tmux.CallCreate).Bound(fakeHangBound))
				return func(id string) apitest.DescCase {
					return apitest.DescLaunchTimeout(apitest.LaunchTimeout{InstanceID: id, Timeout: shortCreateTimeout})
				}
			},
			wantErr: "ErrTmuxUnresponsive",
			wantRow: true,
		},
		{
			name: "permission reply on create",
			arrange: func(t *testing.T, home string) func(string) apitest.DescCase {
				socket := spawnSocket(t, home)
				faketmuxfix.Tables{}.Inject(t, socket, faketmuxfix.Reply(tmux.CallCreate, tmuxfix.SocketDenied(socket)))
				return func(string) apitest.DescCase { return apitest.DescSocketPermission(socket) }
			},
			wantErr: "ErrTmuxNotAvailable",
			wantRow: true,
		},
		{
			name: "unsafe socket directory",
			arrange: func(t *testing.T, home string) func(string) apitest.DescCase {
				socket := spawnSocket(t, home)
				if err := os.Chmod(filepath.Dir(socket), 0o755); err != nil {
					t.Fatalf("chmod per-user dir: %v", err)
				}
				var sde *tmux.SocketDirError
				if _, err := tmux.ResolveSocket(false); !errors.As(err, &sde) {
					t.Fatalf("ResolveSocket err = %v; want a *tmux.SocketDirError", err)
				}
				return func(string) apitest.DescCase { return apitest.DescSocketDir(sde.Socket, sde.Dir, sde.Error()) }
			},
			wantErr: "ErrTmuxNotAvailable",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			bootstrapDB(t, home)
			desc := tc.arrange(t, home)
			_, stderr, code := runSpawnCLI(t, home, fakeDir, "spawn", "--cwd", t.TempDir(), "--no-pre-trust")
			if code == 0 {
				t.Fatalf("exit = 0; want non-zero (stderr=%q)", stderr)
			}
			env := parseEnvelope(t, lastJSONLine(stderr))
			if env.ErrName != tc.wantErr {
				t.Errorf("err_name = %q; want %q (stderr=%q)", env.ErrName, tc.wantErr, stderr)
			}
			ids := spawnRowIDs(t, home, fakeDir)
			if !tc.wantRow {
				if len(ids) != 0 {
					t.Errorf("rows = %q; want none", ids)
				}
				assertInvocationKinds(t, home)
				apitest.AssertDescription(t, env.ErrDescription, desc(""))
				return
			}
			if len(ids) != 1 {
				t.Fatalf("rows = %q; want one", ids)
			}
			if st := statusOf(t, home, fakeDir, ids[0]); st != "pending" {
				t.Errorf("status = %q; want pending", st)
			}
			assertInvocationKinds(t, home, "new-session")
			token, _, storeID := launchIdentity(t, home, ids[0])
			apitest.AssertDescription(t, env.ErrDescription, desc(ids[0]), token, storeID)
		})
	}
}

// TestSpawnCLIScanRefusesLeftover: a session this store labelled for the
// explicit id still runs, so spawn refuses after one lookup and writes nothing.
func TestSpawnCLIScanRefusesLeftover(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	home := t.TempDir()
	bootstrapDB(t, home)
	storeID, err := apitest.ReadStoreID(stateDB(home))
	if err != nil {
		t.Fatalf("ReadStoreID: %v", err)
	}
	claudeJSON := filepath.Join(home, ".claude.json")
	if err := os.WriteFile(claudeJSON, []byte(`{"projects":{}}`), 0o600); err != nil {
		t.Fatalf("seed claude.json: %v", err)
	}
	id := "id-w1-scan-" + uuid.NewString()[:8]
	leftover := apitest.DescSession{Name: "w1-leftover", ID: "$4"}
	label := tmuxfix.LabelValue(tmuxfix.OtherToken, leftover.ID, id, storeID)
	socket := spawnSocket(t, home)
	faketmuxfix.Tables{}.Write(t, socket, faketmuxfix.Table{
		Server: &faketmuxfix.Server{PID: os.Getpid(), Start: 1790549182},
		Sessions: []faketmuxfix.Session{{
			ID: leftover.ID, Created: 1790549182, Name: leftover.Name, Label: label,
			Panes: []faketmuxfix.Pane{{ID: "%4", PID: os.Getpid()}},
		}},
	})

	cwd := t.TempDir()
	_, stderr, code := runSpawnCLI(t, home, fakeDir, "spawn", "--cwd", cwd, "--claude-instance-id", id)
	if code == 0 {
		t.Fatalf("exit = 0; want non-zero (stderr=%q)", stderr)
	}
	env := parseEnvelope(t, lastJSONLine(stderr))
	if env.ErrName != "ErrTmuxSessionConflict" {
		t.Errorf("err_name = %q; want ErrTmuxSessionConflict (stderr=%q)", env.ErrName, stderr)
	}
	apitest.AssertDescription(t, env.ErrDescription,
		apitest.DescScanLeftover(id, []apitest.DescSession{leftover}), storeID, tmuxfix.OtherToken, label)
	if st := statusOf(t, home, fakeDir, id); st != "ErrSpawnNotFound" {
		t.Errorf("status = %q; want ErrSpawnNotFound", st)
	}
	assertInvocationKinds(t, home, "list-sessions")
	if raw, _ := os.ReadFile(claudeJSON); strings.Contains(string(raw), cwd) {
		t.Errorf("claude.json gained a trust entry for %s: %s", cwd, raw)
	}
	held := 0
	for _, l := range trailLinesOrNil(t, trailDir(home)) {
		if l["event"] == "ad.launch.name_held" && l["claude_instance_id"] == id {
			held++
		}
	}
	if held != 1 {
		t.Errorf("ad.launch.name_held records for %s = %d; want 1", id, held)
	}
}

// TestSpawnCLIScanThenCreate: an explicit id with no row and an empty table
// logs one lookup before the create, and the spawn succeeds.
func TestSpawnCLIScanThenCreate(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	home := t.TempDir()
	id := "id-w1-clear-" + uuid.NewString()[:8]
	_, stderr, code := runSpawnCLI(t, home, fakeDir,
		"spawn", "--cwd", t.TempDir(), "--claude-instance-id", id, "--no-pre-trust")
	if code != 0 {
		t.Fatalf("exit = %d; stderr=%s", code, stderr)
	}
	assertInvocationKinds(t, home, "list-sessions", "new-session")
	if st := statusOf(t, home, fakeDir, id); st != "pending" {
		t.Errorf("status = %q; want pending", st)
	}
}
