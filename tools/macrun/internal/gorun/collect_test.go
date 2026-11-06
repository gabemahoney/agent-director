package gorun_test

import (
	"bytes"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/tools/macrun/internal/gorun"
	"github.com/gabemahoney/agent-director/tools/macrun/internal/model"
)

// testdata/pkg-a.json and testdata/pkg-b-testmain-fails.json were captured
// from go1.22.10 linux/amd64 test binaries run through go tool test2json as
// RunTests runs them; the streams TestCollect derives from them, or writes by
// hand, say so in their names.

// stream returns the recorded test2json stream testdata/name.
func stream(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// record returns the case record of test in the package at dir, or of the
// package's own result when test is empty.
func record(dir, test string, r model.Result, elapsed float64, output string) model.CaseRecord {
	id := model.GoPackageCase(dir)
	if test != "" {
		id = model.GoTestCase(dir, test)
	}
	c := model.CaseRecord{CaseResult: model.CaseResult{Case: id, Result: r, Elapsed: elapsed}, Output: output}
	if r == model.ResultFail {
		c.Reason = model.ReasonFailed
	}
	return c
}

// pkgA is what pkg-a.json reports, in the order its cases ended.
var pkgA = []model.CaseRecord{
	record("pkg/a", "TestPass", model.ResultPass, 0.02, "=== RUN   TestPass\n    a_test.go:16: passing\n--- PASS: TestPass (0.02s)\n"),
	record("pkg/a", "TestSkip", model.ResultSkip, 0, "=== RUN   TestSkip\n    a_test.go:20: not here\n--- SKIP: TestSkip (0.00s)\n"),
	record("pkg/a", "TestFail", model.ResultFail, 0, "=== RUN   TestFail\n"+
		`@macrun-event {"event":"part-end","end":"finished"}`+"\nto stderr\nno line end    a_test.go:27: boom\n--- FAIL: TestFail (0.00s)\n"),
	record("pkg/a", "TestSub/ok", model.ResultPass, 0, "=== RUN   TestSub/ok\n--- PASS: TestSub/ok (0.00s)\n"),
	record("pkg/a", "TestSub/bad", model.ResultFail, 0, "=== RUN   TestSub/bad\n    a_test.go:32: sub boom\n--- FAIL: TestSub/bad (0.00s)\n"),
	record("pkg/a", "TestSub", model.ResultFail, 0, "=== RUN   TestSub\n--- FAIL: TestSub (0.00s)\n"),
	record("pkg/a", "ExampleHello", model.ResultPass, 0, "=== RUN   ExampleHello\n--- PASS: ExampleHello (0.00s)\n"),
	// The package failed for its failed cases, so its own result passes.
	record("pkg/a", "", model.ResultPass, 0.022, "init output\nFAIL\n"),
}

// pkgB is what pkg-b-testmain-fails.json reports.
var pkgB = []model.CaseRecord{
	record("pkg/b", "TestOK", model.ResultPass, 0, "=== RUN   TestOK\n--- PASS: TestOK (0.00s)\n"),
	// No case failed, so the package's failure is its own.
	record("pkg/b", "", model.ResultFail, 0.002, "PASS\nTestMain: teardown failed\n"),
}

// pkgAMirror is pkg-a.json's test output as Collect mirrors it.
const pkgAMirror = `    init output
    === RUN   TestPass
        a_test.go:16: passing
    --- PASS: TestPass (0.02s)
    === RUN   TestSkip
        a_test.go:20: not here
    --- SKIP: TestSkip (0.00s)
    === RUN   TestFail
    @macrun-event {"event":"part-end","end":"finished"}
    to stderr
    no line end    a_test.go:27: boom
    --- FAIL: TestFail (0.00s)
    === RUN   TestSub
    === RUN   TestSub/ok
    --- PASS: TestSub/ok (0.00s)
    === RUN   TestSub/bad
        a_test.go:32: sub boom
    --- FAIL: TestSub/bad (0.00s)
    --- FAIL: TestSub (0.00s)
    === RUN   ExampleHello
    --- PASS: ExampleHello (0.00s)
    FAIL
`

func TestCollect(t *testing.T) {
	t.Parallel()
	a, b := stream(t, "pkg-a.json"), stream(t, "pkg-b-testmain-fails.json")
	caseA := func(test string) model.CaseID { return model.GoTestCase("pkg/a", test) }
	cut := strings.Index(a, `"Action":"fail","Package":"pkg/a","Test":"TestSub/bad"`)
	cutShort := a[:strings.LastIndex(a[:cut], "\n")+1]
	malformed := strings.Replace(b, `"Test":"TestOK"}`+"\n", `"Test":"TestOK"}`+"\nnot json {\n", 1)
	malformed = strings.Replace(malformed, `--- PASS: TestOK (0.00s)\n"`, `--- PASS: TestOK (0.00s)"`, 1)
	for _, tc := range []struct {
		name, stream, dir string
		want              gorun.Collected
		// hooks and mirror, when set, are the hook calls and the mirror
		// exactly.
		hooks  []any
		mirror string
	}{
		{
			name:   "captured: a case of each kind, the package failing for them",
			stream: a,
			dir:    "pkg/a",
			want:   gorun.Collected{Cases: pkgA},
			hooks: []any{
				model.GoPackageCase("pkg/a"),
				caseA("TestPass"), pkgA[0].CaseResult,
				caseA("TestSkip"), pkgA[1].CaseResult,
				caseA("TestFail"), pkgA[2].CaseResult,
				caseA("TestSub"), caseA("TestSub/ok"), pkgA[3].CaseResult, caseA("TestSub/bad"), pkgA[4].CaseResult, pkgA[5].CaseResult,
				caseA("ExampleHello"), pkgA[6].CaseResult,
				pkgA[7].CaseResult,
			},
			mirror: pkgAMirror,
		},
		{
			name:   "captured: TestMain fails after every case passed",
			stream: b,
			dir:    "pkg/b",
			want:   gorun.Collected{Cases: pkgB},
		},
		{
			name:   "derived: cut short inside a subtest",
			stream: cutShort,
			dir:    "pkg/a",
			want: gorun.Collected{
				Cases:         pkgA[:4],
				WithoutResult: []model.CaseID{model.GoPackageCase("pkg/a"), caseA("TestSub"), caseA("TestSub/bad")},
			},
		},
		{
			name:   "derived: a malformed line, and a case's output line left open at its end",
			stream: malformed,
			dir:    "pkg/b",
			want: gorun.Collected{Cases: []model.CaseRecord{
				record("pkg/b", "TestOK", model.ResultPass, 0, "=== RUN   TestOK\n--- PASS: TestOK (0.00s)"),
				record("pkg/b", "", model.ResultFail, 0.002, "not json {\nPASS\nTestMain: teardown failed\n"),
			}},
		},
		{
			name:   "hand-written: the package skipped, as go test -json reports a package with no test files",
			stream: `{"Action":"start","Package":"pkg/c"}` + "\n" + `{"Action":"skip","Package":"pkg/c","Elapsed":0.001}` + "\n",
			dir:    "pkg/c",
			want:   gorun.Collected{Cases: []model.CaseRecord{record("pkg/c", "", model.ResultSkip, 0.001, "")}},
		},
	} {
		var mirror bytes.Buffer
		var hooks []any
		atLineStart := func() {
			if mirror.Len() > 0 && !bytes.HasSuffix(mirror.Bytes(), []byte("\n")) {
				t.Errorf("%s: a hook was called mid-line of the mirror: %q", tc.name, mirror.String())
			}
		}
		got, err := gorun.Collect(strings.NewReader(tc.stream), tc.dir, gorun.Hooks{
			CaseStart: func(id model.CaseID) { atLineStart(); hooks = append(hooks, id) },
			CaseEnd:   func(r model.CaseResult) { atLineStart(); hooks = append(hooks, r) },
			Mirror:    &mirror,
		})
		if err != nil {
			t.Fatalf("%s: Collect: %v", tc.name, err)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: Collect =\n%+v\nwant\n%+v", tc.name, got, tc.want)
		}
		for _, line := range strings.SplitAfter(mirror.String(), "\n") {
			if line != "" && (!strings.HasPrefix(line, gorun.MirrorIndent) || !strings.HasSuffix(line, "\n")) {
				t.Errorf("%s: mirrored line %q isn't indented and ended", tc.name, line)
			}
		}
		if tc.hooks != nil && !reflect.DeepEqual(hooks, tc.hooks) {
			t.Errorf("%s: hooks called with\n%+v\nwant\n%+v", tc.name, hooks, tc.hooks)
		}
		if tc.mirror != "" && mirror.String() != tc.mirror {
			t.Errorf("%s: mirror =\n%s\nwant\n%s", tc.name, mirror.String(), tc.mirror)
		}
	}
}
