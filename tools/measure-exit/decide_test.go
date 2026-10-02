package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	adconfig "github.com/gabemahoney/agent-director/internal/config"
)

// curDefaults are the values in force that decide compares against.
var curDefaults = currentDefaults{KillExitWaitMs: 5000, StoppingWindowSec: 90, MinStoppingWindowSec: 30, MinClaudeCode: "2.1.285"}

// repeat is n samples with outcome o, each ms long (ms < 0: no time).
func repeat(n int, o outcome, ms int64) []sample {
	out := make([]sample, n)
	for i := range out {
		out[i] = smp(o, ms)
	}
	return out
}

// sampledCase is a finalized RN-6 or RN-2 case holding the samples in order,
// with the raised layer's budget on a raised case.
func sampledCase(id string, parts ...[]sample) caseResult {
	fam := familyRN6
	if strings.HasPrefix(id, "rn2.") {
		fam = familyRN2
	}
	var samples []sample
	for _, p := range parts {
		samples = append(samples, p...)
	}
	c := caseOf(id, fam, samples...)
	if strings.Contains(id, ".raised-") {
		c.Budgets.SessionEndHooks = append(c.Budgets.SessionEndHooks, hookBudget{Layer: layerGenerated, Program: "sleep", Timeout: "3 s"})
	}
	return c
}

// measured is a sampled case with 20 completed samples whose largest is ms.
func measured(id string, ms int64) caseResult {
	return sampledCase(id, repeat(1, outcomeCompleted, ms), repeat(minSamples-1, outcomeCompleted, ms/2))
}

// finishedRun is a finished, guarded real-mode results directory.
func finishedRun(dir string, m mode) decideInput {
	r := resultsOf(m)
	at := clockStart
	r.FinishedAt = &at
	return decideInput{Dir: dir, Res: *r, Guard: "pass"}
}

// l0 is the version probe's directory with versions as "V:result".
func l0(versions ...string) decideInput {
	in := finishedRun("l0", modeProbe)
	in.Res.Probe = &probeSection{}
	for _, v := range versions {
		ver, res, _ := strings.Cut(v, ":")
		in.Res.Probe.Versions = append(in.Res.Probe.Versions, probeVersion{Version: ver, Result: res})
	}
	return in
}

// l1 is RN-6 and RN-2 with every default-budget RN-6 case at rn6 ms, every
// raised case at 9 s and every RN-2 case at rn2 ms.
func l1(rn6, rn2 int64) decideInput {
	in := finishedRun("l1", modeReal)
	for _, id := range rn6DefaultCases {
		in.Res.Cases = append(in.Res.Cases, measured(id, rn6))
	}
	for _, id := range rn6RaisedCases {
		in.Res.Cases = append(in.Res.Cases, measured(id, 9000))
	}
	for _, id := range rn2Cases {
		in.Res.Cases = append(in.Res.Cases, measured(id, rn2))
	}
	return in
}

// l2 is RN-9 with every scenario passed and RN-7's record.
func l2() decideInput {
	in := finishedRun("l2", modeReal)
	in.Res.RN9 = &rn9Section{}
	for _, id := range rn9ScenarioIDs {
		in.Res.RN9.Scenarios = append(in.Res.RN9.Scenarios, rn9Scenario{ID: id, Verdict: verdictPass, Hooks: []hookObservation{}})
	}
	in.Res.RN7 = &rn7Record{Rows: []rn7Row{{Seq: 1, Event: "SessionStart", TranscriptPresent: true}}}
	return in
}

// standard is a decidable L0, L1 and L2 at the current values.
func standard() []decideInput {
	return []decideInput{l0("2.1.120:args_not_received", "2.1.285:args_received"), l1(2500, 10000), l2()}
}

// caseIn edits case id of the L1 input.
func caseIn(ins []decideInput, id string, edit func(c *caseResult)) []decideInput {
	for i := range ins[1].Res.Cases {
		if ins[1].Res.Cases[i].ID == id {
			edit(&ins[1].Res.Cases[i])
		}
	}
	return ins
}

// samplesIn replaces case id of the L1 input with one holding the samples.
func samplesIn(id string, parts ...[]sample) []decideInput {
	return caseIn(standard(), id, func(c *caseResult) { *c = sampledCase(id, parts...) })
}

// scenario edits RN-9 scenario id of the L2 input.
func scenario(ins []decideInput, id string, edit func(s *rn9Scenario)) []decideInput {
	for i := range ins[2].Res.RN9.Scenarios {
		if ins[2].Res.RN9.Scenarios[i].ID == id {
			edit(&ins[2].Res.RN9.Scenarios[i])
		}
	}
	return ins
}

