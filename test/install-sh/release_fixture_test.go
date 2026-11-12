package installsh_test

// The genuine-release fixture (reusable test fixture): one builder of a
// previous release of agent-director from its own git tag or commit, and an
// installer that puts that release into a move-fixture home with the
// release's own install.sh through the setup-only Host Shim. A test that
// needs a previous release's own binaries, or an install made by them, builds
// them with releaseBuildOf or genuineRelease, never a second builder; stores
// laid from a release's SQL keep their own fixtures. The one bash builder
// left is advice_follow.sh's v0.11.1 tag build (need_old_release), because
// that suite runs outside go test's process and cannot share this one.
//
//   - genuineRelease(t, tag) builds tag once per package run and skips the
//     test with "SKIP: git tag <tag> is not in this checkout" when the
//     checkout lacks the tag.
//   - releaseBuildOf(rev) builds any revision and returns an error instead,
//     for a test that must fail, not skip, when its pinned revision is gone.
//   - (*moveHome).InstallRelease installs a built release (one keeping AD's
//     files in ~/.config/agent-director) as setup and checks that its list exits 0.
//   - moveInstallReleaseSetup runs the case's historical installer, pair and
//     flags through test/driver/host-shim.sh and returns its result, including
//     a refused setup. A way-back install uses h.Install directly, unshimmed.
//   - moveInstallReleasePipedSetup preserves a piped installer's whole argv
//     and names h.Pair explicitly for the scheduling test's download stand-in.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
)

// releaseBuild is a genuine previous release: its binary pair, stamped as the
// release recipe stamps them, and its own install.sh with schedule/ beside it.
type releaseBuild struct {
	Rev, Version, Commit string
	Pair                 movePair
	Install              string
}

var releaseBuilds struct {
	mu    sync.Mutex
	m     map[string]*releaseOnce
	roots []string // each build's directory, for releaseFixtureShutdown
}

type releaseOnce struct {
	once sync.Once
	b    releaseBuild
	err  error
}

// errReleaseAbsent is releaseBuildOf's error for a revision the checkout
// lacks.
var errReleaseAbsent = errors.New("revision is not in this checkout")

// releaseBuildOf builds rev (a tag or commit) once per package run: git
// archive from the repo root, then both binaries with CGO_ENABLED=0 and the
// release recipe's flags (-trimpath, -s -w, Version from the revision's
// pkg/ts-bun-client/package.json, Commit its commit).
func releaseBuildOf(rev string) (releaseBuild, error) {
	releaseBuilds.mu.Lock()
	if releaseBuilds.m == nil {
		releaseBuilds.m = map[string]*releaseOnce{}
	}
	o := releaseBuilds.m[rev]
	if o == nil {
		o = &releaseOnce{}
		releaseBuilds.m[rev] = o
	}
	releaseBuilds.mu.Unlock()
	o.once.Do(func() { o.b, o.err = releaseBuild1(rev) })
	return o.b, o.err
}

// releaseFixtureShutdown removes every release build's directory; a TestMain
// may call it.
func releaseFixtureShutdown() {
	releaseBuilds.mu.Lock()
	defer releaseBuilds.mu.Unlock()
	for _, root := range releaseBuilds.roots {
		_ = os.RemoveAll(root)
	}
	releaseBuilds.roots = nil
}

func releaseBuild1(rev string) (b releaseBuild, err error) {
	repo := filepath.Join("..", "..")
	commit, err := exec.Command("git", "-C", repo, "rev-parse", "-q", "--verify", rev+"^{commit}").Output()
	if err != nil {
		return releaseBuild{}, fmt.Errorf("%s: %w", rev, errReleaseAbsent)
	}
	b = releaseBuild{Rev: rev, Commit: strings.TrimSpace(string(commit))}
	root, err := os.MkdirTemp("", "release-"+strings.ReplaceAll(rev, "/", "_")+"-")
	if err != nil {
		return b, err
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(root)
			return
		}
		releaseBuilds.mu.Lock()
		releaseBuilds.roots = append(releaseBuilds.roots, root)
		releaseBuilds.mu.Unlock()
	}()
	src := filepath.Join(root, "src")
	if err := os.Mkdir(src, 0o755); err != nil {
		return b, err
	}
	archive := exec.Command("git", "-C", repo, "archive", b.Commit)
	tarball, err := archive.Output()
	if err != nil {
		return b, fmt.Errorf("git archive %s: %v", rev, err)
	}
	untar := exec.Command("tar", "-x", "-C", src)
	untar.Stdin = bytes.NewReader(tarball)
	if out, err := untar.CombinedOutput(); err != nil {
		return b, fmt.Errorf("extract %s: %v: %s", rev, err, out)
	}
	pkg, err := os.ReadFile(filepath.Join(src, "pkg", "ts-bun-client", "package.json"))
	if err != nil {
		return b, err
	}
	var meta struct{ Version string }
	if err := json.Unmarshal(pkg, &meta); err != nil || meta.Version == "" {
		return b, fmt.Errorf("%s package.json version: %v", rev, err)
	}
	b.Version = meta.Version
	b.Pair = movePair{filepath.Join(src, "bin", "agent-director"), filepath.Join(src, "bin", "agent-director-admin")}
	flags := fmt.Sprintf("-s -w -X %s.Version=%s -X %s.Commit=%s", moveVersPkg, b.Version, moveVersPkg, b.Commit)
	for out, cmdPkg := range map[string]string{b.Pair.Main: "./cmd/agent-director", b.Pair.Admin: "./cmd/agent-director-admin"} {
		cmd := exec.Command("go", "build", "-trimpath", "-ldflags", flags, "-o", out, cmdPkg)
		cmd.Dir = src
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			return b, fmt.Errorf("build %s %s: %v: %s", rev, cmdPkg, err, out)
		}
	}
	b.Install = filepath.Join(src, "skills", "install-agent-director", "install.sh")
	return b, nil
}

