// Package crosscompilestampsfrompackagejson_test is a synthetic-regression test
// for bug b.9ba.
//
// BACKGROUND
// ==========
// cross-compile.sh previously called `make release-binaries` without
// propagating a version, so all binaries were stamped with the dev sentinel
// "0.0.0-dev" regardless of the version in pkg/ts-bun-client/package.json.
// The fix derives AGENT_DIRECTOR_BUILD_VERSION from that file when the caller
// has not already set one, then exports it so the `make` invocation inherits it.
//
// WHY THE ORIGINAL TEST WAS VACUOUS
// ====================================
// The Makefile's release-binaries target has its own VERSION_STR override that
// falls back to package.json when AGENT_DIRECTOR_BUILD_VERSION is unset.  The
// pre-fix script could therefore produce correctly-stamped binaries via make's
// own logic even without the fix, making the previous test unable to distinguish
// fixed from unfixed code.
//
// DESIGN (fake-make shim approach)
// =================================
// A temp dir is prepended to PATH containing a shell script named "make".  The
// shim records AGENT_DIRECTOR_BUILD_VERSION to a file and exits 0 without
// performing any real build.  This intercepts the make call before the Makefile
// fallback can run, so the only source of a correct value is cross-compile.sh
// itself.
//
// cross-compile.sh is run with AGENT_DIRECTOR_BUILD_VERSION stripped so the
// script must derive it itself.  After the script returns (exit code ignored —
// the shim produces no binaries so later checks in the script will fail), the
// recorded value is asserted to match pkg/ts-bun-client/package.json.
//
// This test runs in milliseconds; no real cross-compilation occurs.
// No real binaries are exec'd, so it works on any host arch.
package crosscompilestampsfrompackagejson_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// repoRoot walks up from the package working directory until it finds go.mod.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("repoRoot: could not find go.mod walking up from %s", dir)
		}
		dir = parent
	}
	panic("unreachable")
}

// envWithout returns os.Environ() with every entry whose key equals stripKey
// removed, then appends the extra entries.
func envWithout(stripKey string, extra ...string) []string {
	prefix := stripKey + "="
	filtered := make([]string, 0, len(os.Environ())+len(extra))
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, prefix) {
			continue
		}
		filtered = append(filtered, e)
	}
	return append(filtered, extra...)
}

// packageJSONVersion reads .version from pkg/ts-bun-client/package.json.
func packageJSONVersion(t *testing.T, root string) string {
	t.Helper()
	pkgJSON := filepath.Join(root, "pkg", "ts-bun-client", "package.json")
	data, err := os.ReadFile(pkgJSON)
	if err != nil {
		t.Fatalf("read %s: %v", pkgJSON, err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal %s: %v", pkgJSON, err)
	}
	raw, ok := m["version"]
	if !ok {
		t.Fatalf("%s has no .version field", pkgJSON)
	}
	var v string
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("unmarshal .version from %s: %v", pkgJSON, err)
	}
	return v
}

// shimScript is the content of the fake `make` executable injected into PATH.
// It records $AGENT_DIRECTOR_BUILD_VERSION to $RECORD_FILE and exits 0 without
// performing any real build.
const shimScript = `#!/usr/bin/env bash
# Fake make shim — b.9ba regression test recorder.
# Records AGENT_DIRECTOR_BUILD_VERSION to RECORD_FILE then exits cleanly.
printf '%s' "${AGENT_DIRECTOR_BUILD_VERSION:-}" > "${RECORD_FILE}"
exit 0
`

// TestCrossCompileStampsFromPackageJSON verifies that cross-compile.sh exports
// AGENT_DIRECTOR_BUILD_VERSION (derived from pkg/ts-bun-client/package.json) to
// the `make` invocation when the caller has not pre-set it (bug b.9ba).
//
// A fake `make` shim is prepended to PATH so it records the env var value
// without performing any real build.  This prevents the Makefile's own fallback
// logic from masking the regression.
func TestCrossCompileStampsFromPackageJSON(t *testing.T) {
	root := repoRoot(t)

	// ── 1. Build the fake-make shim in a temp dir ──────────────────────────
	shimDir := t.TempDir()
	shimPath := filepath.Join(shimDir, "make")
	if err := os.WriteFile(shimPath, []byte(shimScript), 0o755); err != nil {
		t.Fatalf("write make shim: %v", err)
	}

	// ── 2. Create the record file path ─────────────────────────────────────
	recordFile := filepath.Join(shimDir, "recorded-version")

	// ── 3. Prepend shim dir to PATH ────────────────────────────────────────
	origPath := os.Getenv("PATH")
	newPath := shimDir + ":" + origPath

	// ── 4. Run cross-compile.sh with AGENT_DIRECTOR_BUILD_VERSION stripped ─
	scriptPath := filepath.Join(root, "skills", "release-agent-director", "gates", "compile", "cross-compile.sh")

	// Build a clean env: strip the key under test, set PATH to shim-first,
	// and inject RECORD_FILE so the shim knows where to write.
	env := envWithout(
		"AGENT_DIRECTOR_BUILD_VERSION",
		"PATH="+newPath,
		"RECORD_FILE="+recordFile,
	)

	cmd := exec.Command("bash", scriptPath)
	cmd.Dir = root
	cmd.Env = env
	// Exit code is intentionally ignored: the shim produces no binaries so
	// cross-compile.sh's existence/mtime checks will fail.  We only care what
	// AGENT_DIRECTOR_BUILD_VERSION value was passed to make.
	_ = cmd.Run()

	// ── 5. Read the recorded value ─────────────────────────────────────────
	recorded, err := os.ReadFile(recordFile)
	if err != nil {
		t.Fatalf(
			"b.9ba: make shim record file not written (%v); "+
				"cross-compile.sh may not have invoked make, or the PATH shim was not found",
			err,
		)
	}
	got := strings.TrimSpace(string(recorded))

	expectedVersion := packageJSONVersion(t, root)

	// ── 6. Assertions ───────────────────────────────────────────────────────
	if got == "" {
		t.Fatalf(
			"b.9ba regression: make received AGENT_DIRECTOR_BUILD_VERSION=\"\" (empty); "+
				"cross-compile.sh must derive the version from pkg/ts-bun-client/package.json "+
				"and export it before invoking make (want %q)",
			expectedVersion,
		)
	}

	const devSentinel = "0.0.0-dev"
	if got == devSentinel {
		t.Fatalf(
			"b.9ba regression: make received AGENT_DIRECTOR_BUILD_VERSION=%q (dev sentinel); "+
				"cross-compile.sh must derive the version from pkg/ts-bun-client/package.json "+
				"when AGENT_DIRECTOR_BUILD_VERSION is unset (want %q)",
			got, expectedVersion,
		)
	}

	const nullLiteral = "null"
	if got == nullLiteral {
		t.Fatalf(
			"b.9ba regression: make received AGENT_DIRECTOR_BUILD_VERSION=%q (jq null literal); "+
				"pkg/ts-bun-client/package.json may be missing a .version field or jq failed "+
				"(want %q)",
			got, expectedVersion,
		)
	}

	if got != expectedVersion {
		t.Fatalf(
			"b.9ba: make received AGENT_DIRECTOR_BUILD_VERSION=%q; "+
				"want %q (from pkg/ts-bun-client/package.json)",
			got, expectedVersion,
		)
	}

	t.Logf(
		"cross-compile.sh correctly propagated AGENT_DIRECTOR_BUILD_VERSION=%q to make "+
			"(matches pkg/ts-bun-client/package.json)",
		got,
	)
}
