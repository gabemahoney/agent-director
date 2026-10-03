package tla_test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// fakeJava stands in for java in the laptop runner tests; it is never a JVM.
// It logs its arguments to $FAKE_DIR/java.calls. `-version` prints
// $FAKE_DIR/java.version (default: a Java 17 banner). A TLC run answers from
// $FAKE_DIR/verdicts ("<cfg or parse:cfg>\t<result>\t<distinct>" lines; the
// last match wins), else from the manifests: a ci cfg gets its run-4 result
// and state count (laptop/run4.tsv), a launch cfg its expected result.
// A result is PASS, "<Invariant|Action property|Temporal property> <name>",
// or anything else for an error.
const fakeJava = `#!/usr/bin/env bash
d=$FAKE_DIR
printf '%s\n' "$*" >>"$d/java.calls"
if [ "$1" = -version ]; then
  if [ -f "$d/java.version" ]; then cat "$d/java.version" >&2; else echo 'openjdk version "17.0.8" 2023-07-18' >&2; fi
  exit 0
fi
cfg= spec= sim=no
while [ $# -gt 0 ]; do
  case $1 in -config) cfg=${2%.cfg}; shift ;; -simulate) sim=yes ;; *.tla) spec=${1%.tla} ;; esac
  shift
done
[ -f "$cfg.cfg" ] && [ -f "$spec.tla" ] || { echo "Error: no $cfg.cfg or $spec.tla in $PWD"; exit 1; }
key=$cfg; [ $sim = yes ] && key=parse:$cfg
v=$(awk -F'\t' -v k="$key" '$1 == k { r = $2 "\t" $3 } END { print r }' "$d/verdicts")
if [ $sim = yes ]; then
  if [ -n "$v" ]; then echo "Error: ${v%%	*}"; exit 1; fi
  echo "The number of states generated: 3"; exit 0
fi
if [ -z "$v" ]; then
  v=$(awk -F'\t' -v c="$cfg" '$1 == c {
        r = $2; sub(/^FAIL\(/, "", r); sub(/_(is|was)_violated\)$/, "", r)
        sub(/^Action_property_/, "Action property ", r); sub(/^Temporal_property_/, "Temporal property ", r)
        sub(/^Invariant_/, "Invariant ", r); print r "\t" $3 }' "$FAKE_ROOT/laptop/run4.tsv")
fi
if [ -z "$v" ]; then
  v=$(awk -F'\t' -v c="$cfg" '$3 == c { split($8, p, ","); print ($5 == "pass" ? "PASS" : "Invariant " p[1]) "\t7" }' "$FAKE_ROOT/launch/suite.tsv")
fi
res=${v%%	*}; dist=${v#*	}
case $res in
  PASS) echo "Model checking completed. No error has been found."
        echo "9 states generated, $dist distinct states found, 0 states left on queue." ;;
  Invariant*|Action*|Temporal*)
        word=is; case $res in Temporal*) word=was ;; esac
        echo "Error: $res $word violated."
        echo "Error: The behavior up to this point is:"
        echo "State 1: <Initial predicate>"; echo '/\ x = 1'; echo
        echo "State 2: <B_NrKill line 3, col 1 to line 4, col 2 of module Phase4>"; echo '/\ x = 2'; echo
        echo "5 states generated, $dist distinct states found, 1 states left on queue." ;;
  *) echo "Error: TLC threw an unexpected exception: $res"; exit 1 ;;
esac
`

// laptop is one laptop-runner invocation's world.
type laptop struct {
	fake, tmp, bin string
	env            map[string]string
}

func newLaptop(t *testing.T) *laptop {
	t.Helper()
	l := &laptop{fake: t.TempDir(), tmp: t.TempDir(), bin: t.TempDir(), env: map[string]string{}}
	writeFile(t, filepath.Join(l.bin, "java"), fakeJava, 0o755)
	writeFile(t, filepath.Join(l.fake, "verdicts"), "", 0o644)
	return l
}

