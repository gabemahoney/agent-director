package tla_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
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
// model (settle/), the b.36h model (hold36h/) and the b.ssz Codex models
// (codex/) each have their own pin list.
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
		if _, ok := pins[rel]; !ok && !strings.HasPrefix(rel, "launch/") && !strings.HasPrefix(rel, "subagent/") && !strings.HasPrefix(rel, "settle/") && !strings.HasPrefix(rel, "hold36h/") && !strings.HasPrefix(rel, "codex/") {
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

// TestCodexProvenance: codex/PROVENANCE.txt pins every other file under
// codex/ with its SHA-256, and each pin names one of those files.
func TestCodexProvenance(t *testing.T) {
	checkOwnPins(t, "codex")
}

// TestCodexJobArtifacts: only manifest-declared b.w21 scheduler contexts carry
// a canonical jar copy; every Codex job retains the canonical runner bytes.
func TestCodexJobArtifacts(t *testing.T) {
	tree := filepath.Join(repoRoot(t), "spec", "tla")
	codex := filepath.Join(tree, "codex")
	jar, err := os.ReadFile(filepath.Join(tree, "tla2tools.jar"))
	if err != nil {
		t.Fatal(err)
	}
	jarHash := fmt.Sprintf("%x", sha256.Sum256(jar))
	declaredJars := map[string]bool{}
	caseName := regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	for _, dir := range []string{"b.w21", "b.w21/fixed", "b.w21/fixed-r2"} {
		var manifest struct {
			Bug   string `json:"bug"`
			Cases []struct {
				Name    string            `json:"name"`
				Context string            `json:"context"`
				SHA256  map[string]string `json:"sha256"`
			} `json:"cases"`
		}
		if err := json.Unmarshal([]byte(readFile(t, filepath.Join(codex, dir, "manifest.json"))), &manifest); err != nil {
			t.Fatalf("%s/manifest.json: %v", dir, err)
		}
		if manifest.Bug != "b.w21" || len(manifest.Cases) == 0 {
			t.Fatalf("%s/manifest.json: want b.w21 with declared cases", dir)
		}
		for _, c := range manifest.Cases {
			if !caseName.MatchString(c.Name) || !fs.ValidPath(c.Context) || c.Context != "jobs/"+c.Name {
				t.Fatalf("%s/manifest.json: unsafe or unexpected context %q for case %q", dir, c.Context, c.Name)
			}
			rel := filepath.Join(dir, c.Context, "tla2tools.jar")
			if declaredJars[rel] {
				t.Fatalf("%s/manifest.json: duplicate context %q", dir, c.Context)
			}
			if c.SHA256["tla2tools.jar"] != jarHash {
				t.Errorf("%s/manifest.json: %s jar hash %q differs from canonical %s", dir, c.Name, c.SHA256["tla2tools.jar"], jarHash)
			}
			declaredJars[rel] = true
		}
	}
	lib, err := os.ReadFile(filepath.Join(tree, "jobs", "lib", "tlcjob.sh"))
	if err != nil {
		t.Fatal(err)
	}
	jobs := 0
	err = filepath.WalkDir(codex, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if strings.HasSuffix(d.Name(), ".jar") {
			rel, err := filepath.Rel(codex, p)
			if err != nil {
				return err
			}
			if !declaredJars[rel] {
				t.Errorf("%s: jar outside declared b.w21 scheduler contexts", p)
			} else {
				delete(declaredJars, rel)
				if !d.Type().IsRegular() {
					t.Errorf("%s: scheduler jar must be a regular file", p)
				} else {
					b, err := os.ReadFile(p)
					if err != nil {
						return err
					}
					if !bytes.Equal(b, jar) {
						t.Errorf("%s differs from spec/tla/tla2tools.jar", p)
					}
				}
			}
		}
		if d.Name() == "tlcjob.sh" {
			jobs++
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			if !bytes.Equal(b, lib) {
				t.Errorf("%s differs from spec/tla/jobs/lib/tlcjob.sh", p)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for rel := range declaredJars {
		t.Errorf("%s: declared scheduler context is missing its jar", rel)
	}
	if jobs == 0 {
		t.Error("no job directory under codex/")
	}
}

// TestW21DocCommandsAreRepoRelative (b.5jg): no b.w21 doc names the original
// checkout agent-director-w21-tla, and no command block names a machine-local
// absolute path, so every documented command runs from any clone.
func TestW21DocCommandsAreRepoRelative(t *testing.T) {
	root := repoRoot(t)
	local := regexp.MustCompile(`(^|[\s'"=:(])(/(home|Users|root|tmp|run|var|mnt|opt|srv)|~|\$\{?HOME\}?)/`)
	docs := 0
	for _, dir := range []string{"spec/tla/codex/b.w21", "test/b.w21"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(d.Name(), ".md") {
				return err
			}
			docs++
			rel, _ := filepath.Rel(root, p)
			inBlock := false
			for i, line := range strings.Split(readFile(t, p), "\n") {
				if strings.Contains(line, "agent-director-w21-tla") {
					t.Errorf("%s:%d: names the original checkout: %s", rel, i+1, line)
				}
				if fence := strings.TrimSpace(line); strings.HasPrefix(fence, "```") || strings.HasPrefix(fence, "~~~") {
					inBlock = !inBlock
					continue
				}
				if inBlock && local.MatchString(line) {
					t.Errorf("%s:%d: command names a machine-local path: %s", rel, i+1, line)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if docs < 5 {
		t.Errorf("found %d b.w21 docs, want at least 5", docs)
	}
}

// TestW21VerifierRunsFromThisCheckout (b.5jg): the phase-1 b.w21 verifier,
// run from this repo root as its README documents, passes in a checkout other
// than the one that recorded scheduler-commands.txt.
func TestW21VerifierRunsFromThisCheckout(t *testing.T) {
	needPython3(t)
	cmd := exec.Command("python3", "spec/tla/codex/b.w21/verifycontexts.py")
	cmd.Dir = repoRoot(t)
	out, err := cmd.CombinedOutput()
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	want := "STATIC-OK 9 immutable baseline contexts, 41236958 total context bytes; " +
		"nine archived scheduler receipts verified; no model or scheduler execution."
	if err != nil || lines[len(lines)-1] != want {
		t.Errorf("python3 spec/tla/codex/b.w21/verifycontexts.py (%v):\n%s\nwant final line %q", err, out, want)
	}
}

// w21ContextChecks prints each archived-context case the phase-1 or fixed-r2
// verifier accepts or rejects wrongly; all 7 fixed-r2 receipts go through verify_receipts.
const w21ContextChecks = `
import copy, importlib.util, json
def load(path):
    spec = importlib.util.spec_from_file_location(path, path)
    m = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(m)
    return m
v = load("spec/tla/codex/b.w21/verifycontexts.py")
r2 = load("spec/tla/codex/b.w21/fixed-r2/verifycontexts.py")
ctx = v.HERE / "jobs" / "w21_initial_red"
other = "/elsewhere/clone/spec/tla/codex/b.w21/"
v.check_archived_context(other + "jobs/w21_initial_red", ctx)
if v.command_context("submit --context '/a/b' --memory 4g") != "/a/b":
    print("single --context not returned")
archive = json.loads((r2.HERE / "model-r2-receipts.json").read_text())
manifest = json.loads((r2.HERE / "manifest.json").read_text())
r2.verify_receipts(manifest, json.loads(archive["baseline_manifest_text"]), v)
first, r2ctx = "j20261015000702-bf3dab", r2.HERE / "jobs" / "w21_r2_initial"
def metadata(job, context_dir):
    record = copy.deepcopy(archive["logs"][job]["metadata"])
    record["values"]["context_dir"] = context_dir
    return lambda: r2.verify_metadata(record, job, "w21_r2_initial", r2ctx, v)
for label, call in [
    ("wrong case", lambda: v.check_archived_context(other + "jobs/w21_relaunch_red", ctx)),
    ("wrong round", lambda: v.check_archived_context(other + "fixed/jobs/w21_initial_red", ctx)),
    ("relative path", lambda: v.check_archived_context("spec/tla/codex/b.w21/jobs/w21_initial_red", ctx)),
    ("zero --context", lambda: v.command_context("submit --memory 4g")),
    ("two --context", lambda: v.command_context("submit --context '/a' --context '/b'")),
    ("r2 wrong case", metadata(first, other + "fixed-r2/jobs/w21_r2_modern_full_control")),
    ("r2 wrong round", metadata(first, other + "fixed/jobs/w21_r2_initial")),
    ("r2 rerun in a checkout", metadata(r2.RERUN_JOB, other + "fixed-r2/jobs/w21_r2_initial")),
    ("r2 rerun elsewhere", metadata(r2.RERUN_JOB, r2.RERUN_CONTEXT + "-other")),
]:
    try:
        call()
        print(label, "accepted")
    except AssertionError:
        pass
`

// TestW21VerifierRejectsWrongContext (b.5jg): the phase-1 and fixed-r2
// verifiers' checkout-independent context checks still reject a wrong archived
// context. -B keeps __pycache__ out of the pinned codex/ tree.
func TestW21VerifierRejectsWrongContext(t *testing.T) {
	needPython3(t)
	cmd := exec.Command("python3", "-B", "-c", w21ContextChecks)
	cmd.Dir = repoRoot(t)
	if out, err := cmd.CombinedOutput(); err != nil || len(out) != 0 {
		t.Errorf("context checks (%v):\n%s", err, out)
	}
}

// needPython3 skips without python3, except in the sandbox, whose image
// installs it so the b.5jg verifier tests cannot pass by skipping there.
func needPython3(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		if os.Getenv("AGENT_DIRECTOR_TEST_SANDBOX") == "1" {
			t.Fatal("python3 not on PATH in the sandbox")
		}
		t.Skip("python3 not on PATH")
	}
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
