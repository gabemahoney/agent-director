package golist_test

import (
	"go/build"
	"reflect"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/tools/macrun/internal/golist"
	"github.com/gabemahoney/agent-director/tools/macrun/internal/treefix"
)

// testFile returns a test file of package p holding a Test function named
// name, under the build constraint constraint when it isn't empty.
func testFile(constraint, name string) string {
	header := ""
	if constraint != "" {
		header = "//go:build " + constraint + "\n\n"
	}
	return header + "package p\n\nimport \"testing\"\n\nfunc " + name + "(t *testing.T) {}\n"
}

func TestContexts(t *testing.T) {
	t.Parallel()
	if !golist.DarwinCgoEnabled {
		t.Error("DarwinCgoEnabled is false; a native go test on the Mac has cgo on")
	}
	for _, tc := range []struct {
		ctx          build.Context
		goos, goarch string
	}{
		{golist.DarwinContext(), "darwin", "arm64"},
		{golist.LinuxContext(), "linux", "amd64"},
	} {
		ctx := tc.ctx
		if ctx.GOOS != tc.goos || ctx.GOARCH != tc.goarch || !ctx.CgoEnabled {
			t.Errorf("context %s/%s cgo %t; want %s/%s cgo true", ctx.GOOS, ctx.GOARCH, ctx.CgoEnabled, tc.goos, tc.goarch)
		}
		if len(ctx.BuildTags) != 0 {
			t.Errorf("%s context BuildTags = %q; want none", tc.goos, ctx.BuildTags)
		}
		for _, tag := range ctx.ToolTags {
			if !strings.HasPrefix(tag, "goexperiment.") {
				t.Errorf("%s context has tool tag %q; want only goexperiment.* tags", tc.goos, tag)
			}
		}
		if !reflect.DeepEqual(ctx.ReleaseTags, build.Default.ReleaseTags) || ctx.GOROOT != build.Default.GOROOT {
			t.Errorf("%s context doesn't keep build.Default's GOROOT and release tags", tc.goos)
		}
	}
}

func TestListBuildConstraints(t *testing.T) {
	t.Parallel()
	root := treefix.Write(t, map[string]string{
		"p/all_test.go":       testFile("", "TestAll"),
		"p/f_darwin_test.go":  testFile("", "TestDarwinFile"),
		"p/f_linux_test.go":   testFile("", "TestLinuxFile"),
		"p/f_arm64_test.go":   testFile("", "TestArm64File"),
		"p/d_test.go":         testFile("darwin", "TestDarwinTag"),
		"p/l_test.go":         testFile("linux", "TestLinuxTag"),
		"p/u_test.go":         testFile("unix", "TestUnixTag"),
		"p/i_test.go":         testFile("ignore", "TestIgnored"),
		"p/c_test.go":         testFile("cgo", "TestCgoTag"),
		"p/ext_test.go":       "package p_test\n\nimport \"testing\"\n\nfunc TestExternal(t *testing.T) {}\n",
		"p/ext_linux_test.go": "package p_test\n\nimport \"testing\"\n\nfunc TestExternalLinux(t *testing.T) {}\n",
	})
	for _, tc := range []struct {
		name string
		ctx  build.Context
		want []string
	}{
		{"darwin", golist.DarwinContext(), []string{
			"TestAll", "TestArm64File", "TestCgoTag", "TestDarwinFile", "TestDarwinTag", "TestExternal", "TestUnixTag",
		}},
		{"linux", golist.LinuxContext(), []string{
			"TestAll", "TestCgoTag", "TestExternal", "TestExternalLinux", "TestLinuxFile", "TestLinuxTag", "TestUnixTag",
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := golist.List(tc.ctx, root, "p")
			if err != nil {
				t.Fatal(err)
			}
			if want := (golist.Listing{InBuild: true, Cases: tc.want}); !reflect.DeepEqual(got, want) {
				t.Errorf("List = %+v; want %+v", got, want)
			}
		})
	}
}

func TestListCases(t *testing.T) {
	t.Parallel()
	root := treefix.Write(t, map[string]string{
		"p/p.go": "package p\n",
		"p/in_test.go": `package p

import "testing"

type S struct{}

func Test(t *testing.T)               {}
func TestA(t *testing.T)              {}
func Test_x(t *testing.T)             {}
func TestShared(t *testing.T)         {}
func Testfoo(t *testing.T)            {}
func (S) TestMethod(t *testing.T)     {}
func TestMain(m *testing.M)           {}
func BenchmarkB(b *testing.B)         {}
func FuzzF(f *testing.F)              {}

func Example() {
	// Output: hi
}

func ExampleFoo_bar() {
	// Output:
}

func ExampleUnordered() {
	// Unordered output: a
	// b
}

func ExampleNoOutput() {}
`,
		"p/ext_test.go": `package p_test

import tt "testing"

func TestExternal(t *tt.T) {}
func TestShared(t *tt.T)   {}
`,
	})
	got, err := golist.List(golist.DarwinContext(), root, "p")
	if err != nil {
		t.Fatal(err)
	}
	want := golist.Listing{InBuild: true, Cases: []string{
		"Example", "ExampleFoo_bar", "ExampleUnordered", "Test", "TestA", "TestExternal", "TestShared", "Test_x",
	}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("List = %+v; want %+v", got, want)
	}
}

func TestListPackagePresence(t *testing.T) {
	t.Parallel()
	root := treefix.Write(t, map[string]string{
		"testonly/a_test.go":            testFile("", "TestOnly"),
		"nested/go.mod":                 "module example.com/nested\n",
		"nested/a_test.go":              testFile("", "TestNested"),
		"nested/sub/a_test.go":          testFile("", "TestNestedSub"),
		"otherplatform/a_linux.go":      "package p\n",
		"otherplatform/a_linux_test.go": testFile("", "TestLinux"),
		"empty/":                        "",
		"file.txt":                      "not a dir",
	})
	for _, tc := range []struct {
		dir  string
		want golist.Listing
	}{
		{"testonly", golist.Listing{InBuild: true, Cases: []string{"TestOnly"}}},
		{"missing", golist.Listing{}},
		{"nested", golist.Listing{}},
		{"nested/sub", golist.Listing{}},
		{"otherplatform", golist.Listing{}},
		{"empty", golist.Listing{}},
		{"file.txt/sub", golist.Listing{}},
	} {
		got, err := golist.List(golist.DarwinContext(), root, tc.dir)
		if err != nil {
			t.Errorf("List(%s) error: %v", tc.dir, err)
			continue
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("List(%s) = %+v; want %+v", tc.dir, got, tc.want)
		}
	}
}

func TestListParseError(t *testing.T) {
	t.Parallel()
	root := treefix.Write(t, map[string]string{
		"broken/b_test.go": "package broken\n\nimport \"testing\"\n\nfunc TestX(t *testing.T) { if }\n",
	})
	_, err := golist.List(golist.DarwinContext(), root, "broken")
	if want := "package broken: parse test file: broken/b_test.go:5:"; err == nil || !strings.HasPrefix(err.Error(), want) {
		t.Errorf("List error = %v; want one starting %q", err, want)
	}
}
