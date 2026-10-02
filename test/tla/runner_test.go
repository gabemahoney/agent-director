package tla_test

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// runLine is the runner's per-run line: tag, cfg, result, distinct, secs, what.
var runLine = regexp.MustCompile(`^(PASS|FAIL) (\S+) +(\S+) +distinct=(\S+) secs=(\S+) -- (.*)$`)

// gitStatus is `git status` of spec/ (ok false outside a git checkout).
func gitStatus(t *testing.T, tree string) (string, bool) {
	t.Helper()
	out, err := exec.Command("git", "-C", tree, "status", "--porcelain", "--untracked-files=all", "--", ".").Output()
	return string(out), err == nil
}

// TestRunnerFullSuite runs the shipped manifest per tier: the parse job first,
// then each group in manifest order, one job at a time, all ok.
func TestRunnerFullSuite(t *testing.T) {
	for _, tc := range []struct {
		tier     string
		runs     int
		perGroup map[string]int
	}{
		{"fast", 71, map[string]int{"g1": 34, "g2": 3, "g3": 7, "g4a": 1, "g4b": 1, "g5a": 1, "g5b": 1, "g5c": 3, "g6": 20}},
		{"full", 92, map[string]int{"g1": 34, "g2": 7, "g3": 7, "g4a": 5, "g4b": 5, "g5a": 5, "g5b": 4, "g5c": 3, "g6": 22}},
	} {
		t.Run(tc.tier, func(t *testing.T) {
			r := newRig(t)
			rows := inTier(shipped(t), tc.tier)
			r.expectAll(t, rows)
			r.env["TLA_TIER"] = tc.tier
			r.plan(t, "status.g1", state("queued", false), state("running", false), state("succeeded", true))
			before, inGit := gitStatus(t, r.tree)

			res := r.run(t)

			want := fmt.Sprintf(`^CI-VERDICT PASS \(%d/%d runs ok, \d+ min, tier %s\)$`, tc.runs, tc.runs, tc.tier)
			if res.code != 0 || !regexp.MustCompile(want).MatchString(res.last()) {
				t.Fatalf("want exit 0 and %s\n%s", want, res)
			}
			lines := res.lines()
			if len(lines) != len(rows)+1 {
				t.Fatalf("want %d run lines, got %d\n%s", len(rows), len(lines)-1, res)
			}
			for i, rw := range rows {
				m := runLine.FindStringSubmatch(lines[i])
				if m == nil || m[1] != "PASS" || m[2] != rw.cfg || m[4] != "42" || m[5] != "7" || m[6] != rw.what {
					t.Errorf("run line %d = %q, want a PASS line for %s -- %s", i, lines[i], rw.cfg, rw.what)
				}
			}

			groups := groupsOf(rows)
			wantCalls := []call{{"list", ""}}
			for _, job := range append([]string{"parse"}, groups...) {
				wantCalls = append(wantCalls, call{"submit", job}, call{"status", job})
				if job == "g1" {
					wantCalls = append(wantCalls, call{"status", job}, call{"status", job})
				}
				wantCalls = append(wantCalls, call{"results", job})
			}
			if got := r.calls(t); !reflect.DeepEqual(got, wantCalls) {
				t.Errorf("scheduler calls:\n got %v\nwant %v", got, wantCalls)
			}

			checkJob(t, r, "parse", parseRuns(rows), 600)
			counts := map[string]int{}
			for _, g := range groups {
				runs, budget := groupRuns(rows, g)
				counts[g] = len(runs)
				checkJob(t, r, g, runs, budget)
			}
			if !reflect.DeepEqual(counts, tc.perGroup) {
				t.Errorf("runs per group = %v, want %v", counts, tc.perGroup)
			}

			dir := runDir(t, res)
			if tmp, _ := filepath.EvalSymlinks(r.tmp); filepath.Dir(dir) != tmp {
				t.Errorf("run dir %s is not directly under TMPDIR %s", dir, tmp)
			}
			if exists(filepath.Join(dir, "ctx")) {
				t.Errorf("job contexts left behind in %s/ctx", dir)
			}
			for _, job := range append([]string{"parse"}, groups...) {
				if !exists(filepath.Join(dir, "logs", job+".run.log")) {
					t.Errorf("log of job %s not kept", job)
				}
			}
			if exists(filepath.Join(r.fake, "forbidden")) {
				t.Errorf("the runner started a forbidden tool: %s", readFile(t, filepath.Join(r.fake, "forbidden")))
			}
			if after, _ := gitStatus(t, r.tree); inGit && after != before {
				t.Errorf("git status of spec/tla changed:\nbefore:\n%s\nafter:\n%s", before, after)
			}
		})
	}
}

