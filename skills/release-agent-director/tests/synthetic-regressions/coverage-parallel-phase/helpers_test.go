// Package coverageparallelphase_test is a synthetic-regression package for the
// parallel coverage-phase executor, skills/release-agent-director/gates/lib/
// run-parallel.sh (b.mgw parallel /release pipeline).
//
// BACKGROUND
// ==========
// run-parallel.sh runs the coverage gates concurrently, tolerates sibling
// failures (SR-6.5), aggregates each gate's SR-14 stderr diagnostics into a
// consolidated JSON document on stdout, and exits 0/1/2 (all pass / any fail /
// usage-or-config error). Before b.mgw it had NO direct test coverage. This
// package proves the executor's semantics — sibling tolerance, multi-failure
// reporting with nothing masked, diagnostics aggregation with real content, and
// config-error exit codes — by driving run-parallel.sh DIRECTLY with a cheap
// toy gates-config (SR-7.3/7.4/7.5). The executor itself is never modified
// (SR-1.1).
//
// FAILURE CLASS (one dir = one package = one failure class)
// =========================================================
// Parallel coverage-phase executor semantics. Every test here targets
// run-parallel.sh's aggregation and exit-code contract, nothing else.
//
// PACKAGE-SCOPED PROHIBITION (SR-7.5)
// ===================================
// No test in this package may execute — or even reference — any of the five
// REAL coverage gates. All executor runs use the toy gates-config defined in
// toyconfig_test.go, whose gate commands are trivial, seconds-scale shell
// snippets materialized into t.TempDir(). This keeps the package fast and
// hermetic and guarantees it can never rewrite the real ~/.agent-director store.
//
// CONVENTIONS (mirrors coverage-go-root-fires / release-postconditions)
// =====================================================================
//   - helpers_test.go  — shared repoRoot walk-up, run-parallel.sh path resolver,
//     and the typed consolidated-output structs (declare-once home for every
//     JSON-key literal this package asserts on, SR-7.3).
//   - toyconfig_test.go — the reusable toy gates-config fixture mechanism:
//     variant builders, materialize + invoke helpers, and the cross-package
//     reuse anchor for Epic t1.2mt.wc.
//   - mechanism_test.go — the sole exit-0 mechanism-validation test.
//
// The gate-name and SR-14 diagnostic-field literals live once in
// toyconfig_test.go; the consolidated-output JSON keys live once here as struct
// tags. These package-local structs do not violate SR-7.3's shared-helpers
// rule, which governs the report-shape structs in release-postconditions
// (Epic t1.2mt.wc), a different package.
//
// SLOW TEST
// =========
// The toy commands are seconds-scale, but the executor spawns subprocesses and
// shells out to jq many times. Any test measured slower than ~1s is guarded
// with testing.Short() per the sibling convention.
package coverageparallelphase_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// ─── typed consolidated-output shape (SR-2.1) ─────────────────────────────────
//
// These mirror exactly what run-parallel.sh emits on stdout. Every JSON key the
// package asserts on is declared once here as a struct tag (SR-7.3); tests
// assert against fields, never re-spelled raw key strings.

// consolidatedOutput is the top-level document run-parallel.sh writes to stdout.
type consolidatedOutput struct {
	PhaseName string     `json:"phase_name"`
	Outcome   string     `json:"outcome"`
	SubChecks []subCheck `json:"sub_checks"`
}

// subCheck is one gate's entry in the consolidated output's sub_checks array.
type subCheck struct {
	Name          string           `json:"name"`
	Outcome       string           `json:"outcome"`
	DurationMS    int64            `json:"duration_ms"`
	ExitCode      int              `json:"exit_code"`
	StderrExcerpt string           `json:"stderr_excerpt"`
	Diagnostics   []sr14Diagnostic `json:"diagnostics"`
}

// sr14Diagnostic mirrors one parsed SR-14 stderr diagnostic object. The
// executor collects every stderr line beginning with '{' into this array. The
// field set matches the executor's own missing-cwd diagnostic and the gate
// contract (gate / offending_file_or_artifact / description / corrective_action).
type sr14Diagnostic struct {
	Gate                    string `json:"gate"`
	OffendingFileOrArtifact string `json:"offending_file_or_artifact"`
	Description             string `json:"description"`
	CorrectiveAction        string `json:"corrective_action"`
}

// ─── outcome literals (declare-once, SR-7.3) ──────────────────────────────────

const (
	outcomePassed = "passed"
	outcomeFailed = "failed"
)

// ─── path helpers (repoRoot walk-up convention, SR-7.2) ───────────────────────

// repoRoot walks up from the package working directory until it finds go.mod,
// matching the sibling packages' convention.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("repoRoot: os.Getwd: %v", err)
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
}

