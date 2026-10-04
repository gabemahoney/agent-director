package main_test

// testhelpers_test.go holds agent-director-admin's test setup: both binaries
// built once with one version stamp, runs under a per-test HOME with the fake
// tmux first on PATH, and the row, fake-table and trail helpers the tests
// share.

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/faketmuxfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/sandboxguard"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// The version stamp TestMain builds both binaries with.
const (
	testVersion = "0.0.0-vqr-test"
	testCommit  = "0123456789abcdef0123456789abcdef01234567"
)

// approvalStatement is the first line of every help agent-director-admin
// prints (b.vqr), word for word.
const approvalStatement = "agent-director-admin is an operator tool. Do not run any of its commands without explicit approval " +
	"from a human for this specific run. Agents and automated callers must not run it."

// adminPath and mainPath are the binaries TestMain builds.
var adminPath, mainPath string

// The default stopping window and starting-session bound kill-finished uses.
var (
	defWindow = time.Duration(config.DefaultStoppingWindowSeconds) * time.Second
	defBound  = time.Duration(config.DefaultStartingSessionSeconds) * time.Second
)

// TestMain builds agent-director-admin and agent-director with the same
// version stamp, then pins HOME to a temp dir so in-process seeding never
// reaches the real ~/.agent-director.
func TestMain(m *testing.M) {
	sandboxguard.Require()
	tmp, err := os.MkdirTemp("", "agent-director-admin-test-")
	if err != nil {
		panic(err)
	}
	ldflags := "-X github.com/gabemahoney/agent-director/internal/version.Version=" + testVersion +
		" -X github.com/gabemahoney/agent-director/internal/version.Commit=" + testCommit
	adminPath = filepath.Join(tmp, "agent-director-admin")
	mainPath = filepath.Join(tmp, "agent-director")
	for out, pkg := range map[string]string{adminPath: ".", mainPath: "../agent-director"} {
		build := exec.Command("go", "build", "-ldflags="+ldflags, "-o", out, pkg)
		build.Stdout, build.Stderr = os.Stdout, os.Stderr
		if err := build.Run(); err != nil {
			panic(err)
		}
	}
	home := filepath.Join(tmp, "home")
	if err := os.Mkdir(home, 0o700); err != nil {
		panic(err)
	}
	if err := os.Setenv("HOME", home); err != nil {
		panic(err)
	}
	code := m.Run()
	_ = os.RemoveAll(tmp)
	os.Exit(code)
}

// errorEnvelope is the JSON shape both binaries write on stderr for an error.
type errorEnvelope struct {
	ErrName        string `json:"err_name"`
	ErrDescription string `json:"err_description"`
}

// stateDB returns the path of the store under home.
func stateDB(home string) string {
	return filepath.Join(home, ".agent-director", "state.db")
}

// tmuxTmpdir returns home's private TMUX_TMPDIR, making it.
func tmuxTmpdir(t *testing.T, home string) string {
	t.Helper()
	dir := filepath.Join(home, "tmux-tmpdir")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir TMUX_TMPDIR: %v", err)
	}
	return dir
}

// runBin runs bin with args under home, with the fake tmux first on PATH, its
// log under home and home's private TMUX_TMPDIR, so no run can reach a real
// tmux server; it returns stdout, stderr and the exit code.
func runBin(t *testing.T, bin, home string, args ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Env = []string{
		"PATH=" + faketmuxfix.Dir(t) + ":" + os.Getenv("PATH"),
		"HOME=" + home,
		"FAKE_TMUX_LOG=" + filepath.Join(home, "fake-tmux.log"),
		"TMUX_TMPDIR=" + tmuxTmpdir(t, home),
	}
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	code := 0
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			t.Fatalf("run %s %q: %v", filepath.Base(bin), args, err)
		}
		code = ee.ExitCode()
	}
	return stdout.String(), stderr.String(), code
}

// runAdmin runs agent-director-admin as runBin does.
func runAdmin(t *testing.T, home string, args ...string) (string, string, int) {
	t.Helper()
	return runBin(t, adminPath, home, args...)
}

// runMain runs agent-director as runBin does.
func runMain(t *testing.T, home string, args ...string) (string, string, int) {
	t.Helper()
	return runBin(t, mainPath, home, args...)
}

// assertOnlyEnvelope fails unless the run exited 1 with empty stdout and
// stderr holding exactly one error envelope named want; it returns it.
func assertOnlyEnvelope(t *testing.T, stdout, stderr string, code int, want string) errorEnvelope {
	t.Helper()
	if code != 1 || stdout != "" {
		t.Fatalf("exit = %d, stdout = %q; want 1 and empty (stderr=%q)", code, stdout, stderr)
	}
	var env errorEnvelope
	dec := json.NewDecoder(strings.NewReader(stderr))
	if err := dec.Decode(&env); err != nil || dec.More() {
		t.Fatalf("stderr = %q; want exactly one JSON error envelope (decode: %v)", stderr, err)
	}
	if env.ErrName != want {
		t.Errorf("err_name = %q (%s); want %s", env.ErrName, env.ErrDescription, want)
	}
	return env
}

// assertKillSent checks stdout is exactly kill-finished's result with
// kill_sent want.
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

// spawnSocket returns the socket a launch under home uses, as the binaries
// run by runBin resolve it.
func spawnSocket(t *testing.T, home string) string {
	t.Helper()
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_TMPDIR", tmuxTmpdir(t, home))
	socket, err := tmux.ResolveSocket(true)
	if err != nil {
		t.Fatalf("resolve socket: %v", err)
	}
	return socket
}

