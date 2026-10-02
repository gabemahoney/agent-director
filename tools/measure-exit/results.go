package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"
)

// resultsFile and tableFile are the run's outputs in the results directory:
// the machine-readable results (every raw sample) and the printed table.
const (
	resultsFile = "results.json"
	tableFile   = "results-table.txt"
	// resultsSchema versions results.json for its readers (decide).
	resultsSchema = 1
)

// dryRunBanner heads every dry-run output, so it can never pass for data.
const dryRunBanner = "DRY RUN: stub claude, no real agent; these numbers are NOT measurements"

// family is the SRD question a case answers.
type family string

const (
	familyRN6 family = "RN-6"
	familyRN2 family = "RN-2"
	familyRN9 family = "RN-9"
)

// rn2ResolutionNote is printed with every RN-2 row: its times start at the
// stored ended_at, which has whole-second resolution, and end at a poll.
const rn2ResolutionNote = "RN-2 times run from the stored ended_at (whole seconds, so up to 1 s long) to the first poll that no longer lists the session (up to 100 ms late)"

// sample is one measured attempt.
type sample struct {
	Case       string  `json:"case"`
	Index      int     `json:"index"`
	InstanceID string  `json:"claude_instance_id,omitempty"`
	Outcome    outcome `json:"outcome"`
	// Millis is the measured time, set only when Outcome is completed.
	Millis *int64 `json:"elapsed_ms,omitempty"`
	// Reason says why a sample did not complete (or what it noted).
	Reason      string `json:"reason,omitempty"`
	Polls       int    `json:"polls,omitempty"`
	ProbeErrors int    `json:"probe_errors,omitempty"`
}

// setTime marks the sample completed with d.
func (s *sample) setTime(d time.Duration) {
	ms := d.Milliseconds()
	s.Millis = &ms
	s.Outcome = outcomeCompleted
}

// caseResult is one case's row in the table and its raw samples.
type caseResult struct {
	ID     string `json:"id"`
	Family family `json:"family"`
	Title  string `json:"title"`
	// NotRun, when set, is why the case did not run (for example no MCP
	// configuration in real mode); the table shows it as a row.
	NotRun     string `json:"not_run,omitempty"`
	Attempted  int    `json:"attempted"`
	Completed  int    `json:"completed"`
	DidNotExit int    `json:"did_not_exit"`
	NoEndedAt  int    `json:"no_ended_at"`
	Failed     int    `json:"failed"`
	// LargestMillis is the largest completed time; nil with no completed
	// sample (never 0).
	LargestMillis *int64       `json:"largest_ms"`
	Budgets       budgetReport `json:"budgets"`
	// Trigger is the case's measured action as the results state it.
	Trigger    string   `json:"trigger,omitempty"`
	MCPServers []string `json:"mcp_servers,omitempty"`
	Notes      []string `json:"notes,omitempty"`
	Samples    []sample `json:"samples"`
}

// finalize computes the counts and the largest time from the samples.
func (c *caseResult) finalize() {
	c.Attempted, c.Completed, c.DidNotExit, c.NoEndedAt, c.Failed = len(c.Samples), 0, 0, 0, 0
	c.LargestMillis = nil
	for _, s := range c.Samples {
		switch s.Outcome {
		case outcomeCompleted:
			c.Completed++
			if s.Millis != nil && (c.LargestMillis == nil || *s.Millis > *c.LargestMillis) {
				v := *s.Millis
				c.LargestMillis = &v
			}
		case outcomeDidNotExit:
			c.DidNotExit++
		case outcomeNoEndedAt:
			c.NoEndedAt++
		default:
			c.Failed++
		}
	}
	if c.Samples == nil {
		c.Samples = []sample{}
	}
}

