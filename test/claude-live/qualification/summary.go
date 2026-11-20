package qualification

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"

	"github.com/gabemahoney/agent-director/test/claude-live/runner"
)

type CoverageEvidence struct {
	CoverageRow
	Status  string          `json:"status"`
	Reason  string          `json:"reason"`
	Results []runner.Result `json:"results"`
}
type PhaseSummary struct {
	Phase          string         `json:"phase"`
	Attempt        string         `json:"attempt"`
	Capture        string         `json:"capture"`
	ManifestSHA256 string         `json:"manifest_sha256"`
	Kit            string         `json:"kit"`
	Verdict        runner.Verdict `json:"verdict"`
}
type CheckRecord struct {
	Phase string `json:"phase"`
	runner.Result
}
type QualificationSummary struct {
	Checks          []CheckRecord   `json:"checks"`
	ProductInputs   []ProductSource `json:"product_inputs"`
	Cleanup         []runner.Result `json:"cleanup"`
	MeasurementGaps []CheckRecord   `json:"b3_gaps,omitempty"`

	Schema            int                   `json:"schema"`
	Identity          Identity              `json:"identity"`
	Status            string                `json:"status"`
	SelectedStatus    string                `json:"selected_status"`
	FullStatus        string                `json:"full_status"`
	MeasurementStatus string                `json:"b3_measurement_status"`
	Ledger            Ledger                `json:"ledger"`
	Accounting        Accounting            `json:"accounting"`
	Results           []runner.Result       `json:"results"`
	Phases            []PhaseSummary        `json:"phases"`
	Coverage          []CoverageEvidence    `json:"coverage"`
	NamedPremises     []runner.NamedPremise `json:"named_premises"`
	Timings           []Timing              `json:"timings"`
	ForceStates       []runner.Result       `json:"force_states"`
	RouteBounds       []runner.Result       `json:"route_bounds"`
	Gaps              []runner.Result       `json:"gaps"`
	Previous          *QualificationSummary `json:"previous,omitempty"`
	PreviousSHA256    string                `json:"previous_sha256,omitempty"`
	Rerun             string                `json:"rerun"`
}

func aggregateResults(results []runner.Result) string {
	v := runner.Verdict{Results: results}
	v.Aggregate()
	return v.Status
}

// ResolveCoverageEvidence does not modify the signal aggregate. R-a is a
// separately disclosed premise; applicability is explicit, never inferred
// from an omitted live phase or a broad suite heading.
// Future coverage reports must reuse this resolver; see docs/architecture.md.
func ResolveCoverageEvidence(captures []Capture, phases map[string][]runner.Result) []CoverageEvidence {
	var out []CoverageEvidence
	for _, row := range Coverage() {
		resolved := CoverageEvidence{CoverageRow: row}
		switch row.Kind {
		case "claude_inapplicable":
			resolved.Status = "claude_inapplicable"
			resolved.Reason = row.Source
		case "named_premise":
			r := summaryFallback(captures, row.ID, "TestBusySendTurnEndWaitsForTakeEvidence")
			resolved.Results = []runner.Result{r}
			resolved.Status = r.Status
			if r.Status == "pass" {
				resolved.Status = "named_premise"
				resolved.Reason = "R-a is unmeasured: AD's awaiting-take guard prevents issuing a later wake while earlier steer context is pending. Qualification never bypasses this guard for an impossible capture. Guard-test evidence and Task t2.xis.w3.b3 release-content disclosure are separate references; no live PASS/FAIL is claimed."
			}
		case "scheduler":
			tests := map[string][]string{"scheduler/full-backlog-drain": {"TestClaudeWakeDrainsBacklog"}, "scheduler/wake-order-guards": {"TestBusySendWakeOrderEvidenceGuards"}}
			r := summaryFallback(captures, row.ID, tests[row.ID]...)
			resolved.Results = []runner.Result{r}
			resolved.Status = r.Status
			resolved.Reason = r.Reason
		default:
			for _, check := range row.Checks {
				found := false
				for _, phase := range row.Phases {
					for _, r := range phases[phase] {
						if r.SignalID == check {
							resolved.Results = append(resolved.Results, r)
							found = true
						}
					}
				}
				if !found {
					resolved.Results = append(resolved.Results, result(row.ID, "not_completed", "required coverage check absent", check, nil))
				}
			}
			resolved.Status = aggregateResults(resolved.Results)
			resolved.Reason = "every mapped component is required"
		}
		out = append(out, resolved)
	}
	// Expand the parent Dialog heading into every relay/origin/state/options
	// combination even when the selected attempt deliberately omits starts.
	for _, key := range DialogDimensions() {
		row := CoverageEvidence{CoverageRow: CoverageRow{key.ID(), "SR-8.3 Dialog safety", []string{"dialogs"}, []string{key.ID()}, "required"}, Status: "not_completed", Reason: "required Dialog dimension absent"}
		for _, r := range phases["dialogs"] {
			if r.SignalID == key.ID() {
				row.Results = append(row.Results, r)
			}
		}
		if len(row.Results) > 0 {
			row.Status = aggregateResults(row.Results)
		}
		out = append(out, row)
	}
	return out
}
func summaryFallback(cs []Capture, id string, tests ...string) runner.Result {
	for _, c := range cs {
		if _, ok := c.Manifest.Artifacts["raw/scheduler-evidence.json"]; !ok {
			continue
		}
		r := fallbackResult(c, id, tests...)
		if r.Status != "not_completed" {
			return prefixResult(c.Manifest.RunID, r)
		}
	}
	return result(id, "not_completed", "exact final-candidate scheduler case/reason absent", tests, nil)
}

