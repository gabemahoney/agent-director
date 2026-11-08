package tla_test

import (
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// b.osa R1: an overriding model brings its root, imports and cfgs without Phase modules.
func TestRunnerBOSAAnyModel(t *testing.T) {
	r := newRig(t)
	dir := t.TempDir()
	modules := map[string]string{
		"Fresh_Model1": "---- MODULE Fresh_Model1 ----\nEXTENDS LocalHelper\nSpec == TRUE\n====\n",
		"LocalHelper":  "---- MODULE LocalHelper ----\nAnswer == 42\n====\n",
	}
	for name, body := range modules {
		writeFile(t, filepath.Join(dir, name+".tla"), body, 0o644)
	}
	rw := row{"fresh", "Fresh_Model1", "fresh_check", 30, "pass", "fast", "new model with a local import"}
	cfg := "SPECIFICATION Spec\n"
	writeFile(t, filepath.Join(dir, "cfg", rw.cfg+".cfg"), cfg, 0o644)
	r.env["TLA_SPEC_DIR"] = dir
	r.env["TLA_SUITE"] = writeManifest(t, dir, fields(rw))
	r.expectAll(t, []row{rw})

	res := r.run(t)

	if res.code != 0 || !regexp.MustCompile(`^CI-VERDICT PASS \(1/1 runs ok, \d+ min, tier fast\)$`).MatchString(res.last()) {
		t.Fatalf("a non-Phase model with its helper and own cfg must pass preflight and run\n%s", res)
	}
	if got, want := r.submits(t), []string{"parse", "fresh"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("submits = %v, want %v", got, want)
	}
	wantSums := map[string]string{}
	for name, body := range modules {
		wantSums[name+".tla"] = fmt.Sprintf("%x", sha256.Sum256([]byte(body)))
	}
	for _, job := range r.submits(t) {
		// Each file name keeps its own hash, so contents swapped between modules fail.
		sums := map[string]string{}
		for _, line := range strings.Split(strings.TrimSpace(r.seen(t, job, "specs")), "\n") {
			sum, name, _ := strings.Cut(line, "  ")
			sums[name] = sum
		}
		if !reflect.DeepEqual(sums, wantSums) {
			t.Errorf("job %s must package every local module unchanged under its name: got %v, want %v", job, sums, wantSums)
		}
		wantCfg := fmt.Sprintf("%x  ./%s.cfg\n", sha256.Sum256([]byte(cfg)), rw.cfg)
		if got := r.seen(t, job, "cfgs"); got != wantCfg {
			t.Errorf("job %s cfgs = %q, want %q", job, got, wantCfg)
		}
	}
	for job, runs := range map[string]string{
		"parse": "Fresh_Model1 - sany\nFresh_Model1 fresh_check -simulate num=1 -depth 1\n",
		"fresh": "Fresh_Model1 fresh_check @30\n",
	} {
		if got := r.seen(t, job, "runs"); got != runs {
			t.Errorf("job %s runs = %q, want %q", job, got, runs)
		}
	}
}

// b.osa R1: the default spec directory cannot silently package an unpinned helper.
func TestRunnerBOSAUnpinnedModule(t *testing.T) {
	r := newRig(t)
	writeFile(t, filepath.Join(r.private(t), "UnpinnedHelper.tla"), "---- MODULE UnpinnedHelper ----\n====\n", 0o644)

	res := r.run(t, "--print")

	if res.code != 1 || res.last() != "CI-VERDICT FAIL (pin check failed, tier fast)" ||
		!strings.Contains(res.stdout, "FAIL pin -- UnpinnedHelper.tla has no pin line in PROVENANCE.txt\n") {
		t.Errorf("default spec directory must refuse an unpinned .tla before packaging\n%s", res)
	}
	if got := r.calls(t); len(got) != 0 {
		t.Errorf("unpinned module contacted scheduler: %v", got)
	}
}

// b.osa R2: SANY covers each manifest module once, including unselected tiers/groups;
// a missing module verdict fails even when other modules parse successfully.
func TestRunnerBOSAParseOnly(t *testing.T) {
	for _, tc := range []struct {
		name, result, tag string
		code, ok          int
		fullOnly          bool
		prior             string
	}{
		{"pass", "PARSE-OK", "PASS", 0, 3, false, ""},
		{"parse_failure", "PARSE-FAIL(exit_1)", "FAIL", 1, 2, false, ""},
		{"missing_verdict", "", "FAIL", 1, 2, false, ""},
		{"full_only_manifest", "PARSE-OK", "PASS", 0, 3, true, ""},
		{"cap_then_ok", "PARSE-OK", "FAIL", 1, 3, false, "INCOMPLETE(time-budget)"}, // CR-1.2: a cap remains fatal.
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			all := shipped(t)
			rows := []row{byCfg(t, all, "ci_C1"), byCfg(t, all, "ci_C2"), byCfg(t, all, "ci_C7"), byCfg(t, all, "ci_H_Sns")}
			rows[1].group = "duplicate"
			rows[2].group, rows[2].tier = "full_only", "full"
			if tc.fullOnly {
				for i := range rows {
					rows[i].tier = "full"
				}
			}
			r.env["TLA_SUITE"] = writeSuite(t, rows)
			r.env["TLA_GROUPS"] = "g1"
			if tc.prior != "" {
				r.verdict(t, "sany:Phase4", tc.prior)
			}
			r.verdict(t, "sany:Phase4", tc.result)
			wantLines := []string{"PASS sany:Phase4Split PARSE-OK", "PASS sany:Phase4 PARSE-OK", "PASS sany:Phase5Hook PARSE-OK"}
			shown := tc.result
			if shown == "" {
				shown = "NO-VERDICT"
			}
			moduleTag := "PASS"
			if tc.result != "PARSE-OK" {
				moduleTag = "FAIL"
			}
			wantLines[1] = moduleTag + " sany:Phase4 " + shown

			res := r.run(t, "--parse")

			want := fmt.Sprintf(`^CI-VERDICT %s \(%d/3 modules parse, \d+ min, parse-only\)$`, tc.tag, tc.ok)
			if res.code != tc.code || !regexp.MustCompile(want).MatchString(res.last()) {
				t.Fatalf("parse-only must exit %d with %s\n%s", tc.code, want, res)
			}
			if got := res.lines(); !reflect.DeepEqual(got[:len(got)-1], wantLines) {
				t.Errorf("module verdicts = %v, want %v", got[:len(got)-1], wantLines)
			}
			if got := r.submits(t); !reflect.DeepEqual(got, []string{"parse"}) {
				t.Fatalf("parse-only submits = %v, want one parse job", got)
			}
			if got, want := r.seen(t, "parse", "runs"), "Phase4Split - sany\nPhase4 - sany\nPhase5Hook - sany\n"; got != want {
				t.Errorf("parse-only runs = %q, want %q", got, want)
			}
		})
	}
}

