package part_test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/tools/macrun/internal/macfix"
	"github.com/gabemahoney/agent-director/tools/macrun/internal/model"
	"github.com/gabemahoney/agent-director/tools/macrun/internal/part"
	"github.com/gabemahoney/agent-director/tools/macrun/internal/treefix"
)

func TestSetupFailures(t *testing.T) {
	t.Parallel()
	type row struct {
		name string
		// noMacTests leaves test/mac/parts.txt out of the checkout.
		noMacTests bool
		change     func(t *testing.T, opts *part.Options, tools *macfix.Tools)
		step       string
		// message gives the failure's message for the part's options.
		message func(opts part.Options) string
	}
	text := func(s string) func(part.Options) string { return func(part.Options) string { return s } }
	rows := []row{
		{
			name:    "plan without the part",
			change:  func(_ *testing.T, opts *part.Options, _ *macfix.Tools) { opts.Part = "two" },
			step:    part.StepPlan,
			message: text(`the plan has no part "two"`),
		},
		{
			name:       "no Mac Tests in the checkout",
			noMacTests: true,
			step:       part.StepPlan,
			message:    text("the plan is no-mac-tests, not planned: The tree has no test/mac/parts.txt, so it doesn't have the Mac Tests and no part is planned."),
		},
		{
			name: "HEAD not the tested commit",
			change: func(_ *testing.T, opts *part.Options, tools *macfix.Tools) {
				tools.Outputs["/usr/bin/git -C "+opts.Root+" rev-parse HEAD"] = "0bad"
			},
			step: part.StepCheckout,
			message: func(opts part.Options) string {
				return "HEAD of " + opts.Root + " is 0bad, not the tested commit c0ffee"
			},
		},
		{
			name:    "self-hosted runner",
			change:  func(_ *testing.T, opts *part.Options, _ *macfix.Tools) { opts.RunnerEnvironment = "self-hosted" },
			step:    part.CheckRunner,
			message: text(`runner.environment is "self-hosted", not "github-hosted"; a Mac Run runs only on GitHub-hosted runners`),
		},
		{
			name: "bash older than 4.4",
			change: func(_ *testing.T, opts *part.Options, tools *macfix.Tools) {
				opts.Bash = "/bin/bash"
				tools.Outputs[strings.Replace(macfix.BashVersionInfo, macfix.Bash, "/bin/bash", 1)] = "3 2"
			},
			step:    part.StepTools,
			message: text(`-bash /bin/bash is bash "3 2", older than 4.4`),
		},
		{
			name:    "-bash not an absolute path",
			change:  func(_ *testing.T, opts *part.Options, _ *macfix.Tools) { opts.Bash = "bash" },
			step:    part.StepTools,
			message: text(`-bash "bash" is not an executable by absolute path`),
		},
		{
			name:    "-sqlite3 not an absolute path",
			change:  func(_ *testing.T, opts *part.Options, _ *macfix.Tools) { opts.SQLite3 = "sqlite3" },
			step:    part.StepTools,
			message: text(`-sqlite3 "sqlite3" is not an executable by absolute path`),
		},
		{
			name:    "Homebrew's bash where macOS's should be",
			change:  func(_ *testing.T, _ *part.Options, tools *macfix.Tools) { delete(tools.Executables, "/bin/bash") },
			step:    part.StepTools,
			message: text("bash resolves to /opt/homebrew/bin/bash on the tests' PATH, not /bin/bash"),
		},
		{
			name: "another sqlite3 where macOS's should be",
			change: func(_ *testing.T, _ *part.Options, tools *macfix.Tools) {
				delete(tools.Executables, "/usr/bin/sqlite3")
				tools.Executables["/opt/homebrew/bin/sqlite3"] = true
			},
			step:    part.StepTools,
			message: text("sqlite3 resolves to /opt/homebrew/bin/sqlite3 on the tests' PATH, not /usr/bin/sqlite3"),
		},
		{
			name:    "a go in a macOS folder ahead of the one found",
			change:  func(_ *testing.T, _ *part.Options, tools *macfix.Tools) { tools.Executables["/usr/bin/go"] = true },
			step:    part.StepTools,
			message: text("go resolves to /usr/bin/go on the tests' PATH, not /usr/local/go/bin/go"),
		},
		{
			// macrun found Homebrew's go, so Homebrew's folder comes before
			// the found bun's.
			name: "Homebrew's bun ahead of the one found",
			change: func(_ *testing.T, opts *part.Options, tools *macfix.Tools) {
				delete(tools.Executables, macfix.Go)
				opts.Environ = append(slices.Clone(opts.Environ), "PATH=/Users/runner/.bun/bin:/opt/homebrew/bin:/usr/bin:/bin")
			},
			step:    part.StepTools,
			message: text("bun resolves to /opt/homebrew/bin/bun on the tests' PATH, not /Users/runner/.bun/bin/bun"),
		},
		{
			name: "a version command failing",
			change: func(_ *testing.T, _ *part.Options, tools *macfix.Tools) {
				tools.Fail = "/usr/bin/sw_vers -productVersion"
			},
			step:    part.StepVersions,
			message: text("macos-product-version: /usr/bin/sw_vers -productVersion: exit status 1"),
		},
	}
	// Each agent state path in the home stops the part, a dangling link too.
	for _, home := range []struct {
		rel  string
		make func(path string) error
	}{
		{".config/agent-director", func(path string) error { return os.MkdirAll(path, 0o755) }},
		{".agent-director", func(path string) error { return os.WriteFile(path, nil, 0o644) }},
		{".claude", func(path string) error { return os.Symlink(filepath.Join(filepath.Dir(path), "gone"), path) }},
		{".codex", func(path string) error { return os.Mkdir(path, 0o755) }},
	} {
		rows = append(rows, row{
			name: "home holds " + home.rel,
			change: func(t *testing.T, opts *part.Options, _ *macfix.Tools) {
				if err := home.make(filepath.Join(opts.Home, filepath.FromSlash(home.rel))); err != nil {
					t.Fatal(err)
				}
			},
			step: part.CheckHome,
			message: func(opts part.Options) string {
				return filepath.Join(opts.Home, filepath.FromSlash(home.rel)) + " exists, so this Mac may hold a real agent-director store or agent account"
			},
		})
	}

	for _, tc := range rows {
		files := map[string]string{
			"pkg/a/a_test.go":    "package a\n\nimport \"testing\"\n\nfunc TestA(t *testing.T) {}\n",
			"test/mac/parts.txt": "one\tgo\tpkg/a\n",
		}
		if tc.noMacTests {
			delete(files, "test/mac/parts.txt")
		}
		root := treefix.Write(t, files)
		opts := macfix.PartOptions(t, root, "one")
		tools := macfix.Mac(root, "c0ffee")
		if tc.change != nil {
			tc.change(t, &opts, tools)
		}
		g := &macfix.GoRunner{}
		var goCmd string
		var stdout, stderr bytes.Buffer
		code := part.Run(context.Background(), opts, deps(tools, g, &goCmd), &stdout, &stderr)

		message := tc.message(opts)
		wantStdout := marked(t, model.Event{
			Event: model.EventSetupFailed, Part: opts.Part, RunID: 7, Attempt: 2, Commit: "c0ffee", Step: tc.step, Message: message,
		})
		wantStderr := fmt.Sprintf("macrun part: %s failed: %s\n", tc.step, message)
		if code != 1 || stdout.String() != wantStdout || stderr.String() != wantStderr {
			t.Errorf("%s: Run = %d, stdout %q, stderr %q; want 1, %q, %q", tc.name, code, stdout.String(), stderr.String(), wantStdout, wantStderr)
		}
		wantRec := model.Record{
			SchemaVersion: model.RecordSchemaVersion, Part: opts.Part, RunID: 7, Attempt: 2, Commit: "c0ffee",
			SetupFailure: &model.SetupFailure{Step: tc.step, Message: message},
		}
		if rec := readRecord(t, opts.Out); !reflect.DeepEqual(rec, wantRec) {
			t.Errorf("%s: record =\n%+v\nwant\n%+v", tc.name, rec, wantRec)
		}
		if len(g.Calls) > 0 {
			t.Errorf("%s: Go runner calls = %+v; want none", tc.name, g.Calls)
		}
	}
}