// AggregateQualification performs file-only analysis, preserving the complete
// prior summary and charges. Each phase uses the newest explicitly linked
// attempt which actually captured it; missing rerun evidence cannot erase a
// failure from that attempt.
// Future summary consumers must reuse this aggregation and provenance gate;
// see the qualification reuse instructions in docs/architecture.md.
func AggregateQualification(ledger Ledger, captures []Capture, previous *QualificationSummary) (QualificationSummary, error) {
	s := QualificationSummary{Schema: Schema, Identity: ledger.Identity, Ledger: ledger, Previous: previous}
	var err error
	s.Accounting, err = AccountTurns(ledger)
	if err != nil {
		s.Results = append(s.Results, errorResult("accounting", err))
	}
	if len(s.Accounting.Unreconciled) > 0 {
		s.Results = append(s.Results, result("accounting", "not_completed", "unreconciled starts remain", nil, s.Accounting.Unreconciled))
	}
	if previous != nil {
		prior, known := ledger.History[previous.Identity.Attempt]
		if !known || prior.Identity != previous.Identity || ledger.Identity.Previous == "" {
			return s, errors.New("previous summary is not an authorized historical attempt")
		}
		if !previous.Identity.Compatible(ledger.Identity) || previous.Identity.Kit != ledger.Identity.Kit || !samePriorLedger(previous.Ledger.Prior, ledger.Prior) {
			return s, errors.New("previous summary candidate/provider/kit differs")
		}
		s.PreviousSHA256 = ledger.Identity.Previous
	}
	byPhase := map[string][]runner.Result{}
	current := map[string]bool{}
	if previous != nil {
		var restore func(*QualificationSummary)
		restore = func(prior *QualificationSummary) {
			if prior == nil {
				return
			}
			restore(prior.Previous)
			for _, p := range prior.Phases {
				byPhase[p.Phase] = p.Verdict.Results
			}
		}
		restore(previous)
		s.Timings = append(s.Timings, previous.Timings...)
	}
	for _, c := range captures {
		if !ledger.Identity.Compatible(c.Before.Identity) || ledger.Identity.Kit != c.Before.Identity.Kit || !samePriorLedger(ledger.Prior, c.Before.Prior) {
			return s, errors.New("summary includes an incompatible capture")
		}
		if c.Before.Identity.Attempt != ledger.Identity.Attempt {
			return s, errors.New("historical captures must come through the sealed previous summary")
		}
		for _, charge := range c.After.Charges {
			found := false
			for _, current := range ledger.Charges {
				if charge.Attempt == current.Attempt && charge.ID == current.ID {
					charge.Consumers = nil
					current.Consumers = nil
					if !reflect.DeepEqual(charge, current) {
						return s, errors.New("capture charge differs from final ledger")
					}
					found = true
					break
				}
			}
			if !found {
				return s, errors.New("capture usage was removed from final ledger")
			}
		}
		s.ProductInputs = append(s.ProductInputs, c.Product)
		phase := c.Manifest.Phase
		if current[phase] {
			return s, fmt.Errorf("duplicate current phase capture %s", phase)
		}
		current[phase] = true
		v := AnalyzePhase(c.Root)
		data, er := runner.Read(c.Root, "manifest.json")
		if er != nil {
			return s, er
		}
		prefix := c.Manifest.RunID
		for i := range v.Results {
			v.Results[i] = prefixResult(prefix, v.Results[i])
		}
		if phase == "dialogs" && previous != nil {
			// Replace only dimensions owned by this selection. Other prior results
			// remain, including failures, until an explicit matching rerun repairs them.
			replace := map[string]bool{}
			for _, r := range v.Results {
				replace[r.SignalID] = true
			}
			for _, old := range byPhase[phase] {
				if (strings.HasPrefix(old.SignalID, "dialog/") || old.SignalID == "permission-force" || old.SignalID == "plan-force") && !replace[old.SignalID] {
					v.Results = append(v.Results, old)
				}
			}
			v.Aggregate()
		}
		byPhase[phase] = v.Results
		s.Phases = append(s.Phases, PhaseSummary{phase, c.Before.Identity.Attempt, prefix, runner.Digest(data), c.Manifest.KitSHA256, v})
		timings := ExtractTimings(c)
		for i := range timings {
			timings[i].Evidence = prefixRefs(prefix, timings[i].Evidence)
		}
		s.Timings = append(s.Timings, timings...)
	}
	// ci remains mandatory and separately charged, although it is not one of
	// the six selected shakedown invocations.
	if len(byPhase["signals"]) == 0 {
		for _, c := range captures {
			checks := c.ciResults()
			if aggregateResults(checks) == "pass" {
				for i := range checks {
					checks[i] = prefixResult(c.Manifest.RunID, checks[i])
				}
				byPhase["signals"] = checks
				break
			}
		}
	}
	for _, phase := range Registry() {
		checks := byPhase[phase.ID]
		if slices.Contains(ledger.Selected, phase.ID) && phase.ID != "signals" && !current[phase.ID] {
			checks = PhaseResult(runner.Manifest{Phase: phase.ID}, nil).Results
			checks = append(checks, result("current-attempt-capture", "not_completed", "selected phase has no current-attempt capture", ledger.Identity.Attempt, nil))
			byPhase[phase.ID] = checks
		}
		if len(checks) == 0 {
			checks = PhaseResult(runner.Manifest{Phase: phase.ID}, nil).Results
			byPhase[phase.ID] = checks
		}
		for _, r := range checks {
			display := r
			if facts, ok := r.Observed.(map[string]any); ok && r.Status == "pass" {
				if _, ok = facts["accepted_limit"]; ok {
					display.Status = "accepted_limit"
				}
			}
			s.Checks = append(s.Checks, CheckRecord{phase.ID, display})
			if r.SignalID == "cleanup" {
				s.Cleanup = append(s.Cleanup, r)
			}
			if strings.HasPrefix(r.SignalID, "force/") || r.SignalID == "permission-force" || r.SignalID == "plan-force" {
				s.ForceStates = append(s.ForceStates, r)
			}
			if strings.Contains(r.SignalID, "ladder") {
				s.RouteBounds = append(s.RouteBounds, r)
			}
			if r.SignalID == "group-gaps" {
				s.Gaps = append(s.Gaps, r)
			}
		}
	}
	s.Coverage = ResolveCoverageEvidence(captures, byPhase)
	if previous != nil {
		old := map[string]CoverageEvidence{}
		for _, row := range previous.Coverage {
			old[row.ID] = row
		}
		for i, row := range s.Coverage {
			if row.Kind == "scheduler" || row.Kind == "named_premise" {
				if row.Status == "not_completed" {
					if p, ok := old[row.ID]; ok {
						s.Coverage[i] = p
					}
				}
			}
		}
	}
	for _, row := range s.Coverage {
		if row.Status == "named_premise" {
			s.NamedPremises = append(s.NamedPremises, runner.NamedPremise{ID: row.ID, Reference: row.Source + "; t2.xis.w3.b3", Reason: row.Reason})
			continue
		}
		if row.Status == "claude_inapplicable" {
			continue
		}

	}
	sort.SliceStable(s.ForceStates, func(i, j int) bool { return s.ForceStates[i].SignalID < s.ForceStates[j].SignalID })
	scopes := AggregatePhaseResults(ledger.Kind, ledger.Selected, byPhase, s.Coverage, s.Results)
	s.MeasurementGaps = measurementGaps(byPhase, s.Coverage, s.Results)
	s.Results = scopes.Results
	s.SelectedStatus, s.FullStatus, s.MeasurementStatus, s.Status = scopes.SelectedStatus, scopes.FullStatus, scopes.MeasurementStatus, scopes.Status
	s.Rerun = "qualification-rerun --ledger LEDGER --attempt NEW --allowance N --phase SELECTED --dialog-cases D1,D2,D3,D4,D5,D6 --previous SUMMARY_JSON (new explicit MI authority required)"
	return s, nil
}

