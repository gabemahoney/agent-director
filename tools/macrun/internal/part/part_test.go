package part_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/tools/macrun/internal/macfix"
	"github.com/gabemahoney/agent-director/tools/macrun/internal/model"
	"github.com/gabemahoney/agent-director/tools/macrun/internal/part"
	"github.com/gabemahoney/agent-director/tools/macrun/internal/treefix"
)

// tree writes a checkout whose part "one" runs pkg/a's TestA and TestB, then
// pkg/b's TestC.
func tree(t *testing.T) string {
	t.Helper()
	return treefix.Write(t, map[string]string{
		"pkg/a/a_test.go":    "package a\n\nimport \"testing\"\n\nfunc TestA(t *testing.T) {}\nfunc TestB(t *testing.T) {}\n",
		"pkg/b/b_test.go":    "package b\n\nimport \"testing\"\n\nfunc TestC(t *testing.T) {}\n",
		"test/mac/parts.txt": "one\tgo\tpkg/a pkg/b\n",
	})
}

// deps returns the part's seams over the fakes; goCmd gets the go command
// the Go runner was made for.
func deps(tools *macfix.Tools, g *macfix.GoRunner, goCmd *string) part.Deps {
	return part.Deps{Tools: tools, NewGoRunner: func(c string) part.GoRunner {
		*goCmd = c
		return g
	}}
}

// marked returns e's log line.
func marked(t *testing.T, e model.Event) string {
	t.Helper()
	var b bytes.Buffer
	if err := model.WriteEvent(&b, e); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// readRecord reads the record a part wrote to out.
func readRecord(t *testing.T, out string) model.Record {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(out, model.RecordFile))
	if err != nil {
		t.Fatal(err)
	}
	var rec model.Record
	if err := json.Unmarshal(data, &rec); err != nil {
		t.Fatal(err)
	}
	return rec
}

var stopCommands = regexp.MustCompile(`(?m)^::stop-commands::([0-9a-f]{32})$`)

func TestFinishedPart(t *testing.T) {
	t.Parallel()
	root := tree(t)
	opts := macfix.PartOptions(t, root, "one")
	// Test output that looks like a marked line, as any test may print.
	lookalike := marked(t, model.Event{Event: model.EventPartEnd, End: model.EndCancelled})
	lookalike = strings.TrimSuffix(lookalike, "\n")
	g := &macfix.GoRunner{Packages: map[string]macfix.Package{
		"pkg/a": {BuildOutput: "pkg/a/a_test.go:3:2: undefined: x\n"},
		"pkg/b": {Steps: []macfix.Step{
			macfix.Start("go:pkg/b"), macfix.Start("go:pkg/b:TestC"),
			macfix.Output(lookalike),
			macfix.End("go:pkg/b:TestC", model.ResultFail, 0.5), macfix.End("go:pkg/b", model.ResultPass, 0.75),
		}},
	}}
	var goCmd string
	var stdout, stderr bytes.Buffer
	if code := part.Run(context.Background(), opts, deps(macfix.Mac(root, "c0ffee"), g, &goCmd), &stdout, &stderr); code != 0 || stderr.Len() > 0 {
		t.Fatalf("Run = %d, stderr %q; want 0 although a case failed", code, stderr.String())
	}

	m := stopCommands.FindStringSubmatch(stdout.String())
	if m == nil {
		t.Fatalf("stdout has no ::stop-commands:: line:\n%s", stdout.String())
	}
	token := m[1]
	want := marked(t, model.Event{
		Event: model.EventPartStart, Part: "one", RunID: 7, Attempt: 2, Commit: "c0ffee",
		Versions: macfix.Versions, Configurations: []model.Configuration{model.ConfigurationDefault},
	}) +
		"::stop-commands::" + token + "\n" +
		"    pkg/a/a_test.go:3:2: undefined: x\n" +
		marked(t, model.Event{Event: model.EventPackageBuild, Dir: "pkg/a"}) +
		marked(t, model.Event{Event: model.EventPackageBuild, Dir: "pkg/b", Built: true}) +
		marked(t, model.Event{Event: model.EventCaseStart, Case: "go:pkg/b"}) +
		marked(t, model.Event{Event: model.EventCaseStart, Case: "go:pkg/b:TestC"}) +
		"    " + lookalike + "\n" +
		marked(t, model.Event{Event: model.EventCaseEnd, Case: "go:pkg/b:TestC", Result: model.ResultFail, Reason: model.ReasonFailed, Elapsed: 0.5}) +
		marked(t, model.Event{Event: model.EventCaseEnd, Case: "go:pkg/b", Result: model.ResultPass, Elapsed: 0.75}) +
		"::" + token + "::\n" +
		marked(t, model.Event{Event: model.EventPartEnd, End: model.EndFinished})
	if stdout.String() != want {
		t.Errorf("stdout =\n%s\nwant\n%s", stdout.String(), want)
	}

	wantRec := model.Record{
		SchemaVersion:  model.RecordSchemaVersion,
		Part:           "one",
		RunID:          7,
		Attempt:        2,
		Commit:         "c0ffee",
		Versions:       macfix.Versions,
		Configurations: []model.Configuration{model.ConfigurationDefault},
		ExpectedCases:  []model.CaseID{"go:pkg/a", "go:pkg/a:TestA", "go:pkg/a:TestB", "go:pkg/b", "go:pkg/b:TestC"},
		Packages: []model.PackageBuild{
			{Dir: "pkg/a", Output: "pkg/a/a_test.go:3:2: undefined: x\n"},
			{Dir: "pkg/b", Built: true},
		},
		Cases: []model.CaseRecord{
			{CaseResult: model.CaseResult{Case: "go:pkg/b:TestC", Result: model.ResultFail, Reason: model.ReasonFailed, Elapsed: 0.5}, Output: lookalike + "\n"},
			{CaseResult: model.CaseResult{Case: "go:pkg/b", Result: model.ResultPass, Elapsed: 0.75}},
		},
		End: model.EndFinished,
	}
	rec := readRecord(t, opts.Out)
	if !reflect.DeepEqual(rec, wantRec) {
		t.Errorf("record =\n%+v\nwant\n%+v", rec, wantRec)
	}

	// The job log gives the same record, without output or expected cases.
	fromLog, ok, err := model.ReadLog(bytes.NewReader(macfix.GitHubLog(stdout.String())))
	wantLog := wantRec
	wantLog.ExpectedCases, wantLog.FromLog = nil, true
	wantLog.Packages = []model.PackageBuild{{Dir: "pkg/a"}, {Dir: "pkg/b", Built: true}}
	wantLog.Cases = []model.CaseRecord{{CaseResult: wantRec.Cases[0].CaseResult}, {CaseResult: wantRec.Cases[1].CaseResult}}
	if err != nil || !ok || !reflect.DeepEqual(fromLog, wantLog) {
		t.Errorf("ReadLog = %+v, %v, %v; want\n%+v", fromLog, ok, err, wantLog)
	}

	// The tests' environment, as the Go runner got it: macOS's own folders
	// first, so Homebrew's go and bun don't shadow the ones macrun found.
	if goCmd != macfix.Go {
		t.Errorf("Go runner made for %q; want %q", goCmd, macfix.Go)
	}
	env := []string{
		"HOME=/Users/runner",
		"BYPASS_CONTAINER_FOR_AGENT_DIRECTOR_TESTS=1",
		"RUNNER_TEMP=/Users/runner/work/_temp",
		"PATH=/usr/bin:/bin:/usr/sbin:/sbin:/usr/local/go/bin:/Users/runner/.bun/bin:/opt/homebrew/bin:/opt/homebrew/sbin",
		"AGENT_DIRECTOR_TEST_BASH=" + macfix.Bash,
		"AGENT_DIRECTOR_TEST_SQLITE3=" + macfix.SQLite3,
	}
	wantCalls := []macfix.Call{
		{Op: "build", Dir: "pkg/a", Env: env},
		{Op: "build", Dir: "pkg/b", Env: env},
		{Op: "run", Dir: "pkg/b", Names: []string{"TestC"}, Env: env},
	}
	if !reflect.DeepEqual(g.Calls, wantCalls) {
		t.Errorf("Go runner calls =\n%+v\nwant\n%+v", g.Calls, wantCalls)
	}
}