// b.osa R2: normal runs must gate exhaustive jobs on SANY as well as depth-1 evaluation.
func TestRunnerBOSASANYGate(t *testing.T) {
	r := newRig(t)
	all := shipped(t)
	rows := []row{byCfg(t, all, "ci_C1"), byCfg(t, all, "ci_C7")}
	rows[1].group, rows[1].tier = "full_only", "full"
	r.env["TLA_SUITE"] = writeSuite(t, rows)
	r.env["TLA_GROUPS"] = "g1"
	r.expectAll(t, rows)
	r.verdict(t, "sany:Phase4", "PARSE-FAIL(exit_1)")

	res := r.run(t)

	runs := r.seen(t, "parse", "runs")
	if want := "Phase4Split - sany\nPhase4 - sany\n"; !strings.HasPrefix(runs, want) ||
		!strings.Contains(runs, "Phase4Split ci_C1 -simulate num=1 -depth 1\n") {
		t.Errorf("normal first job must carry all manifest modules' SANY before depth-1 evaluation: got %q", runs)
	}
	if res.code != 1 || !strings.HasPrefix(res.last(), "CI-VERDICT FAIL (") {
		t.Errorf("SANY failure must fail the normal run\n%s", res)
	}
	if got := r.submits(t); !reflect.DeepEqual(got, []string{"parse"}) {
		t.Errorf("SANY failure submitted later jobs: %v", got)
	}
}

// b.osa R3: every job asks for the fixed memory/disk and its own budget plus margin.
func TestRunnerBOSALimits(t *testing.T) {
	r := newRig(t)
	rows := []row{byCfg(t, shipped(t), "ci_C1"), byCfg(t, shipped(t), "ci_H_Sns")}
	r.env["TLA_SUITE"] = writeSuite(t, rows)
	r.expectAll(t, rows)

	res := r.run(t)

	if res.code != 0 {
		t.Fatalf("limits fixture must otherwise succeed\n%s", res)
	}
	if got, want := r.submits(t), []string{"parse", "g1", "g6"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("submits = %v, want %v", got, want)
	}
	for _, job := range r.submits(t) {
		budget := 600
		if job != "parse" {
			_, budget = groupRuns(rows, job)
		}
		args := strings.Split(strings.TrimSpace(r.seen(t, job, "submit-args")), "\n")
		for flag, want := range map[string]string{"--memory": "28g", "--disk": "20g", "--timeout": fmt.Sprintf("%ds", budget+300)} {
			var got []string
			for i := 0; i+1 < len(args); i++ {
				if args[i] == flag {
					got = append(got, args[i+1])
				}
			}
			if !reflect.DeepEqual(got, []string{want}) {
				t.Errorf("job %s submit %s = %v, want exactly %s", job, flag, got, want)
			}
		}
		if got, want := r.seen(t, job, "limits"), fmt.Sprintf("BUDGET=${BUDGET:-%d}\nDISKCAP_MB=${DISKCAP_MB:-15000}\n", budget); got != want {
			t.Errorf("job %s context limits = %q, want %q", job, got, want)
		}
	}
}

