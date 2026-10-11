package sandboxcigates_test

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/test/sandbox/internal/sandboxtest"
)

// workflowsDir is .github/workflows, or WORKFLOWS_UNDER_TEST=<dir> to prove the
// fails-before direction against pre-fix workflows.
func workflowsDir(t *testing.T) string {
	t.Helper()
	if d := os.Getenv("WORKFLOWS_UNDER_TEST"); d != "" {
		return d
	}
	return filepath.Join(sandboxtest.RepoRoot(t), ".github", "workflows")
}

// workflowLines maps each workflow file name to its lines, full-line comments dropped.
func workflowLines(t *testing.T) map[string][]string {
	t.Helper()
	entries, err := os.ReadDir(workflowsDir(t))
	if err != nil {
		t.Fatalf("read workflows: %v", err)
	}
	files := map[string][]string{}
	for _, e := range entries {
		if ext := filepath.Ext(e.Name()); ext != ".yml" && ext != ".yaml" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(workflowsDir(t), e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		for _, l := range strings.Split(string(data), "\n") {
			if !strings.HasPrefix(strings.TrimSpace(l), "#") {
				files[e.Name()] = append(files[e.Name()], l)
			}
		}
	}
	return files
}

func indentOf(l string) int { return len(l) - len(strings.TrimLeft(l, " ")) }

// listStep returns the `run: |` script and the env of integration.yml's `id: list` step.
func listStep(t *testing.T) (script string, env map[string]string) {
	t.Helper()
	lines := workflowLines(t)["integration.yml"]
	idAt := slices.IndexFunc(lines, func(l string) bool { return strings.TrimSpace(l) == "id: list" })
	if idAt < 0 {
		t.Fatal("integration.yml has no step with `id: list`, the step that lists the Docker harness groups")
	}
	keyIndent := indentOf(lines[idAt])
	start := idAt
	for start > 0 && !strings.HasPrefix(strings.TrimSpace(lines[start]), "- ") {
		start--
	}
	end := idAt + 1
	for end < len(lines) && (strings.TrimSpace(lines[end]) == "" || indentOf(lines[end]) >= keyIndent) {
		end++
	}
	env = map[string]string{}
	var key string
	var body []string
	for _, l := range lines[start:end] {
		trimmed := strings.TrimPrefix(strings.TrimSpace(l), "- ")
		switch {
		case trimmed == "":
			if key == "run" {
				body = append(body, "")
			}
		case indentOf(l) <= keyIndent:
			key, _, _ = strings.Cut(trimmed, ":")
		case key == "env":
			k, v, _ := strings.Cut(trimmed, ":")
			env[k] = strings.Trim(strings.TrimSpace(v), `"'`)
		case key == "run":
			body = append(body, l)
		}
	}
	if len(body) == 0 {
		t.Fatal("integration.yml's `id: list` step has no `run: |` script")
	}
	cut := indentOf(body[0])
	for i, l := range body {
		body[i] = l[min(cut, len(l)):]
	}
	return strings.Join(body, "\n"), env
}

// listedSlugs runs `make list-test-docker-epics` in the repo and returns its slugs.
func listedSlugs(t *testing.T) []string {
	t.Helper()
	c := exec.Command("make", "-f", sandboxtest.MakefileUnderTest(t), "--no-print-directory", "-s", "list-test-docker-epics")
	c.Dir = sandboxtest.RepoRoot(t)
	c.Env = sandboxtest.ScrubbedMakeEnv()
	out, err := c.Output()
	if err != nil {
		t.Fatalf("make list-test-docker-epics: %v", err)
	}
	return strings.Fields(string(out))
}

// runListStep runs the list step (script, stepEnv) for event in a tree whose test/docker-epics.txt
// is list, or that has no such file if list is "". It returns the step's GITHUB_OUTPUT outputs and its output.
func runListStep(t *testing.T, script string, stepEnv map[string]string, event, list string) (outputs map[string]string, out string, err error) {
	t.Helper()
	root := t.TempDir()
	if err := os.Symlink(sandboxtest.MakefileUnderTest(t), filepath.Join(root, "Makefile")); err != nil {
		t.Fatalf("link Makefile: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "test"), 0o755); err != nil {
		t.Fatalf("make test dir: %v", err)
	}
	if list != "" {
		if err := os.WriteFile(filepath.Join(root, "test", "docker-epics.txt"), []byte(list), 0o644); err != nil {
			t.Fatalf("write docker-epics.txt: %v", err)
		}
	}
	ghOutput := filepath.Join(t.TempDir(), "github_output")
	env := sandboxtest.ScrubbedMakeEnv("GITHUB_OUTPUT=" + ghOutput)
	for k, v := range stepEnv {
		switch {
		case v == "${{ github.event_name }}":
			v = event
		case strings.Contains(v, "${{"):
			t.Fatalf("list step env %s=%s: the test supports only ${{ github.event_name }}", k, v)
		}
		env = append(env, k+"="+v)
	}
	c := exec.Command("bash", "-e", "-c", script) // a run: step's default shell on a Linux runner
	c.Dir = root
	c.Env = env
	b, err := c.CombinedOutput()
	var exitErr *exec.ExitError
	if err != nil && !errors.As(err, &exitErr) {
		t.Fatalf("run list step: %v", err)
	}
	outputs = map[string]string{}
	data, _ := os.ReadFile(ghOutput)
	for _, l := range strings.Split(string(data), "\n") {
		if k, v, ok := strings.Cut(l, "="); ok {
			outputs[k] = v
		}
	}
	return outputs, string(b), err
}

// TestDockerMatrix_ListStep (b.ug8): the list step emits every listed slug, an added one
// included, or harness-smoke alone on a pull_request, plus pr_epic; a bad list fails the step.
func TestDockerMatrix_ListStep(t *testing.T) {
	sandboxtest.RequireTools(t, "skipping b.ug8 docker matrix test", "make", "bash", "jq")
	repoList, err := os.ReadFile(filepath.Join(sandboxtest.RepoRoot(t), "test", "docker-epics.txt"))
	if err != nil {
		t.Fatalf("read test/docker-epics.txt: %v", err)
	}
	withAdded := string(repoList) + "\nb-ug8-added-group\n"
	all := append(listedSlugs(t), "b-ug8-added-group")
	script, stepEnv := listStep(t)
	const noGroup = "::error::make list-test-docker-epics listed no Docker harness group: test/docker-epics.txt is missing or has no slug"

	for _, tc := range []struct {
		name, event, list string   // list "": no test/docker-epics.txt
		want              []string // nil: the step fails and writes no output
		wantOut           string
	}{
		{name: "push_runs_every_slug", event: "push", list: withAdded, want: all},
		{name: "workflow_dispatch_runs_every_slug", event: "workflow_dispatch", list: withAdded, want: all},
		{name: "pull_request_runs_harness_smoke", event: "pull_request", list: withAdded, want: []string{"harness-smoke"}},
		{name: "pull_request_without_harness_smoke_fails", event: "pull_request", list: "epic-03-spawn\n", wantOut: "harness-smoke, the group a pull_request runs, is not in test/docker-epics.txt"},
		{name: "missing_list_fails", event: "push", wantOut: noGroup},
		{name: "empty_list_fails", event: "push", list: "# no slug\n\n", wantOut: noGroup},
		{name: "no_break_space_only_list_fails", event: "push", list: " \n", wantOut: noGroup},
		{name: "slug_with_shell_metacharacter_fails", event: "push", list: "harness-smoke\nbad;slug\n", wantOut: "bad;slug"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			outputs, out, err := runListStep(t, script, stepEnv, tc.event, tc.list)
			if tc.want == nil {
				if err == nil || len(outputs) != 0 {
					t.Errorf("list step: err = %v, outputs = %q; want a failure and no output; output:\n%s", err, outputs, out)
				}
			} else {
				var epics []string
				if jerr := json.Unmarshal([]byte(outputs["epics"]), &epics); err != nil || jerr != nil || !slices.Equal(epics, tc.want) {
					t.Errorf("list step: err = %v, epics output %q (%v), want\n  %q\noutput:\n%s", err, outputs["epics"], jerr, tc.want, out)
				}
				if outputs["pr_epic"] != "harness-smoke" {
					t.Errorf("pr_epic output = %q, want harness-smoke, the group a pull_request runs; output:\n%s", outputs["pr_epic"], out)
				}
			}
			if !strings.Contains(out, tc.wantOut) {
				t.Errorf("output does not contain %q:\n%s", tc.wantOut, out)
			}
		})
	}
}

// TestDockerMatrix_HarnessJobUsesListedSlugs (b.ug8): the harness job's matrix is
// the list step's output, and no listed slug is hard-coded except PR_EPIC's.
func TestDockerMatrix_HarnessJobUsesListedSlugs(t *testing.T) {
	sandboxtest.RequireTools(t, "skipping b.ug8 docker matrix test", "make")
	lines := workflowLines(t)["integration.yml"]
	text := strings.Join(lines, "\n")
	for _, want := range []string{"${{ steps.list.outputs.epics }}", "${{ fromJSON(needs.docker-epics.outputs.epics) }}"} {
		if !strings.Contains(text, want) {
			t.Errorf("integration.yml does not use %s: the harness matrix is not built from the list step", want)
		}
	}
	for _, slug := range listedSlugs(t) {
		word := regexp.MustCompile(`(^|[^A-Za-z0-9._-])` + regexp.QuoteMeta(slug) + `($|[^A-Za-z0-9._-])`)
		for _, l := range lines {
			if word.MatchString(l) && strings.TrimSpace(l) != "PR_EPIC: "+slug {
				t.Errorf("integration.yml hard-codes Docker harness group %s: %s", slug, strings.TrimSpace(l))
			}
		}
	}
}

// TestDockerMatrix_SecretsOnlyForPREpic (b.ug8): the harness job passes each secret only to the
// job for pr_epic, the group a pull_request runs, which docker-epics outputs from the list step.
func TestDockerMatrix_SecretsOnlyForPREpic(t *testing.T) {
	lines := workflowLines(t)["integration.yml"]
	if !slices.ContainsFunc(lines, func(l string) bool { return strings.TrimSpace(l) == "pr_epic: ${{ steps.list.outputs.pr_epic }}" }) {
		t.Error("integration.yml's docker-epics job does not output pr_epic from the list step")
	}
	start := slices.Index(lines, "  linux-integration:")
	if start < 0 {
		t.Fatal("integration.yml has no linux-integration job, the Docker harness matrix job")
	}
	gated := regexp.MustCompile(`: \$\{\{ matrix\.epic == needs\.docker-epics\.outputs\.pr_epic && secrets\.\w+ \|\| '' \}\}$`)
	for _, l := range lines[start+1:] {
		if strings.TrimSpace(l) != "" && indentOf(l) <= 2 {
			break
		}
		if strings.Contains(l, "secrets.") && !gated.MatchString(l) {
			t.Errorf("linux-integration passes a secret to every Docker harness group, not only pr_epic's: %s", strings.TrimSpace(l))
		}
	}
}
