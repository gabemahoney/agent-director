// Package tla_test tests spec/tla/ci/run_ci.sh, the runner behind `make tla`
// (Epic 22), offline: it execs the runner against a fake job scheduler CLI on
// PATH and never runs the model checker, java, docker or a real scheduler.
package tla_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/testsupport/sandboxguard"
)

func TestMain(m *testing.M) {
	sandboxguard.Require()
	os.Exit(m.Run())
}

// fakeJobsched is the scheduler CLI. It logs every call to $FAKE_DIR/calls and
// keeps what each submit's context held under $FAKE_DIR/seen/<job>/. The Nth
// call for a key (list, submit.<job>, status.<job>, results.<job>) answers
// line N of $FAKE_DIR/plan.<key> (the last line once they run out), else a
// default; a line is "<exit code> <stdout>", with @ID@ standing for the job
// id. $FAKE_DIR/hold.<cmd> makes that call mark held.<cmd> and block. results
// writes a run log from $FAKE_DIR/verdicts ("<cfg or parse:cfg>\t<result>"
// lines, in order); a parse cfg without one gets PARSE-OK.
const fakeJobsched = `#!/usr/bin/env bash
set -u
d=$FAKE_DIR cmd=$1 id=
shift
printf '%s %s\n' "$cmd" "$*" >>"$d/calls"
hold() { if [ -e "$d/hold.$1" ]; then : >"$d/held.$1"; sleep 30; exit 1; fi; }
answer() {
  local n line
  n=$(( $(cat "$d/count.$1" 2>/dev/null || echo 0) + 1 ))
  echo "$n" >"$d/count.$1"
  if [ -f "$d/plan.$1" ]; then
    line=$(sed -n "${n}p" "$d/plan.$1"); [ -n "$line" ] || line=$(tail -n 1 "$d/plan.$1")
  else
    line=$2
  fi
  line=${line//@ID@/$id}
  printf '%s\n' "${line#* }"
  exit "${line%% *}"
}
case $cmd in
list)
  hold list
  answer list '0 {"summary":{"dispatcher_running":true}}' ;;
submit)
  ctx= name=
  while [ $# -gt 0 ]; do
    case $1 in --context) ctx=$2; shift ;; --name) name=$2; shift ;; esac
    shift
  done
  job=${name#tla-}
  id="j$(grep -c '^submit ' "$d/calls")-$job"
  s="$d/seen/$job"; mkdir -p "$s"
  cp "$ctx/runs" "$s/runs"
  ls "$ctx" >"$s/ctx"
  ls "$(dirname "$ctx")" >"$s/ctxdir"
  grep -E '^(BUDGET|DISKCAP_MB)=' "$ctx/tlcjob.sh" >"$s/limits"
  (cd "$ctx" && sha256sum Phase4.tla Phase4Split.tla Phase5Hook.tla) >"$s/specs"
  hold submit
  answer "submit.$job" "0 {\"id\":\"$id\",\"dispatcher_running\":true,\"timeout_s\":10800,\"memory\":34359738368}" ;;
status)
  id=$1; job=${id#*-}
  answer "status.$job" '0 {"state":"succeeded","terminal":true,"dispatcher_running":true}' ;;
results)
  id=$1; job=${id#*-}; log="$d/logs/$job.log"
  mkdir -p "$d/logs"; : >"$log"
  while read -r _ cfg _; do
    key=$cfg found=no
    [ "$job" = parse ] && key=parse:$cfg
    while IFS=$'\t' read -r k res; do
      [ "$k" = "$key" ] || continue
      echo "VERDICT $cfg checks=inv result=$res distinct=42 depth=3 secs=7" >>"$log"; found=yes
    done <"$d/verdicts"
    if [ "$job" = parse ] && [ "$found" = no ]; then
      echo "VERDICT $cfg checks=parse result=PARSE-OK distinct=1 depth=1 secs=1" >>"$log"
    fi
  done <"$d/seen/$job/runs"
  answer "results.$job" "0 {\"paths\":{\"run_log\":\"$log\"},\"present\":{\"run_log\":true}}" ;;
*) echo "{\"error\":\"unknown command $cmd\"}"; exit 64 ;;
esac
`

// neverShim stands in for a tool the runner must never start; it logs the call.
const neverShim = "#!/bin/sh\necho \"$0 $*\" >> \"$FAKE_DIR/forbidden\"\nexit 1\n"

// holdCpShim blocks the copy of the jar into a job context (the context
// build) once $FAKE_DIR/hold.cp exists; every other copy goes to %s.
const holdCpShim = `#!/usr/bin/env bash
if [ -e "$FAKE_DIR/hold.cp" ] && [[ "$*" == *tla2tools.jar* ]]; then : >"$FAKE_DIR/held.cp"; sleep 30; exit 1; fi
exec %s "$@"
`

