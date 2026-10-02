package tla_test

import (
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// TestProvenancePinsEveryVendoredFile checks every pin line of PROVENANCE.txt
// against the file's SHA-256, and that only the repo's own files are unpinned.
func TestProvenancePinsEveryVendoredFile(t *testing.T) {
	tree := filepath.Join(repoRoot(t), "spec", "tla")
	prov := readFile(t, filepath.Join(tree, "PROVENANCE.txt"))
	pins := map[string]string{}
	for _, m := range regexp.MustCompile(`(?m)^([0-9a-f]{64})  ([^ \n]+)$`).FindAllStringSubmatch(prov, -1) {
		if _, dup := pins[m[2]]; dup {
			t.Errorf("%s is pinned twice", m[2])
		}
		pins[m[2]] = m[1]
	}
	if len(pins) != 100 {
		t.Errorf("%d pin lines, want 100", len(pins))
	}
	var unpinned []string
	err := filepath.WalkDir(tree, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(tree, p)
		if _, ok := pins[rel]; !ok {
			unpinned = append(unpinned, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(unpinned)
	if want := []string{".gitattributes", "PROVENANCE.txt", "ci/run_ci.sh"}; !reflect.DeepEqual(unpinned, want) {
		t.Errorf("unpinned files = %v, want %v", unpinned, want)
	}
	for rel, want := range pins {
		b, err := os.ReadFile(filepath.Join(tree, rel))
		if err != nil {
			t.Errorf("pinned file: %v", err)
			continue
		}
		if got := fmt.Sprintf("%x", sha256.Sum256(b)); got != want {
			t.Errorf("%s: SHA-256 %s, pinned %s", rel, got, want)
		}
	}

	m := regexp.MustCompile(`(?m)^  jar\.bytes +(\d+)$`).FindStringSubmatch(prov)
	info, err := os.Stat(filepath.Join(tree, "tla2tools.jar"))
	if m == nil || err != nil || strconv.FormatInt(info.Size(), 10) != m[1] {
		t.Errorf("jar.bytes line %v does not match the jar (%v)", m, err)
	}

	var named, vendored []string
	for _, rw := range shipped(t) {
		named = append(named, rw.cfg+".cfg")
	}
	entries, err := os.ReadDir(filepath.Join(tree, "ci", "cfg"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		vendored = append(vendored, e.Name())
	}
	sort.Strings(named)
	if !reflect.DeepEqual(vendored, named) {
		t.Errorf("ci/cfg holds %d cfgs; want exactly the %d suite.tsv names", len(vendored), len(named))
	}
}

// TestRunnerNeverStartsChecker: the runner text names no java, docker or TLC
// command (the file names Dockerfile and tlcjob.sh are fine).
func TestRunnerNeverStartsChecker(t *testing.T) {
	text := readFile(t, filepath.Join(repoRoot(t), "spec", "tla", "ci", "run_ci.sh"))
	if m := regexp.MustCompile(`(?im)^.*\b(java|docker|podman|tlc2?)\b.*$`).FindAllString(text, -1); m != nil {
		t.Errorf("run_ci.sh names a checker or container command:\n%s", strings.Join(m, "\n"))
	}
}

// makeRun runs make in the repo with the caller's make and TLA_* state scrubbed.
func makeRun(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("make", append([]string{"--no-print-directory"}, args...)...)
	cmd.Dir = repoRoot(t)
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "TLA_") && !strings.HasPrefix(kv, "MAKE") && !strings.HasPrefix(kv, "MFLAGS=") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	out, err := cmd.Output()
	return string(out), err
}

// TestMakeTlaTargets: tla and tla-print only run the runner, no other target
// reaches it, and tla-print plans without contacting the scheduler.
func TestMakeTlaTargets(t *testing.T) {
	if _, err := exec.LookPath("make"); err != nil {
		t.Skip("make not on PATH")
	}
	for target, want := range map[string]string{"tla": "bash spec/tla/ci/run_ci.sh", "tla-print": "bash spec/tla/ci/run_ci.sh --print"} {
		if out, err := makeRun(t, "-n", target); err != nil || strings.TrimSpace(out) != want {
			t.Errorf("make -n %s = %q (%v), want only %q", target, out, err, want)
		}
	}
	for _, target := range []string{"all", "test", "test-sandbox"} {
		out, err := makeRun(t, "-n", target)
		if err != nil {
			t.Errorf("make -n %s: %v", target, err)
		}
		if regexp.MustCompile(`\btla\b|spec/tla|run_ci`).MatchString(out) {
			t.Errorf("make -n %s reaches the tla runner:\n%s", target, out)
		}
	}
	for _, tc := range []struct {
		vars []string
		runs string
	}{
		{nil, "  runs:       71\n"},
		{[]string{"TLA_TIER=full"}, "  runs:       92\n"},
		{[]string{"TLA_GROUPS=g6 g1"}, "  runs:       54\n"},
	} {
		r := newRig(t)
		js := filepath.Join(r.bin, "jobsched")
		out, err := makeRun(t, append([]string{"tla-print", "TLA_JOBSCHED=" + js, "FAKE_DIR=" + r.fake}, tc.vars...)...)
		if err != nil || !strings.Contains(out, tc.runs) || !strings.Contains(out, "TLA_JOBSCHED: "+js+" (found)") {
			t.Errorf("make tla-print %v (%v):\n%s\nwant %q", tc.vars, err, out, tc.runs)
		}
		if calls := r.calls(t); calls != nil {
			t.Errorf("make tla-print %v called the scheduler: %v", tc.vars, calls)
		}
	}
}