// ScopeResults contains aggregation only, without any capture or provider claim.
type ScopeResults struct {
	Status            string          `json:"status"`
	SelectedStatus    string          `json:"selected_status"`
	FullStatus        string          `json:"full_status"`
	MeasurementStatus string          `json:"b3_measurement_status"`
	Results           []runner.Result `json:"results"`
}

// AggregatePhaseResults is the pure status seam used after capture analysis.
// Phase keys label outcomes explicitly; unit callers can supply labelled unit
// results without creating a synthetic live capture. This function establishes
// no provenance: AggregateQualification validates and analyzes captures first.
// Future status-only consumers must reuse this seam without treating it as
// capture validation; see docs/architecture.md.
func AggregatePhaseResults(kind string, selectedPhases []string, phases map[string][]runner.Result, coverage []CoverageEvidence, prerequisites []runner.Result) ScopeResults {
	full := append([]runner.Result{}, prerequisites...)
	selected := append([]runner.Result{}, prerequisites...)
	measure := append([]runner.Result{}, prerequisites...)
	for _, phase := range Registry() {
		checks := phases[phase.ID]
		if len(checks) == 0 {
			checks = PhaseResult(runner.Manifest{Phase: phase.ID}, nil).Results
		}
		full = append(full, checks...)
		if slices.Contains(selectedPhases, phase.ID) || phase.ID == "signals" {
			selected = append(selected, checks...)
		}
		if phase.Shakedown || phase.ID == "signals" {
			measure = append(measure, checks...)
		}
	}
	for _, row := range coverage {
		if row.Status == "named_premise" || row.Status == "claude_inapplicable" {
			continue
		}
		r := result(row.ID, row.Status, row.Reason, "complete applicable coverage", row.Results)
		full = append(full, r)
		if row.ID == "o4/r-a" || row.ID == "scheduler/wake-order-guards" {
			measure = append(measure, r)
		}
		if strings.HasPrefix(row.ID, "dialog/") {
			selected = append(selected, r)
			measure = append(measure, r)
		}
	}
	out := ScopeResults{SelectedStatus: aggregateResults(selected), FullStatus: aggregateResults(full), MeasurementStatus: aggregateResults(measure), Results: full}
	out.Status = out.SelectedStatus
	if kind == "full" {
		out.Status = out.FullStatus
	}
	return out
}