func TestDecide(t *testing.T) {
	withL1 := func(rn6, rn2 int64) []decideInput {
		ins := standard()
		ins[1] = l1(rn6, rn2)
		return ins
	}
	withL0 := func(versions ...string) []decideInput {
		ins := standard()
		ins[0] = l0(versions...)
		return ins
	}
	dropCases := func(prefix string) []decideInput {
		ins := standard()
		var kept []caseResult
		for _, c := range ins[1].Res.Cases {
			if !strings.HasPrefix(c.ID, prefix) {
				kept = append(kept, c)
			}
		}
		ins[1].Res.Cases = kept
		return ins
	}
	tests := []struct {
		name string
		ins  []decideInput
		exit int
		text string // in the record
	}{
		// RN-6
		{"E equals the current value", standard(), exitDecided, "Decision: 5000 ms stands (it equals the requirement)"},
		{"E rounds up to a whole second", withL1(2501, 10000), exitDecided, "Required E = ceil(2 x 2501 ms) = 6 s"},
		{"a higher E is raised", withL1(2501, 10000), exitDecided, "raise kill_exit_wait_ms from 5000 ms to 6000 ms"},
		{"E of 7 s keeps the ceiling within 15 s", withL1(3500, 10000), exitDecided,
			"path (i) 2Q + 2A + E + 4W = 7.4 s + 7 s = 14.4 s; path (ii) 3Q + 2A + 5W = 9.0 s; ceiling max(path (i), path (ii)) = 14.4 s"},
		{"path (ii) is the ceiling at a small E", withL1(400, 10000), exitStop,
			"path (i) 2Q + 2A + E + 4W = 7.4 s + 1 s = 8.4 s; path (ii) 3Q + 2A + 5W = 9.0 s; ceiling max(path (i), path (ii)) = 9.0 s"},
		{"E of 8 s is STOP", withL1(3501, 10000), exitStop, "STOP: RN-6: E = 8 s puts kill's ceiling at 15.4 s (STOP at E >= 8 s; limit 15.0 s)"},
		{"a lower E is STOP and not applied", withL1(1000, 10000), exitStop, "Decision: 5000 ms stands (never lowered without the user)"},
		{"the largest default case sets E", samplesIn("rn6.midturn", repeat(1, outcomeCompleted, 3000), repeat(19, outcomeCompleted, 1250)),
			exitDecided, "Largest default-budget time: 3000 ms"},
		{"raised times are reported, not used", standard(), exitDecided, "Largest default-budget time: 2500 ms"},
		// samples: the first 20 usable, in recorded order
		{"a larger completed sample past the 20th is dropped", samplesIn("rn6.idle", repeat(20, outcomeCompleted, 2500), repeat(2, outcomeCompleted, 9000)),
			exitDecided, "- rn6.idle: 22 recorded (22 completed, did not exit 0, no ended_at 0, failed 0); used the first 20 completed, largest 2.5 s; dropped 2 (completed 2)"},
		{"failed and unready samples are dropped", samplesIn("rn6.idle", repeat(1, outcomeFailed, -1), repeat(1, outcomeNotReady, -1), repeat(20, outcomeCompleted, 2500)),
			exitDecided, "22 recorded (20 completed, did not exit 0, no ended_at 0, failed 2); used the first 20 completed, largest 2.5 s; dropped 2 (did_not_reach_prompt 1, failed 1)"},
		{"fewer than 20 completed", samplesIn("rn2.natural", repeat(19, outcomeCompleted, 10000), repeat(3, outcomeFailed, -1)),
			exitInvalid, "case rn2.natural has 19 completed samples, fewer than 20"},
		{"did not exit at the default budget", samplesIn("rn6.idle", repeat(1, outcomeDidNotExit, -1), repeat(20, outcomeCompleted, 2500)),
			exitInvalid, "case rn6.idle: 1 samples did not exit under the default SessionEnd budget"},
		{"did not exit at the default past the 20th completed", samplesIn("rn6.midturn", repeat(21, outcomeCompleted, 2500), repeat(1, outcomeDidNotExit, -1)),
			exitInvalid, "case rn6.midturn: 1 samples did not exit under the default SessionEnd budget"},
		{"did not exit at the default with 19 completed", samplesIn("rn2.pause", repeat(19, outcomeCompleted, 10000), repeat(1, outcomeDidNotExit, -1)),
			exitInvalid, "case rn2.pause has 19 completed samples, fewer than 20"},
		{"did not exit at a raised budget", samplesIn("rn6.idle.raised-hook", repeat(19, outcomeCompleted, 9000), repeat(1, outcomeDidNotExit, -1)), exitDecided,
			"rn6.idle.raised-hook: 1 agents did not exit within the sample ceiling under the raised budget (allowed; reported for the README)"},
		{"a raised sample past the 20th measured is dropped", samplesIn("rn6.midturn.raised-env", repeat(19, outcomeCompleted, 9000),
			repeat(1, outcomeDidNotExit, -1), repeat(1, outcomeCompleted, 9500), repeat(1, outcomeDidNotExit, -1)),
			exitDecided, "used the first 20 measured, largest 9.0 s; dropped 2 (completed 1, did_not_exit 1)"},
		{"19 measured at a raised budget", samplesIn("rn6.idle.raised-env", repeat(18, outcomeCompleted, 9000), repeat(1, outcomeDidNotExit, -1), repeat(3, outcomeFailed, -1)),
			exitInvalid, "case rn6.idle.raised-env has 19 measured samples (18 completed, 1 did not exit), fewer than 20"},
		{"stored counts that disagree with the samples", caseIn(standard(), "rn6.idle", func(c *caseResult) { c.Completed = 21 }),
			exitInvalid, "case rn6.idle: its counts or largest time disagree with its 20 samples"},
		{"a stored largest time that disagrees with the samples", caseIn(standard(), "rn2.mcp", func(c *caseResult) { *c.LargestMillis = 3000 }),
			exitInvalid, "case rn2.mcp: its counts or largest time disagree with its 20 samples"},
		// RN-2
		{"RN-2 at 15 s exactly", withL1(2500, 15000), exitDecided, "stopping window minimum: 15000 ms <= 15000 ms (half of 30 s): 30 s stands"},
		{"RN-2 just above 15 s", withL1(2500, 15001), exitDecided, "stopping window minimum: 15001 ms > 15000 ms (half of 30 s): raise to 31 s"},
		{"RN-2 at 45 s exactly", withL1(2500, 45000), exitDecided, "stopping window default: 45000 ms <= 45000 ms (half of 90 s): 90 s stands"},
		{"RN-2 just above 45 s", withL1(2500, 45001), exitDecided, "stopping window default: 45001 ms > 45000 ms (half of 90 s): raise to 91 s"},
		{"RN-2 not run", dropCases("rn2."), exitDecided, "RN-2 not run: 90 s and 30 s stand."},
		// inputs
		{"a dry-run flag", func() []decideInput { ins := standard(); ins[1].Res.DryRun = true; return ins }(), exitInvalid, "l1: a dry run"},
		{"dry mode", func() []decideInput { ins := standard(); ins[2].Res.Isolation.Mode = modeDry; return ins }(), exitInvalid, "l2: a dry run"},
		{"another mode", func() []decideInput { ins := standard(); ins[2].Res.Isolation.Mode = "fast"; return ins }(), exitInvalid, `mode "fast" is not real or probe`},
		{"another schema", func() []decideInput { ins := standard(); ins[1].Res.Schema = 2; return ins }(), exitInvalid, "results schema 2"},
		{"an unfinished run", func() []decideInput { ins := standard(); ins[1].Res.FinishedAt = nil; return ins }(), exitInvalid, "l1: the run did not finish"},
		{"no guard status", func() []decideInput { ins := standard(); ins[0].Guard = "missing"; return ins }(), exitInvalid, "l0: the host guard did not pass (missing)"},
		{"a failed guard", func() []decideInput { ins := standard(); ins[2].Guard = "fail 1"; return ins }(), exitInvalid, "the host guard did not pass (fail 1)"},
		{"a missing case", dropCases("rn6.idle.raised-env"), exitInvalid, "case rn6.idle.raised-env is missing"},
		{"a hook timeout at the default", caseIn(standard(), "rn6.idle", func(c *caseResult) {
			c.Budgets.SessionEndHooks = []hookBudget{{Layer: layerUser, Program: "notify", Timeout: "10 s"}}
		}), exitInvalid, "budgets not at their defaults (user layer SessionEnd hook notify has timeout 10 s)"},
		{"the budget variable at the default", caseIn(standard(), "rn2.pause", func(c *caseResult) { c.Budgets.EnvEffective = "9000 (container environment)" }),
			exitInvalid, sessionEndBudgetEnv + " is 9000"},
		{"an unread layer at the default", caseIn(standard(), "rn6.mcp", func(c *caseResult) { c.Budgets.Warnings = []string{"project layer: bad"} }),
			exitInvalid, "unread layer: project layer: bad"},
		{"an MCP case not run", caseIn(caseIn(standard(), "rn6.mcp", func(c *caseResult) { *c = caseResult{ID: c.ID, NotRun: "no MCP configuration"} }),
			"rn2.mcp", func(c *caseResult) { *c = caseResult{ID: c.ID, NotRun: "no MCP configuration"} }), exitDecided, "rn6.mcp: not run (no MCP configuration)"},
		{"another case not run", caseIn(standard(), "rn6.idle", func(c *caseResult) { *c = caseResult{ID: c.ID, NotRun: "x"} }), exitInvalid, "case rn6.idle was not run: x"},
		{"a case in two inputs", func() []decideInput {
			ins := standard()
			ins[2].Res.Cases = []caseResult{measured("rn2.pause", 10000)}
			return ins
		}(), exitInvalid, "case rn2.pause is in two inputs"},
		{"a scenario in two inputs", func() []decideInput {
			ins := standard()
			ins[1].Res.RN9 = &rn9Section{Scenarios: []rn9Scenario{{ID: rn9DriveID, Verdict: verdictPass}}}
			return ins
		}(), exitInvalid, "RN-9 scenario rn9.drive is in two inputs"},
		// RN-9
		{"a missing scenario", func() []decideInput {
			ins := standard()
			ins[2].Res.RN9.Scenarios = ins[2].Res.RN9.Scenarios[:3]
			return ins
		}(), exitInvalid, "RN-9 scenario rn9.team-splitpane is missing"},
		{"an inconclusive scenario", scenario(standard(), rn9ResumeID, func(s *rn9Scenario) { s.Verdict, s.Reason = verdictInconclusive, "cut short" }),
			exitInvalid, "RN-9 scenario rn9.resume is inconclusive: cut short"},
		{"a scenario not run", scenario(standard(), rn9DriveID, func(s *rn9Scenario) { s.Verdict = verdictNotRun }), exitInvalid, "rn9.drive is not_run"},
		{"a STOP flag", scenario(standard(), rn9TeamSplitPaneID, func(s *rn9Scenario) {
			s.Verdict, s.Stop = verdictFail, []string{stopSplitPaneApplied}
		}), exitStop, "STOP: RN-9 rn9.team-splitpane: split-pane-teammate-hook-applied"},
		{"a failed scenario without a flag", scenario(standard(), rn9DriveID, func(s *rn9Scenario) { s.Verdict, s.Reason = verdictFail, "seq 3 ignored" }),
			exitStop, "STOP: RN-9 rn9.drive failed: seq 3 ignored"},
		// probe
		{"no probe", standard()[1:], exitInvalid, "the exec-form version probe (L0) is missing"},
		{"a version that did not start", withL0("2.1.120:args_not_received", "2.1.200:did_not_start", "2.1.285:args_received"), exitInvalid, "probe: Claude Code 2.1.200 did_not_start"},
		{"no version ran args", withL0("2.1.120:args_not_received"), exitInvalid, "no probed version ran exec-form args"},
		{"the minimum not bracketed", withL0("2.1.285:args_received"), exitInvalid, "the minimum is not bracketed"},
		{"inconsistent versions", withL0("2.1.120:args_received", "2.1.285:args_not_received"), exitInvalid, "probe: inconsistent: 2.1.285 ignores args but the older 2.1.120 runs them"},
		{"a minimum below the stated one", withL0("2.1.150:args_not_received", "2.1.200:args_received", "2.1.285:args_received"), exitStop,
			"STOP: probe: the measured minimum 2.1.200 is below the stated 2.1.285"},
		{"a minimum above the stated one", withL0("2.1.285:args_not_received", "2.1.290:args_received"), exitDecided, "raise the stated minimum from 2.1.285 to 2.1.290"},
		{"versions merged across L0 directories", func() []decideInput {
			ins := standard()
			a, b := l0("2.1.120:args_not_received"), l0("2.1.285:args_received")
			return append([]decideInput{a, b}, ins[1:]...)
		}(), exitDecided, "Decision: 2.1.285 stands."},
		// precedence
		{"invalid input wins over STOP", func() []decideInput {
			ins := withL1(3501, 10000)
			ins[2].Res.DryRun = true
			return ins
		}(), exitInvalid, "INVALID INPUT (exit 2)"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := decide(tc.ins, curDefaults)
			if got := d.exitCode(); got != tc.exit {
				t.Fatalf("exit %d, want %d; invalid %q, stops %q\n%s", got, tc.exit, d.Invalid, d.Stops, d.Record)
			}
			if !strings.Contains(d.Record, tc.text) {
				t.Errorf("record lacks %q:\n%s", tc.text, d.Record)
			}
		})
	}
}