// hookObservation is one hook the RN-9 recorder saw, joined with
// agent-director's outcome from the container trail.
type hookObservation struct {
	Seq       int    `json:"seq"`
	Event     string `json:"event"`
	Source    string `json:"source,omitempty"`
	ParentPID int    `json:"parent_pid"`
	PanePID   int    `json:"pane_pid"`
	PIDMatch  bool   `json:"pid_match"`
	SessionID string `json:"session_id,omitempty"`
	// TranscriptBase is the payload's transcript_path basename without its
	// extension: the session id agent-director records and its trail
	// reports.
	TranscriptBase string `json:"transcript_basename,omitempty"`
	AgentID        string `json:"agent_id,omitempty"`
	AgentType      string `json:"agent_type,omitempty"`
	// Outcome is agent-director's outcome for the same hook, from the
	// container trail (joinOutcomes): "applied", "ignored" (Reason is the
	// ad.hook.ignored reason), "no_change" (ad.hook.fired no_change with no
	// ad.hook.ignored: the hook passed or met no row but changed nothing)
	// or "no_record" (no agent-director hook ran for
	// it: an event agent-director does not register, or a process that
	// does not carry its hooks).
	Outcome string `json:"outcome"`
	Reason  string `json:"reason,omitempty"`
}

// rn9Scenario is one RN-9 scenario's verdict.
type rn9Scenario struct {
	ID string `json:"id"`
	// Verdict is "pass", "fail" (a finding for the user), "inconclusive"
	// (the run did not produce the evidence; re-run) or "not_run".
	Verdict string            `json:"verdict"`
	Reason  string            `json:"reason,omitempty"`
	Hooks   []hookObservation `json:"hooks"`
	// Stop lists the STOP flags this scenario raised (decide exits 3 on any).
	Stop []string `json:"stop,omitempty"`
	// Notes record what the drive did and saw (session ids read with get,
	// the permission answers, Claude's own split panes); never content.
	Notes []string `json:"notes,omitempty"`
}

// rn9Section is the RN-9 results.
type rn9Section struct {
	Scenarios []rn9Scenario `json:"scenarios"`
}

// rn7Row is one event of RN-7's record (keys and ids only, never content).
// It carries no verdict: a missing transcript_path is a note.
type rn7Row struct {
	Seq               int      `json:"seq"`
	Event             string   `json:"event"`
	Source            string   `json:"source,omitempty"`
	Keys              []string `json:"keys"`
	TranscriptPresent bool     `json:"transcript_path_present"`
	TranscriptBase    string   `json:"transcript_basename,omitempty"`
	SessionID         string   `json:"session_id,omitempty"`
	Note              string   `json:"note,omitempty"`
}

// rn7Record is RN-7's record, taken inside RN-9's event drive.
type rn7Record struct {
	Rows []rn7Row `json:"rows"`
}

// probeVersion is one Claude Code version the exec-form probe tried.
type probeVersion struct {
	Version string `json:"version"`
	// Result is "args_received", "args_not_received" or "did_not_start".
	Result string `json:"result"`
	Note   string `json:"note,omitempty"`
}

// probeSection is the exec-form version probe's results.
type probeSection struct {
	Versions []probeVersion `json:"versions"`
	// Minimum is the oldest version that received its args, or "" when
	// none did.
	Minimum string `json:"minimum,omitempty"`
}

// seedRecord is what the driver seeded into its HOME's .claude.json: the
// key names only, never a value.
type seedRecord struct {
	Path           string   `json:"path"`
	Keys           []string `json:"keys"`
	CredentialMode string   `json:"credential_mode"`
	APIKeyApproved bool     `json:"api_key_approved"`
}

// results is a run's whole output. RN9, RN7 and Probe are nil when the run
// had no such section.
type results struct {
	Schema     int           `json:"schema"`
	DryRun     bool          `json:"dry_run"`
	Banner     string        `json:"banner,omitempty"`
	StartedAt  time.Time     `json:"started_at"`
	FinishedAt *time.Time    `json:"finished_at,omitempty"`
	Isolation  isolation     `json:"isolation"`
	Seed       *seedRecord   `json:"seed,omitempty"`
	Cases      []caseResult  `json:"cases"`
	RN9        *rn9Section   `json:"rn9,omitempty"`
	RN7        *rn7Record    `json:"rn7_record,omitempty"`
	Probe      *probeSection `json:"probe,omitempty"`
	// Notes are run-level notes (a refusal, an aborted case).
	Notes []string `json:"notes,omitempty"`
}

