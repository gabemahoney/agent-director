package coveragegorootfires_test

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestCoverageGoRootRetainsEarlyFailures (b.5sn) exercises real Go output through the
// phase executor, whose temporary stdout captures disappear before it returns.
func TestCoverageGoRootRetainsEarlyFailures(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the coverage gate over a tiny fixture module")
	}
	root, fixture := repoRoot(t), t.TempDir()
	materializeEarlyFailureFixture(t, root, fixture)
	configPath := earlyFailurePhaseConfig(t, root, fixture)
	runner := filepath.Join(root, "skills/release-agent-director/gates/lib/run-parallel.sh")
	var previousPath, previousLog string
	for attempt := 0; attempt < 2; attempt++ {
		captureDir := t.TempDir()
		cmd := exec.Command("bash", runner, configPath)
		cmd.Dir = fixture
		cmd.Env = append(os.Environ(), "HOME="+t.TempDir(), "TMPDIR="+captureDir,
			"GOFLAGS=-p=1", "GOPROXY=off", "GORACE=atexit_sleep_ms=0")
		stdout, stderr, exit := captureFixtureCommand(t, cmd)
		if exit != 1 {
			t.Fatalf("phase exit = %d, want 1\nstdout: %s\nstderr: %s", exit, stdout, stderr)
		}
		d := decodeFailedCoveragePhase(t, fixture, stdout, stderr)
		assertEarlyFailureDiagnostic(t, d)
		logPath, relative, log := assertRetainedFailureLog(t, fixture, d.Description)
		entries, err := os.ReadDir(captureDir)
		if err != nil || len(entries) != 0 {
			t.Fatalf("phase's disposable captures were not cleaned up: %v (%v)", entries, err)
		}
		runFixtureCommand(t, fixture, "git", "check-ignore", "--quiet", relative)
		runFixtureCommand(t, fixture, "bash", filepath.Join(root, "skills/release-agent-director/gates/preflight/worktree-clean.sh"))
		if attempt > 0 {
			old, err := os.ReadFile(previousPath)
			if previousPath == logPath || err != nil || string(old) != previousLog {
				t.Fatal("retry overwrote or removed the earlier failure log")
			}
		}
		previousPath, previousLog = logPath, log
	}
}

func materializeEarlyFailureFixture(t *testing.T, root, fixture string) {
	t.Helper()
	materializeFixture(t, fixture)
	writeFixtureFile(t, filepath.Join(fixture, "broken", "add_test.go"), `package broken
import (
    "strings"
    "testing"
)
func TestEarlyFailure(t *testing.T) {
    t.Error("early failure: quoted \"message\"\t" + strings.Repeat("large-message-", 11000))
    for i := 0; i < 25; i++ { t.Logf("context line %02d", i) }
    t.Log("only-in-full-log-after-bounded-summary")
    t.Run("nested", func(t *testing.T) { t.Error("nested failure message") })
}
func TestAnotherFailure(t *testing.T) { t.Error("another failure message") }
`)
	// Serial package execution makes the failures precede all later packages;
	// these packages contain real passing tests rather than fabricated go output.
	for i := 0; i < 56; i++ {
		writeFixtureFile(t, filepath.Join(fixture, fmt.Sprintf("later%02d", i), "later_test.go"),
			"package later\nimport \"testing\"\nfunc TestPass(t *testing.T) {}\n")
	}
	ignore, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	writeFixtureFile(t, filepath.Join(fixture, ".gitignore"), string(ignore))
	runFixtureCommand(t, fixture, "git", "init", "--quiet")
	// Exclude only the test's own inputs; release artifacts use the real ignore
	// rules, so preflight still detects an artifact left outside ignored paths.
	excludes := "go.mod\n.gitignore\nbroken/\n"
	for i := 0; i < 56; i++ {
		excludes += fmt.Sprintf("later%02d/\n", i)
	}
	writeFixtureFile(t, filepath.Join(fixture, ".git", "info", "exclude"), excludes)
}

