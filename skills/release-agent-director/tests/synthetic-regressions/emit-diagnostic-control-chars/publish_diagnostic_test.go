package emitdiagnosticcontrolchars_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// loadPublishDiagnostic defines emit_publish_diagnostic alone from the orchestrator
// ($0), which runs the whole publish phase when executed, after two substeps succeeded.
const loadPublishDiagnostic = `eval "$(sed -n '/^emit_publish_diagnostic() {$/,/^}$/p' "$0")" && ` +
	`SUCCEEDED_SUBSTEPS=(publish.push-branch publish.create-tag)`

// runPublishEmit runs call with emit_publish_diagnostic loaded (see runLoaded).
func runPublishEmit(t *testing.T, call, stdin string, args ...string) map[string]any {
	t.Helper()
	return runLoaded(t, loadPublishDiagnostic, filepath.Join("publish", "publish-orchestrator.sh"), call, stdin, args...)
}

// TestEmitPublishDiagnosticOneLine (b.nsa): the diagnostic is one line, as
// run-parallel.sh's `grep '^{'` needs, not a pretty-printed object, and each
// field decodes back to exactly its argument.
func TestEmitPublishDiagnosticOneLine(t *testing.T) {
	for _, tc := range []struct{ name, text string }{
		{"plain", "push rejected"},
		{"raw_output_lines", oddLine + "\nsecond\tline\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			want := map[string]any{
				"gate":                       "publish.npm-publish",
				"offending_file_or_artifact": "file:" + tc.text,
				"description":                "desc:" + tc.text,
				"corrective_action":          "fix:" + tc.text,
				"which_substep_failed":       "publish.npm-publish",
				"prior_substeps_succeeded":   []any{"publish.push-branch", "publish.create-tag"},
				"upstream_response_verbatim": "up:" + tc.text,
			}
			got := runPublishEmit(t, `emit_publish_diagnostic "$@"`, "",
				"npm-publish", "desc:"+tc.text, "fix:"+tc.text, "up:"+tc.text, "file:"+tc.text)
			if !reflect.DeepEqual(got, want) {
				t.Errorf("decoded diagnostic\n got %q\nwant %q", got, want)
			}
		})
	}
}

// TestEmitPublishDiagnosticOffendingNull: an omitted or empty fifth argument
// becomes JSON null, not a string.
func TestEmitPublishDiagnosticOffendingNull(t *testing.T) {
	for name, call := range map[string]string{
		"omitted": `emit_publish_diagnostic s d c u`,
		"empty":   `emit_publish_diagnostic s d c u ""`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			v, ok := runPublishEmit(t, call, "")["offending_file_or_artifact"]
			if !ok || v != nil {
				t.Errorf("offending_file_or_artifact = %#v (present %v), want JSON null", v, ok)
			}
		})
	}
}

// TestEmitPublishDiagnosticLargeField (b.nsa): a description or upstream response
// over Linux's 128 KiB single-argument limit still yields one exact diagnostic;
// passed to jq as an argument, its exec fails and nothing is written.
func TestEmitPublishDiagnosticLargeField(t *testing.T) {
	// "$(cat)" builds the argument inside bash, past the exec limit; longTail
	// has no trailing newline for it to strip.
	for field, call := range map[string]string{
		"description":                `emit_publish_diagnostic npm-publish "$(cat)" fix up`,
		"upstream_response_verbatim": `emit_publish_diagnostic npm-publish desc fix "$(cat)"`,
	} {
		t.Run(field, func(t *testing.T) {
			t.Parallel()
			if got, _ := runPublishEmit(t, call, longTail)[field].(string); got != longTail {
				t.Errorf("%s did not round-trip: got %d bytes, want %d", field, len(got), len(longTail))
			}
		})
	}
}

// TestPublishOrchestratorLargeUpstream (b.nsa): when git push fails with a stderr
// tail over 128 KiB, the orchestrator still writes one SR-14 line to stderr, and
// the report carries that diagnostic and the substep's response_excerpt in full.
func TestPublishOrchestratorLargeUpstream(t *testing.T) {
	t.Parallel()
	requireJQ(t)
	work, bin, reportDir := t.TempDir(), t.TempDir(), t.TempDir()
	fakeFailingBin(t, bin, "git", longTail+"\n") // push-branch is the first substep
	for _, name := range []string{"pkg.tgz", "notes.md", "asset"} {
		if err := os.WriteFile(filepath.Join(work, name), nil, 0o644); err != nil {
			t.Fatalf("write artifact %s: %v", name, err)
		}
	}
	cmd := exec.Command("bash", filepath.Join(gatesDir(t), "publish", "publish-orchestrator.sh"),
		"--target", "0.0.0-b-nsa", "--bump-sha", "0000000",
		"--tarball", "pkg.tgz", "--notes", "notes.md", "--binaries", "asset", "--release")
	cmd.Dir = work
	cmd.Env = append(os.Environ(),
		"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"RELEASE_REPORT_DIR="+reportDir)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	_ = cmd.Run()
	if got := cmd.ProcessState.ExitCode(); got != 1 {
		t.Fatalf("exit = %d, want 1\nstderr: %.2000q", got, stderr.String())
	}

	var lines []string
	for _, l := range strings.Split(stderr.String(), "\n") {
		if strings.HasPrefix(l, "{") {
			lines = append(lines, l)
		}
	}
	if len(lines) != 1 {
		t.Fatalf("want one stderr line starting with '{', got %d\nstderr: %.2000q", len(lines), stderr.String())
	}
	diag := decodeOneLine(t, lines[0])
	upstream, _ := diag["upstream_response_verbatim"].(string)
	if !strings.HasSuffix(upstream, longTail) { // after the fake's "args=[…] "
		t.Errorf("upstream_response_verbatim lost output: got %d bytes ending %.200q", len(upstream), upstream[max(0, len(upstream)-200):])
	}

	data, err := os.ReadFile(filepath.Join(reportDir, "release-report.json"))
	if err != nil {
		t.Fatalf("read report: %v", err)
	}
	var report struct {
		PublishSubsteps []map[string]any `json:"publish_substeps"`
		Diagnostics     []map[string]any `json:"diagnostics"`
	}
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatalf("report is not valid JSON: %v (%d bytes)", err, len(data))
	}
	if len(report.Diagnostics) != 1 || !reflect.DeepEqual(report.Diagnostics[0], diag) {
		t.Errorf("report diagnostics differ from the stderr line: got %d diagnostics", len(report.Diagnostics))
	}
	if n := len(report.PublishSubsteps); n != 1 {
		t.Fatalf("want 1 substep (the failed push-branch), got %d", n)
	}
	sub := report.PublishSubsteps[0]
	if sub["name"] != "publish.push-branch" || sub["outcome"] != "failed" {
		t.Errorf("substep = %v / %v, want publish.push-branch / failed", sub["name"], sub["outcome"])
	}
	if sub["response_excerpt"] != upstream {
		got, _ := sub["response_excerpt"].(string)
		t.Errorf("response_excerpt: got %d bytes, want the %d-byte upstream response", len(got), len(upstream))
	}
}
