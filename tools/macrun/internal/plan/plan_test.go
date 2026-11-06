package plan_test

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/tools/macrun/internal/model"
	"github.com/gabemahoney/agent-director/tools/macrun/internal/plan"
	"github.com/gabemahoney/agent-director/tools/macrun/internal/treefix"
)

// testFile returns a test file of package pkg holding one Test function per
// name, and an Example with output for each Example name.
func testFile(pkg string, names ...string) string {
	var b strings.Builder
	b.WriteString("package " + pkg + "\n\nimport \"testing\"\n\nvar _ testing.T\n")
	for _, name := range names {
		if strings.HasPrefix(name, "Example") {
			b.WriteString("\nfunc " + name + "() {\n\t// Output: ok\n}\n")
		} else {
			b.WriteString("\nfunc " + name + "(t *testing.T) {}\n")
		}
	}
	return b.String()
}

func lines(l ...string) string { return strings.Join(l, "\n") + "\n" }

var partial = model.Coverage{Go: model.CoveragePartial, Testplan: model.CoveragePartial}

func TestComputeNoMacTests(t *testing.T) {
	t.Parallel()
	want := model.Plan{
		SchemaVersion: model.PlanSchemaVersion,
		Status:        model.StatusNoMacTests,
		Reason:        "The tree has no test/mac/parts.txt, so it doesn't have the Mac Tests and no part is planned.",
		Coverage:      partial,
	}
	for _, tc := range []struct {
		name  string
		files map[string]string
	}{
		{"empty tree", map[string]string{}},
		// A read of expected-failures.txt would fault on the directory.
		{"unread expected failures", map[string]string{"test/mac/expected-failures.txt/": ""}},
		{"parts.txt not a regular file", map[string]string{"test/mac/parts.txt/": ""}},
		{"test/mac a regular file", map[string]string{"test/mac": "not a dir"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := plan.Compute(treefix.Write(t, tc.files))
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("Compute = %+v; want %+v", got, want)
			}
		})
	}
}

func TestComputeInvalidSetup(t *testing.T) {
	t.Parallel()
	root := treefix.Write(t, map[string]string{
		"pkg/a/a_test.go":             testFile("a", "TestA1", "TestA2"),
		"pkg/a/a_linux_test.go":       testFile("a", "TestLinuxOnly"),
		"pkg/empty/empty.go":          "package empty\n",
		"pkg/lin/l_test.go":           testFile("lin", "TestL"),
		"pkg/lin/l_linux_test.go":     testFile("lin", "TestLinuxL"),
		"pkg/linonly/l_linux_test.go": testFile("linonly", "TestLinuxPkg"),
		"test/mac/parts.txt": lines(
			"one\tgo\tpkg/a:^TestA1$ pkg/a:TestA1",
			"two\tgo\tpkg/a:^TestA",
			"bad\ttestplan\tsmoke",
			"three\tgo\tpkg/missing",
			"four\tgo\tpkg/empty",
			"five\tgo\tpkg/a:^TestNone$",
			"# linux-only tests are refused, and checked for two parts too",
			"six\tgo\tpkg/a:Linux",
			"seven\tgo\tpkg/a:LinuxOnly",
			"eight\tgo\tgo.mod/x",
			"nine\tgo\tpkg/lin",
			"ten\tgo\tpkg/linonly",
		),
		"test/mac/expected-failures.txt": lines(
			"go:pkg/a:TestA1\tb.2pk\tfails on darwin",
			"go:pkg/a:TestA1\tB.x\tfails on darwin",
		),
	})
	got, err := plan.Compute(root)
	if err != nil {
		t.Fatal(err)
	}
	parts := func(line int, message string) model.SetupError {
		return model.SetupError{File: "test/mac/parts.txt", Line: line, Message: message}
	}
	linuxOnly := func(line int, selector, name, dir string) model.SetupError {
		return parts(line, `selector "`+selector+`" selects `+name+` in `+dir+
			`, which only the linux build compiles, so it would vanish from a Mac Run; make it build for darwin or leave it out of the selection`)
	}
	want := model.Plan{
		SchemaVersion: model.PlanSchemaVersion,
		Status:        model.StatusInvalidSetup,
		Reason:        "The Mac Run setup under test/mac/ is invalid, so no part is planned; fix every listed error.",
		Errors: []model.SetupError{
			parts(2, `TestA1 in pkg/a is selected by part "one" (line 1) and part "two" (line 2); select each test in one part`),
			parts(3, `suite "testplan" isn't supported yet; use go`),
			parts(4, `selector "pkg/missing": pkg/missing is not a Go package of the tested source in its linux or darwin build`),
			parts(5, `selector "pkg/empty" selects no top-level test or Example in the linux or darwin build of pkg/empty`),
			parts(6, `selector "pkg/a:^TestNone$" selects no top-level test or Example in the linux or darwin build of pkg/a`),
			linuxOnly(8, "pkg/a:Linux", "TestLinuxOnly", "pkg/a"),
			linuxOnly(9, "pkg/a:LinuxOnly", "TestLinuxOnly", "pkg/a"),
			parts(9, `TestLinuxOnly in pkg/a is selected by part "six" (line 8) and part "seven" (line 9); select each test in one part`),
			parts(10, `selector "go.mod/x": go.mod/x is not a Go package of the tested source in its linux or darwin build`),
			linuxOnly(11, "pkg/lin", "TestLinuxL", "pkg/lin"),
			linuxOnly(12, "pkg/linonly", "TestLinuxPkg", "pkg/linonly"),
			{File: "test/mac/expected-failures.txt", Line: 2, Message: `Bug ID "B.x" is not of the form b.<id> (lower-case letters and digits)`},
		},
		Coverage: partial,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Compute =\n%+v\nwant\n%+v", got, want)
	}
}