// genuineRelease returns tag's build, skipping the test when the checkout
// lacks the tag (git fetch --tags brings it in) and failing it on any other
// build error.
func genuineRelease(t *testing.T, tag string) releaseBuild {
	t.Helper()
	b, err := releaseBuildOf(tag)
	if err != nil {
		if errors.Is(err, errReleaseAbsent) {
			t.Skipf("SKIP: git tag %s is not in this checkout", tag)
		}
		t.Fatalf("genuine release %s: %v", tag, err)
	}
	return b
}

// moveInstallReleaseSetup runs the historical installer selected by h only
// for setup. Wrap h.command so the case's flags, environment and cwd stay the
// same without marking h.S: a later way back must run without the shim.
func moveInstallReleaseSetup(t *testing.T, h *moveHome, args ...string) moveResult {
	t.Helper()
	return moveRunReleaseSetup(t, h, h.command(args...), movePair{})
}

// moveInstallReleasePipedSetup is schedRunArgv's piped invocation through the
// Host Shim. The curl stand-in serves h.Pair, even when the old installer asks
// for amd64 asset names on ARM. Only the historical installer uses this helper.
func moveInstallReleasePipedSetup(t *testing.T, h *moveHome, script string, argv []string) moveResult {
	t.Helper()
	f, err := os.Open(script)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	cmd := h.command()
	cmd.Args = append([]string{cmd.Args[0], "-s", "--"}, argv...)
	cmd.Stdin = f
	return moveRunReleaseSetup(t, h, cmd, h.Pair)
}

// moveRunReleaseSetup wraps one installer command without changing its stdin,
// environment or cwd. installs names a pair supplied outside the command's flags.
func moveRunReleaseSetup(t *testing.T, h *moveHome, cmd *exec.Cmd, installs movePair) moveResult {
	t.Helper()
	shim, err := filepath.Abs(filepath.Join("..", "..", "test", "driver", "host-shim.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.ReadFile(shim); err != nil { // include the shim in go test's cache inputs
		t.Fatal(err)
	}
	shimArgs := []string{cmd.Path, shim}
	if installs != (movePair{}) {
		shimArgs = append(shimArgs, "--installs", installs.Main, installs.Admin, "--")
	}
	cmd.Args = append(shimArgs, cmd.Args...)
	r := &moveRunning{cmd: cmd}
	cmd.Stdout, cmd.Stderr = &r.stdout, &r.stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	h.OnRelease(func() {
		r.Signal(syscall.SIGKILL)
		r.Wait(t)
	})
	return r.Wait(t)
}

// InstallRelease installs r into h as setup with r's own install.sh and h.Flags
// through the Host Shim, then puts h back on this tree's install.sh and
// pair. It fails the test unless the install exits 0 and r's installed list
// exits 0; it returns r's installed agent-director.
func (h *moveHome) InstallRelease(r releaseBuild) string {
	h.t.Helper()
	current, pair := h.S, h.Pair
	old := *h.S
	old.Install = r.Install
	h.S, h.Pair = &old, r.Pair
	res := moveInstallReleaseSetup(h.t, h)
	h.S, h.Pair = current, pair
	if res.Code != 0 {
		h.t.Fatalf("%s's setup install: exit %d: %s\n%s", r.Rev, res.Code, res.Stdout, res.Stderr)
	}
	bin := filepath.Join(h.New, "bin", "agent-director")
	if res := h.Run(bin, "list"); res.Code != 0 {
		h.t.Fatalf("%s's installed list: exit %d: %s", r.Rev, res.Code, res.Stderr)
	}
	return bin
}
