package rebootrecovery_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// buildBinary compiles the agent-director CLI into dir and returns its absolute
// path. Mirrors the spawn_cli_test.go / cmd testhelpers pattern: build once,
// exec the real binary rather than mocking dispatch. The path is baked into the
// stub `claude` script so the stub can invoke `<abs-binary> hook`.
func buildBinary(t *testing.T, dir string) string {
	t.Helper()
	out := filepath.Join(dir, "agent-director")
	build := exec.Command("go", "build", "-o", out, "github.com/gabemahoney/agent-director/cmd/agent-director")
	build.Stdout = os.Stderr
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		t.Fatalf("build agent-director: %v", err)
	}
	abs, err := filepath.Abs(out)
	if err != nil {
		t.Fatalf("abs binary path: %v", err)
	}
	return abs
}

// stubMarkerKey/stubMarkerVal is a unique env var the stub `claude` process
// exports so the test can find and kill exactly the stub processes it spawned —
// without racing the host's real tmux/claude — by scanning /proc/<pid>/environ.
// An env-var marker (not an argv token) is used so the long-lived `sleep` keeps
// a valid, single-argument command line: extra argv on `sleep` are parsed as
// additional durations and abort it immediately.
const (
	stubMarkerKey = "AD_REBOOT_RECOVERY_STUB"
	stubMarkerVal = "1"
)

// stubSessionIDPrefix is the token the stub prepends to the injected
// AGENT_DIRECTOR_INSTANCE_ID to mint the session id on a FRESH spawn. Because
// the hook derives claude_session_id from the transcript-path basename
// (classify: basename-without-.jsonl), choosing the basename here IS choosing
// the persisted claude_session_id — which resume then hands back verbatim via
// `--resume <sid>`. The test derives the expected transcript path independently
// from this same rule (see stubSessionID / spawn.JsonlPathIn), so the assertion
// is non-circular: the stub composes the path from env+argv, the test composes
// it from the store-observable inputs.
const stubSessionIDPrefix = "sess-"

// stubSessionID mints the session id the stub would use on a fresh spawn for a
// given AD instance id — mirroring the shell's `SID="sess-$INSTANCE"`. Used by
// the test to derive the expected transcript path independently of the stub.
func stubSessionID(instanceID string) string { return stubSessionIDPrefix + instanceID }