// runParallelPath resolves the absolute path to run-parallel.sh from repoRoot.
// Declared once; never hardcoded relative to the test's cwd.
func runParallelPath(t *testing.T, root string) string {
	t.Helper()
	p := filepath.Join(root, "skills", "release-agent-director", "gates", "lib", "run-parallel.sh")
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("runParallelPath: %s: %v", p, err)
	}
	return p
}

// ─── executor invocation (no test-only seam — a config path, as production) ───

// executorResult captures one direct run-parallel.sh invocation.
type executorResult struct {
	stdout   string
	stderr   string
	exitCode int
}

// runExecutor runs run-parallel.sh directly against the given gates-config
// path, exactly as production callers do (bash "$RUN_PARALLEL" "$CONFIG_FILE"),
// capturing stdout, stderr, and the exit code. HOME is redirected to a
// throwaway dir so the executor and its toy children can never touch the real
// ~/.agent-director store (b.93m trail-leak isolation).
func runExecutor(t *testing.T, configPath string) executorResult {
	t.Helper()
	root := repoRoot(t)
	cmd := exec.Command("bash", runParallelPath(t, root), configPath)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "HOME="+t.TempDir())
	var stdoutBuf, stderrBuf strings.Builder
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf
	_ = cmd.Run() // non-zero exit is expected for failure/config-error cases
	return executorResult{
		stdout:   stdoutBuf.String(),
		stderr:   stderrBuf.String(),
		exitCode: cmd.ProcessState.ExitCode(),
	}
}

// parseConsolidated decodes the executor's stdout into the typed shape,
// failing the test with the raw document on a decode error.
func parseConsolidated(t *testing.T, stdout string) consolidatedOutput {
	t.Helper()
	var out consolidatedOutput
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		t.Fatalf("parseConsolidated: unmarshal: %v\nstdout:\n%s", err, stdout)
	}
	return out
}

// subCheckByName returns the sub_check entry for the named gate, or fails the
// test if it is absent (proves the gate ran to completion and was reported).
func subCheckByName(t *testing.T, out consolidatedOutput, name string) subCheck {
	t.Helper()
	for _, sc := range out.SubChecks {
		if sc.Name == name {
			return sc
		}
	}
	t.Fatalf("subCheckByName: gate %q not present in consolidated output; sub_checks=%+v", name, out.SubChecks)
	panic("unreachable")
}

// subCheckJSONKeys returns the SR-2.1 sub_check JSON key names, derived once from
// the subCheck struct's own json tags so the key spellings never diverge from the
// declare-once home in this file (SR-7.3). A typed decode proves a key's TYPE but
// silently zero-values a MISSING key; presence assertions need the raw key names.
// The returned slice is ordered by struct-field order, so keys[0] is the name key.
func subCheckJSONKeys(t *testing.T) []string {
	t.Helper()
	typ := reflect.TypeOf(subCheck{})
	keys := make([]string, 0, typ.NumField())
	for i := 0; i < typ.NumField(); i++ {
		tag := typ.Field(i).Tag.Get("json")
		if tag == "" || tag == "-" {
			t.Fatalf("subCheckJSONKeys: field %q has no usable json tag", typ.Field(i).Name)
		}
		keys = append(keys, strings.Split(tag, ",")[0])
	}
	return keys
}

// rawSubChecksByName decodes the executor stdout a second time, keeping each
// sub_check as a raw JSON object keyed by its gate name. This preserves the exact
// key set the executor emitted (a typed decode discards it), so callers can assert
// SR-2.1 key PRESENCE, not just type. The name-key spelling is derived from the
// subCheck struct tags (subCheckJSONKeys), never re-spelled. Fails the test on any
// decode error.
func rawSubChecksByName(t *testing.T, stdout string) map[string]map[string]json.RawMessage {
	t.Helper()
	nameKey := subCheckJSONKeys(t)[0] // subCheck.Name is the first field (SR-7.3 tag home)
	var top struct {
		SubChecks []map[string]json.RawMessage `json:"sub_checks"`
	}
	if err := json.Unmarshal([]byte(stdout), &top); err != nil {
		t.Fatalf("rawSubChecksByName: unmarshal: %v\nstdout:\n%s", err, stdout)
	}
	byName := make(map[string]map[string]json.RawMessage, len(top.SubChecks))
	for _, raw := range top.SubChecks {
		nameRaw, ok := raw[nameKey]
		if !ok {
			t.Fatalf("rawSubChecksByName: sub_check missing %q key: %v", nameKey, raw)
		}
		var name string
		if err := json.Unmarshal(nameRaw, &name); err != nil {
			t.Fatalf("rawSubChecksByName: sub_check %q key not a string: %v", nameKey, err)
		}
		byName[name] = raw
	}
	return byName
}
