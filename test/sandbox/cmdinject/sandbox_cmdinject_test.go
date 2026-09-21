// Package sandboxcmdinject_test is a regression test for bug b.ay3.
//
// BACKGROUND (b.ay3 near-miss)
// ============================
// The Makefile `sandbox` target used to interpolate CMD into a single-quoted
// HOST wrapper:
//
//	sandbox: _sandbox-build
//		$(_SANDBOX_RUN) bash -c '$(CMD)'
//
// A single quote inside CMD therefore TERMINATED the wrapper early on the HOST
// shell, and any shell operator after the break (`&&`, `||`, `;`, `|`) executed
// on the HOST — outside the container. That silently defeats the central safety
// invariant ("never run this repo's tests/binaries on the host — it can rewrite
// the real ~/.agent-director", b.8dr / CLAUDE.md). The failure was quiet: the
// command appeared to run, and the escaped half hit the host with no objection
// from packages that don't call sandboxguard.Require().
//
// The fix threads CMD to the container through the ENVIRONMENT instead of
// interpolating it into the recipe line. As hardened in b.ay3 round 2 the
// relevant Makefile fragment is:
//
//	unexport CMD
//	MAKEOVERRIDES =
//	...
//	sandbox: export AGENT_DIRECTOR_SANDBOX_CMD = $(value CMD)
//	sandbox: override SANDBOX_FLAGS += -e AGENT_DIRECTOR_SANDBOX_CMD
//	sandbox: _sandbox-build
//		@if [ -z "$AGENT_DIRECTOR_SANDBOX_CMD" ]; then … exit 2; fi
//		$(_SANDBOX_RUN) bash -c 'exec bash -c "${AGENT_DIRECTOR_SANDBOX_CMD:?…}"'
//
// Two independent channels are closed:
//   - HOST shell re-tokenization: CMD's value is never spliced into a recipe
//     shell line, so quotes, `&&`, `;`, `|`, backslashes stay inert data.
//   - Make `$(…)` re-expansion: `$(value CMD)` takes CMD's RAW UNEXPANDED text,
//     and the file-scope `unexport CMD` + `MAKEOVERRIDES =` directives close the
//     MAKEFLAGS/MAKEOVERRIDES channel through which Make would otherwise expand
//     `$(shell …)` inside CMD on the HOST while building recipe environments.
//   - `override` on the SANDBOX_FLAGS `+=` keeps the `-e AGENT_DIRECTOR_SANDBOX_CMD`
//     forward even when the caller passes a command-line SANDBOX_FLAGS=… (a plain
//     `+=` is ignored for command-line variables, dropping the forward — b.ay3
//     round 2).
//
// Because CMD reaches the container VERBATIM ($(value CMD)), a single `$` now
// reaches the container shell and a literal `$$` is the container shell's PID —
// the opposite of the historical `$$` convention. The tests below carry CMD's
// bytes through unaltered and assert that.
//
// DESIGN
// ======
// The real Makefile recipe is exercised end-to-end — `make sandbox` is run
// against the actual repo Makefile — but the container engine is replaced with
// a FAKE shim on PATH (CONTAINER_ENGINE points at it). The fake NEVER executes
// the container command; it only records the exact argv it was handed and the
// value of the threaded CMD env var. The test then asserts on that recording.
//
// This mirrors the b.kbe git-mount regression next door (test/sandbox/gitmount):
// exercise the Makefile's real plumbing without a real container. It runs `make`
// and a shell shim only; it never builds or execs the agent-director binary and
// never touches ~/.agent-director, so it needs no sandboxguard TestMain and runs
// fine under the normal suite (`go test ./...`) — including inside the sandbox,
// where no real container engine (docker-in-docker) is available.
//
// FAILS-BEFORE / PASSES-AFTER
// ===========================
//   - PASSES-AFTER: TestSandboxCmd_NoHostEscape asserts a CMD with an inner
//     single quote + `&&` is threaded to the engine as inert env data and never
//     appears in the container command argv, so the host shell cannot run it.
//   - FAILS-BEFORE: TestSandboxCmd_PreFixWrapperEscapesToHost reconstructs the
//     PRE-FIX recipe in a throwaway Makefile and drives the SAME CMD through it,
//     with a probe that touches a sentinel FILE. Pre-fix, the `&& touch`
//     half runs on the host and the sentinel appears — the exact b.ay3 defect.
//     The probe only touches a file; it runs no repo code and reads/writes no
//     agent-director state, so demonstrating the escape here is safe.
package sandboxcmdinject_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/test/sandbox/internal/sandboxtest"
)