// writeStubClaude generates the PM-pinned stub `claude` shell script into
// stubDir (named exactly "claude" so a PATH-prepend wins over any system
// claude) and returns stubDir. binaryAbs is baked in so the stub invokes the
// real hook verb. The stub takes NO baked transcript path (b.1ba AC6): it
// DERIVES the path at runtime from $CLAUDE_CONFIG_DIR (default ~/.claude) +
// slug($PWD) + a session id — so the E2E exercises resume's real CONFIG_DIR
// resolution rather than asserting against the stub's own hardcoded literal.
//
// Transcript-path derivation (the auditable non-circular contract):
//
//	cfg  = ${CLAUDE_CONFIG_DIR:-$HOME/.claude}
//	slug = $PWD with every non-[A-Za-z0-9-] rune replaced by '-'  (sed)
//	sid  = the `--resume <sid>` argv value on resume, else "sess-$AGENT_DIRECTOR_INSTANCE_ID"
//	JSONL = $cfg/projects/$slug/$sid.jsonl
//
// This matches Go's spawn.JsonlPathIn(cfg, cwd, sid) byte-for-byte. On a fresh
// spawn the basename is $sid, so the SessionStart hook persists
// claude_session_id = $sid; resume hands that same $sid back via `--resume`, so
// the continuation lands in the SAME derived file — without any literal path
// crossing the stub/test boundary.
//
// PM-PINNED stub mechanism:
//   - On start the stub invokes `<binaryAbs> hook` with stdin JSON
//     {"hook_event_name":"SessionStart","transcript_path":"<derived JSONL>"},
//     relying on the AGENT_DIRECTOR_INSTANCE_ID that tmux injected via -e — the
//     exact two fields the real Claude Code sends and the only two the handler
//     consumes for this flow. That SessionStart is what makes find-missing's
//     checker record the STUB's own pid + proc_starttime as the row identity
//     (the stub is the topmost env-carrying ancestor of the hook subprocess),
//     and what persists jsonl_path + claude_session_id.
//   - The stub appends a line to the derived JSONL on every start (a pre-kill
//     marker on the fresh spawn, a continuation marker on --resume), then stays
//     alive (exec-ing a long sleep carrying stubMarker) so the probe can read
//     its /proc/<pid>/environ.
//
// PM-REQUIRED FIDELITY NOTE: appending to the SAME jsonl on --resume
// deliberately diverges from real Claude Code, which rotates the session UUID
// and starts a NEW transcript on every --resume (per architecture.md's
// SessionStart contract). The acceptance requires only that the transcript
// "continues the pre-kill session", so a single same-file transcript is an
// accepted simplification here; jsonl_path re-persistence on resurrection is
// covered by Epic is's SessionStart unit tests, not this E2E.
func writeStubClaude(t *testing.T, stubDir, binaryAbs string) string {
	t.Helper()
	if err := os.MkdirAll(stubDir, 0o755); err != nil {
		t.Fatalf("mkdir stubDir: %v", err)
	}
	// The stub detects a --resume argument to decide which transcript marker to
	// append AND to read the session id back from argv. It derives the
	// transcript path from $CLAUDE_CONFIG_DIR + slug($PWD) + sid, fires the
	// SessionStart hook, appends its line, then execs a long-lived sleep tagged
	// with stubMarker so the pane process persists with a readable environ.
	// `exec` replaces the shell so the surviving pid IS the process whose
	// environ carries AGENT_DIRECTOR_INSTANCE_ID.
	script := `#!/bin/sh
# Stub 'claude' for the reboot-recovery E2E. See writeStubClaude in harness_test.go.
# It DERIVES its transcript path from $CLAUDE_CONFIG_DIR + slug($PWD) + sid so
# the b.1ba assertion is non-circular (no baked literal path).
BINARY='` + binaryAbs + `'

resume=no
sid=''
prev=''
for a in "$@"; do
  if [ "$prev" = "--resume" ]; then sid="$a"; fi
  if [ "$a" = "--resume" ]; then resume=yes; fi
  prev="$a"
done

# Fresh spawn: mint the session id from the injected instance id. The hook
# derives claude_session_id from this path's basename, so this IS the sid resume
# will hand back via --resume.
if [ -z "$sid" ]; then
  sid='` + stubSessionIDPrefix + `'"$AGENT_DIRECTOR_INSTANCE_ID"
fi

# cfg = $CLAUDE_CONFIG_DIR or ~/.claude (mirrors spawn.JsonlPath's fallback).
cfg="$CLAUDE_CONFIG_DIR"
if [ -z "$cfg" ]; then cfg="$HOME/.claude"; fi

# slug($PWD): every non-[A-Za-z0-9-] rune → '-' (mirrors Go slugifyCwd).
slug=$(printf '%s' "$PWD" | sed 's/[^A-Za-z0-9-]/-/g')
JSONL="$cfg/projects/$slug/$sid.jsonl"

mkdir -p "$(dirname "$JSONL")"

# Fire SessionStart so the hook persists identity (pid+starttime) + jsonl_path
# + claude_session_id (basename of this path).
printf '%s' '{"hook_event_name":"SessionStart","transcript_path":"'"$JSONL"'"}' \
  | "$BINARY" hook >/dev/null 2>&1 || true

if [ "$resume" = yes ]; then
  printf '{"type":"assistant","marker":"post-resume-continuation"}\n' >> "$JSONL"
else
  printf '{"type":"assistant","marker":"pre-kill"}\n' >> "$JSONL"
fi

# Stay alive with a readable environ. exec so this pid inherits the tmux -e env
# (AGENT_DIRECTOR_INSTANCE_ID) that find-missing's probe reads, and export the
# stub marker so the test can target exactly these processes for the kill step.
# 'sleep' takes a single duration arg — a marker in argv would abort it, so the
# marker rides in the environment instead.
export ` + stubMarkerKey + `=` + stubMarkerVal + `
exec sleep 100000
`
	stubPath := filepath.Join(stubDir, "claude")
	if err := os.WriteFile(stubPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write stub claude: %v", err)
	}
	return stubDir
}

// cliEnv is the child-process env used for every agent-director invocation and
// (transitively, via the tmux server it starts) the stub `claude`: HOME is the
// isolated temp home, PATH has stubDir first so `claude` resolves to the stub.
func cliEnv(home, stubDir string) []string {
	return []string{
		"HOME=" + home,
		"PATH=" + stubDir + ":" + os.Getenv("PATH"),
	}
}

// runCLI execs the built binary with env and returns stdout, stderr, exit code.
func runCLI(t *testing.T, binaryAbs string, env []string, args ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(binaryAbs, args...)
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	code := 0
	if err != nil {
		if e, ok := err.(*exec.ExitError); ok {
			code = e.ExitCode()
		} else {
			t.Fatalf("exec %v: %v (stderr=%s)", args, err, stderr.String())
		}
	}
	return stdout.String(), stderr.String(), code
}

// spawnResult mirrors api.SpawnResult.
type spawnResult struct {
	ClaudeInstanceID string `json:"claude_instance_id"`
}

// findMissingResult mirrors api.FindMissingResult.
type findMissingResult struct {
	Count         int      `json:"count"`
	IDs           []string `json:"ids"`
	Unverified    int      `json:"unverified"`
	UnverifiedIDs []string `json:"unverified_ids"`
}

