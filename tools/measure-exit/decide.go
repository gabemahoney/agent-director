package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	adconfig "github.com/gabemahoney/agent-director/internal/config"
)

// `measure-exit decide -in DIR [-in DIR]...` applies the RN-6, RN-2 and
// RN-9 decision rules (SRD Open Questions RN-2, RN-6, RN-9; SR-13.2; lead
// decision 11) to the operator's results directories (L0, L1, L2) and
// prints a markdown decision record that shows the arithmetic. It writes
// nothing. Exit codes:
//
//	0  decided
//	2  invalid input: a dry run, an unfinished run, a failed or missing
//	   guard, a missing case or scenario, a case whose counts disagree
//	   with its samples, fewer than 20 completed samples under the default
//	   budget (20 measured, completed or "did not exit", under a raised
//	   one), a "did not exit" (in any position) or budgets not at their
//	   defaults under the default budget, an incomplete or inconsistent
//	   probe, an inconclusive RN-9 scenario
//
// A sampled case's rules read its first 20 usable samples in recorded
// order (useSamples): L1 takes 22 per case as a buffer (user, 2026-10-02),
// so one flaky sample forces no re-run; the record says how many were
// recorded, used and dropped.
//	3  STOP for the user: E >= 8 s, a default lower than the current one,
//	   an exec-form minimum below the stated one, any RN-9 STOP flag or
//	   failed scenario
//
// Invalid input wins over STOP: the decisions of an invalid input are not
// to be acted on.

// Exit codes of decide.
const (
	exitDecided = 0
	exitInvalid = 2
	exitStop    = 3
)

// guardStatusFile is the host runner's record of the guard's verify in a
// results directory: "pass", or "fail <exit status>".
const guardStatusFile = "guard-status.txt"

// killCeilingLimitMs is the bound kill's ceiling at the defaults must stay
// within (SRD RN-6, SR-13.2), and rn6StopExitWaitSec the required E at or
// above which decide stops for the user (lead decision 10; at today's
// defaults the two agree: 7.4 s + 8 s = 15.4 s).
const (
	killCeilingLimitMs = 15000
	rn6StopExitWaitSec = 8
)

// killCeiling is kill's SR-13.2 ceiling at the defaults for an exit wait of
// eMs: max(path (i), path (ii)), with path (i) = 2Q + 2A + E + 4W (lookup,
// pane listing, pane kill, session kill, then the exit wait) and path (ii) =
// 3Q + 2A + 5W (the same four calls, then the follow-up lookup). Q, A and W
// are the query timeout, the action timeout and the pipe-close wait, each
// read from its internal/config constant.
type killCeiling struct {
	Q, A, W, E    int64
	FixedI        int64 // 2Q + 2A + 4W
	PathI, PathII int64
}

// killCeilingAt is the ceiling at the config defaults with exit wait eMs.
func killCeilingAt(eMs int64) killCeiling {
	k := killCeiling{Q: adconfig.DefaultQueryTimeoutMs, A: adconfig.DefaultActionTimeoutMs, W: adconfig.DefaultPipeCloseWaitMs, E: eMs}
	k.FixedI = 2*k.Q + 2*k.A + 4*k.W
	k.PathI = k.FixedI + k.E
	k.PathII = 3*k.Q + 2*k.A + 5*k.W
	return k
}

// Max is the ceiling: the larger path.
func (k killCeiling) Max() int64 { return max(k.PathI, k.PathII) }

// secondsText formats ms as seconds with one decimal ("7.4 s").
func secondsText(ms int64) string { return fmt.Sprintf("%.1f s", float64(ms)/1000) }

// RN-2's thresholds are half the current default and half the current
// minimum (SRD RN-2): above them the value is raised to at least twice the
// largest time.

// rn6DefaultCases and rn6RaisedCases are the RN-6 case ids decide reads.
var (
	rn6DefaultCases = []string{"rn6.idle", "rn6.midturn", "rn6.mcp"}
	rn6RaisedCases  = []string{
		"rn6.idle.raised-hook", "rn6.midturn.raised-hook", "rn6.mcp.raised-hook",
		"rn6.idle.raised-env", "rn6.midturn.raised-env", "rn6.mcp.raised-env",
	}
	rn2Cases = []string{"rn2.natural", "rn2.pause", "rn2.mcp"}
)