func TestCancelledPart(t *testing.T) {
	t.Parallel()
	root := tree(t)
	opts := macfix.PartOptions(t, root, "one")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	g := &macfix.GoRunner{OnBlock: cancel, Packages: map[string]macfix.Package{"pkg/a": {Steps: []macfix.Step{
		macfix.Start("go:pkg/a"), macfix.Start("go:pkg/a:TestA"), macfix.End("go:pkg/a:TestA", model.ResultPass, 0.25),
		macfix.Start("go:pkg/a:TestB"), macfix.Block,
	}}}}
	var goCmd string
	var stdout, stderr bytes.Buffer
	if code := part.Run(ctx, opts, deps(macfix.Mac(root, "c0ffee"), g, &goCmd), &stdout, &stderr); code != 1 || stderr.Len() > 0 {
		t.Errorf("Run = %d, stderr %q; want 1 and no stderr", code, stderr.String())
	}
	m := stopCommands.FindStringSubmatch(stdout.String())
	if m == nil {
		t.Fatalf("stdout has no ::stop-commands:: line:\n%s", stdout.String())
	}
	wantEnd := "::" + m[1] + "::\n" + marked(t, model.Event{Event: model.EventPartEnd, End: model.EndCancelled})
	if !strings.HasSuffix(stdout.String(), wantEnd) {
		t.Errorf("stdout ends\n%s\nwant it to end\n%s", stdout.String(), wantEnd)
	}
	rec := readRecord(t, opts.Out)
	wantCases := []model.CaseRecord{{CaseResult: model.CaseResult{Case: "go:pkg/a:TestA", Result: model.ResultPass, Elapsed: 0.25}}}
	wantWithout := []model.CaseID{"go:pkg/a", "go:pkg/a:TestB"}
	if rec.End != model.EndCancelled || !reflect.DeepEqual(rec.Cases, wantCases) || !reflect.DeepEqual(rec.WithoutResult, wantWithout) {
		t.Errorf("record ends %q with cases %+v, without result %v; want %q, %+v, %v",
			rec.End, rec.Cases, rec.WithoutResult, model.EndCancelled, wantCases, wantWithout)
	}
	// The cancel stops the part before its next package.
	if len(g.Calls) != 2 {
		t.Errorf("Go runner calls = %+v; want pkg/a's build and run only", g.Calls)
	}
}

func TestUnwritableOut(t *testing.T) {
	t.Parallel()
	root := tree(t)
	opts := macfix.PartOptions(t, root, "one")
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	opts.Out = filepath.Join(file, "out")
	var goCmd string
	var stdout, stderr bytes.Buffer
	code := part.Run(context.Background(), opts, deps(macfix.Mac(root, "c0ffee"), &macfix.GoRunner{}, &goCmd), &stdout, &stderr)
	want := "macrun part: create output folder: mkdir " + file + ": not a directory\n"
	if code != 1 || stdout.Len() > 0 || stderr.String() != want {
		t.Errorf("Run = %d, stdout %q, stderr %q; want 1, nothing, %q", code, stdout.String(), stderr.String(), want)
	}
}