func TestComputePlanned(t *testing.T) {
	t.Parallel()
	root := treefix.Write(t, map[string]string{
		"pkg/split/s_test.go":        testFile("split", "TestS1", "TestS2", "TestS3", "ExampleS"),
		"pkg/dup/d_test.go":          testFile("dup", "TestA", "TestB", "TestC"),
		"pkg/whole/w_test.go":        testFile("whole", "TestW", "ExampleW"),
		"pkg/whole/x_test.go":        testFile("whole_test", "TestWX"),
		"pkg/mixed/m_test.go":        testFile("mixed", "TestM"),
		"pkg/mixed/m_darwin_test.go": testFile("mixed", "TestDarwinOnly"),
		"pkg/mixed/m_linux_test.go":  testFile("mixed", "TestLinuxOnly"),
		"test/mac/parts.txt": lines(
			"alpha\tgo\tpkg/split:^TestS1$ pkg/dup:^TestA$ pkg/dup:^TestB$",
			"beta\tgo\tpkg/split:S2|S3|ExampleS pkg/whole pkg/whole:^TestW$",
			"gamma\tgo\tpkg/mixed:^Test(M|DarwinOnly)$",
		),
		"test/mac/expected-failures.txt": lines("go:pkg/dup:TestA\tb.2pk\tfails on darwin"),
	})
	want := model.Plan{
		SchemaVersion: model.PlanSchemaVersion,
		Status:        model.StatusPlanned,
		Coverage:      partial,
		Parts: []model.Part{
			{
				Name: "alpha", Suite: model.SuiteGo, Selection: "pkg/split:^TestS1$ pkg/dup:^TestA$ pkg/dup:^TestB$",
				Packages: []model.Package{
					{Dir: "pkg/dup", Tests: []string{"TestA", "TestB"}},
					{Dir: "pkg/split", Tests: []string{"TestS1"}},
				},
				ExpectedCases: []model.CaseID{
					"go:pkg/dup", "go:pkg/dup:TestA", "go:pkg/dup:TestB", "go:pkg/split", "go:pkg/split:TestS1",
				},
			},
			{
				Name: "beta", Suite: model.SuiteGo, Selection: "pkg/split:S2|S3|ExampleS pkg/whole pkg/whole:^TestW$",
				Packages: []model.Package{
					{Dir: "pkg/split", Tests: []string{"ExampleS", "TestS2", "TestS3"}},
					{Dir: "pkg/whole", Tests: []string{"ExampleW", "TestW", "TestWX"}},
				},
				ExpectedCases: []model.CaseID{
					"go:pkg/split", "go:pkg/split:ExampleS", "go:pkg/split:TestS2", "go:pkg/split:TestS3",
					"go:pkg/whole", "go:pkg/whole:ExampleW", "go:pkg/whole:TestW", "go:pkg/whole:TestWX",
				},
			},
			{
				Name: "gamma", Suite: model.SuiteGo, Selection: "pkg/mixed:^Test(M|DarwinOnly)$",
				Packages: []model.Package{
					{Dir: "pkg/mixed", Tests: []string{"TestDarwinOnly", "TestM"}},
				},
				ExpectedCases: []model.CaseID{"go:pkg/mixed", "go:pkg/mixed:TestDarwinOnly", "go:pkg/mixed:TestM"},
			},
		},
	}
	first, err := plan.Compute(root)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, want) {
		t.Errorf("Compute =\n%+v\nwant\n%+v", first, want)
	}
	second, err := plan.Compute(root)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(first)
	b, _ := json.Marshal(second)
	if !bytes.Equal(a, b) {
		t.Errorf("two plans of one tree differ:\n%s\n%s", a, b)
	}
}