// newResults starts a run's results. A dry run carries the banner.
func newResults(iso isolation, startedAt time.Time) *results {
	r := &results{Schema: resultsSchema, StartedAt: startedAt.UTC(), Isolation: iso, Cases: []caseResult{}}
	if iso.Mode == modeDry {
		r.DryRun, r.Banner = true, dryRunBanner
	}
	return r
}

// writeJSON writes results.json atomically (a temp file renamed into
// place), scrubbing credential values from the encoded text.
func (r *results) writeJSON(path string, scr scrubber) error {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(scr.scrub(string(b))+"\n"), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// writeTable renders the human-readable table. A dry run's table starts and
// ends with the banner.
func (r *results) writeTable(w io.Writer, scr scrubber) error {
	var b strings.Builder
	if r.DryRun {
		b.WriteString("*** " + dryRunBanner + " ***\n\n")
	}
	fmt.Fprintf(&b, "measure-exit run %s (%s), agent-director %s, Claude Code %s, parallelism %d\n\n",
		r.Isolation.RunID, r.Isolation.Mode, r.Isolation.AgentDirector, r.Isolation.ClaudeCode, r.Isolation.Parallelism)
	r.writeCases(&b)
	r.writeRN9(&b)
	r.writeRN7(&b)
	r.writeProbe(&b)
	for _, n := range r.Notes {
		b.WriteString("note: " + n + "\n")
	}
	if r.DryRun {
		b.WriteString("\n*** " + dryRunBanner + " ***\n")
	}
	_, err := io.WriteString(w, scr.scrub(b.String()))
	return err
}

// writeCases renders one row per RN-6/RN-2 case, then each case's budgets.
func (r *results) writeCases(b *strings.Builder) {
	if len(r.Cases) == 0 {
		return
	}
	tw := tabwriter.NewWriter(b, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "case\tfamily\tattempted\tcompleted\tdid not exit\tno ended_at\tfailed\tlargest\tSessionEnd budgets")
	hasRN2 := false
	for _, c := range r.Cases {
		if c.Family == familyRN2 {
			hasRN2 = true
		}
		if c.NotRun != "" {
			fmt.Fprintf(tw, "%s\t%s\tnot run: %s\t\t\t\t\t\t\n", c.ID, c.Family, c.NotRun)
			continue
		}
		fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t%d\t%d\t%d\t%s\t%s\n", c.ID, c.Family, c.Attempted, c.Completed,
			c.DidNotExit, c.NoEndedAt, c.Failed, millisText(c.LargestMillis), c.Budgets.summary())
	}
	_ = tw.Flush()
	if hasRN2 {
		b.WriteString("\n" + rn2ResolutionNote + "\n")
	}
	for _, c := range r.Cases {
		writeCaseDetail(b, c)
	}
	b.WriteString("\n")
}

// writeCaseDetail renders one case's trigger, MCP servers, budgets, notes
// and warnings.
func writeCaseDetail(b *strings.Builder, c caseResult) {
	fmt.Fprintf(b, "\n[%s] %s\n", c.ID, c.Title)
	if c.Trigger != "" {
		b.WriteString("  measured action: " + c.Trigger + "\n")
	}
	if len(c.MCPServers) > 0 {
		b.WriteString("  MCP servers: " + strings.Join(c.MCPServers, ", ") + "\n")
	}
	for _, h := range c.Budgets.SessionEndHooks {
		fmt.Fprintf(b, "  SessionEnd hook: %s layer, %s, timeout %s\n", h.Layer, h.Program, h.Timeout)
	}
	fmt.Fprintf(b, "  %s: %s\n", sessionEndBudgetEnv, c.Budgets.EnvEffective)
	for _, w := range c.Budgets.Warnings {
		b.WriteString("  warning: " + w + "\n")
	}
	for _, n := range c.Notes {
		b.WriteString("  note: " + n + "\n")
	}
}