// checkJob checks what the fake saw at job's submit: its runs, its patched
// limits, a full context, and no other job's context beside it.
func checkJob(t *testing.T, r *rig, job string, runs []string, budget int) {
	t.Helper()
	if got, want := r.seen(t, job, "runs"), strings.Join(runs, "\n")+"\n"; got != want {
		t.Errorf("job %s runs:\n got %q\nwant %q", job, got, want)
	}
	if got, want := r.seen(t, job, "limits"), fmt.Sprintf("BUDGET=${BUDGET:-%d}\nDISKCAP_MB=${DISKCAP_MB:-60000}\n", budget); got != want {
		t.Errorf("job %s tlcjob.sh limits = %q, want %q", job, got, want)
	}
	ctx := []string{"Dockerfile", "Phase4.tla", "Phase4Split.tla", "Phase5Hook.tla", "runs", "tla2tools.jar", "tlcjob.sh"}
	for _, line := range runs {
		ctx = append(ctx, strings.Fields(line)[1]+".cfg")
	}
	if got, want := sortedFields(r.seen(t, job, "ctx")), sortedFields(strings.Join(ctx, " ")); !reflect.DeepEqual(got, want) {
		t.Errorf("job %s context = %v, want %v", job, got, want)
	}
	if got := strings.TrimSpace(r.seen(t, job, "ctxdir")); got != job {
		t.Errorf("contexts present at job %s's submit = %q, want only its own", job, got)
	}
}

// TestRunnerOkRules: a run is ok only for expect=pass with PASS or
// expect=violation with FAIL(...); the last VERDICT line for a cfg wins.
func TestRunnerOkRules(t *testing.T) {
	cases := []struct {
		name, expect string
		results      []string // the cfg's VERDICT results in the log, in order
		ok           bool
		shown        string
	}{
		{"pass, PASS", "pass", []string{"PASS"}, true, "PASS"},
		{"pass, INCOMPLETE", "pass", []string{"INCOMPLETE(budget)"}, false, "INCOMPLETE(budget)"},
		{"pass, ERROR", "pass", []string{"ERROR(exit_12)"}, false, "ERROR(exit_12)"},
		{"pass, SKIPPED", "pass", []string{"SKIPPED(budget)"}, false, "SKIPPED(budget)"},
		{"pass, no verdict", "pass", nil, false, "NO-VERDICT"},
		{"pass, FAIL", "pass", []string{"FAIL(Invariant_Safe_is_violated)"}, false, "FAIL(Invariant_Safe_is_violated)"},
		{"violation, FAIL", "violation", []string{"FAIL(Invariant_Safe_is_violated)"}, true, "FAIL(Invariant_Safe_is_violated)"},
		{"violation, PASS", "violation", []string{"PASS"}, false, "PASS"},
		{"violation, INCOMPLETE", "violation", []string{"INCOMPLETE(depth)"}, false, "INCOMPLETE(depth)"},
		{"violation, no verdict", "violation", nil, false, "NO-VERDICT"},
		{"last line wins, ok", "pass", []string{"INCOMPLETE(budget)", "PASS"}, true, "PASS"},
		{"last line wins, not ok", "pass", []string{"PASS", "ERROR(exit_1)"}, false, "ERROR(exit_1)"},
	}
	r := newRig(t)
	pool := inTier(shipped(t), "fast")
	var rows []row
	ok := 0
	for i, c := range cases {
		rw := pool[i]
		rw.group, rw.expect, rw.what = "g1", c.expect, c.name
		rows = append(rows, rw)
		for _, v := range c.results {
			r.verdict(t, rw.cfg, v)
		}
		if c.ok {
			ok++
		}
	}
	r.env["TLA_SUITE"] = writeSuite(t, rows)

	res := r.run(t)

	want := fmt.Sprintf(`^CI-VERDICT FAIL \(%d/%d runs ok, \d+ min, tier fast\)$`, ok, len(cases))
	if res.code != 1 || !regexp.MustCompile(want).MatchString(res.last()) {
		t.Fatalf("want exit 1 and %s\n%s", want, res)
	}
	lines := res.lines()
	for i, c := range cases {
		m := runLine.FindStringSubmatch(lines[i])
		tag, dist := "FAIL", "42"
		if c.ok {
			tag = "PASS"
		}
		if c.results == nil {
			dist = "?"
		}
		if m == nil || m[1] != tag || m[2] != rows[i].cfg || m[3] != c.shown || m[4] != dist || m[6] != c.name {
			t.Errorf("%s: line %q, want %s %s %s distinct=%s", c.name, lines[i], tag, rows[i].cfg, c.shown, dist)
		}
	}
}