func earlyFailurePhaseConfig(t *testing.T, root, fixture string) string {
	t.Helper()
	gate := filepath.Join(root, "skills/release-agent-director/gates/coverage/go-root.sh")
	config, err := json.Marshal(map[string]any{
		"phase_name": "coverage", "max_parallel": 1,
		"gates": []map[string]string{{"name": gateName, "cwd": fixture,
			"command": "bash '" + strings.ReplaceAll(gate, "'", "'\"'\"'") + "'"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(t.TempDir(), "gates.json")
	writeFixtureFile(t, configPath, string(config))
	return configPath
}

func decodeFailedCoveragePhase(t *testing.T, fixture, stdout, stderr string) diagnostic {
	t.Helper()
	var phase struct {
		Outcome string `json:"outcome"`
		Checks  []struct {
			Name        string       `json:"name"`
			Outcome     string       `json:"outcome"`
			Diagnostics []diagnostic `json:"diagnostics"`
		} `json:"sub_checks"`
	}
	if err := json.Unmarshal([]byte(stdout), &phase); err != nil {
		t.Fatalf("decode phase result: %v\n%s\nstderr: %s", err, stdout, stderr)
	}
	if phase.Outcome != "failed" || len(phase.Checks) != 1 || phase.Checks[0].Name != gateName || phase.Checks[0].Outcome != "failed" || len(phase.Checks[0].Diagnostics) != 1 {
		t.Fatalf("missing failed gate and diagnostic: %s", stdout)
	}
	d := phase.Checks[0].Diagnostics[0]
	if d.Gate != gateName || d.Offending == nil || *d.Offending != fixturePkgImportPath {
		t.Fatalf("wrong failing package: %+v", d)
	}
	return d
}

func assertEarlyFailureDiagnostic(t *testing.T, d diagnostic) {
	t.Helper()
	for _, want := range []string{
		"--- FAIL: TestEarlyFailure (", "--- FAIL: TestEarlyFailure/nested (", "--- FAIL: TestAnotherFailure (",
		"early failure: quoted \"message\"\t", "nested failure message", "another failure message",
	} {
		if !strings.Contains(d.Description, want) {
			t.Errorf("phase diagnostic lost early failure %q", want)
		}
	}
	if len(d.Description) <= 128*1024 {
		t.Errorf("fixture did not exercise a diagnostic larger than one exec argument: %d bytes", len(d.Description))
	}
	if !strings.Contains(d.Description, "omitted") || strings.Contains(d.Description, "only-in-full-log-after-bounded-summary") {
		t.Error("long failure context needs a bounded summary with an omission notice")
	}
}

func assertRetainedFailureLog(t *testing.T, fixture, description string) (string, string, string) {
	t.Helper()
	const prefix = "Full go test output saved to: "
	_, following, found := strings.Cut(description, prefix)
	if !found {
		t.Fatalf("diagnostic does not name a retained full log")
	}
	logPath := strings.SplitN(following, "\n", 2)[0]
	relative, err := filepath.Rel(fixture, logPath)
	if err != nil || !filepath.IsAbs(logPath) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		t.Fatalf("saved log must be inside fixture worktree: %q (%v)", logPath, err)
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("full output did not survive phase cleanup: %v", err)
	}
	log := string(data)
	if !strings.Contains(log, "only-in-full-log-after-bounded-summary") || !strings.HasSuffix(log, "FAIL\n") {
		t.Error("saved log is missing full failure context or the final Go result")
	}
	lines := strings.Split(strings.TrimSpace(log), "\n")
	if len(lines) < 50 || strings.Contains(strings.Join(lines[len(lines)-50:], "\n"), "--- FAIL:") || strings.Count(log, "ok  \t"+fixtureModulePath+"/later") != 56 {
		t.Error("fixture must place all failing tests before the last 50 lines of passing packages")
	}
	return logPath, relative, log
}

func writeFixtureFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func runFixtureCommand(t *testing.T, dir, command string, args ...string) {
	t.Helper()
	cmd := exec.Command(command, args...)
	cmd.Dir, cmd.Env = dir, append(os.Environ(), "HOME="+t.TempDir())
	stdout, stderr, exit := captureFixtureCommand(t, cmd)
	if exit != 0 {
		t.Fatalf("%s %v: exit %d\n%s\n%s", command, args, exit, stdout, stderr)
	}
}

func captureFixtureCommand(t *testing.T, cmd *exec.Cmd) (string, string, int) {
	t.Helper()
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if err != nil && cmd.ProcessState == nil {
		t.Fatalf("start %v: %v", cmd.Args, err)
	}
	return stdout.String(), stderr.String(), cmd.ProcessState.ExitCode()
}
