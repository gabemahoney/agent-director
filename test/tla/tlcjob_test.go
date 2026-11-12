package tla_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"
)

// fakeJobJava records argv and emits checker output without a JVM. The cap
// modes affect only the first invocation, so unwanted later launches succeed.
const fakeJobJava = `#!/bin/sh
set -eu
n=$(wc -l < "$FAKE_DIR/java.calls")
printf '%s\t' "$@" >> "$FAKE_DIR/java.calls"
printf '\n' >> "$FAKE_DIR/java.calls"
case "$FAKE_MODE:$n" in
  sany-output:*)
    cat "$FAKE_DIR/sany-output"
    exit 0 ;;
  sany-ok:*)
    printf 'Parsing file %s\nSemantic processing of module Demo\n' "$WORK/Demo.tla"
    exit 0 ;;
  sany-error:*)
    printf 'Semantic errors:\n\n*** Errors: 1\n\nline 3, col 8 of module Demo\nUnknown operator: Missing\n'
    exit 0 ;;
  sany-exit:*)
    exit 17 ;;
  sany-time-budget:0)
    printf 'Parsing file %s\nSemantic processing of module Demo\n' "$WORK/Demo.tla"
    echo 1300 > "$FAKE_DIR/now"
    exit 137 ;;
  time-budget:0)
    printf 'Model checking completed. No error has been found.\n'
    printf 'Progress(3) at 1970-01-01: 100 states generated, 42 distinct states found, 10 states left on queue.\n'
    echo 1060 > "$FAKE_DIR/now"
    exit 124 ;;
  disk-cap:0)
    printf 'Model checking completed. No error has been found.\n'
    metadir=
    while [ "$#" -gt 0 ]; do
      case "$1" in -metadir) metadir=$2; break ;; esac
      shift
    done
    case "$metadir" in "$WORK"/states/*) ;; *) echo 'bad metadir' >&2; exit 65 ;; esac
    mkdir -p "$metadir"
    dd if=/dev/zero of="$metadir/queue" bs=1048576 count=2 2>/dev/null
    echo "$metadir" > "$FAKE_DIR/metadir"
    mkfifo "$metadir/hold"
    exec cat "$metadir/hold" ;;
esac
printf 'Model checking completed. No error has been found.\n42 distinct states found\nThe depth of the complete state graph search is 3.\n'
`

// runTLCJob reuses the runner rig's file, result and root helpers, but invokes
// the in-job script directly; only Java and the budget clock are substituted.
func runTLCJob(t *testing.T, runs, mode string, diskCap string, sanyOutput ...string) (result, [][]string, string) {
	t.Helper()
	// b.osa: every runtime case also exercises WORK quoting, including metadir.
	work := filepath.Join(t.TempDir(), "job work")
	bin, fake := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(work, "runs"), runs, 0o644)
	for _, cfg := range []string{"first", "later"} {
		writeFile(t, filepath.Join(work, cfg+".cfg"), "INVARIANT Safe\n", 0o644)
	}
	for _, line := range strings.Split(strings.TrimSpace(runs), "\n") {
		module := strings.Fields(line)[0]
		writeFile(t, filepath.Join(work, module+".tla"), "---- MODULE "+module+" ----\n====\n", 0o644)
	}
	if len(sanyOutput) > 0 {
		writeFile(t, filepath.Join(fake, "sany-output"), sanyOutput[0], 0o644)
	}
	writeFile(t, filepath.Join(fake, "java.calls"), "", 0o644)
	writeFile(t, filepath.Join(fake, "now"), "1000\n", 0o644)
	writeFile(t, filepath.Join(bin, "java"), fakeJobJava, 0o755)
	writeFile(t, filepath.Join(bin, "date"), "#!/bin/sh\ncat \"$FAKE_DIR/now\"\n", 0o755)
	// timeout would put itself and Java in a group of their own; --foreground
	// keeps every descendant in the job's group, which one kill reaches.
	realTimeout, err := exec.LookPath("timeout")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(bin, "timeout"), "#!/bin/sh\nexec "+realTimeout+" --foreground \"$@\"\n", 0o755)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	script := filepath.Join(repoRoot(t), "spec", "tla", "jobs", "lib", "tlcjob.sh")
	cmd := exec.CommandContext(ctx, "sh", script)
	cmd.Dir = work
	cmd.Env = []string{
		"PATH=" + bin + ":" + os.Getenv("PATH"), "HOME=" + fake, "LC_ALL=C",
		"WORK=" + work, "FAKE_DIR=" + fake, "FAKE_MODE=" + mode, "BUDGET=300",
	}
	if diskCap != "" {
		cmd.Env = append(cmd.Env, "DISKCAP_MB="+diskCap)
	}
	// b.osa: the job owns its process group, killed at the deadline and again
	// once the script ends, so no descendant outlives the temporary directories.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 5 * time.Second
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err = cmd.Run()
	if cmd.Process != nil {
		_ = cmd.Cancel() // ESRCH when nothing is left
	}
	if ctx.Err() != nil {
		t.Fatalf("tlcjob did not finish within a minute\nstdout:\n%s\nstderr:\n%s", &stdout, &stderr)
	}
	res := result{code: exitCode(t, err), stdout: stdout.String(), stderr: stderr.String()}
	if res.code != 0 {
		t.Fatalf("tlcjob failed before reporting verdicts\n%s", res)
	}
	if !strings.HasPrefix(res.last(), "JOB-VERDICT") {
		t.Errorf("JOB-VERDICT must be the last output line, got %q\n%s", res.last(), res)
	}
	var calls [][]string
	for _, line := range strings.Split(readFile(t, filepath.Join(fake, "java.calls")), "\n") {
		if line != "" {
			calls = append(calls, strings.Split(strings.TrimSuffix(line, "\t"), "\t"))
		}
	}
	return res, calls, fake
}