// rig is one runner invocation's world: the spec/tla tree it runs from, the
// fake scheduler's state, a TMPDIR outside the repo and a PATH dir of shims.
type rig struct {
	tree string            // spec/tla the runner lives in (the repo's, or a private copy)
	fake string            // FAKE_DIR
	tmp  string            // the runner's TMPDIR
	bin  string            // PATH dir: jobsched plus the never-call shims
	env  map[string]string // per-test overrides of the runner environment
}

func newRig(t *testing.T) *rig {
	t.Helper()
	r := &rig{tree: filepath.Join(repoRoot(t), "spec", "tla"), fake: t.TempDir(), tmp: t.TempDir(), bin: t.TempDir(), env: map[string]string{}}
	writeFile(t, filepath.Join(r.bin, "jobsched"), fakeJobsched, 0o755)
	for _, name := range []string{"java", "docker", "podman", "tlc", "tlc2"} {
		writeFile(t, filepath.Join(r.bin, name), neverShim, 0o755)
	}
	writeFile(t, filepath.Join(r.fake, "verdicts"), "", 0o644)
	return r
}

// private moves the rig onto a copy of spec/tla at <tmp>/repo/spec/tla and
// returns the copy's path, so a test may change vendored files.
func (r *rig) private(t *testing.T) string {
	t.Helper()
	dst := filepath.Join(t.TempDir(), "repo", "spec", "tla")
	err := filepath.WalkDir(r.tree, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(r.tree, p)
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	r.tree = dst
	return dst
}

// plan sets the fake's answers for key, one line per call.
func (r *rig) plan(t *testing.T, key string, lines ...string) {
	t.Helper()
	writeFile(t, filepath.Join(r.fake, "plan."+key), strings.Join(lines, "\n")+"\n", 0o644)
}

// verdict adds a VERDICT result for key (a cfg, or parse:<cfg>) to the run logs.
func (r *rig) verdict(t *testing.T, key, result string) {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(r.fake, "verdicts"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	fmt.Fprintf(f, "%s\t%s\n", key, result)
}

// expectAll gives every row the result its expect column asks for.
func (r *rig) expectAll(t *testing.T, rows []row) {
	t.Helper()
	for _, rw := range rows {
		res := "PASS"
		if rw.expect == "violation" {
			res = "FAIL(Invariant_" + rw.cfg + "_is_violated)"
		}
		r.verdict(t, rw.cfg, res)
	}
}

// hold makes the fake's call cmd (or, for "cp", the jar copy) block.
func (r *rig) hold(t *testing.T, cmd string) {
	t.Helper()
	if cmd == "cp" {
		cp, err := exec.LookPath("cp")
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(r.bin, "cp"), fmt.Sprintf(holdCpShim, cp), 0o755)
	}
	writeFile(t, filepath.Join(r.fake, "hold."+cmd), "", 0o644)
}

// state is a status --json answer line for a plan.
func state(st string, terminal bool) string {
	return fmt.Sprintf(`0 {"state":"%s","terminal":%t,"dispatcher_running":true}`, st, terminal)
}

// command builds the runner's command line with a clean environment.
func (r *rig) command(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "bash", append([]string{filepath.Join(r.tree, "ci", "run_ci.sh")}, args...)...)
	env := map[string]string{
		"PATH": r.bin + ":" + os.Getenv("PATH"), "HOME": r.fake, "LC_ALL": "C", "TMPDIR": r.tmp, "FAKE_DIR": r.fake,
		"TLA_CHANNEL": "C0TESTCHAN", "TLA_JOBSCHED": "jobsched", "TLA_POLL_S": "0",
	}
	for k, v := range r.env {
		env[k] = v
	}
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	return cmd
}

type result struct {
	code           int
	stdout, stderr string
}

// lines are the runner's stdout lines.
func (res result) lines() []string { return strings.Split(strings.TrimRight(res.stdout, "\n"), "\n") }

// last is the runner's final stdout line (its CI-VERDICT line).
func (res result) last() string { l := res.lines(); return l[len(l)-1] }

func (res result) String() string {
	return fmt.Sprintf("exit %d\nstdout:\n%s\nstderr:\n%s", res.code, res.stdout, res.stderr)
}

// run runs the runner to its end; a run that does not end in time fails.
func (r *rig) run(t *testing.T, args ...string) result {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := r.command(ctx, args...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	res := result{code: exitCode(t, err), stdout: out.String(), stderr: errb.String()}
	if ctx.Err() != nil {
		t.Fatalf("the runner did not end within a minute\n%s", res)
	}
	return res
}

func exitCode(t *testing.T, err error) int {
	t.Helper()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &exitErr):
		return exitErr.ExitCode() // -1 when killed by a signal
	}
	t.Fatal(err)
	return 0
}

