package setup

import (
	"reflect"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/tools/macrun/internal/model"
	"github.com/gabemahoney/agent-director/tools/macrun/internal/treefix"
)

func TestReadExpectedFailures(t *testing.T) {
	t.Parallel()
	root := treefix.Write(t, map[string]string{ExpectedFailuresFile: strings.Join([]string{
		"go:test/install-sh:TestAgentCodexOnly\tb.2pk\tfails on darwin",
		"go:test/install-sh\tb.2pk\tpackage result",
		"go:pkg/api:TestSpawn/relay_mode/off\tb.x9\tsubtest",
		"testplan:harness-smoke:t2.75s.ab.cd\tb.12\ttestplan case",
		"go:pkg/a:TestA\tb.2pk",
		"pkg/a:TestA\tb.2pk\tnote",
		"go:pkg/a:TestA\tb.2PK\tnote",
		"go:pkg/a:TestA\tb.2pk\t  ",
		"go:./a\tbug-1\t",
	}, "\n")})
	marks, errs, err := ReadExpectedFailures(root)
	if err != nil {
		t.Fatal(err)
	}
	// None of these cases exists in root: an unmatched mark is the verdict's to report.
	wantMarks := []Mark{
		{Case: "go:test/install-sh:TestAgentCodexOnly", Bug: "b.2pk", Note: "fails on darwin", Line: 1},
		{Case: "go:test/install-sh", Bug: "b.2pk", Note: "package result", Line: 2},
		{Case: "go:pkg/api:TestSpawn/relay_mode/off", Bug: "b.x9", Note: "subtest", Line: 3},
		{Case: "testplan:harness-smoke:t2.75s.ab.cd", Bug: "b.12", Note: "testplan case", Line: 4},
	}
	if !reflect.DeepEqual(marks, wantMarks) {
		t.Errorf("marks = %+v; want %+v", marks, wantMarks)
	}
	const forms = "; use go:<package dir>:<test name>, go:<package dir> or testplan:<slug>:<t2 id>"
	at := func(line int, message string) model.SetupError {
		return model.SetupError{File: "test/mac/expected-failures.txt", Line: line, Message: message}
	}
	wantErrs := []model.SetupError{
		at(5, `has 2 fields; expected <case ID>\t<Bug ID>\t<note>`),
		at(6, `case ID "pkg/a:TestA" has no go: or testplan: prefix`+forms),
		at(7, `Bug ID "b.2PK" is not of the form b.<id> (lower-case letters and digits)`),
		at(8, `note is empty; say why the case fails`),
		at(9, `case ID "go:./a": package dir is not a clean relative path`+forms),
		at(9, `Bug ID "bug-1" is not of the form b.<id> (lower-case letters and digits)`),
		at(9, `note is empty; say why the case fails`),
	}
	if !reflect.DeepEqual(errs, wantErrs) {
		t.Errorf("errors:\n%s\nwant:\n%s", errorLines(errs), errorLines(wantErrs))
	}
}