func jobVerdicts(t *testing.T, res result) map[string]string {
	t.Helper()
	verdicts := map[string]string{}
	for _, line := range res.lines() {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != "VERDICT" {
			continue
		}
		name := fields[1]
		if _, duplicate := verdicts[name]; duplicate {
			t.Fatalf("duplicate verdict for %s\n%s", name, res)
		}
		for _, field := range fields[2:] {
			if strings.HasPrefix(field, "result=") {
				verdicts[name] = strings.TrimPrefix(field, "result=")
			}
		}
	}
	return verdicts
}

// b.osa: SANY rejects diagnostics at exit 0 and a silent nonzero exit.
func TestTLCJobSANY(t *testing.T) {
	for _, tc := range []struct {
		mode, want string
	}{
		{"sany-ok", `^PARSE-OK$`},
		{"sany-error", `^PARSE-FAIL\([^)]+\)$`},
		{"sany-exit", `^PARSE-FAIL\(rc=17\)$`},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			res, calls, _ := runTLCJob(t, "Demo - sany\n", tc.mode, "")
			wantArgs := []string{"-cp", "tla2tools.jar", "tla2sany.SANY", "Demo.tla"}
			if len(calls) != 1 || len(calls[0]) < len(wantArgs) || !reflect.DeepEqual(calls[0][len(calls[0])-len(wantArgs):], wantArgs) {
				t.Errorf("SANY row must invoke java with %q; got %q", wantArgs, calls)
			}
			got := jobVerdicts(t, res)
			if len(got) != 1 || !regexp.MustCompile(tc.want).MatchString(got["sany:Demo"]) {
				t.Errorf("SANY verdict = %v, want sany:Demo result matching %s\n%s", got, tc.want, res)
			}
		})
	}
}

// b.osa CR-1.1: ordinary module names are not diagnostics. Exit 0 needs
// positive root progress and no diagnostic, including ones after that progress.
func TestTLCJobBOSASANYEvidence(t *testing.T) {
	cases := []struct {
		name, module, output, want string
	}{
		{"empty", "Demo", "", `^PARSE-FAIL\([^)]+\)$`},
		{"unrecognized", "Demo", "SANY version 2.2\n", `^PARSE-FAIL\([^)]+\)$`},
		{"late_semantic_error", "Demo", "Semantic processing of module Demo\nSemantic errors:\n*** Errors: 1\nUnknown operator: Missing\n", `^PARSE-FAIL\([^)]+\)$`},
		{"late_parse_error", "Demo", "Semantic processing of module Demo\n*** Parse Error ***\nEncountered unexpected token\n", `^PARSE-FAIL\([^)]+\)$`},
	}
	for _, name := range []string{"Error", "Error2", "Error_Recovery"} {
		cases = append(cases,
			struct{ name, module, output, want string }{
				"root_" + name, name, "Parsing file /work/" + name + ".tla\nSemantic processing of module " + name + "\n", `^PARSE-OK$`,
			},
			struct{ name, module, output, want string }{
				"helper_" + name, "Demo", "Parsing file /work/" + name + ".tla\nSemantic processing of module " + name + "\nSemantic processing of module Demo\n", `^PARSE-OK$`,
			})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel() // Each job has its own files, clock and subprocess environment.
			res, calls, _ := runTLCJob(t, tc.module+" - sany\n", "sany-output", "", tc.output)
			wantCalls := [][]string{{"-cp", "tla2tools.jar", "tla2sany.SANY", tc.module + ".tla"}}
			if !reflect.DeepEqual(calls, wantCalls) {
				t.Errorf("SANY Java calls = %q, want %q", calls, wantCalls)
			}
			got := jobVerdicts(t, res)
			if len(got) != 1 || !regexp.MustCompile(tc.want).MatchString(got["sany:"+tc.module]) {
				t.Errorf("SANY verdict = %v, want sany:%s result matching %s\n%s", got, tc.module, tc.want, res)
			}
		})
	}
}

