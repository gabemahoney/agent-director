package setup

import (
	"reflect"
	"testing"

	"github.com/gabemahoney/agent-director/tools/macrun/internal/treefix"
)

func TestReadLines(t *testing.T) {
	t.Parallel()
	root := treefix.Write(t, map[string]string{
		"test/mac/x.txt": "# comment\r\n  # indented\n\n \t \nname\tgo\tpkg/a\r\n\tlead\n#x\nlast",
	})
	got, err := readLines(root, "test/mac/x.txt")
	if err != nil {
		t.Fatal(err)
	}
	want := []line{
		{number: 5, fields: []string{"name", "go", "pkg/a"}},
		{number: 6, fields: []string{"", "lead"}},
		{number: 8, fields: []string{"last"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("readLines = %+v; want %+v", got, want)
	}
}

func TestReadLinesMissingAndUnreadable(t *testing.T) {
	t.Parallel()
	root := treefix.Write(t, map[string]string{
		"test/mac/dir.txt/": "",
		"test/file":         "not a dir",
	})
	for _, tc := range []struct {
		file    string
		wantErr string
	}{
		{file: "test/mac/missing.txt"},
		{file: "test/file/x.txt"},
		{file: "test/mac/dir.txt", wantErr: "read test/mac/dir.txt: is a directory"},
	} {
		got, err := readLines(root, tc.file)
		gotErr := ""
		if err != nil {
			gotErr = err.Error()
		}
		if got != nil || gotErr != tc.wantErr {
			t.Errorf("readLines(%s) = %+v, %q; want no entries and error %q", tc.file, got, gotErr, tc.wantErr)
		}
	}
}