// unitSep separates argv elements the fake engine records, chosen so it cannot
// collide with anything in a normal docker/podman argv.
const unitSep = "\x1f"

// fakeEngineScript records each invocation's argv (unit-separated) and the
// threaded CMD env var to $FAKE_ENGINE_LOG, then exits 0 WITHOUT running the
// container command. Not executing the command is the whole point: a real host
// escape must be detectable as data in the log, never as a side effect.
const fakeEngineScript = `#!/usr/bin/env bash
set -u
{
  printf 'ARGV'
  for a in "$@"; do printf '\037%s' "$a"; done
  printf '\n'
  printf 'ENV_CMD\037%s\n' "${AGENT_DIRECTOR_SANDBOX_CMD-<unset>}"
  printf -- '---\n'
} >> "$FAKE_ENGINE_LOG"
exit 0
`

// requireTools skips if make or bash is unavailable.
func requireTools(t *testing.T) {
	t.Helper()
	sandboxtest.RequireTools(t, "skipping b.ay3 sandbox cmd-injection test", "make", "bash")
}

// installFakeEngine writes the fake engine shim to a temp dir under both the
// `docker` and `podman` names and returns (binDir, logPath). Putting binDir
// first on PATH makes `make sandbox CONTAINER_ENGINE=<name>` invoke the shim.
func installFakeEngine(t *testing.T) (binDir, logPath string) {
	t.Helper()
	binDir = t.TempDir()
	shim := filepath.Join(binDir, "docker")
	if err := os.WriteFile(shim, []byte(fakeEngineScript), 0o755); err != nil {
		t.Fatalf("write fake engine: %v", err)
	}
	if err := os.Symlink("docker", filepath.Join(binDir, "podman")); err != nil {
		t.Fatalf("symlink podman: %v", err)
	}
	return binDir, filepath.Join(t.TempDir(), "engine.log")
}

// runMakeSandbox runs `make -f makefile sandbox CMD=<cmd>` from the repo root
// with the fake engine on PATH. tmpdir isolates the pid-probe cache marker so a
// stale real-engine marker can't skew the run. extraArgs are appended to the
// make command line (e.g. a caller-supplied SANDBOX_FLAGS=…). It returns the
// recorded log; the make invocation must succeed.
func runMakeSandbox(t *testing.T, makefile, cmd, binDir, logPath string, extraArgs ...string) string {
	t.Helper()
	tmpdir := t.TempDir()
	args := append([]string{"-f", makefile, "sandbox",
		"CONTAINER_ENGINE=docker", "CMD=" + cmd}, extraArgs...)
	c := exec.Command("make", args...)
	c.Dir = sandboxtest.RepoRoot(t)
	// Scrub inherited make state so an ancestor `make sandbox CMD=...` can't
	// override our CMD, prepend the fake engine to PATH, and isolate TMPDIR.
	c.Env = sandboxtest.ScrubbedMakeEnv(
		"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"FAKE_ENGINE_LOG="+logPath,
		"TMPDIR="+tmpdir,
	)
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("make sandbox failed: %v\noutput:\n%s", err, out)
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read engine log: %v", err)
	}
	return string(data)
}

