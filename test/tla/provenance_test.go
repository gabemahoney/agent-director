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

// pinLines returns the sha256sum-format pin lines of a PROVENANCE.txt text as
// path -> SHA-256; a path pinned twice is an error.
func pinLines(t *testing.T, prov string) map[string]string {
	t.Helper()
	pins := map[string]string{}
	for _, m := range regexp.MustCompile(`(?m)^([0-9a-f]{64})  ([^ \n]+)$`).FindAllStringSubmatch(prov, -1) {
		if _, dup := pins[m[2]]; dup {
			t.Errorf("%s is pinned twice", m[2])
		}
		pins[m[2]] = m[1]
	}
	return pins
}

// TestProvenancePinsEveryVendoredFile checks every pin line of PROVENANCE.txt
// against the file's SHA-256, and that only the repo's own files are unpinned:
// three named files plus the unpinned run sets (launch/, subagent/). The b.3ie
// model (settle/) and the b.36h model (hold36h/) each have their own pin list.
func TestProvenancePinsEveryVendoredFile(t *testing.T) {
	tree := filepath.Join(repoRoot(t), "spec", "tla")
	prov := readFile(t, filepath.Join(tree, "PROVENANCE.txt"))
	pins := pinLines(t, prov)
	if len(pins) != 100 {
		t.Errorf("%d pin lines, want 100", len(pins))
	}
	var unpinned []string
	err := filepath.WalkDir(tree, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(tree, p)
		if _, ok := pins[rel]; !ok && !strings.HasPrefix(rel, "launch/") && !strings.HasPrefix(rel, "subagent/") && !strings.HasPrefix(rel, "settle/") && !strings.HasPrefix(rel, "hold36h/") {
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

// TestSettleProvenance: settle/PROVENANCE.txt pins every other file under
// settle/ with its SHA-256, and each pin names one of those files.
func TestSettleProvenance(t *testing.T) {
	checkOwnPins(t, "settle")
}

// TestHold36hProvenance: hold36h/PROVENANCE.txt pins every other file under
// hold36h/ with its SHA-256, and each pin names one of those files.
func TestHold36hProvenance(t *testing.T) {
	checkOwnPins(t, "hold36h")
}

// checkOwnPins checks that spec/tla/<dir>/PROVENANCE.txt pins every other
// file under dir with its SHA-256, and that each pin names one of those files.
func checkOwnPins(t *testing.T, dir string) {
	t.Helper()
	tree := filepath.Join(repoRoot(t), "spec", "tla", dir)
	pins := pinLines(t, readFile(t, filepath.Join(tree, "PROVENANCE.txt")))
	err := filepath.WalkDir(tree, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(tree, p)
		if rel == "PROVENANCE.txt" {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		want, ok := pins[rel]
		delete(pins, rel)
		if got := fmt.Sprintf("%x", sha256.Sum256(b)); !ok || got != want {
			t.Errorf("%s/%s: SHA-256 %s, pinned %q", dir, rel, got, want)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for rel := range pins {
		t.Errorf("%s/PROVENANCE.txt pins %s, which is not a vendored file under %s/", dir, rel, dir)
	}
}

// TestRunSets: for each unpinned run set (launch/, subagent/) its cfg/ holds
// exactly the cfgs suite.tsv names, and each row has the eight columns ci/run_ci.sh reads.
func TestRunSets(t *testing.T) {
	tree := filepath.Join(repoRoot(t), "spec", "tla")
	for _, tc := range []struct {
		dir, spec, knob string
		off             map[string]bool // cfgs with the knob off
	}{
		{"launch", "Phase4Split", "LaunchObs", nil},
		{"subagent", "Phase5Hook", "Sub", map[string]bool{"sx_eq_Sns": true}},
	} {
		t.Run(tc.dir, func(t *testing.T) {
			readme := "spec/tla/" + tc.dir + "/README.md"
			knobOn := regexp.MustCompile(`(?m)^  ` + tc.knob + ` <- KnobOn$`)
			knobAny := regexp.MustCompile(`(?m)^\s*` + tc.knob + ` <-`)
			var named []string
			for _, line := range strings.Split(readFile(t, filepath.Join(tree, tc.dir, "suite.tsv")), "\n") {
				if line == "" || strings.HasPrefix(line, "#") {
					continue
				}
				f := strings.Split(line, "\t")
				if len(f) != 8 || f[1] != tc.spec || (f[4] != "pass" && f[4] != "violation") || (f[4] == "pass") != (f[7] == "-") {
					t.Errorf("%s/suite.tsv: bad row %q", tc.dir, line)
					continue
				}
				named = append(named, f[2]+".cfg")
				cfg := readFile(t, filepath.Join(tree, tc.dir, "cfg", f[2]+".cfg"))
				if tc.off[f[2]] && knobAny.MatchString(cfg) {
					t.Errorf("%s/cfg/%s.cfg sets %s; this knobs-off cfg must leave it unset", tc.dir, f[2], tc.knob)
				}
				if !tc.off[f[2]] && !knobOn.MatchString(cfg) {
					t.Errorf("%s/cfg/%s.cfg does not turn %s on", tc.dir, f[2], tc.knob)
				}
				var comment []string
				for _, l := range strings.Split(cfg, "\n") {
					if strings.HasPrefix(l, `\*`) {
						comment = append(comment, l)
					}
				}
				if c := strings.Join(comment, "\n"); !strings.Contains(c, readme) || strings.Contains(c, "tickets/") {
					t.Errorf("%s/cfg/%s.cfg: comment must name %s and no tickets/ path:\n%s", tc.dir, f[2], readme, c)
				}
			}
			entries, err := os.ReadDir(filepath.Join(tree, tc.dir, "cfg"))
			if err != nil {
				t.Fatal(err)
			}
			var have []string
			for _, e := range entries {
				have = append(have, e.Name())
			}
			sort.Strings(named)
			if len(named) == 0 || !reflect.DeepEqual(have, named) {
				t.Errorf("%s/cfg holds %v; want exactly the %s/suite.tsv names %v", tc.dir, have, tc.dir, named)
			}
		})
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