// seedRow seeds one row in state with Claude session id sessionID under a
// fresh HOME on the socket the binaries resolve; it returns the HOME, the
// row's id and its socket.
func seedRow(t *testing.T, state, sessionID string, opts ...apitest.SpawnOption) (home, id, socket string) {
	t.Helper()
	home = t.TempDir()
	socket = spawnSocket(t, home)
	opts = append([]apitest.SpawnOption{apitest.WithTmuxSocket(socket)}, opts...)
	id, err := apitest.SeedSpawn(stateDB(home), "", state, "", "", sessionID, true, opts...)
	if err != nil {
		t.Fatalf("SeedSpawn: %v", err)
	}
	return home, id, socket
}

// launchIdentity reads id's launch token and the store's id under home.
func launchIdentity(t *testing.T, home, id string) (token, storeID string) {
	t.Helper()
	token, _ = rowColumns(t, home, id).LaunchToken.(string)
	storeID, err := apitest.ReadStoreID(stateDB(home))
	if err != nil {
		t.Fatalf("ReadStoreID: %v", err)
	}
	return token, storeID
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

// assertRowUnchanged fails unless id's columns under home still equal before.
func assertRowUnchanged(t *testing.T, home, id string, before apitest.SpawnColumns) {
	t.Helper()
	after, err := apitest.ReadSpawnColumns(stateDB(home), id)
	if err != nil || !reflect.DeepEqual(after, before) {
		t.Errorf("row %s = %+v (err %v); want unchanged %+v", id, after, err, before)
	}
}

// assertRowGone fails unless the store under home has no row id.
func assertRowGone(t *testing.T, home, id string) {
	t.Helper()
	if _, err := apitest.ReadSpawnColumns(stateDB(home), id); !errors.Is(err, store.ErrSpawnNotFound) {
		t.Errorf("read row %s: %v; want ErrSpawnNotFound", id, err)
	}
}

// ownSession writes id's fake table under home: a server held by this test
// process with one session labelled for id's current launch, created at
// created, and one pane labelled for it.
func ownSession(t *testing.T, home, id, socket string, created time.Time) {
	t.Helper()
	token, storeID := launchIdentity(t, home, id)
	name, _ := rowColumns(t, home, id).TmuxSessionName.(string)
	const sessionID = "$5"
	faketmuxfix.Tables{}.Write(t, socket, faketmuxfix.Table{
		Server: &faketmuxfix.Server{PID: os.Getpid(), Start: time.Now().Unix()},
		Sessions: []faketmuxfix.Session{{
			ID: sessionID, Created: created.Unix(), Name: name, Label: tmuxfix.LabelValue(token, sessionID, id, storeID),
			Panes: []faketmuxfix.Pane{{ID: apitest.TestPaneID, PID: apitest.TestPanePID,
				AdPane: tmuxfix.PaneLabelValue(token, apitest.TestPaneID)}},
		}},
	})
}

// sessionsLeft returns the sessions still in socket's fake table.
func sessionsLeft(t *testing.T, socket string) []faketmuxfix.Session {
	t.Helper()
	return faketmuxfix.Tables{}.Read(t, socket).Sessions
}

// finishedRow is an ended row seeded with its own labelled session.
type finishedRow struct {
	home, id, socket string
	before           apitest.SpawnColumns
}

// seedFinishedWithSession seeds an ended row (pid above the pid limit, a
// session id) an hour past the default window, with its own session created
// an hour past the default bound before ended_at; opts override the defaults.
func seedFinishedWithSession(t *testing.T, opts ...apitest.SpawnOption) finishedRow {
	t.Helper()
	endedAt := time.Now().Add(-defWindow - time.Hour).Truncate(time.Second)
	home, id, socket := seedRow(t, store.StateEnded, uuid.NewString(),
		append([]apitest.SpawnOption{apitest.WithPID(apitest.TestPanePID + 1), apitest.WithEndedAt(endedAt)}, opts...)...)
	ownSession(t, home, id, socket, endedAt.Add(-defBound-time.Hour))
	return finishedRow{home: home, id: id, socket: socket, before: rowColumns(t, home, id)}
}

// invocations returns each argv the fake tmux logged under home, without
// argv[0], in call order; nil when it was never run.
func invocations(t *testing.T, home string) [][]string {
	t.Helper()
	var out [][]string
	for _, argv := range faketmuxfix.ReadLog(t, filepath.Join(home, "fake-tmux.log")) {
		out = append(out, argv[1:])
	}
	return out
}

// assertInvocationKinds fails unless the fake's invocations under home are,
// in order, the named tmux commands; it returns them.
func assertInvocationKinds(t *testing.T, home string, want ...string) [][]string {
	t.Helper()
	invs := invocations(t, home)
	if len(invs) != len(want) {
		t.Fatalf("fake-tmux invocations = %d; want %d (%v): %q", len(invs), len(want), want, invs)
	}
	for i, argv := range invs {
		if !slices.Contains(argv, want[i]) {
			t.Errorf("invocation %d = %q; want a %s", i, argv, want[i])
		}
	}
	return invs
}

// killCalled returns the ad.kill.called records of the trail under home.
func killCalled(t *testing.T, home string) []map[string]any {
	t.Helper()
	f, err := os.Open(filepath.Join(home, ".agent-director", "ad-trail.jsonl"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatalf("open trail: %v", err)
	}
	defer f.Close()
	var out []map[string]any
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		var rec map[string]any
		if err := json.Unmarshal(sc.Bytes(), &rec); err != nil {
			t.Fatalf("trail line %q: %v", sc.Text(), err)
		}
		if rec["event"] == "ad.kill.called" {
			out = append(out, rec)
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("read trail: %v", err)
	}
	return out
}