// lastRunArgv returns the argv of the last `run` invocation the fake engine
// recorded (skipping the `build` invocation), split on the unit separator.
func lastRunArgv(t *testing.T, log string) []string {
	t.Helper()
	var last []string
	for _, rec := range strings.Split(log, "\n---\n") {
		for _, line := range strings.Split(rec, "\n") {
			if !strings.HasPrefix(line, "ARGV"+unitSep) {
				continue
			}
			fields := strings.Split(strings.TrimPrefix(line, "ARGV"+unitSep), unitSep)
			if len(fields) > 0 && fields[0] == "run" {
				last = fields
			}
		}
	}
	if last == nil {
		t.Fatalf("no `run` invocation recorded in engine log:\n%s", log)
	}
	return last
}

// envCmd returns the value the fake engine recorded for AGENT_DIRECTOR_SANDBOX_CMD
// on the last container `run` (the exact bytes forwarded into the container),
// or fails if no ENV_CMD line was recorded.
func envCmd(t *testing.T, log string) string {
	t.Helper()
	var last string
	found := false
	for _, line := range strings.Split(log, "\n") {
		if v, ok := strings.CutPrefix(line, "ENV_CMD"+unitSep); ok {
			last = v
			found = true
		}
	}
	if !found {
		t.Fatalf("no ENV_CMD line recorded in engine log:\n%s", log)
	}
	return last
}

// assertForwardFlagPresent asserts argv contains the adjacent token pair
// `-e` `AGENT_DIRECTOR_SANDBOX_CMD`. A loose substring check would be satisfied
// by the static wrapper text alone, so a dropped `override SANDBOX_FLAGS += -e …`
// line (the round-2 hazard) would go unnoticed: the shim inherits the exported
// var from make's recipe shell and would falsely pass. Requiring the adjacent
// pair pins the actual `-e AGENT_DIRECTOR_SANDBOX_CMD` engine flag.
func assertForwardFlagPresent(t *testing.T, argv []string) {
	t.Helper()
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == "-e" && argv[i+1] == "AGENT_DIRECTOR_SANDBOX_CMD" {
			return
		}
	}
	t.Fatalf("container-run argv is missing the adjacent `-e AGENT_DIRECTOR_SANDBOX_CMD` forward flag; argv:\n%v", argv)
}

// The probe is a CMD with the exact shape b.ay3 escaped on: an inner single
// quote (which terminated the old HOST wrapper) followed by `&&`. The trailing
// clause, had it reached the host shell, would touch a sentinel file.
const probeCmd = `bash -c 'true && touch %s'`

// TestSandboxCmd_NoHostEscape drives the real repo Makefile and asserts the
// malicious CMD is threaded to the engine only as inert env data — never as a
// token in the container command argv the host shell could split and run.
func TestSandboxCmd_NoHostEscape(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: runs make and a shell shim")
	}
	requireTools(t)

	binDir, logPath := installFakeEngine(t)
	sentinel := filepath.Join(t.TempDir(), "host_sentinel")
	cmd := strings.Replace(probeCmd, "%s", sentinel, 1)

	log := runMakeSandbox(t, filepath.Join(sandboxtest.RepoRoot(t), "Makefile"), cmd, binDir, logPath)

	// The sentinel touch must not have run anywhere — the fake engine never
	// executes the container command, so its absence confirms the recipe itself
	// did not leak the probe to the host shell.
	if _, err := os.Stat(sentinel); err == nil {
		t.Fatalf("sentinel %s exists: the CMD escaped to the host shell (b.ay3 regression)", sentinel)
	}

	argv := lastRunArgv(t, log)
	joined := strings.Join(argv, " ")

	// The container command must be the fixed static wrapper, with CMD absent
	// from the argv entirely.
	if strings.Contains(joined, "touch") || strings.Contains(joined, sentinel) {
		t.Fatalf("container-run argv contains the CMD probe as a token (should be threaded via env, not argv):\n%v", argv)
	}
	// The `-e AGENT_DIRECTOR_SANDBOX_CMD` forward must be present as an adjacent
	// pair, not merely as a substring of the wrapper text (see helper).
	assertForwardFlagPresent(t, argv)

	// And the probe must have been threaded to the engine verbatim as env data.
	if got := envCmd(t, log); got != cmd {
		t.Fatalf("threaded AGENT_DIRECTOR_SANDBOX_CMD env = %q, want the CMD verbatim %q", got, cmd)
	}
}