// summary is the budgets' table cell: the SessionEnd hook timeouts and the
// variable's effective value.
func (br budgetReport) summary() string {
	parts := make([]string, 0, len(br.SessionEndHooks)+1)
	for _, h := range br.SessionEndHooks {
		parts = append(parts, string(h.Layer)+": "+h.Timeout)
	}
	if len(parts) == 0 {
		parts = append(parts, "no SessionEnd hooks")
	}
	parts = append(parts, "env: "+br.EnvEffective)
	if br.ClaudeCode != "" {
		parts = append(parts, "default = Claude Code "+br.ClaudeCode+"'s")
	}
	return strings.Join(parts, "; ")
}

// millisText renders a largest time; nil is "no data", never 0.
func millisText(ms *int64) string {
	if ms == nil {
		return "no data"
	}
	return fmt.Sprintf("%.1f s", float64(*ms)/1000)
}

// writeRN9 renders the RN-9 scenarios and their hooks.
func (r *results) writeRN9(b *strings.Builder) {
	if r.RN9 == nil {
		return
	}
	b.WriteString("RN-9\n")
	tw := tabwriter.NewWriter(b, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "scenario\tverdict\thooks\tSTOP\treason")
	for _, s := range r.RN9.Scenarios {
		fmt.Fprintf(tw, "%s\t%s\t%d\t%s\t%s\n", s.ID, s.Verdict, len(s.Hooks), strings.Join(s.Stop, ", "), s.Reason)
	}
	_ = tw.Flush()
	for _, s := range r.RN9.Scenarios {
		for _, n := range s.Notes {
			fmt.Fprintf(b, "[%s] note: %s\n", s.ID, n)
		}
		if len(s.Hooks) == 0 {
			continue
		}
		fmt.Fprintf(b, "\n[%s] hooks\n", s.ID)
		tw = tabwriter.NewWriter(b, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "seq\tevent\tsource\tparent pid\tpane pid\tmatch\tagent_id\tagent_type\toutcome\treason")
		for _, h := range s.Hooks {
			fmt.Fprintf(tw, "%d\t%s\t%s\t%d\t%d\t%t\t%s\t%s\t%s\t%s\n", h.Seq, h.Event, h.Source, h.ParentPID,
				h.PanePID, h.PIDMatch, h.AgentID, h.AgentType, h.Outcome, h.Reason)
		}
		_ = tw.Flush()
	}
	b.WriteString("\n")
}

// writeRN7 renders RN-7's record, for the record only.
func (r *results) writeRN7(b *strings.Builder) {
	if r.RN7 == nil {
		return
	}
	b.WriteString("RN-7 record (for the record; no verdict)\n")
	tw := tabwriter.NewWriter(b, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "seq\tevent\tsource\ttranscript_path\tbasename\tsession_id\tkeys\tnote")
	for _, row := range r.RN7.Rows {
		present := "absent"
		if row.TranscriptPresent {
			present = "present"
		}
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", row.Seq, row.Event, row.Source, present,
			row.TranscriptBase, row.SessionID, strings.Join(row.Keys, ","), row.Note)
	}
	_ = tw.Flush()
	b.WriteString("\n")
}

// writeProbe renders the exec-form version probe.
func (r *results) writeProbe(b *strings.Builder) {
	if r.Probe == nil {
		return
	}
	b.WriteString("Exec-form version probe\n")
	tw := tabwriter.NewWriter(b, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "Claude Code\tresult\tnote")
	for _, v := range r.Probe.Versions {
		fmt.Fprintf(tw, "%s\t%s\t%s\n", v.Version, v.Result, v.Note)
	}
	_ = tw.Flush()
	minimum := r.Probe.Minimum
	if minimum == "" {
		minimum = "none of the probed versions"
	}
	b.WriteString("oldest version that ran exec-form args: " + minimum + "\n\n")
}
