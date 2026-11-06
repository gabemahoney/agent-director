package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/gabemahoney/agent-director/tools/macrun/internal/model"
	"github.com/gabemahoney/agent-director/tools/macrun/internal/plan"
	"github.com/gabemahoney/agent-director/tools/macrun/internal/treefix"
)

const testA = "package a\n\nimport \"testing\"\n\nfunc TestA(t *testing.T) {}\nfunc TestB(t *testing.T) {}\n"

const noMacTestsSummary = "status: no-mac-tests\n" +
	"reason: The tree has no test/mac/parts.txt, so it doesn't have the Mac Tests and no part is planned.\n" +
	"coverage: go partial, testplan partial\n"

func TestRunPlan(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		files      map[string]string
		wantStdout string
		wantOutput string
	}{
		{
			name: "planned",
			files: map[string]string{
				"pkg/a/a_test.go":    testA,
				"test/mac/parts.txt": "zeta\tgo\tpkg/a:^TestB$\nalpha\tgo\tpkg/a:^TestA$\n",
			},
			wantStdout: "status: planned\n" +
				"part zeta: 2 expected cases\n" +
				"part alpha: 2 expected cases\n" +
				"coverage: go partial, testplan partial\n",
			wantOutput: "parts=[\"zeta\",\"alpha\"]\nplanned=true\n",
		},
		{
			name:       "no Mac Tests",
			files:      map[string]string{"pkg/a/a_test.go": testA},
			wantStdout: noMacTestsSummary,
			wantOutput: "parts=[]\nplanned=false\n",
		},
		{
			name: "invalid setup",
			files: map[string]string{
				"pkg/a/a_test.go":                testA,
				"test/mac/parts.txt":             "# only a comment\n",
				"test/mac/expected-failures.txt": "go:pkg/a:TestA\tb.2pk\n",
			},
			wantStdout: "status: invalid-setup\n" +
				"reason: The Mac Run setup under test/mac/ is invalid, so no part is planned; fix every listed error.\n" +
				"error: test/mac/parts.txt: lists no part; add a line <part name>\\t<suite>\\t<selection>\n" +
				"error: test/mac/expected-failures.txt:1: has 2 fields; expected <case ID>\\t<Bug ID>\\t<note>\n" +
				"coverage: go partial, testplan partial\n",
			wantOutput: "parts=[]\nplanned=false\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := treefix.Write(t, tc.files)
			out := t.TempDir()
			jsonPath := filepath.Join(out, "plan.json")
			githubOutput := filepath.Join(out, "github-output")
			if err := os.WriteFile(githubOutput, []byte("earlier=1\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			var stdout, stderr bytes.Buffer
			code := run([]string{"plan", "-root", root, "-json", jsonPath, "-github-output", githubOutput}, &stdout, &stderr)
			if code != 0 || stderr.String() != "" {
				t.Fatalf("run = %d, stderr %q; want 0 and no stderr", code, stderr.String())
			}
			if stdout.String() != tc.wantStdout {
				t.Errorf("stdout =\n%s\nwant\n%s", stdout.String(), tc.wantStdout)
			}
			if got, _ := os.ReadFile(githubOutput); string(got) != "earlier=1\n"+tc.wantOutput {
				t.Errorf("GitHub output = %q; want %q", got, "earlier=1\n"+tc.wantOutput)
			}
			want, err := plan.Compute(root)
			if err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(jsonPath)
			if err != nil {
				t.Fatal(err)
			}
			var got model.Plan
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("-json plan = %+v; want %+v", got, want)
			}
		})
	}
}

func TestRunErrors(t *testing.T) {
	t.Parallel()
	noMacTests := treefix.Write(t, map[string]string{})
	unreadable := treefix.Write(t, map[string]string{
		"pkg/a/a_test.go":                 testA,
		"test/mac/parts.txt":              "one\tgo\tpkg/a\n",
		"test/mac/expected-failures.txt/": "",
	})
	missingDir := filepath.Join(t.TempDir(), "missing", "plan.json")
	for _, tc := range []struct {
		name       string
		args       []string
		wantCode   int
		wantStdout string
		wantStderr string
	}{
		{"no subcommand", nil, 2, "", "macrun: no subcommand\n" + usage},
		{"unknown subcommand", []string{"plans"}, 2, "", "macrun: unknown subcommand \"plans\"\n" + usage},
		{
			"unwritable GitHub output", []string{"plan", "-root", noMacTests, "-github-output", noMacTests},
			1, noMacTestsSummary, "macrun plan: open GitHub output: open " + noMacTests + ": is a directory\n",
		},
		{
			"unwritable JSON", []string{"plan", "-root", noMacTests, "-json", missingDir},
			1, noMacTestsSummary, "macrun plan: write plan: open " + missingDir + ": no such file or directory\n",
		},
		{
			"unreadable tree", []string{"plan", "-root", unreadable},
			1, "", "macrun plan: read test/mac/expected-failures.txt: is a directory\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var stdout, stderr bytes.Buffer
			code := run(tc.args, &stdout, &stderr)
			if code != tc.wantCode || stdout.String() != tc.wantStdout || stderr.String() != tc.wantStderr {
				t.Errorf("run = %d, stdout %q, stderr %q; want %d, %q, %q",
					code, stdout.String(), stderr.String(), tc.wantCode, tc.wantStdout, tc.wantStderr)
			}
		})
	}
}