// TestKillCeilingAt checks kill's ceiling reads Q, A and W from internal/config.
func TestKillCeilingAt(t *testing.T) {
	q, a, w := int64(adconfig.DefaultQueryTimeoutMs), int64(adconfig.DefaultActionTimeoutMs), int64(adconfig.DefaultPipeCloseWaitMs)
	for _, e := range []int64{0, 1000, 7000, 8000} {
		k := killCeilingAt(e)
		pathI, pathII := 2*q+2*a+e+4*w, 3*q+2*a+5*w
		if k.Q != q || k.A != a || k.W != w || k.PathI != pathI || k.PathII != pathII || k.Max() != max(pathI, pathII) {
			t.Errorf("E %d ms: %+v, want path (i) %d ms, path (ii) %d ms", e, k, pathI, pathII)
		}
	}
	want := fmt.Sprintf("Q %d ms, A %d ms, W %d ms from internal/config", q, a, w)
	if rec := decide(standard(), curDefaults).Record; !strings.Contains(rec, want) {
		t.Errorf("record lacks %q:\n%s", want, rec)
	}
}

// writeInput writes in as a results directory under root.
func writeInput(t *testing.T, root string, in decideInput, guard bool) string {
	t.Helper()
	dir := filepath.Join(root, in.Dir)
	b, err := json.Marshal(in.Res)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, resultsFile), string(b))
	if guard {
		writeFile(t, filepath.Join(dir, guardStatusFile), in.Guard+"\n")
	}
	return dir
}