// TestRunnerFailsClosed: each refusal prints FAIL <step> and the CI-VERDICT FAIL
// line, exits 1 at once and submits nothing more.
func TestRunnerFailsClosed(t *testing.T) {
	setEnv := func(k, v string) func(*testing.T, *rig) { return func(_ *testing.T, r *rig) { r.env[k] = v } }
	planned := func(key string, lines ...string) func(*testing.T, *rig) {
		return func(t *testing.T, r *rig) { r.plan(t, key, lines...) }
	}
	appendTo := func(rel string) func(*testing.T, *rig) {
		return func(t *testing.T, r *rig) {
			p := filepath.Join(r.private(t), rel)
			writeFile(t, p, readFile(t, p)+"\n", 0o644)
		}
	}
	queued := state("queued", false)
	ended := []string{"list", "submit", "status", "results"}
	cases := []struct {
		name   string
		setup  func(*testing.T, *rig)
		reason string   // as in CI-VERDICT FAIL (<reason>, tier fast), unless it names its own tier
		fail   []string // stdout substrings: the FAIL line and any others
		calls  []string // the scheduler calls made, in order
		ctx    string   // the parse context afterwards: "" (no run dir yet), "kept" or "gone"
	}{
		{name: "bad tier", setup: setEnv("TLA_TIER", "bogus"), reason: "bad TLA_TIER, tier bogus",
			fail: []string{"FAIL preflight -- TLA_TIER must be fast or full (got 'bogus'); nothing was submitted"}},
		{name: "pin: cfg changed", setup: appendTo("ci/cfg/ci_C1.cfg"), reason: "pin check failed",
			fail: []string{"FAIL pin -- ci/cfg/ci_C1.cfg: SHA-256 expected ", "FAIL preflight -- 1 pin check(s) failed against PROVENANCE.txt; nothing was submitted"}},
		{name: "pin: jar changed", setup: appendTo("tla2tools.jar"), reason: "pin check failed",
			fail: []string{"FAIL pin -- tla2tools.jar: SHA-256 expected ", "FAIL pin -- tla2tools.jar: size expected 4493033 bytes, got 4493034", "2 pin check(s) failed"}},
		{name: "pin: spec changed", setup: appendTo("Phase5Hook.tla"), reason: "pin check failed",
			fail: []string{"FAIL pin -- Phase5Hook.tla: SHA-256 expected "}},
		{name: "pin: line missing", setup: func(t *testing.T, r *rig) {
			p := filepath.Join(r.private(t), "PROVENANCE.txt")
			re := regexp.MustCompile(`(?m)^[0-9a-f]{64}  jobs/lib/Dockerfile\n`)
			body := readFile(t, p)
			if !re.MatchString(body) {
				t.Fatal("no Dockerfile pin line to drop")
			}
			writeFile(t, p, re.ReplaceAllString(body, ""), 0o644)
		}, reason: "pin check failed", fail: []string{"FAIL pin -- jobs/lib/Dockerfile has no pin line in PROVENANCE.txt"}},
		{name: "manifest: duplicate cfg", setup: func(t *testing.T, r *rig) {
			rw := shipped(t)[0]
			r.env["TLA_SUITE"] = writeSuite(t, []row{rw, rw})
		}, reason: "bad manifest", fail: []string{`line 3: duplicate cfg "ci_C1"; nothing was submitted`}},
		{name: "unknown group", setup: setEnv("TLA_GROUPS", "g1 nope"), reason: "unknown group",
			fail: []string{"FAIL preflight -- group 'nope' is not in the manifest"}},
		{name: "duplicate group", setup: setEnv("TLA_GROUPS", "g1 g1"), reason: "duplicate group",
			fail: []string{"group 'g1' is listed twice in TLA_GROUPS; nothing was submitted"}},
		{name: "channel unset", setup: setEnv("TLA_CHANNEL", ""), reason: "TLA_CHANNEL not set",
			fail: []string{"FAIL preflight -- TLA_CHANNEL is required"}},
		{name: "CLI missing", setup: setEnv("TLA_JOBSCHED", "/nonexistent/jobsched"), reason: "scheduler CLI not found",
			fail: []string{"the job scheduler CLI '/nonexistent/jobsched' was not found or is not executable; pass its absolute path as TLA_JOBSCHED=<path>; nothing was submitted"}},
		{name: "TMPDIR inside the repo", setup: func(t *testing.T, r *rig) {
			r.tmp = filepath.Join(filepath.Dir(filepath.Dir(r.private(t))), "scratch")
			if err := os.Mkdir(r.tmp, 0o755); err != nil {
				t.Fatal(err)
			}
		}, reason: "bad TMPDIR", fail: []string{"lies inside the repo"}},
		{name: "list fails", setup: planned("list", `1 {"error":"store locked"}`), reason: "scheduler list failed",
			fail: []string{"'list --json' exited 1: store locked; nothing was submitted"}, calls: []string{"list"}},
		{name: "dispatcher down", setup: planned("list", `0 {"summary":{"dispatcher_running":false}}`), reason: "dispatcher not running",
			fail: []string{"dispatcher_running=false); a job submitted now would wait"}, calls: []string{"list"}},
		{name: "submit rejected", setup: planned("submit.parse", `2 {"error":"channel not on the allow-list"}`), reason: "submit refused",
			fail:  []string{"FAIL scheduler -- the job scheduler refused job parse (submit exit 2: channel not on the allow-list); nothing more was submitted"},
			calls: []string{"list", "submit"}, ctx: "gone"},
		{name: "submit without id", setup: planned("submit.parse", `0 {"dispatcher_running":true}`), reason: "no job id",
			fail: []string{"returned no job id"}, calls: []string{"list", "submit"}, ctx: "kept"},
		{name: "dispatcher down at submit", setup: planned("submit.parse", `0 {"id":"@ID@","dispatcher_running":false,"timeout_s":10800,"memory":34359738368}`),
			reason: "dispatcher not running", fail: []string{"job parse (j1-parse) was queued but submit reports the dispatcher is not running"},
			calls: []string{"list", "submit"}, ctx: "kept"},
		{name: "status unknown job", setup: planned("status.parse", queued, `3 {"error":"no such job"}`), reason: "status failed",
			fail: []string{"the job scheduler does not know job parse (j1-parse) (status exit 3)"}, calls: []string{"list", "submit", "status", "status"}, ctx: "kept"},
		{name: "unknown state", setup: planned("status.parse", `0 {"state":"paused","terminal":false,"dispatcher_running":true}`), reason: "unknown job state",
			fail: []string{"reports state 'paused'"}, calls: []string{"list", "submit", "status"}, ctx: "kept"},
		{name: "running marked ended", setup: planned("status.parse", state("running", true)), reason: "unknown job state",
			fail: []string{"is 'running' but marked ended"}, calls: []string{"list", "submit", "status"}, ctx: "kept"},
		{name: "dispatcher stops", setup: planned("status.parse", `0 {"state":"queued","terminal":false,"dispatcher_running":false}`), reason: "dispatcher stopped",
			fail: []string{"the dispatcher stopped while job parse (j1-parse) was queued"}, calls: []string{"list", "submit", "status"}, ctx: "kept"},
		{name: "timeout too low", setup: planned("submit.parse", `0 {"id":"@ID@","dispatcher_running":true,"timeout_s":10499,"memory":34359738368}`),
			reason: "scheduler limits too low", fail: []string{"FAIL limits -- the job scheduler gave job parse a run timeout of 10499 s, under the 10500 s needed"},
			calls: ended, ctx: "gone"},
		{name: "memory too low", setup: planned("submit.parse", `0 {"id":"@ID@","dispatcher_running":true,"timeout_s":10800,"memory":25769803775}`),
			reason: "scheduler limits too low", fail: []string{"25769803775 bytes of memory, under the 24 GiB"}, calls: ended, ctx: "gone"},
		{name: "parse error", setup: func(t *testing.T, r *rig) { r.verdict(t, "parse:ci_C7", "ERROR(parse)") }, reason: "parse check failed",
			fail: []string{"FAIL parse-check -- the specs do not parse or evaluate"}, calls: ended, ctx: "gone"},
		{name: "parse log missing", setup: planned("results.parse", `0 {"paths":{},"present":{"run_log":false}}`), reason: "parse check failed",
			fail: []string{"(see no log)"}, calls: ended, ctx: "gone"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			tc.setup(t, r)

			res := r.run(t)

			want := "CI-VERDICT FAIL (" + tc.reason + ", tier fast)"
			if strings.Contains(tc.reason, ", tier ") {
				want = "CI-VERDICT FAIL (" + tc.reason + ")"
			}
			if res.code != 1 || res.last() != want {
				t.Fatalf("want exit 1 and %q\n%s", want, res)
			}
			for _, s := range tc.fail {
				if !strings.Contains(res.stdout, s) {
					t.Errorf("stdout lacks %q\n%s", s, res)
				}
			}
			var got []string
			for _, c := range r.calls(t) {
				got = append(got, c.cmd)
			}
			if !reflect.DeepEqual(got, tc.calls) {
				t.Errorf("scheduler calls = %v, want %v", got, tc.calls)
			}
			if tc.ctx == "" {
				if runDirRe.MatchString(res.stderr) {
					t.Errorf("a run dir was made before the refusal\n%s", res)
				}
				return
			}
			dir := runDir(t, res)
			if kept := exists(filepath.Join(dir, "ctx", "parse")); kept != (tc.ctx == "kept") {
				t.Errorf("parse context kept = %v, want %s", kept, tc.ctx)
			}
		})
	}
}