// call is one recorded scheduler call and the job it was about.
type call struct{ cmd, job string }

func (r *rig) calls(t *testing.T) []call {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(r.fake, "calls"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var out []call
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		f := strings.Fields(line)
		c := call{cmd: f[0]}
		switch {
		case c.cmd == "submit":
			for i := 0; i+1 < len(f); i++ {
				if f[i] == "--name" {
					c.job = strings.TrimPrefix(f[i+1], "tla-")
				}
			}
		case (c.cmd == "status" || c.cmd == "results") && len(f) > 1:
			c.job = f[1][strings.Index(f[1], "-")+1:]
		}
		out = append(out, c)
	}
	return out
}

// submits are the jobs submitted, in order.
func (r *rig) submits(t *testing.T) []string {
	var jobs []string
	for _, c := range r.calls(t) {
		if c.cmd == "submit" {
			jobs = append(jobs, c.job)
		}
	}
	return jobs
}

// seen is what the fake kept from job's submit (runs, ctx, ctxdir, limits, specs).
func (r *rig) seen(t *testing.T, job, name string) string {
	t.Helper()
	return readFile(t, filepath.Join(r.fake, "seen", job, name))
}

var runDirRe = regexp.MustCompile(`(?m)^# run dir: (\S+)`)

// runDir is the per-run directory the runner printed on stderr.
func runDir(t *testing.T, res result) string {
	t.Helper()
	m := runDirRe.FindStringSubmatch(res.stderr)
	if m == nil {
		t.Fatalf("no run dir printed\n%s", res)
	}
	return m[1]
}

// row is one manifest row.
type row struct {
	group, spec, cfg string
	capS             int
	expect, tier     string
	what             string
}

func loadSuite(t *testing.T, path string) []row {
	t.Helper()
	var rows []row
	for _, line := range strings.Split(readFile(t, path), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) != 7 {
			t.Fatalf("%s: bad row %q", path, line)
		}
		c, err := strconv.Atoi(f[3])
		if err != nil {
			t.Fatal(err)
		}
		rows = append(rows, row{f[0], f[1], f[2], c, f[4], f[5], f[6]})
	}
	return rows
}

// writeSuite writes rows as a manifest and returns its path.
func writeSuite(t *testing.T, rows []row) string {
	t.Helper()
	var b strings.Builder
	b.WriteString("# group\tspec\tcfg\tcap_s\texpect\ttier\twhat\n")
	for _, rw := range rows {
		fmt.Fprintf(&b, "%s\t%s\t%s\t%d\t%s\t%s\t%s\n", rw.group, rw.spec, rw.cfg, rw.capS, rw.expect, rw.tier, rw.what)
	}
	p := filepath.Join(t.TempDir(), "suite.tsv")
	writeFile(t, p, b.String(), 0o644)
	return p
}

// shipped is the vendored manifest.
func shipped(t *testing.T) []row {
	return loadSuite(t, filepath.Join(repoRoot(t), "spec", "tla", "ci", "suite.tsv"))
}

// inTier keeps the rows a tier runs.
func inTier(rows []row, tier string) []row {
	var out []row
	for _, rw := range rows {
		if tier == "full" || rw.tier == "fast" {
			out = append(out, rw)
		}
	}
	return out
}

// byCfg finds a manifest row.
func byCfg(t *testing.T, rows []row, cfg string) row {
	t.Helper()
	for _, rw := range rows {
		if rw.cfg == cfg {
			return rw
		}
	}
	t.Fatalf("no row %s", cfg)
	return row{}
}

// parseRuns is the parse job's runs file for rows: the first row of each spec.
func parseRuns(rows []row) []string {
	seen := map[string]bool{}
	var out []string
	for _, rw := range rows {
		if !seen[rw.spec] {
			seen[rw.spec] = true
			out = append(out, rw.spec+" "+rw.cfg+" -simulate num=1 -depth 1")
		}
	}
	return out
}

// groupRuns is a group job's runs file lines and its budget.
func groupRuns(rows []row, group string) ([]string, int) {
	var out []string
	b := 120
	for _, rw := range rows {
		if rw.group == group {
			out = append(out, fmt.Sprintf("%s %s @%d", rw.spec, rw.cfg, rw.capS))
			b += rw.capS + 20
		}
	}
	return out, min(b, 10200)
}

// groupsOf lists rows' groups in manifest order.
func groupsOf(rows []row) []string {
	var out []string
	for _, rw := range rows {
		if len(out) == 0 || out[len(out)-1] != rw.group {
			out = append(out, rw.group)
		}
	}
	return out
}

func sortedFields(s string) []string {
	f := strings.Fields(s)
	sort.Strings(f)
	return f
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test directory")
		}
		dir = parent
	}
}

func writeFile(t *testing.T, path, body string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