// mustSpawn runs `spawn` with the given extra args and returns the new
// instance id, failing the test on a non-zero exit.
func mustSpawn(t *testing.T, binaryAbs string, env []string, args ...string) string {
	t.Helper()
	full := append([]string{"spawn"}, args...)
	stdout, stderr, code := runCLI(t, binaryAbs, env, full...)
	if code != 0 {
		t.Fatalf("spawn exit=%d stderr=%s", code, stderr)
	}
	var res spawnResult
	if err := json.Unmarshal([]byte(lastJSONLine(stdout)), &res); err != nil {
		t.Fatalf("parse spawn stdout %q: %v", stdout, err)
	}
	if res.ClaudeInstanceID == "" {
		t.Fatalf("spawn returned empty claude_instance_id: %q", stdout)
	}
	return res.ClaudeInstanceID
}

// getRow fetches a spawn row as a generic map via `get`.
func getRow(t *testing.T, binaryAbs string, env []string, id string) map[string]any {
	t.Helper()
	stdout, stderr, code := runCLI(t, binaryAbs, env, "get", "--claude-instance-id", id)
	if code != 0 {
		t.Fatalf("get %s exit=%d stderr=%s", id, code, stderr)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(stdout), &m); err != nil {
		t.Fatalf("parse get %q: %v", stdout, err)
	}
	return m
}

// lastJSONLine returns the last non-empty line beginning with '{' (spawn may
// emit soft pre-trust warnings to stdout/stderr ahead of the JSON envelope).
func lastJSONLine(s string) string {
	lines := strings.Split(s, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		ln := strings.TrimSpace(lines[i])
		if strings.HasPrefix(ln, "{") {
			return ln
		}
	}
	return s
}

// ── poll-with-deadline helpers (no fixed sleeps) ────────────────────────────

// pollDeadline is the max wall-clock any poll waits before failing. A poll
// returns the instant its condition holds (0.6s unloaded for the slowest one),
// so a generous budget costs nothing when the machine is idle; it only matters
// when the full `go test ./...` suite loads the CPU and the SQLite file, where
// 20s proved too tight (b.129). One shared constant is correct here: every
// waitFor call site is an early-returning poll, so raising the ceiling never
// slows a passing run — it only widens the margin for the loaded ones.
const pollDeadline = 60 * time.Second

// pollInterval is the gap between poll attempts.
const pollInterval = 50 * time.Millisecond

// waitFor polls cond until it returns true or pollDeadline elapses, failing the
// test with msg on timeout. No fixed sleeps: the loop returns as soon as cond
// holds.
func waitFor(t *testing.T, msg string, cond func() bool) {
	t.Helper()
	waitForObserved(t, pollDeadline, msg, cond, nil)
}

// waitForObserved is waitFor with a self-diagnosing timeout: on expiry it
// appends observe()'s output — the state ACTUALLY seen at the deadline — to the
// failure, so a loaded-machine timeout reports what it got, not only what it
// wanted (b.129). observe may be nil (falls back to the plain message). The
// deadline is a parameter so the regression test can inject a short one without
// slowing real waits.
func waitForObserved(t testing.TB, budget time.Duration, msg string, cond func() bool, observe func() string) {
	t.Helper()
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(pollInterval)
	}
	if observe != nil {
		t.Fatalf("timed out after %s waiting for: %s\n  observed at expiry: %s", budget, msg, observe())
	}
	t.Fatalf("timed out after %s waiting for: %s", budget, msg)
}

// stubPIDs returns the pids of live stub `claude` processes — those whose
// /proc/<pid>/environ carries the stub marker env var — by scanning /proc
// directly (procps is present in the sandbox but /proc scanning keeps the
// helper dependency-free). Matching on environ (not cmdline) is deliberate: the
// long-lived process is a bare `sleep`, so the identifying token must ride in
// the environment.
func stubPIDs(t *testing.T) []int {
	t.Helper()
	entries, err := os.ReadDir("/proc")
	if err != nil {
		t.Fatalf("read /proc: %v", err)
	}
	marker := []byte(stubMarkerKey + "=" + stubMarkerVal)
	var pids []int
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		var pid int
		if _, err := fmt.Sscanf(e.Name(), "%d", &pid); err != nil {
			continue
		}
		data, err := os.ReadFile("/proc/" + e.Name() + "/environ")
		if err != nil {
			continue // process vanished or environ unreadable
		}
		// environ is NUL-separated KEY=VAL; the marker must be a full entry.
		for _, kv := range bytes.Split(data, []byte{0}) {
			if bytes.Equal(kv, marker) {
				pids = append(pids, pid)
				break
			}
		}
	}
	return pids
}

// killStubs sends SIGKILL to every current stub process. Used after
// tmux kill-server to guarantee the stub panes are gone even if tmux left a
// lingering child.
func killStubs(t *testing.T) {
	t.Helper()
	for _, pid := range stubPIDs(t) {
		p, err := os.FindProcess(pid)
		if err != nil {
			continue
		}
		_ = p.Signal(os.Kill)
	}
}