// b.osa R4: terminal wait answers avoid status; timeout checks status then waits again.
func TestRunnerBOSAWait(t *testing.T) {
	for _, timeout := range []bool{false, true} {
		t.Run(fmt.Sprintf("timeout=%t", timeout), func(t *testing.T) {
			r := newRig(t)
			rows := []row{byCfg(t, shipped(t), "ci_C1")}
			r.env["TLA_SUITE"] = writeSuite(t, rows)
			r.expectAll(t, rows)
			want := []call{{"list", ""}, {"submit", "parse"}, {"wait", "parse"}}
			if timeout {
				r.plan(t, "wait.parse", "0 timeout", "0 @ID@ succeeded")
				r.plan(t, "status.parse", state("running", false), state("succeeded", true))
				want = append(want, call{"status", "parse"}, call{"wait", "parse"})
			}
			want = append(want, call{"results", "parse"}, call{"submit", "g1"}, call{"wait", "g1"}, call{"results", "g1"})

			res := r.run(t)

			if res.code != 0 {
				t.Fatalf("wait fixture must otherwise succeed\n%s", res)
			}
			if got := r.calls(t); !reflect.DeepEqual(got, want) {
				t.Errorf("scheduler must wait, checking status only on timeout:\n got %v\nwant %v", got, want)
			}
			for _, line := range strings.Split(readFile(t, filepath.Join(r.fake, "calls")), "\n") {
				if strings.HasPrefix(line, "wait ") && !regexp.MustCompile(`^wait j\d+-(parse|g1) --timeout [1-9][0-9]*[smh]$`).MatchString(line) {
					t.Errorf("wait must name its job and an explicit timeout: %q", line)
				}
			}
		})
	}
}

// b.osa R5: one capped group prevents every subsequent group and reports their rows.
func TestRunnerBOSAStopAfterCap(t *testing.T) {
	r := newRig(t)
	all := shipped(t)
	rows := []row{byCfg(t, all, "ci_C1"), byCfg(t, all, "ci_H_Sns"), byCfg(t, all, "ci_H_Rns")}
	r.env["TLA_SUITE"] = writeSuite(t, rows)
	r.verdict(t, rows[0].cfg, "INCOMPLETE(time-budget)")
	r.expectAll(t, rows[1:])

	res := r.run(t)

	if got := r.submits(t); !reflect.DeepEqual(got, []string{"parse", "g1"}) {
		t.Errorf("capped first group must stop submissions: got %v, want [parse g1]", got)
	}
	if res.code != 1 || !regexp.MustCompile(`^CI-VERDICT FAIL \(0/3 runs ok, \d+ min, tier fast\)$`).MatchString(res.last()) {
		t.Errorf("cap must fail with every planned row counted\n%s", res)
	}
	lines := res.lines()
	if len(lines) != len(rows)+1 {
		t.Fatalf("want a verdict for all %d rows\n%s", len(rows), res)
	}
	for i, rw := range rows {
		want := "NOT-RUN(after-cap)"
		if i == 0 {
			want = "INCOMPLETE(time-budget)"
		}
		m := runLine.FindStringSubmatch(lines[i])
		if m == nil || m[1] != "FAIL" || m[2] != rw.cfg || m[3] != want || m[6] != rw.what {
			t.Errorf("row %s = %q, want FAIL %s %s -- %s", rw.cfg, lines[i], rw.cfg, want, rw.what)
		}
	}
}