// ReadSummary validates the ledger against the immutable prior file before a
// rerun can change authority. It never resets charges or invents an allowance.
func ReadSummary(path string) (QualificationSummary, string, error) {
	var s QualificationSummary
	b, err := runner.Read(filepath.Dir(path), filepath.Base(path))
	if err != nil {
		return s, "", err
	}
	if err = json.Unmarshal(b, &s); err != nil {
		return s, "", err
	}
	if s.Schema != Schema || s.Identity != s.Ledger.Identity {
		return s, "", errors.New("invalid summary schema/identity")
	}
	if _, err = AccountTurns(s.Ledger); err != nil {
		return s, "", err
	}
	data, err := runner.Read(filepath.Dir(path), "index.json")
	if err != nil {
		return s, "", err
	}
	var index map[string]runner.Artifact
	if err = json.Unmarshal(data, &index); err != nil {
		return s, "", err
	}
	entry, ok := index[filepath.Base(path)]
	if !ok || entry.SHA256 != runner.Digest(b) || entry.Bytes != int64(len(b)) {
		return s, "", errors.New("sealed summary does not match its evidence index")
	}
	for ref, want := range index {
		data, er := runner.Read(filepath.Dir(path), ref)
		if er != nil {
			return s, "", er
		}
		if runner.Digest(data) != want.SHA256 || int64(len(data)) != want.Bytes {
			return s, "", errors.New("previous summary evidence changed")
		}
	}
	return s, runner.Digest(b), nil
}
func Rerun(path, attempt string, allowance int, phases, dialogs []string, previousPath string) error {
	prior, hash, err := ReadSummary(previousPath)
	if err != nil {
		return err
	}
	return UpdateLedger(path, func(l *Ledger) error {
		a, _ := json.Marshal(l)
		b, _ := json.Marshal(prior.Ledger)
		if !bytes.Equal(a, b) {
			return errors.New("previous summary ledger differs from current durable authority")
		}
		return NewAttempt(l, attempt, allowance, phases, dialogs, hash)
	})
}