// verdict makes the fake java answer result (and state count) for key.
func (l *laptop) verdict(t *testing.T, key, result, distinct string) {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(l.fake, "verdicts"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	fmt.Fprintf(f, "%s\t%s\t%s\n", key, result, distinct)
}

func (l *laptop) run(t *testing.T, args ...string) result {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	root := filepath.Join(repoRoot(t), "spec", "tla")
	cmd := exec.CommandContext(ctx, "bash", append([]string{filepath.Join(root, "laptop", "run.sh")}, args...)...)
	env := map[string]string{
		"PATH": l.bin + ":" + os.Getenv("PATH"), "HOME": l.fake, "LC_ALL": "C", "TMPDIR": l.tmp,
		"FAKE_DIR": l.fake, "FAKE_ROOT": root, "TLA_JAVA": filepath.Join(l.bin, "java"),
	}
	for k, v := range l.env {
		env[k] = v
	}
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	res := result{code: exitCode(t, err), stdout: out.String(), stderr: errb.String()}
	if ctx.Err() != nil {
		t.Fatalf("the laptop runner did not end in time\n%s", res)
	}
	return res
}

// tlcCalls are the fake java's TLC runs (not -version), as argument strings.
func (l *laptop) tlcCalls(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, c := range strings.Split(strings.TrimSpace(readFile(t, filepath.Join(l.fake, "java.calls"))), "\n") {
		if c != "-version" {
			out = append(out, c)
		}
	}
	return out
}

var laptopLine = regexp.MustCompile(`^(PASS|FAIL) (\S+) +(\S+) +distinct=(\S+) secs=\d+ -- (.*)$`)

// runLines are the per-run lines, by cfg.
func runLines(res result) map[string][]string {
	m := map[string][]string{}
	for _, line := range res.lines() {
		if f := laptopLine.FindStringSubmatch(line); f != nil {
			m[f[2]] = f
		}
	}
	return m
}

func TestLaptopRefusesWithoutJava11(t *testing.T) {
	for _, tc := range []struct {
		name, version, java, want, reason string
	}{
		{"missing", "", "/nonexistent/java", "FAIL preflight -- no working java ('/nonexistent/java'). install a Java 11+ runtime: sudo apt-get install -y openjdk-17-jre-headless", "java missing"},
		{"too old", `java version "1.8.0_292"`, "", "FAIL preflight -- java 8 is too old; TLC needs Java 11+. install a Java 11+ runtime:", "java too old"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := newLaptop(t)
			if tc.version != "" {
				writeFile(t, filepath.Join(l.fake, "java.version"), tc.version+"\n", 0o644)
			}
			if tc.java != "" {
				l.env["TLA_JAVA"] = tc.java
			}
			res := l.run(t, "--launch")
			if res.code != 1 || !strings.Contains(res.stdout, tc.want) || res.last() != "LAPTOP-VERDICT FAIL ("+tc.reason+")" {
				t.Fatalf("want exit 1, %q and the %q verdict\n%s", tc.want, tc.reason, res)
			}
			if exists(filepath.Join(l.fake, "java.calls")) && len(l.tlcCalls(t)) != 0 {
				t.Errorf("TLC ran: %v", l.tlcCalls(t))
			}
		})
	}
}

// TestLaptopLaunchRuns: --launch parses every b.66h cfg, runs each once with
// the heap, direct memory and worker settings, prints the control traces, and
// passes when each run meets its expectation.
func TestLaptopLaunchRuns(t *testing.T) {
	l := newLaptop(t)
	l.env["TLA_XMX"], l.env["TLA_DIRECT"], l.env["TLA_WORKERS"] = "3g", "1500m", "2"
	res := l.run(t, "--launch")
	if res.code != 0 || !regexp.MustCompile(`^LAPTOP-VERDICT PASS \(9/9 runs ok, \d+ min, mode launch, tier fast, 2 workers\)$`).MatchString(res.last()) {
		t.Fatalf("want a 9/9 PASS\n%s", res)
	}
	lines := runLines(res)
	for cfg, want := range map[string]string{
		"ls_V_bot": "FAIL(Invariant_NotLaunchChanged_is_violated)", "ls_V_orch": "FAIL(Invariant_NotLaunchChanged_is_violated)",
		"ls_V_window": "FAIL(Invariant_NotRelaunchInKillWindow_is_violated)", "ls_S1o_rst": "PASS",
		"ls_C_S1h_lab": "FAIL(Invariant_ScopedActsOnlyOnObservedLaunch_is_violated)",
		"ls_C_S1o_lab": "FAIL(Invariant_ScopedActsOnlyOnObservedLaunch_is_violated)",
		"ls_Cact":      "FAIL(Invariant_HandsOff_is_violated)", "ls_S1o_lab": "PASS", "ls_S1h_lab": "PASS",
	} {
		if f := lines[cfg]; f == nil || f[1] != "PASS" || f[3] != want {
			t.Errorf("%s: line %v, want a PASS line with result %s", cfg, f, want)
		}
	}
	if n := strings.Count(res.stdout, "PASS parse "); n != 9 {
		t.Errorf("%d parse lines, want 9\n%s", n, res)
	}
	if n := strings.Count(res.stdout, "-- State 2: <B_NrKill>"); n != 6 {
		t.Errorf("%d condensed traces, want one for each of the 6 must-fail runs\n%s", n, res)
	}
	calls := l.tlcCalls(t)
	if len(calls) != 18 {
		t.Fatalf("%d TLC runs, want 9 parse + 9 checks: %v", len(calls), calls)
	}
	for _, c := range calls {
		for _, want := range []string{"-Xmx3g", "-XX:MaxDirectMemorySize=1500m", "tlc2.TLC -workers 2 -noGenerateSpecTE -metadir ", "Phase4Split.tla"} {
			if !strings.Contains(c, want) {
				t.Errorf("TLC call %q lacks %q", c, want)
			}
		}
	}
	if !strings.Contains(calls[0], "-config ls_V_bot.cfg -simulate num=1 -depth 1 Phase4Split.tla") {
		t.Errorf("first call %q, want the parse check of ls_V_bot", calls[0])
	}
}