// b.osa: wait errors and unusable answers fail closed without fetching results
// or removing a context whose job may still be running.
func TestRunnerBOSAWaitRefusals(t *testing.T) {
	for _, tc := range []struct {
		name, answer, reason, detail string
	}{
		{"malformed", "0 garbage", "unknown job state", "returned an unreadable answer"},
		{"nonterminal", "0 @ID@ running", "unknown job state", "reports state 'running'"},
		{"nonzero_exit", "3 @ID@ succeeded", "wait failed", "exited 3"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			rows := []row{byCfg(t, shipped(t), "ci_C1")}
			r.env["TLA_SUITE"] = writeSuite(t, rows)
			r.expectAll(t, rows)
			r.plan(t, "wait.parse", tc.answer)

			res := r.run(t)

			want := "FAIL scheduler -- wait for job parse (j1-parse) " + tc.detail + "; nothing more was submitted\n" +
				"CI-VERDICT FAIL (" + tc.reason + ", tier fast)\n"
			if res.code != 1 || res.stdout != want {
				t.Errorf("want exit 1 and %q\n%s", want, res)
			}
			wantCalls := []call{{"list", ""}, {"submit", "parse"}, {"wait", "parse"}}
			if got := r.calls(t); !reflect.DeepEqual(got, wantCalls) {
				t.Errorf("wait refusal calls = %v, want %v", got, wantCalls)
			}
			if !exists(filepath.Join(runDir(t, res), "ctx", "parse", "runs")) {
				t.Error("wait refusal removed the potentially live job's context")
			}
		})
	}
}

// b.osa: repeated timeout answers reach the existing overrun deadline. The
// clock advances with fake wait answers, never with real sleeps or date-call counts.
func TestRunnerBOSAWaitOverrun(t *testing.T) {
	r := newRig(t)
	rows := []row{byCfg(t, shipped(t), "ci_C1")}
	r.env["TLA_SUITE"] = writeSuite(t, rows)
	r.expectAll(t, rows)
	writeFile(t, filepath.Join(r.fake, "now"), "1000\n", 0o644)
	writeFile(t, filepath.Join(r.bin, "date"), "#!/bin/sh\ncat \"$FAKE_DIR/now\"\n", 0o755)
	// First running observation is at 1000; timeout 900 + margin 3600 puts
	// the deadline at 5500. The second wait stays one second below it.
	r.plan(t, "clock.wait.parse", "1000", "5499", "5500")
	r.plan(t, "status.parse", state("running", false))
	// A terminal fourth answer bounds the test if the deadline is wrongly reset.
	r.plan(t, "wait.parse", "0 timeout", "0 timeout", "0 timeout", "0 @ID@ succeeded")

	res := r.run(t)

	want := "FAIL scheduler -- job parse (j1-parse) is still running more than 3600 s past its 900 s run timeout; nothing more was submitted\n" +
		"CI-VERDICT FAIL (job overran, tier fast)\n"
	if res.code != 1 || res.stdout != want {
		t.Errorf("want exit 1 at the overrun deadline and %q\n%s", want, res)
	}
	wantCalls := []call{{"list", ""}, {"submit", "parse"},
		{"wait", "parse"}, {"status", "parse"},
		{"wait", "parse"}, {"status", "parse"},
		{"wait", "parse"}, {"status", "parse"}}
	if got := r.calls(t); !reflect.DeepEqual(got, wantCalls) {
		t.Errorf("overrun calls = %v, want %v", got, wantCalls)
	}
	if !exists(filepath.Join(runDir(t, res), "ctx", "parse", "runs")) {
		t.Error("overrun removed the still-running job's context")
	}
}

// b.osa: every manifest module is validated before tier/group selection.
func TestRunnerBOSAModulePreflight(t *testing.T) {
	for _, tc := range []struct {
		name, spec, reason string
	}{
		{"missing_outside_tier", "MissingModule", "spec missing"},
		{"leading_digit", "1Model", "bad manifest"},
		{"leading_underscore", "_Model", "bad manifest"},
		{"punctuation", "Model-name", "bad manifest"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			all := shipped(t)
			rows := []row{byCfg(t, all, "ci_C1"), byCfg(t, all, "ci_C7")}
			rows[1].spec, rows[1].tier, rows[1].group = tc.spec, "full", "full_only"
			r.env["TLA_SUITE"] = writeSuite(t, rows)
			r.env["TLA_GROUPS"] = "g1"

			res := r.run(t)

			detail := fmt.Sprintf("manifest '%s': line 3: bad spec %q", r.env["TLA_SUITE"], tc.spec)
			if tc.reason == "spec missing" {
				detail = filepath.Join(r.tree, tc.spec+".tla") + " is not a readable file"
			}
			want := "FAIL preflight -- " + detail + "; nothing was submitted\n" +
				"CI-VERDICT FAIL (" + tc.reason + ", tier fast)\n"
			if res.code != 1 || res.stdout != want {
				t.Errorf("want exit 1 and %q\n%s", want, res)
			}
			if got := r.calls(t); len(got) != 0 {
				t.Errorf("module preflight refusal contacted scheduler: %v", got)
			}
		})
	}
}