// TestRunnerOverrides covers TLA_GROUPS, TLA_SPEC_DIR and TLA_SUITE, and a
// group with no rows in the tier.
func TestRunnerOverrides(t *testing.T) {
	all := shipped(t)
	pick := func(cfg, tier string) row { rw := byCfg(t, all, cfg); rw.tier = tier; return rw }
	cases := []struct {
		name    string
		setup   func(*testing.T, *rig)
		submits []string
		code    int
		verdict string
		check   func(*testing.T, *rig, result)
	}{
		{name: "TLA_GROUPS runs the groups in the order given", setup: func(t *testing.T, r *rig) {
			r.expectAll(t, inTier(all, "fast"))
			r.env["TLA_GROUPS"] = "g6 g1"
		}, submits: []string{"parse", "g6", "g1"}, verdict: `^CI-VERDICT PASS \(54/54 runs ok, \d+ min, tier fast\)$`,
			check: func(t *testing.T, r *rig, _ result) {
				checkJob(t, r, "parse", parseRuns(inTier(all, "fast")), 600)
			}},
		{name: "TLA_SPEC_DIR and TLA_SUITE: a broken spec copy fails", setup: func(t *testing.T, r *rig) {
			dir := t.TempDir()
			for _, s := range []string{"Phase4.tla", "Phase4Split.tla", "Phase5Hook.tla"} {
				body := readFile(t, filepath.Join(r.tree, s))
				if s == "Phase5Hook.tla" {
					body += "\\* broken copy\n"
				}
				writeFile(t, filepath.Join(dir, s), body, 0o644)
			}
			r.env["TLA_SPEC_DIR"] = dir
			r.env["TLA_SUITE"] = writeSuite(t, []row{byCfg(t, all, "ci_H_Sns")})
			r.verdict(t, "ci_H_Sns", "FAIL(Invariant_RowOnlyByAgent_is_violated)")
		}, submits: []string{"parse", "g6"}, code: 1, verdict: `^CI-VERDICT FAIL \(0/1 runs ok, \d+ min, tier fast\)$`,
			check: func(t *testing.T, r *rig, res result) {
				broken := fmt.Sprintf("%x", sha256.Sum256([]byte(readFile(t, filepath.Join(r.env["TLA_SPEC_DIR"], "Phase5Hook.tla")))))
				if !strings.Contains(r.seen(t, "g6", "specs"), broken+"  Phase5Hook.tla") {
					t.Errorf("the g6 context does not hold the TLA_SPEC_DIR spec:\n%s", r.seen(t, "g6", "specs"))
				}
				checkJob(t, r, "parse", []string{"Phase5Hook ci_H_Sns -simulate num=1 -depth 1"}, 600)
				if !strings.HasPrefix(res.lines()[0], "FAIL ci_H_Sns ") {
					t.Errorf("run line = %q, want FAIL ci_H_Sns", res.lines()[0])
				}
			}},
		{name: "a group with no rows in the tier is skipped", setup: func(t *testing.T, r *rig) {
			rows := []row{pick("ci_C1", "fast"), byCfg(t, all, "ci_H_S"), pick("ci_H_Sns", "fast")}
			rows[1].group = "g2"
			r.env["TLA_SUITE"] = writeSuite(t, rows)
			r.expectAll(t, rows)
		}, submits: []string{"parse", "g1", "g6"}, verdict: `^CI-VERDICT PASS \(2/2 runs ok, \d+ min, tier fast\)$`,
			check: func(t *testing.T, _ *rig, res result) {
				if !strings.Contains(res.stderr, "# group g2: no rows in tier fast, skipped") {
					t.Errorf("no skip note for g2\n%s", res)
				}
			}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			tc.setup(t, r)

			res := r.run(t)

			if res.code != tc.code || !regexp.MustCompile(tc.verdict).MatchString(res.last()) {
				t.Fatalf("want exit %d and %s\n%s", tc.code, tc.verdict, res)
			}
			if got := r.submits(t); !reflect.DeepEqual(got, tc.submits) {
				t.Errorf("submits = %v, want %v", got, tc.submits)
			}
			tc.check(t, r, res)
		})
	}
}