// WriteQualificationSummary packages the exact retained inputs under the new
// output directory; all returned evidence references remain retrievable.
func WriteQualificationSummary(output string, ledger Ledger, roots []string, previousPath string) (QualificationSummary, error) {
	var previous *QualificationSummary
	if ledger.Identity.Previous != "" {
		p, hash, err := ReadSummary(previousPath)
		if err != nil {
			return QualificationSummary{}, err
		}
		if hash != ledger.Identity.Previous {
			return QualificationSummary{}, errors.New("previous summary hash differs from rerun link")
		}
		previous = &p
		priorRoot := filepath.Dir(previousPath)
		rel, er := filepath.Rel(priorRoot, output)
		if er != nil {
			return QualificationSummary{}, er
		}
		if rel == "." || (!filepath.IsAbs(rel) && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
			return QualificationSummary{}, errors.New("new summary output must be outside the previous export")
		}
	}
	var captures []Capture
	seen := map[string]bool{}
	for _, root := range roots {
		c, err := LoadCapture(root)
		if err != nil {
			return QualificationSummary{}, err
		}
		if seen[c.Manifest.RunID] {
			return QualificationSummary{}, errors.New("duplicate capture run")
		}
		seen[c.Manifest.RunID] = true
		captures = append(captures, c)
	}
	s, err := AggregateQualification(ledger, captures, previous)
	if err != nil {
		return s, err
	}
	evidence, err := runner.NewEvidence(output)
	if err != nil {
		return s, err
	}
	for _, c := range captures {
		for ref := range c.Manifest.Artifacts {
			b, er := c.read(ref)
			if er != nil {
				return s, er
			}
			if er = evidence.Put(c.Manifest.RunID+"/"+ref, b); er != nil {
				return s, er
			}
		}
		b, er := runner.Read(c.Root, "manifest.json")
		if er != nil {
			return s, er
		}
		if er = evidence.Put(c.Manifest.RunID+"/manifest.json", b); er != nil {
			return s, er
		}
	}
	if previous != nil {
		// Preserve its exported evidence, not only a link to a VM path which may
		// disappear after evidence retrieval. The previous summary remains intact.
		source := filepath.Dir(previousPath)
		err = filepath.WalkDir(source, func(path string, d os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if d.IsDir() {
				return nil
			}
			rel, er := filepath.Rel(source, path)
			if er != nil {
				return er
			}
			if rel == "summary.json" || rel == "report.txt" || rel == "index.json" {
				return nil
			}
			if seen[strings.Split(filepath.ToSlash(rel), "/")[0]] {
				return nil
			}
			b, er := runner.Read(source, filepath.ToSlash(rel))
			if er != nil {
				return er
			}
			return evidence.Put(filepath.ToSlash(rel), b)
		})
		if err != nil {
			return s, err
		}
	}
	if err = evidence.JSON("summary.json", s); err != nil {
		return s, err
	}
	var report strings.Builder
	fmt.Fprintf(&report, "%s selected=%s full=%s b3=%s\n", strings.ToUpper(s.Status), s.SelectedStatus, s.FullStatus, s.MeasurementStatus)
	fmt.Fprintf(&report, "candidate=%s provider=%s kit=%s attempt=%s\n", s.Identity.Candidate, s.Identity.Provider, s.Identity.Kit, s.Identity.Attempt)
	fmt.Fprintf(&report, "turns: historical=%d ci=%d actual=%d E8_remaining=%d shakedown_remaining=%d; no unused authority grants a retry\n", s.Accounting.Historical, s.Accounting.CI, s.Accounting.Actual, s.Accounting.E8Remaining, s.Accounting.ShakedownRemaining)
	for _, r := range s.Results {
		fmt.Fprintf(&report, "%s %s: %s\n", r.Status, r.SignalID, r.Reason)
	}
	if err = evidence.Put("report.txt", []byte(report.String())); err != nil {
		return s, err
	}
	index, err := evidence.Index()
	if err != nil {
		return s, err
	}
	err = evidence.JSON("index.json", index)
	return s, err
}