func TestDecideCommand(t *testing.T) {
	run := func(args ...string) (int, string) {
		var out, errOut bytes.Buffer
		code := dispatch(append([]string{"decide"}, args...), &out, &errOut)
		return code, out.String() + errOut.String()
	}
	root := t.TempDir()
	var args []string
	for _, in := range standard() {
		args = append(args, "-in", writeInput(t, root, in, true))
	}
	if code, out := run(args...); code != exitDecided || !strings.HasPrefix(out, "# measure-exit decision record") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	dry := standard()[1]
	dry.Dir, dry.Res.DryRun, dry.Res.Banner = "dry", true, dryRunBanner
	unguarded := standard()[2]
	unguarded.Dir = "unguarded"
	for _, tc := range []struct {
		name string
		args []string
		exit int
		text string
	}{
		{"no -in", nil, exitUsage, "want one or more -in DIR"},
		{"a stray argument", []string{"-in", root, "extra"}, exitUsage, ""},
		{"no results file", []string{"-in", filepath.Join(root, "nowhere")}, exitInvalid, "no such file"},
		{"a dry run", []string{"-in", writeInput(t, root, dry, true)}, exitInvalid, "dry: a dry run (stub claude) is not a measurement"},
		{"no guard status", []string{"-in", writeInput(t, root, unguarded, false)}, exitInvalid, "the host guard did not pass (missing)"},
	} {
		code, out := run(tc.args...)
		if code != tc.exit || !strings.Contains(out, tc.text) {
			t.Errorf("%s: exit %d, want %d:\n%s", tc.name, code, tc.exit, out)
		}
	}
	notJSON := filepath.Join(root, "garbage")
	writeFile(t, filepath.Join(notJSON, resultsFile), "{")
	if code, _ := run("-in", notJSON); code != exitInvalid {
		t.Errorf("a malformed results file: exit %d", code)
	}
}

func TestClassifyProbeLine(t *testing.T) {
	for line, want := range map[string]string{
		"argc=1 arg1=mx-exec-form-arg ppid=42": probeArgsReceived,
		"argc=2 arg1=mx-exec-form-arg ppid=42": probeArgsReceived,
		"argc=0 arg1= ppid=42":                 probeArgsNotReceived,
		"argc=1 arg1=something-else ppid=42":   probeArgsNotReceived,
		"":                                     probeArgsNotReceived,
	} {
		if got, note := classifyProbeLine(line); got != want || !strings.Contains(note, "parent pid") {
			t.Errorf("%q: %s (%s), want %s", line, got, note, want)
		}
	}
}