// mcpCase reports whether a case id is an MCP case: one that may be "not
// run" when the deployment has no MCP configuration, without invalidating
// the decision.
func mcpCase(id string) bool { return strings.Contains(id, ".mcp") }

// currentDefaults are the values in force, from internal/config.
type currentDefaults struct {
	KillExitWaitMs       int
	StoppingWindowSec    int
	MinStoppingWindowSec int
	MinClaudeCode        string
}

// configDefaults reads the current values from their one constant each.
func configDefaults() currentDefaults {
	return currentDefaults{
		KillExitWaitMs:       adconfig.DefaultKillExitWaitMs,
		StoppingWindowSec:    adconfig.DefaultStoppingWindowSeconds,
		MinStoppingWindowSec: adconfig.MinStoppingWindowSeconds,
		MinClaudeCode:        minClaudeCodeVersion,
	}
}

// decideInput is one results directory.
type decideInput struct {
	Dir   string
	Res   results
	Guard string // "pass", "fail …" or "missing"
}

// decision is decide's output.
type decision struct {
	Record  string
	Invalid []string
	Stops   []string
}

// exitCode maps a decision to decide's exit code.
func (d decision) exitCode() int {
	switch {
	case len(d.Invalid) > 0:
		return exitInvalid
	case len(d.Stops) > 0:
		return exitStop
	default:
		return exitDecided
	}
}