// TestLaptopExpectations: the ok rules beyond "violation or not".
func TestLaptopExpectations(t *testing.T) {
	t.Run("a b.66h control that violates another property fails", func(t *testing.T) {
		l := newLaptop(t)
		l.verdict(t, "ls_C_S1o_lab", "Invariant KillHonest", "40")
		res := l.run(t, "ls_C_S1o_lab")
		f := runLines(res)["ls_C_S1o_lab"]
		if res.code != 1 || f == nil || f[1] != "FAIL" || !strings.Contains(f[5], "(violates KillHonest, but the expected violation is ScopedActsOnlyOnObservedLaunch)") {
			t.Fatalf("want a FAIL line naming both properties\n%s", res)
		}
	})
	t.Run("a b.zuj pass run with another state count fails", func(t *testing.T) {
		l := newLaptop(t)
		l.verdict(t, "ci_S1o_lab", "PASS", "1,079,503")
		res := l.run(t, "ci_S1o_lab")
		f := runLines(res)["ci_S1o_lab"]
		if res.code != 1 || f == nil || f[1] != "FAIL" || f[4] != "1079503" ||
			!strings.Contains(f[5], "(state count differs from b.zuj run 4 (1079502): the model change is not inert here)") {
			t.Fatalf("want a FAIL line for the state count\n%s", res)
		}
	})
	t.Run("a design run that finds an error fails", func(t *testing.T) {
		l := newLaptop(t)
		l.verdict(t, "ls_S1h_lab", "Action property ChangedIsInert", "12")
		res := l.run(t, "ls_S1h_lab")
		f := runLines(res)["ls_S1h_lab"]
		if res.code != 1 || f == nil || f[1] != "FAIL" || f[3] != "FAIL(Action_property_ChangedIsInert_is_violated)" || !strings.Contains(res.stdout, "     log: ") {
			t.Fatalf("want a FAIL line and the log path\n%s", res)
		}
	})
	t.Run("a TLC error is not ok", func(t *testing.T) {
		l := newLaptop(t)
		l.verdict(t, "ls_V_orch", "boom", "")
		res := l.run(t, "ls_V_orch")
		f := runLines(res)["ls_V_orch"]
		if res.code != 1 || f == nil || f[1] != "FAIL" || !strings.HasPrefix(f[3], "ERROR(rc=1:") {
			t.Fatalf("want an ERROR result\n%s", res)
		}
	})
	t.Run("a parse failure stops before any check", func(t *testing.T) {
		l := newLaptop(t)
		l.verdict(t, "parse:ls_S1o_lab", "Unknown operator: `LaunchObs'", "")
		res := l.run(t, "--launch")
		if res.code != 1 || !strings.Contains(res.stdout, "FAIL parse ls_S1o_lab (Phase4Split)") ||
			res.last() != "LAPTOP-VERDICT FAIL (parse check failed; nothing else was run)" {
			t.Fatalf("want the parse failure verdict\n%s", res)
		}
		for _, c := range l.tlcCalls(t) {
			if !strings.Contains(c, "-simulate") {
				t.Errorf("a check ran after the parse failure: %q", c)
			}
		}
	})
	t.Run("an unknown cfg is refused", func(t *testing.T) {
		l := newLaptop(t)
		res := l.run(t, "ls_nope")
		if res.code != 1 || res.last() != "LAPTOP-VERDICT FAIL (unknown cfg)" {
			t.Fatalf("want the unknown-cfg refusal\n%s", res)
		}
	})
}

// TestLaptopDefaultPlan: with no arguments the runner runs the 9 b.66h runs,
// then the 71 fast-tier runs, and every b.zuj pass run is held to its run-4
// state count.
func TestLaptopDefaultPlan(t *testing.T) {
	l := newLaptop(t)
	res := l.run(t)
	if res.code != 0 || !regexp.MustCompile(`^LAPTOP-VERDICT PASS \(80/80 runs ok, \d+ min, mode all, tier fast, \d+ workers\)$`).MatchString(res.last()) {
		t.Fatalf("want a 80/80 PASS\n%s", res)
	}
	lines := runLines(res)
	for _, rw := range inTier(shipped(t), "fast") {
		f := lines[rw.cfg]
		if f == nil {
			t.Errorf("%s did not run", rw.cfg)
			continue
		}
		if rw.expect == "pass" && !strings.Contains(f[5], "(same state count as b.zuj run 4)") {
			t.Errorf("%s: %q does not report the run-4 state count check", rw.cfg, f[0])
		}
	}
	if n := strings.Count(res.stdout, "PASS parse "); n != 11 {
		t.Errorf("%d parse lines, want 11 (the 9 b.66h cfgs plus one each for Phase4 and Phase5Hook)", n)
	}
}