// TestSandboxCmd_PreFixWrapperEscapesToHost proves the fails-before direction:
// the pre-fix recipe (`bash -c '$(CMD)'`), reconstructed in a throwaway copy of
// the repo Makefile, re-tokenizes the same CMD on the HOST and runs the trailing
// `&& touch` clause outside the container — creating the sentinel.
func TestSandboxCmd_PreFixWrapperEscapesToHost(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: runs make and a shell shim")
	}
	requireTools(t)

	prefixMakefile := writePreFixMakefile(t)

	binDir, logPath := installFakeEngine(t)
	sentinel := filepath.Join(t.TempDir(), "host_sentinel")
	cmd := strings.Replace(probeCmd, "%s", sentinel, 1)

	runMakeSandbox(t, prefixMakefile, cmd, binDir, logPath)

	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("pre-fix recipe did NOT reproduce the b.ay3 escape: sentinel %s absent (%v). "+
			"The reconstructed pre-fix wrapper should have run `&& touch` on the host.", sentinel, err)
	}
}

// TestSandboxCmd_MakeShellNotRunOnHost covers the round-2 hardening: a CMD whose
// value contains a Make `$(shell …)` call must NOT be expanded on the host while
// make builds the recipe environments. The probe would create a sentinel file
// host-side if `$(shell touch …)` were expanded; `$(value CMD)` + the file-scope
// `unexport CMD` / `MAKEOVERRIDES =` directives keep the text inert, so the
// sentinel must stay absent and the literal `$(shell …)` text must reach the
// container env verbatim.
func TestSandboxCmd_MakeShellNotRunOnHost(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: runs make and a shell shim")
	}
	requireTools(t)

	binDir, logPath := installFakeEngine(t)
	sentinel := filepath.Join(t.TempDir(), "make_shell_sentinel")
	cmd := "true $(shell touch " + sentinel + ")"

	log := runMakeSandbox(t, filepath.Join(sandboxtest.RepoRoot(t), "Makefile"), cmd, binDir, logPath)

	if _, err := os.Stat(sentinel); err == nil {
		t.Fatalf("sentinel %s exists: Make expanded $(shell …) inside CMD on the host (b.ay3 round-2 regression)", sentinel)
	}
	if got := envCmd(t, log); got != cmd {
		t.Fatalf("threaded env = %q, want the raw $(shell …) text verbatim %q", got, cmd)
	}
}

// TestSandboxCmd_OverrideKeepsForwardFlag covers the round-2 `override` fix: with
// a command-line SANDBOX_FLAGS=… the recipe's `override SANDBOX_FLAGS += -e …`
// must still win, so the recorded argv carries BOTH the caller's extra flag and
// the adjacent `-e AGENT_DIRECTOR_SANDBOX_CMD` forward. A plain `+=` would be
// ignored for the command-line variable and silently drop the forward.
func TestSandboxCmd_OverrideKeepsForwardFlag(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: runs make and a shell shim")
	}
	requireTools(t)

	binDir, logPath := installFakeEngine(t)
	cmd := "true"

	log := runMakeSandbox(t, filepath.Join(sandboxtest.RepoRoot(t), "Makefile"), cmd, binDir, logPath,
		"SANDBOX_FLAGS=--label ay3probe=1")

	argv := lastRunArgv(t, log)
	joined := strings.Join(argv, " ")
	if !strings.Contains(joined, "--label ay3probe=1") {
		t.Fatalf("caller-supplied SANDBOX_FLAGS did not reach the engine argv:\n%v", argv)
	}
	assertForwardFlagPresent(t, argv)
}

