package runner

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// runnerFixture supplies only private paths, the shared clock, and the runner's
// command seam. Database setup remains with apitest and storefix.
func runnerFixture(t *testing.T) (*Run, *tmuxfix.Clock) {
	t.Helper()
	base := t.TempDir()
	e, err := NewEvidence(filepath.Join(base, "evidence"))
	if err != nil {
		t.Fatal(err)
	}
	clock := tmuxfix.NewClock(time.Now().UTC())
	r := &Run{Home: filepath.Join(base, "home"), Kit: filepath.Join(base, "kit"), Evidence: e, Now: clock.Now}
	r.runtimeDir = filepath.Join(base, "runtime")
	if err := os.Mkdir(r.runtimeDir, 0700); err != nil {
		t.Fatal(err)
	}
	r.Socket = filepath.Join(r.runtimeDir, "tmux.sock")
	t.Setenv("HOME", r.Home)
	r.Manifest = Manifest{Schema: Schema, RunID: uuid.NewString(), Phase: "runner-test", Started: clock.Now(), Deadline: clock.Now().Add(PhaseLimit), TurnBudget: 6}
	r.Sleep = func(ctx context.Context, d time.Duration) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		clock.Advance(d)
		return nil
	}
	r.Commands = &Commands{Evidence: e, Now: clock.Now, Environment: []string{"TMUX=" + r.Socket + ",0,0"},
		Execute: func(context.Context, []string, []string, io.Writer, io.Writer) (int, error) {
			t.Error("unexpected command")
			return -1, context.Canceled
		}}
	return r, clock
}

// lifecycleKit supplies real stamped executables and the supported installer.
// Its synthetic provenance is setup only, never live qualification evidence.
func lifecycleKit(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "kit")
	const module = "github.com/gabemahoney/agent-director/"
	k := Kit{Schema: Schema, BaseCommit: strings.Repeat("1", 40), SnapshotTree: strings.Repeat("2", 40),
		SourceSHA256: Digest([]byte("synthetic source")), DiffSHA256: Digest(nil), Version: "0.0.0-runner-fixture",
		Platform: runtime.GOOS + "/" + runtime.GOARCH, Files: map[string]Artifact{}}
	k.CommitStamp = k.BaseCommit
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	for _, component := range []string{"agent-director", "agent-director-admin", "claude-live"} {
		name, pkg := "payload/"+component+"-linux-"+runtime.GOARCH, "cmd/"+component
		if component == "claude-live" {
			name, pkg = "bin/claude-live", "test/claude-live/cmd/claude-live"
		}
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		flags := "-s -w -X " + module + "internal/version.Version=" + k.Version + " -X " + module + "internal/version.Commit=" + k.CommitStamp
		cmd := exec.CommandContext(ctx, "go", "build", "-ldflags="+flags, "-o", path, module+pkg)
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build lifecycle candidate: %v\n%s", err, out)
		}
	}
	for _, name := range []string{"install.sh", "schedule/agent-director.cron", "schedule/agent-director-find-missing.service", "schedule/agent-director-find-missing.timer", "schedule/agent-director-expire.service", "schedule/agent-director-expire.timer"} {
		rel := filepath.Join("skills/install-agent-director", name)
		putFixture(t, filepath.Join(root, rel), readFixture(t, filepath.Join("../../..", rel)))
	}
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		name, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		b := readFixture(t, path)
		k.Files[name] = Artifact{SHA256: Digest(b), Bytes: int64(len(b))}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(k)
	if err != nil {
		t.Fatal(err)
	}
	putFixture(t, filepath.Join(root, "kit.json"), b)
	cmd := exec.CommandContext(ctx, "tar", "-C", root, "-czf", filepath.Join(filepath.Dir(root), "claude-live-kit.tar.gz"), ".")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("archive lifecycle candidate: %v\n%s", err, out)
	}
	return root
}

func scopedRunner(t *testing.T, scope, kit, phase string) *Run {
	t.Helper()
	home := filepath.Join(scope, "home")
	if err := os.MkdirAll(home, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	r, err := newRunAt(scope, home, kit, filepath.Join(scope, phase+"-evidence"), phase, 1)
	if err != nil {
		t.Fatal(err)
	}
	clock := tmuxfix.NewClock(r.Manifest.Started)
	r.Now, r.Commands.Now = clock.Now, clock.Now
	r.Sleep = func(ctx context.Context, d time.Duration) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		clock.Advance(d)
		return nil
	}
	return r
}

func putFixture(t *testing.T, path string, b []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
}

func readFixture(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func readJSON[T any](t *testing.T, path string) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(readFixture(t, path), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func seedRunner(t *testing.T, r *Run, state string, recordedPath bool, opts ...apitest.SpawnOption) (string, Owned) {
	t.Helper()
	path := filepath.Join(r.Home, ".config/agent-director/state.db")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		if _, err := apitest.InitStore(path); err != nil {
			t.Fatal(err)
		}
	}
	o := Owned{InstanceID: "live-" + r.Manifest.RunID + "-" + uuid.NewString(), SessionID: uuid.NewString(),
		CWD: r.Home, Socket: r.Socket, Started: r.Now(), LaunchToken: tmuxfix.Token}
	o.Transcript = apitest.SeedJsonlUnder(t, filepath.Join(r.Home, ".claude"), o.CWD, o.SessionID)
	options := []apitest.SpawnOption{apitest.WithLaunchIdentity(store.LaunchIdentity{Token: o.LaunchToken, Socket: o.Socket}),
		apitest.WithNoPID(), apitest.WithLaunchStartedAt(o.Started.UnixMilli())}
	if recordedPath {
		options = append(options, apitest.WithJsonlPath(o.Transcript))
	}
	options = append(options, opts...)
	if _, err := apitest.SeedSpawn(path, o.InstanceID, state, r.Home, "off", o.SessionID, false, options...); err != nil {
		t.Fatal(err)
	}
	return path, o
}

// socketFile leaves a real Unix socket entry without a running server.
func socketFile(t *testing.T, path string) {
	t.Helper()
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	l.SetUnlinkOnClose(false)
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
}