// b.osa: a cap overrides apparent success and skips later TLC/SANY rows,
// including their ordered entries in the final job summary.
func TestTLCJobStopsAfterCap(t *testing.T) {
	for _, tc := range []struct{ cap, distinct string }{
		{"time-budget", "42"},
		{"disk-cap", "?"},
	} {
		t.Run(tc.cap, func(t *testing.T) {
			res, calls, fake := runTLCJob(t, "Demo first @60\nDemo later @60\nDemo - sany\n", tc.cap, "1")
			want := map[string]string{
				"first":     "INCOMPLETE(" + tc.cap + ")",
				"later":     "SKIPPED(after-cap)",
				"sany:Demo": "SKIPPED(after-cap)",
			}
			if got := jobVerdicts(t, res); !reflect.DeepEqual(got, want) {
				t.Errorf("cap verdicts = %v, want %v\n%s", got, want, res)
			}
			if len(calls) != 1 {
				t.Errorf("java started %d times, want 1 after %s: %q", len(calls), tc.cap, calls)
			}
			wantSummary := "JOB-VERDICT first=INCOMPLETE(" + tc.cap + ")/" + tc.distinct + " later=SKIPPED(after-cap) sany:Demo=SKIPPED(after-cap)"
			if got := res.last(); got != wantSummary {
				t.Errorf("job summary = %q, want %q", got, wantSummary)
			}
			if tc.cap == "disk-cap" {
				// The fake allocated 2 MiB in the actual -metadir, over the 1 MB
				// override. No fake du or fabricated diskcap marker is involved.
				metadir := strings.TrimSpace(readFile(t, filepath.Join(fake, "metadir")))
				if !strings.HasSuffix(metadir, "/states/first") {
					t.Errorf("disk cap did not cover the first TLC metadir: %q", metadir)
				}
			}
		})
	}
}

// b.osa: a cap in SANY is INCOMPLETE, not PARSE-FAIL, and stops later rows.
func TestTLCJobSANYTimeCap(t *testing.T) {
	res, calls, _ := runTLCJob(t, "Demo - sany\nDemo later @60\nOther - sany\n", "sany-time-budget", "")
	want := map[string]string{
		"sany:Demo":  "INCOMPLETE(time-budget)",
		"later":      "SKIPPED(after-cap)",
		"sany:Other": "SKIPPED(after-cap)",
	}
	if got := jobVerdicts(t, res); !reflect.DeepEqual(got, want) {
		t.Errorf("SANY cap verdicts = %v, want %v\n%s", got, want, res)
	}
	wantCalls := [][]string{{"-cp", "tla2tools.jar", "tla2sany.SANY", "Demo.tla"}}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Errorf("SANY cap Java calls = %q, want %q", calls, wantCalls)
	}
	wantSummary := "JOB-VERDICT sany:Demo=INCOMPLETE(time-budget)/? later=SKIPPED(after-cap) sany:Other=SKIPPED(after-cap)"
	if got := res.last(); got != wantSummary {
		t.Errorf("job summary = %q, want %q", got, wantSummary)
	}
}

// b.osa: pin the 15000 MB default without allocating 15 GB in a unit test;
// TestTLCJobStopsAfterCap/disk-cap exercises the guard with real disk usage.
func TestTLCJobDiskCapDefault(t *testing.T) {
	script := readFile(t, filepath.Join(repoRoot(t), "spec", "tla", "jobs", "lib", "tlcjob.sh"))
	match := regexp.MustCompile(`(?m)^DISKCAP_MB=\$\{DISKCAP_MB:-([0-9]+)\}$`).FindStringSubmatch(script)
	if len(match) != 2 {
		t.Fatal("missing DISKCAP_MB default assignment")
	}
	if match[1] != "15000" {
		t.Errorf("DISKCAP_MB default = %s, want 15000", match[1])
	}
}