// TestSandboxCmd_VerbatimBytes covers the round-2 transport: CMD's exact bytes —
// including a literal `$HOME` (which must NOT be expanded by make or the host
// shell) and an inner single quote — reach the container env unaltered.
func TestSandboxCmd_VerbatimBytes(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: runs make and a shell shim")
	}
	requireTools(t)

	binDir, logPath := installFakeEngine(t)
	cmd := `echo "$HOME" 'inner quote'`

	log := runMakeSandbox(t, filepath.Join(sandboxtest.RepoRoot(t), "Makefile"), cmd, binDir, logPath)

	if got := envCmd(t, log); got != cmd {
		t.Fatalf("threaded env = %q, want CMD's verbatim bytes %q", got, cmd)
	}
}

// writePreFixMakefile copies the repo Makefile into a temp file with the b.ay3
// hardening reverted to its pre-fix form — the `sandbox` target back to
// `bash -c '$(CMD)'` interpolation, and the file-scope `unexport CMD` /
// `MAKEOVERRIDES =` directives removed — and returns the copy's path. It fails
// if the current fixed text is not present verbatim (the anchor drifted and this
// test must be re-anchored). Using a copy keeps the fails-before demonstration
// from touching the real Makefile.
func writePreFixMakefile(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(sandboxtest.RepoRoot(t), "Makefile"))
	if err != nil {
		t.Fatalf("read Makefile: %v", err)
	}
	src := string(data)

	// Anchor 1: the file-scope directives that close the Make `$(…)` channel.
	// Pre-fix they did not exist, so drop them for the reconstruction.
	directives := "unexport CMD\nMAKEOVERRIDES =\n"
	if !strings.Contains(src, directives) {
		t.Fatalf("file-scope `unexport CMD` / `MAKEOVERRIDES =` directives not found in Makefile; the b.ay3 anchor drifted and this test must be re-anchored")
	}

	// Anchor 2: the fixed `sandbox` recipe block (env transport + override
	// forward + fail-closed :? guard).
	fixedBlock := "sandbox: export AGENT_DIRECTOR_SANDBOX_CMD = $(value CMD)\n" +
		"sandbox: override SANDBOX_FLAGS += -e AGENT_DIRECTOR_SANDBOX_CMD\n" +
		"sandbox: _sandbox-build\n" +
		"\t@if [ -z \"$$AGENT_DIRECTOR_SANDBOX_CMD\" ]; then \\\n" +
		"\t\techo 'ERROR: CMD is required. Example: make sandbox CMD=\"go build ./...\"' >&2; \\\n" +
		"\t\texit 2; \\\n" +
		"\tfi\n" +
		"\t$(_SANDBOX_RUN) bash -c 'exec bash -c \"$${AGENT_DIRECTOR_SANDBOX_CMD:?not forwarded into the container (the -e AGENT_DIRECTOR_SANDBOX_CMD flag was dropped) — refusing to run an empty command}\"'"
	if !strings.Contains(src, fixedBlock) {
		t.Fatalf("fixed `sandbox` target block not found in Makefile; the b.ay3 anchor drifted and this test must be re-anchored")
	}

	// Pre-fix recipe: CMD interpolated straight into a single-quoted HOST
	// wrapper (the original defect), with no env transport and no forward flag.
	preFixBlock := "sandbox: _sandbox-build\n" +
		"\t@if [ -z '$(CMD)' ]; then \\\n" +
		"\t\techo 'ERROR: CMD is required. Example: make sandbox CMD=\"go build ./...\"' >&2; \\\n" +
		"\t\texit 2; \\\n" +
		"\tfi\n" +
		"\t$(_SANDBOX_RUN) bash -c '$(CMD)'"

	out := strings.Replace(src, directives, "", 1)
	out = strings.Replace(out, fixedBlock, preFixBlock, 1)

	path := filepath.Join(t.TempDir(), "Makefile.prefix")
	if err := os.WriteFile(path, []byte(out), 0o644); err != nil {
		t.Fatalf("write pre-fix Makefile: %v", err)
	}
	return path
}