// decideCommand is the decide subcommand.
func decideCommand(args []string, stdout, stderr io.Writer) int {
	var dirs stringList
	fs := flag.NewFlagSet("decide", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Var(&dirs, "in", "a results directory (results.json and the runner's guard-status.txt); repeat for L0, L1 and L2")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() > 0 || len(dirs) == 0 {
		fmt.Fprintln(stderr, "measure-exit decide: want one or more -in DIR and no other arguments")
		return exitUsage
	}
	var inputs []decideInput
	for _, dir := range dirs {
		in, err := loadDecideInput(dir)
		if err != nil {
			fmt.Fprintln(stderr, "measure-exit decide:", err)
			return exitInvalid
		}
		inputs = append(inputs, in)
	}
	d := decide(inputs, configDefaults())
	fmt.Fprint(stdout, d.Record)
	return d.exitCode()
}

// stringList is a repeatable string flag.
type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

// loadDecideInput reads one results directory.
func loadDecideInput(dir string) (decideInput, error) {
	in := decideInput{Dir: dir, Guard: "missing"}
	b, err := os.ReadFile(filepath.Join(dir, resultsFile))
	if err != nil {
		return in, err
	}
	if err := json.Unmarshal(b, &in.Res); err != nil {
		return in, fmt.Errorf("%s: not a results file: %w", filepath.Join(dir, resultsFile), err)
	}
	g, err := os.ReadFile(filepath.Join(dir, guardStatusFile))
	switch {
	case err == nil:
		in.Guard = strings.TrimSpace(string(g))
	case !errors.Is(err, os.ErrNotExist):
		return in, err
	}
	return in, nil
}

// mergedResults is every input's sections, keyed by case and scenario id.
type mergedResults struct {
	cases map[string]caseResult
	scen  map[string]rn9Scenario
	probe []probeVersion
	rn7   *rn7Record
}

// mergeInputs lists the inputs in the record, checks each is a finished,
// guarded, non-dry real or probe run, and merges their sections; a case or
// scenario in two inputs is invalid.
func mergeInputs(b *strings.Builder, d *decision, inputs []decideInput) mergedResults {
	m := mergedResults{cases: map[string]caseResult{}, scen: map[string]rn9Scenario{}}
	b.WriteString("## Inputs\n")
	for _, in := range inputs {
		r := in.Res
		fmt.Fprintf(b, "- %s: run %s, mode %s, Claude Code %s, guard %s\n", in.Dir, r.Isolation.RunID, r.Isolation.Mode, r.Isolation.ClaudeCode, in.Guard)
		switch {
		case r.Schema != resultsSchema:
			d.Invalid = append(d.Invalid, fmt.Sprintf("%s: results schema %d, want %d", in.Dir, r.Schema, resultsSchema))
		case r.DryRun || r.Isolation.Mode == modeDry:
			d.Invalid = append(d.Invalid, in.Dir+": a dry run (stub claude) is not a measurement")
		case r.Isolation.Mode != modeReal && r.Isolation.Mode != modeProbe:
			d.Invalid = append(d.Invalid, fmt.Sprintf("%s: mode %q is not real or probe", in.Dir, r.Isolation.Mode))
		}
		if r.FinishedAt == nil {
			d.Invalid = append(d.Invalid, in.Dir+": the run did not finish")
		}
		if in.Guard != "pass" {
			d.Invalid = append(d.Invalid, fmt.Sprintf("%s: the host guard did not pass (%s)", in.Dir, in.Guard))
		}
		for _, c := range r.Cases {
			if _, dup := m.cases[c.ID]; dup {
				d.Invalid = append(d.Invalid, "case "+c.ID+" is in two inputs")
			}
			m.cases[c.ID] = c
		}
		if r.RN9 != nil {
			for _, s := range r.RN9.Scenarios {
				if _, dup := m.scen[s.ID]; dup {
					d.Invalid = append(d.Invalid, "RN-9 scenario "+s.ID+" is in two inputs")
				}
				m.scen[s.ID] = s
			}
		}
		if r.Probe != nil {
			m.probe = append(m.probe, r.Probe.Versions...)
		}
		if r.RN7 != nil {
			m.rn7 = r.RN7
		}
	}
	b.WriteString("\n")
	return m
}

// decide applies every rule to the merged inputs. It is pure: the record
// and the reasons depend only on its arguments.
func decide(inputs []decideInput, cur currentDefaults) decision {
	var (
		d decision
		b strings.Builder
	)
	b.WriteString("# measure-exit decision record\n\n")
	m := mergeInputs(&b, &d, inputs)
	decideRN6(&b, &d, m.cases, cur)
	decideRN2(&b, &d, m.cases, cur)
	decideRN9(&b, &d, m.scen)
	decideProbe(&b, &d, m.probe, cur)
	writeRN7Summary(&b, m.rn7)
	b.WriteString("## Outcome\n")
	switch d.exitCode() {
	case exitInvalid:
		b.WriteString("INVALID INPUT (exit 2): nothing above may be applied.\n")
		for _, s := range d.Invalid {
			b.WriteString("- " + s + "\n")
		}
	case exitStop:
		b.WriteString("STOP FOR THE USER (exit 3):\n")
	default:
		b.WriteString("DECIDED (exit 0).\n")
	}
	for _, s := range d.Stops {
		b.WriteString("- STOP: " + s + "\n")
	}
	d.Record = b.String()
	return d
}

// sampleUse is the samples decide reads from one case: the first
// minSamples usable samples in recorded order, completed ones at the
// default budget and completed or "did not exit" ones under a raised
// budget. Every other sample (unusable, or usable past the first
// minSamples) is dropped and counted by outcome.
type sampleUse struct {
	Used       int
	DidNotExit int    // "did not exit" samples among the used
	Largest    *int64 // the largest completed time among the used
	Dropped    map[outcome]int
}

// useSamples picks the samples a case's rule reads (sampleUse). The user's
// 2026-10-02 decision allows all completed samples once there are at least
// 20; decide takes the first 20, so a run's buffer never changes the rule's
// sample count.
func useSamples(samples []sample, atDefault bool) sampleUse {
	u := sampleUse{Dropped: map[outcome]int{}}
	for _, s := range samples {
		usable := s.Outcome == outcomeCompleted || (!atDefault && s.Outcome == outcomeDidNotExit)
		if !usable || u.Used == minSamples {
			u.Dropped[s.Outcome]++
			continue
		}
		u.Used++
		if s.Outcome == outcomeDidNotExit {
			u.DidNotExit++
			continue
		}
		if s.Millis != nil && (u.Largest == nil || *s.Millis > *u.Largest) {
			v := *s.Millis
			u.Largest = &v
		}
	}
	return u
}

// droppedText is the dropped samples by outcome: "2 (completed 1, failed 1)".
func (u sampleUse) droppedText() string {
	n, kinds := 0, make([]string, 0, len(u.Dropped))
	for o, k := range u.Dropped {
		n += k
		kinds = append(kinds, fmt.Sprintf("%s %d", o, k))
	}
	if n == 0 {
		return "0"
	}
	sort.Strings(kinds)
	return fmt.Sprintf("%d (%s)", n, strings.Join(kinds, ", "))
}

// countsDisagree reports whether a case's stored counts or largest time
// differ from those its samples give (finalize): the rules read the
// samples, so a results.json edited or damaged by hand is invalid.
func countsDisagree(c caseResult) bool {
	r := c
	r.Samples = append([]sample(nil), c.Samples...)
	r.finalize()
	sameLargest := (r.LargestMillis == nil) == (c.LargestMillis == nil) &&
		(r.LargestMillis == nil || *r.LargestMillis == *c.LargestMillis)
	return r.Attempted != c.Attempted || r.Completed != c.Completed || r.DidNotExit != c.DidNotExit ||
		r.NoEndedAt != c.NoEndedAt || r.Failed != c.Failed || !sameLargest
}

// checkSampledCase checks one RN-6 or RN-2 case the rules read: present,
// with counts that agree with its samples, and, when atDefault, at least
// minSamples completed, no "did not exit" and budgets at their defaults.
// The rule reads only the first minSamples usable samples in recorded
// order (useSamples); the rest are dropped, and the record says how many.
// A "did not exit" under the default budget invalidates the case wherever
// it falls, past the first minSamples included: SRD RN-6 sets E from the
// largest time measured under the default budget, and an agent that
// outlived the sample ceiling is such a time, unbounded, not a flaky
// sample. A raised-budget case (reported for the README, not part of a
// rule) needs at least minSamples measured agents, completed or "did not
// exit": SRD RN-6 asks for at least 20 agents per case and the largest
// time per case, and a raised-budget agent that outlived the sample
// ceiling is a measurement of that budget, reported, not an invalid run.
// An MCP case reported "not run" is allowed and noted. It returns the
// largest time of the used samples and whether the case contributes it.
func checkSampledCase(b *strings.Builder, d *decision, cases map[string]caseResult, id string, atDefault bool) (int64, bool) {
	c, ok := cases[id]
	switch {
	case !ok:
		d.Invalid = append(d.Invalid, "case "+id+" is missing")
		return 0, false
	case c.NotRun != "" && mcpCase(id):
		fmt.Fprintf(b, "- %s: not run (%s)\n", id, c.NotRun)
		return 0, false
	case c.NotRun != "":
		d.Invalid = append(d.Invalid, fmt.Sprintf("case %s was not run: %s", id, c.NotRun))
		return 0, false
	}
	u := useSamples(c.Samples, atDefault)
	usable := "completed"
	if !atDefault {
		usable = "measured"
	}
	fmt.Fprintf(b, "- %s: %d recorded (%d completed, did not exit %d, no ended_at %d, failed %d); used the first %d %s, largest %s; dropped %s; budgets: %s\n",
		id, len(c.Samples), c.Completed, c.DidNotExit, c.NoEndedAt, c.Failed, u.Used, usable, millisText(u.Largest), u.droppedText(), c.Budgets.summary())
	valid := true
	if countsDisagree(c) {
		d.Invalid = append(d.Invalid, fmt.Sprintf("case %s: its counts or largest time disagree with its %d samples", id, len(c.Samples)))
		valid = false
	}
	switch {
	case atDefault && u.Used < minSamples:
		d.Invalid = append(d.Invalid, fmt.Sprintf("case %s has %d completed samples, fewer than %d", id, u.Used, minSamples))
		valid = false
	case !atDefault && u.Used < minSamples:
		d.Invalid = append(d.Invalid, fmt.Sprintf("case %s has %d measured samples (%d completed, %d did not exit), fewer than %d",
			id, u.Used, u.Used-u.DidNotExit, u.DidNotExit, minSamples))
		valid = false
	case !atDefault && u.DidNotExit > 0:
		fmt.Fprintf(b, "  %s: %d agents did not exit within the sample ceiling under the raised budget (allowed; reported for the README)\n", id, u.DidNotExit)
	}
	if atDefault {
		if n := u.Dropped[outcomeDidNotExit]; n > 0 {
			d.Invalid = append(d.Invalid, fmt.Sprintf("case %s: %d samples did not exit under the default SessionEnd budget", id, n))
			valid = false
		}
		if why := budgetsNotDefault(c.Budgets); why != "" {
			d.Invalid = append(d.Invalid, fmt.Sprintf("case %s: budgets not at their defaults (%s)", id, why))
			valid = false
		}
	}
	if !valid || u.Largest == nil {
		return 0, false
	}
	return *u.Largest, true
}

// budgetsNotDefault says why a case's budgets are not Claude Code's
// default SessionEnd budget, or "" when they are: every SessionEnd hook
// without a timeout, the budget variable unset, every layer readable.
func budgetsNotDefault(br budgetReport) string {
	var why []string
	for _, h := range br.SessionEndHooks {
		if h.Timeout != "default" {
			why = append(why, fmt.Sprintf("%s layer SessionEnd hook %s has timeout %s", h.Layer, h.Program, h.Timeout))
		}
	}
	if br.EnvEffective != "unset" {
		why = append(why, sessionEndBudgetEnv+" is "+br.EnvEffective)
	}
	for _, w := range br.Warnings {
		why = append(why, "unread layer: "+w)
	}
	return strings.Join(why, "; ")
}

// ceilSeconds is ms rounded up to a whole second, in seconds.
func ceilSeconds(ms int64) int64 { return int64(math.Ceil(float64(ms) / 1000)) }

// decideRN6 applies RN-6's rule: E = 2 x the largest default-budget time
// (over each case's first 20 completed samples; checkSampledCase),
// rounded up to a whole second; kill's ceiling at the defaults,
// max(2Q + 2A + E + 4W, 3Q + 2A + 5W) from the config constants
// (killCeilingAt), must stay within 15 s, and E >= 8 s is STOP; a value
// below the current default is never applied without the user (STOP). The
// raised-budget times are reported for the README.
func decideRN6(b *strings.Builder, d *decision, cases map[string]caseResult, cur currentDefaults) {
	b.WriteString("## RN-6: kill_exit_wait_ms\n")
	var largest int64 = -1
	for _, id := range rn6DefaultCases {
		if l, ok := checkSampledCase(b, d, cases, id, true); ok && l > largest {
			largest = l
		}
	}
	b.WriteString("Raised budgets (reported for the README; not part of the rule):\n")
	for _, id := range rn6RaisedCases {
		checkSampledCase(b, d, cases, id, false)
	}
	if largest < 0 {
		d.Invalid = append(d.Invalid, "RN-6: no default-budget case has a measured time")
		b.WriteString("\n")
		return
	}
	e := ceilSeconds(2 * largest)
	fmt.Fprintf(b, "Largest default-budget time: %d ms. Required E = ceil(2 x %d ms) = %d s.\n", largest, largest, e)
	k := killCeilingAt(e * 1000)
	fmt.Fprintf(b, "Kill ceiling at the defaults (SR-13.2; Q %d ms, A %d ms, W %d ms from internal/config): path (i) 2Q + 2A + E + 4W = %s + %d s = %s; path (ii) 3Q + 2A + 5W = %s; ceiling max(path (i), path (ii)) = %s (limit %s; STOP at E >= %d s).\n",
		k.Q, k.A, k.W, secondsText(k.FixedI), e, secondsText(k.PathI), secondsText(k.PathII), secondsText(k.Max()),
		secondsText(killCeilingLimitMs), rn6StopExitWaitSec)
	curMs := int64(cur.KillExitWaitMs)
	switch {
	case e >= rn6StopExitWaitSec || k.Max() > killCeilingLimitMs:
		d.Stops = append(d.Stops, fmt.Sprintf("RN-6: E = %d s puts kill's ceiling at %s (STOP at E >= %d s; limit %s); the ceiling must be restated or the value chosen by the user",
			e, secondsText(k.Max()), rn6StopExitWaitSec, secondsText(killCeilingLimitMs)))
	case e*1000 < curMs:
		d.Stops = append(d.Stops, fmt.Sprintf("RN-6: the measured requirement %d s is below the current %d ms; the current value stands unless the user agrees to lower it", e, curMs))
		fmt.Fprintf(b, "Decision: %d ms stands (never lowered without the user).\n", curMs)
	case e*1000 == curMs:
		fmt.Fprintf(b, "Decision: %d ms stands (it equals the requirement).\n", curMs)
	default:
		fmt.Fprintf(b, "Decision: raise kill_exit_wait_ms from %d ms to %d ms.\n", curMs, e*1000)
	}
	b.WriteString("\n")
}

// decideRN2 applies RN-2's rule over every RN-2 case (all run under the
// default budget; the largest time is over each case's first 20 completed
// samples): the window stays unless the largest time exceeds half of
// it, the minimum stays unless the largest time exceeds half of it; either
// is then raised to twice the largest time, rounded up to a whole second.
// With no RN-2 case the current values stand.
func decideRN2(b *strings.Builder, d *decision, cases map[string]caseResult, cur currentDefaults) {
	b.WriteString("## RN-2: stopping window and its minimum\n")
	present := false
	for _, id := range rn2Cases {
		if _, ok := cases[id]; ok {
			present = true
		}
	}
	if !present {
		fmt.Fprintf(b, "RN-2 not run: %d s and %d s stand.\n\n", cur.StoppingWindowSec, cur.MinStoppingWindowSec)
		return
	}
	var largest int64 = -1
	for _, id := range rn2Cases {
		if l, ok := checkSampledCase(b, d, cases, id, true); ok && l > largest {
			largest = l
		}
	}
	if largest < 0 {
		d.Invalid = append(d.Invalid, "RN-2: no case has a measured time")
		b.WriteString("\n")
		return
	}
	raised := ceilSeconds(2 * largest)
	fmt.Fprintf(b, "Largest time: %d ms (from the stored ended_at; up to 1.1 s long). Twice it, rounded up: %d s.\n", largest, raised)
	for _, v := range []struct {
		name string
		cur  int
	}{{"stopping window default", cur.StoppingWindowSec}, {"stopping window minimum", cur.MinStoppingWindowSec}} {
		half := int64(v.cur) * 1000 / 2
		if largest > half {
			fmt.Fprintf(b, "Decision: %s: %d ms > %d ms (half of %d s): raise to %d s.\n", v.name, largest, half, v.cur, raised)
		} else {
			fmt.Fprintf(b, "Decision: %s: %d ms <= %d ms (half of %d s): %d s stands.\n", v.name, largest, half, v.cur, v.cur)
		}
	}
	b.WriteString("\n")
}

// decideRN9 requires every RN-9 scenario: pass decides; a STOP flag or a
// failed scenario is STOP; inconclusive or not run is invalid (re-run).
func decideRN9(b *strings.Builder, d *decision, scen map[string]rn9Scenario) {
	b.WriteString("## RN-9: /resume, agent teams\n")
	for _, id := range rn9ScenarioIDs {
		s, ok := scen[id]
		if !ok {
			d.Invalid = append(d.Invalid, "RN-9 scenario "+id+" is missing")
			fmt.Fprintf(b, "- %s: missing\n", id)
			continue
		}
		fmt.Fprintf(b, "- %s: %s %s\n", id, s.Verdict, s.Reason)
		for _, f := range s.Stop {
			d.Stops = append(d.Stops, fmt.Sprintf("RN-9 %s: %s", id, f))
		}
		switch s.Verdict {
		case verdictPass:
		case verdictFail:
			if len(s.Stop) == 0 {
				d.Stops = append(d.Stops, fmt.Sprintf("RN-9 %s failed: %s", id, s.Reason))
			}
		default:
			d.Invalid = append(d.Invalid, fmt.Sprintf("RN-9 scenario %s is %s: %s", id, s.Verdict, s.Reason))
		}
	}
	b.WriteString("\n")
}

// decideProbe merges the probe's per-version results: every version must
// have started, the results must be monotonic (no version that ran args
// older than one that did not) and must bracket the minimum. A minimum
// above the stated one is applied (raised); one below it is STOP (never
// lowered without the user).
func decideProbe(b *strings.Builder, d *decision, versions []probeVersion, cur currentDefaults) {
	b.WriteString("## RN-9: oldest Claude Code that runs exec-form hooks\n")
	if len(versions) == 0 {
		d.Invalid = append(d.Invalid, "the exec-form version probe (L0) is missing")
		b.WriteString("missing\n\n")
		return
	}
	sort.Slice(versions, func(i, j int) bool { return versionLess(versions[i].Version, versions[j].Version) })
	newestNo, oldestYes := "", ""
	for _, v := range versions {
		fmt.Fprintf(b, "- %s: %s %s\n", v.Version, v.Result, v.Note)
		switch v.Result {
		case "args_received":
			if oldestYes == "" {
				oldestYes = v.Version
			}
		case "args_not_received":
			newestNo = v.Version
		default:
			d.Invalid = append(d.Invalid, fmt.Sprintf("probe: Claude Code %s %s", v.Version, v.Result))
		}
	}
	switch {
	case oldestYes == "":
		d.Invalid = append(d.Invalid, "probe: no probed version ran exec-form args")
	case newestNo == "":
		d.Invalid = append(d.Invalid, "probe: no older version that ignores args was probed; the minimum is not bracketed")
	case !versionLess(newestNo, oldestYes):
		d.Invalid = append(d.Invalid, fmt.Sprintf("probe: inconsistent: %s ignores args but the older %s runs them", newestNo, oldestYes))
	default:
		fmt.Fprintf(b, "Minimum: %s (newest that ignores args: %s). Stated: %s.\n", oldestYes, newestNo, cur.MinClaudeCode)
		switch {
		case versionLess(cur.MinClaudeCode, oldestYes):
			fmt.Fprintf(b, "Decision: raise the stated minimum from %s to %s.\n", cur.MinClaudeCode, oldestYes)
		case versionLess(oldestYes, cur.MinClaudeCode):
			d.Stops = append(d.Stops, fmt.Sprintf("probe: the measured minimum %s is below the stated %s; it is not applied without the user", oldestYes, cur.MinClaudeCode))
		default:
			fmt.Fprintf(b, "Decision: %s stands.\n", cur.MinClaudeCode)
		}
	}
	b.WriteString("\n")
}

// versionLess orders X.Y.Z versions; unparseable ones sort first.
func versionLess(a, b string) bool {
	va, oka := parseVersion(a)
	vb, okb := parseVersion(b)
	if !oka || !okb {
		return !oka && okb
	}
	for i := range va {
		if va[i] != vb[i] {
			return va[i] < vb[i]
		}
	}
	return false
}

// writeRN7Summary reports RN-7's record (for the record; never a flag).
func writeRN7Summary(b *strings.Builder, rn7 *rn7Record) {
	b.WriteString("## RN-7 (for the record)\n")
	if rn7 == nil {
		b.WriteString("not recorded\n\n")
		return
	}
	noID := map[string]int{}
	for _, r := range rn7.Rows {
		if !r.TranscriptPresent {
			noID[r.Event]++
		}
	}
	fmt.Fprintf(b, "%d hooks recorded.", len(rn7.Rows))
	if len(noID) == 0 {
		b.WriteString(" Every one carried transcript_path.\n\n")
		return
	}
	events := make([]string, 0, len(noID))
	for e, n := range noID {
		events = append(events, fmt.Sprintf("%s (%d)", e, n))
	}
	sort.Strings(events)
	b.WriteString(" Records no session id: " + strings.Join(events, ", ") + ".\n\n")
}
