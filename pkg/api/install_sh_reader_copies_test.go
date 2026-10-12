package api

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The ad_store_db_path block lives twice, once in install.sh and once, verbatim,
// in uninstall.sh (SR-11.4): the marked lines run from the open marker to the
// close marker inclusive. The behavioural guards (TestInstallShLaunchPathMatchesGo,
// TestInstallShUninstallReaderMatchesInstall) run both copies over a matrix;
// this one makes a drift outside the matrix (a comment, a branch no case
// reaches) fail too, naming the first line that differs.
const (
	readerBlockOpen  = "# >>> ad_store_db_path (b.2io) >>>"
	readerBlockClose = "# <<< ad_store_db_path (b.2io) <<<"
)

// markedBlock returns the lines of text from the line equal to open through the
// line equal to close, and the 1-based line number of the open marker.
func markedBlock(text, open, closeMarker string) (lines []string, first int, err error) {
	all := strings.Split(text, "\n")
	start, end := -1, -1
	for i, l := range all {
		switch {
		case l == open:
			if start >= 0 {
				return nil, 0, fmt.Errorf("the open marker %q is on lines %d and %d; want one", open, start+1, i+1)
			}
			start = i
		case l == closeMarker:
			if end >= 0 {
				return nil, 0, fmt.Errorf("the close marker %q is on lines %d and %d; want one", closeMarker, end+1, i+1)
			}
			end = i
		}
	}
	switch {
	case start < 0:
		return nil, 0, fmt.Errorf("no line is the open marker %q", open)
	case end < 0:
		return nil, 0, fmt.Errorf("no line is the close marker %q", closeMarker)
	case end < start:
		return nil, 0, fmt.Errorf("the close marker (line %d) is before the open marker (line %d)", end+1, start+1)
	}
	return all[start : end+1], start + 1, nil
}

// firstDifference describes the first line at which a and b differ ("" when they
// are the same), as "block line N: <a's> != <b's>".
func firstDifference(a, b []string) string {
	for i := 0; i < len(a) || i < len(b); i++ {
		var x, y string
		var hasX, hasY bool
		if i < len(a) {
			x, hasX = a[i], true
		}
		if i < len(b) {
			y, hasY = b[i], true
		}
		if hasX && hasY && x == y {
			continue
		}
		switch {
		case !hasX:
			return fmt.Sprintf("block line %d: the first copy ends; the second has %q", i+1, y)
		case !hasY:
			return fmt.Sprintf("block line %d: the second copy ends; the first has %q", i+1, x)
		}
		return fmt.Sprintf("block line %d: %q != %q", i+1, x, y)
	}
	return ""
}

// TestInstallShReaderBlockIsByteIdenticalInUninstall: the marked ad_store_db_path
// block is the same bytes in install.sh and uninstall.sh.
func TestInstallShReaderBlockIsByteIdenticalInUninstall(t *testing.T) {
	t.Parallel()
	skills := filepath.Join("..", "..", "skills", "install-agent-director")
	var blocks [2][]string
	var starts [2]int
	for i, name := range []string{"install.sh", "uninstall.sh"} {
		b, err := os.ReadFile(filepath.Join(skills, name))
		if err != nil {
			t.Fatal(err)
		}
		blocks[i], starts[i], err = markedBlock(string(b), readerBlockOpen, readerBlockClose)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if d := firstDifference(blocks[0], blocks[1]); d != "" {
		t.Errorf("uninstall.sh's copy of the ad_store_db_path block differs from install.sh's (blocks start at install.sh:%d, uninstall.sh:%d): %s",
			starts[0], starts[1], d)
	}
}

// TestInstallShReaderBlockGuardCatchesDrift: the guard's own parts find a block,
// refuse a missing or doubled marker, and name the first differing line.
func TestInstallShReaderBlockGuardCatchesDrift(t *testing.T) {
	t.Parallel()
	text := "head\n" + readerBlockOpen + "\none\ntwo\n" + readerBlockClose + "\ntail\n"
	got, first, err := markedBlock(text, readerBlockOpen, readerBlockClose)
	if err != nil || first != 2 || strings.Join(got, "|") != readerBlockOpen+"|one|two|"+readerBlockClose {
		t.Fatalf("markedBlock = %q, %d, %v", got, first, err)
	}
	for name, bad := range map[string]string{
		"no-open":   "one\n" + readerBlockClose + "\n",
		"no-close":  readerBlockOpen + "\none\n",
		"reversed":  readerBlockClose + "\n" + readerBlockOpen + "\n",
		"two-opens": readerBlockOpen + "\n" + readerBlockOpen + "\n" + readerBlockClose + "\n",
	} {
		if _, _, err := markedBlock(bad, readerBlockOpen, readerBlockClose); err == nil {
			t.Errorf("%s: markedBlock accepted %q", name, bad)
		}
	}
	for name, tc := range map[string]struct {
		a, b []string
		want string
	}{
		"same":         {[]string{"a", "b"}, []string{"a", "b"}, ""},
		"middle":       {[]string{"a", "b", "c"}, []string{"a", "x", "c"}, `block line 2: "b" != "x"`},
		"whitespace":   {[]string{"a "}, []string{"a"}, `block line 1: "a " != "a"`},
		"first-ends":   {[]string{"a"}, []string{"a", "b"}, `block line 2: the first copy ends; the second has "b"`},
		"second-ends":  {[]string{"a", "b"}, []string{"a"}, `block line 2: the second copy ends; the first has "b"`},
		"earliest-one": {[]string{"x", "b", "c"}, []string{"y", "b", "d"}, `block line 1: "x" != "y"`},
	} {
		if got := firstDifference(tc.a, tc.b); got != tc.want {
			t.Errorf("%s: firstDifference = %q, want %q", name, got, tc.want)
		}
	}
}