// b.osa CR-1.2: a parse-stage cap reports every planned row as NOT-RUN(after-cap).
func TestRunnerBOSAParseCap(t *testing.T) {
	for _, key := range []string{"sany:Phase4Split", "parse:ci_C1"} {
		t.Run(key, func(t *testing.T) {
			r := newRig(t)
			all := shipped(t)
			rows := []row{byCfg(t, all, "ci_C1"), byCfg(t, all, "ci_H_Sns")}
			r.env["TLA_SUITE"] = writeSuite(t, rows)
			r.expectAll(t, rows)
			r.verdict(t, key, "INCOMPLETE(time-budget)")

			res := r.run(t)

			dir := runDir(t, res)
			if res.code != 1 || !regexp.MustCompile(`^CI-VERDICT FAIL \(0/2 runs ok, \d+ min, tier fast\)$`).MatchString(res.last()) {
				t.Errorf("parse cap must fail with both planned rows counted\n%s", res)
			}
			lines := res.lines()
			if len(lines) != len(rows)+1 {
				t.Fatalf("want a NOT-RUN verdict for each planned row\n%s", res)
			}
			for i, rw := range rows {
				m := runLine.FindStringSubmatch(lines[i])
				if m == nil || m[1] != "FAIL" || m[2] != rw.cfg || m[3] != "NOT-RUN(after-cap)" || m[4] != "?" || m[5] != "?" || m[6] != rw.what {
					t.Errorf("parse-cap row %s = %q, want FAIL %s NOT-RUN(after-cap)", rw.cfg, lines[i], rw.cfg)
				}
			}
			wantCalls := []call{{"list", ""}, {"submit", "parse"}, {"wait", "parse"}, {"results", "parse"}}
			if got := r.calls(t); !reflect.DeepEqual(got, wantCalls) {
				t.Errorf("parse cap calls = %v, want %v", got, wantCalls)
			}
			if exists(filepath.Join(dir, "ctx", "parse")) {
				t.Error("terminal parse job's context was not removed")
			}
		})
	}
}

// b.osa CR-1.2: a final group's earlier cap fails the suite even when its last
// row verdict is PASS; row presentation still follows the last-verdict rule.
func TestRunnerBOSAFinalGroupCapThenPass(t *testing.T) {
	r := newRig(t)
	rw := byCfg(t, shipped(t), "ci_H_Sns")
	r.env["TLA_SUITE"] = writeSuite(t, []row{rw})
	r.verdict(t, rw.cfg, "INCOMPLETE(time-budget)")
	r.verdict(t, rw.cfg, "PASS")

	res := r.run(t)

	if res.code != 1 || !regexp.MustCompile(`^CI-VERDICT FAIL \(1/1 runs ok, \d+ min, tier fast\)$`).MatchString(res.last()) {
		t.Errorf("a final-group cap cannot be erased by a later PASS\n%s", res)
	}
	if len(res.lines()) != 2 {
		t.Fatalf("want one row and the failing suite verdict\n%s", res)
	}
	m := runLine.FindStringSubmatch(res.lines()[0])
	if m == nil || m[1] != "PASS" || m[2] != rw.cfg || m[3] != "PASS" {
		t.Errorf("last row verdict must still be presented as PASS: %q", res.lines()[0])
	}
	if got := r.submits(t); !reflect.DeepEqual(got, []string{"parse", rw.group}) {
		t.Errorf("submits = %v, want parse then the single final group", got)
	}
}

// b.osa CR-1.2: depth-1 evaluation uses the last verdict, as SANY and groups do.
func TestRunnerBOSAParseLatestVerdict(t *testing.T) {
	r := newRig(t)
	rows := []row{byCfg(t, shipped(t), "ci_C1")}
	r.env["TLA_SUITE"] = writeSuite(t, rows)
	r.expectAll(t, rows)
	r.verdict(t, "parse:ci_C1", "PARSE-OK")
	r.verdict(t, "parse:ci_C1", "ERROR(exit_17)")

	res := r.run(t)

	want := "FAIL parse-check -- the specs do not parse or evaluate (see " + filepath.Join(runDir(t, res), "logs", "parse.run.log") + ")\n" +
		"CI-VERDICT FAIL (parse check failed, tier fast)\n"
	if res.code != 1 || res.stdout != want {
		t.Errorf("a late depth-1 error must fail the parse check: want exit 1 and %q\n%s", want, res)
	}
	if got := r.submits(t); !reflect.DeepEqual(got, []string{"parse"}) {
		t.Errorf("late depth-1 error allowed a group submit: %v", got)
	}
}
