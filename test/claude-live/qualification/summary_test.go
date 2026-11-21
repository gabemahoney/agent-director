package qualification_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/test/claude-live/internal/qualificationfix"
	q "github.com/gabemahoney/agent-director/test/claude-live/qualification"
	"github.com/gabemahoney/agent-director/test/claude-live/runner"
)

func coverageRow(t *testing.T, rows []q.CoverageEvidence, id string) q.CoverageEvidence {
	t.Helper()
	for _, r := range rows {
		if r.ID == id {
			return r
		}
	}
	t.Fatalf("required coverage row disappeared: %s", id)
	return q.CoverageEvidence{}
}

// Independent SR-8.3/PRD input list: do not derive this requirement set from
// Coverage(), or deleting a production row would silently shrink the test.
func requiredCoverageIDs() []string {
	return []string{"sr8.3/fresh-resumed", "sr8.3/dialogs", "sr8.3/busy-idle-compaction-startup", "sr8.3/parent-subagent-background", "sr8.3/forced-states", "sr8.3/mixed-options", "sr8.3/whole-unicode-tags", "sr8.3/packing-boundaries", "sr8.3/multi-prompt-groups", "sr8.3/backlog", "sr8.3/crash-lost-reply", "sr8.3/concurrent-callbacks", "sr8.3/replacement", "sr8.3/typed-controls", "sr8.3/hooks-stale-unknown", "sr8.3/uncovered-agents", "o4/p-b", "o4/p-c", "o4/turn-end-premise", "o4/wake-order-point", "o4/r-a", "sr2.5/never-sighted-hold", "prd/Seamless upgrades", "scheduler/full-backlog-drain", "scheduler/wake-order-guards", "scheduler/interface-parity", "sr8.3/hook-block", "sr8.3/hook-context", "prd/Agents busy send does not cover", "prd/Dialog safety", "prd/Send Modes", "prd/Accepting", "prd/Order and integrity", "prd/Waiting and resume", "prd/Forced delivery", "prd/Confirm From Transcripts", "prd/Event-only delivery", "prd/Agent Defaults", "prd/Results, IDs, Lookup and recovery", "prd/No raw keys", "prd/Limits and CSCB traffic", "prd/Release", "sr4.4/dialog-force-states"}
}

func TestQualificationCoverageObligations(t *testing.T) {
	rows := q.ResolveCoverageEvidence(nil, nil)
	for _, id := range requiredCoverageIDs() {
		if got := coverageRow(t, rows, id); got.Status != "not_completed" {
			t.Fatalf("missing evidence resolved: %+v", got)
		}
	}
	replacement := coverageRow(t, rows, "claude/prompt-replacement")
	if replacement.Status != "claude_inapplicable" || !strings.Contains(replacement.Reason, "https://code.claude.com/docs/en/hooks#decision-control") {
		t.Fatalf("replacement invented: %+v", replacement)
	}
	for _, r := range rows {
		if r.ID == "o4/p-a" || r.ID == "hook-completion-order" {
			t.Fatalf("obsolete obligation: %+v", r)
		}
	}
}

func TestQualificationDialogDimensions(t *testing.T) {
	var keys []q.DialogKey
	for _, mode := range []string{"wait_turn", "steer_turn"} {
		for _, confirm := range []bool{false, true} {
			for _, relay := range []bool{false, true} {
				for _, origin := range []string{"main", "subagent", "background"} {
					for _, state := range []string{"open", "about_to_open"} {
						keys = append(keys, q.DialogKey{Mode: mode, Confirmed: confirm, Relay: relay, Origin: origin, State: state})
					}
				}
			}
		}
	}
	if !reflect.DeepEqual(q.DialogDimensions(), keys) {
		t.Fatal("required Dialog dimensions changed")
	}
	for _, missing := range []q.DialogKey{
		{Mode: "wait_turn", Relay: false, Origin: "background", State: "open"},
		{Mode: "steer_turn", Confirmed: true, Relay: true, Origin: "subagent", State: "about_to_open"},
	} {
		for _, status := range []string{"not_completed", "fail"} {
			t.Run(missing.ID()+"/"+status, func(t *testing.T) {
				checks := []runner.Result{{SignalID: "dialog-matrix", Status: "pass"}}
				for _, k := range keys {
					if k != missing {
						checks = append(checks, runner.Result{SignalID: k.ID(), Status: "pass", Evidence: []string{"unit/coverage"}})
					}
				}
				if status == "fail" {
					checks = append(checks, runner.Result{SignalID: missing.ID(), Status: "fail", Reason: "unit contradiction"})
				}
				rows := q.ResolveCoverageEvidence(nil, map[string][]runner.Result{"dialogs": checks})
				got := coverageRow(t, rows, missing.ID())
				if got.Status != status {
					t.Fatalf("parent or opposite relay hid missing dimension: %+v", got)
				}
				matching := missing
				matching.Relay = !matching.Relay
				if coverageRow(t, rows, matching.ID()).Status != "pass" {
					t.Fatal("unrelated evidence lost")
				}
			})
		}
	}
}

func TestQualificationSchedulerFallback(t *testing.T) {
	for _, kind := range []string{"exact", "wrong-candidate", "wrong-product", "failed-job", "wrong-arch", "no-reason", "wrong-subject", "parent-only", "wrong-case", "wrong-log-hash", "contradictory-log", "upgrade-go-heading"} {
		t.Run(kind, func(t *testing.T) {
			i := q.Identity{Candidate: "candidate", Product: "product"}
			key := q.DialogKey{Mode: "wait_turn", Origin: "background", State: "open"}
			log := []byte("{\"Action\":\"pass\",\"Package\":\"unit/dialog\",\"Test\":\"TestDialog/relay-off-background\"}\n")
			job := q.SchedulerJob{ID: "unit-job", Candidate: i.Candidate, Product: i.Product, Status: "succeeded", Platform: "linux/amd64", Environment: json.RawMessage(`{"dpkg_architecture":"amd64"}`)}
			test := q.SchedulerTest{Coverage: key.ID(), Package: "unit/dialog", Name: "TestDialog", Case: "relay-off-background", Log: "unit/log", SHA256: runner.Digest(log), Format: "go-test-json", Reason: "native combination unavailable", Kind: "native_unavailable", Source: "unit/source.go", Subject: &key}
			switch kind {
			case "wrong-candidate":
				job.Candidate = "stale"
			case "wrong-product":
				job.Product = "stale"
			case "failed-job":
				job.Status = "failed"
			case "wrong-arch":
				job.Platform = "linux/arm64"
			case "no-reason":
				test.Reason = ""
			case "wrong-subject":
				other := key
				other.Relay = true
				test.Subject = &other
			case "parent-only":
				test.Case = ""
			case "wrong-case":
				test.Case = "relay-on-background"
			case "wrong-log-hash":
				test.SHA256 = runner.Digest([]byte("other"))
			case "contradictory-log":
				log = append(log, []byte("{\"Action\":\"fail\",\"Package\":\"unit/dialog\",\"Test\":\"TestDialog/relay-off-background\"}\n")...)
				test.SHA256 = runner.Digest(log)
			case "upgrade-go-heading":
				test.Coverage = "prd/Seamless upgrades"
				test.Kind = "upgrade"
				test.Subject = nil
				f := qualificationfix.New(t, "groups")
				c := f.Capture()
				job.Candidate, job.Product = c.Before.Identity.Candidate, c.Before.Identity.Product
				job.Tests = []q.SchedulerTest{test}
				f.PutBytes("raw/scheduler/"+test.SHA256, log)
				f.Put("raw/scheduler-evidence.json", q.SchedulerEvidence{Schema: q.Schema, Jobs: []q.SchedulerJob{job}})
				got := coverageRow(t, q.ResolveCoverageEvidence([]q.Capture{c}, nil), test.Coverage)
				if got.Status != "fail" {
					t.Fatalf("unrelated Go test qualified E7 Docker coverage: %+v", got)
				}
				return
			}
			err := q.ValidateSchedulerTest(i, job, test, log)
			if (err == nil) != (kind == "exact") {
				t.Fatalf("%s: %v", kind, err)
			}
		})
	}
}

func TestQualificationCaptureIdentity(t *testing.T) {
	for _, kind := range []string{"same", "different-kit", "different-product", "different-provider", "different-run", "unlinked-attempt", "linked-attempt"} {
		t.Run(kind, func(t *testing.T) {
			i := q.Identity{Run: "unit", Attempt: "a1", Candidate: "candidate", Product: "product", Provider: "provider", Kit: "kit"}
			consumer := q.Capture{Before: q.Ledger{Identity: i}}
			producer := q.Capture{Before: q.Ledger{Identity: i}}
			want := kind == "same" || kind == "linked-attempt"
			switch kind {
			case "different-kit":
				producer.Before.Identity.Kit = "other"
			case "different-product":
				producer.Before.Identity.Product = "other"
			case "different-provider":
				producer.Before.Identity.Provider = "other"
			case "different-run":
				producer.Before.Identity.Run = "other"
			case "unlinked-attempt", "linked-attempt":
				producer.Before.Identity.Attempt = "a0"
				if kind == "linked-attempt" {
					consumer.Before.Identity.Previous = "sealed-hash"
					consumer.Before.History = map[string]q.AttemptAuthority{"a0": {Identity: producer.Before.Identity}}
				}
			}
			if err := q.CompatibleCapture(consumer, producer); (err == nil) != want {
				t.Fatalf("compatible=%t want=%t: %v", err == nil, want, err)
			}
		})
	}
}

func TestQualificationFileCapture(t *testing.T) {
	for _, kind := range []string{"historical-adaptation", "candidate", "provider", "kit", "attempt-authority", "source-product"} {
		t.Run(kind, func(t *testing.T) {
			f := qualificationfix.New(t, "groups")
			switch kind {
			case "candidate":
				f.Manifest.Candidate = "other"
			case "provider":
				f.Manifest.ProviderVersion = "other"
			case "kit":
				f.Manifest.KitSHA256 = "other"
			case "attempt-authority":
				var l q.Ledger
				f.Decode("raw/qualification-ledger-after.json", &l)
				l.Identity.Attempt = "other"
				f.Put("raw/qualification-ledger-after.json", l)
			case "source-product":
				var p q.ProductSource
				f.Decode("raw/qualification/source.json", &p)
				p.Product = runner.Digest([]byte("other"))
				f.Put("raw/qualification/source.json", p)
			}
			f.Reindex()
			c, err := q.LoadCapture(f.Root)
			if (err == nil) != (kind == "historical-adaptation") {
				t.Fatalf("%s: %v", kind, err)
			}
			if kind == "historical-adaptation" && (c.Manifest.Candidate != f.Manifest.Candidate || c.Before.Identity.Attempt != "unit-adaptation" || len(c.Owners) != 2) {
				t.Fatalf("provenance relabelled: %+v", c.Manifest)
			}
		})
	}
}

func TestQualificationAggregateMissingAndFailure(t *testing.T) {
	f := qualificationfix.New(t, "groups")
	for _, kind := range []string{"missing", "failure", "unreconciled", "stale-capture"} {
		t.Run(kind, func(t *testing.T) {
			ledger := f.Ledger
			var captures []q.Capture
			if kind == "failure" || kind == "stale-capture" {
				c := f.Capture()
				if kind == "stale-capture" {
					c.Before.Identity.Kit = "other"
				}
				captures = []q.Capture{c}
			}
			if kind == "unreconciled" {
				ledger.Charges = []q.Charge{{ID: "G0", Phase: "groups", Attempt: ledger.Identity.Attempt, Reserved: 1}}
			}
			s, err := q.AggregateQualification(ledger, captures, nil)
			if kind == "stale-capture" {
				if err == nil {
					t.Fatal("mixed-kit summary accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if s.FullStatus == "pass" || s.MeasurementStatus == "pass" || s.Status == "pass" {
				t.Fatalf("missing phases qualified: %+v", s)
			}
			for _, id := range requiredCoverageIDs() {
				coverageRow(t, s.Coverage, id)
			}
			if len(s.Coverage) < 48 || len(s.Checks) == 0 {
				t.Fatal("nonpass erased incomplete obligations")
			}
			if kind == "missing" && s.Status != "not_completed" {
				t.Fatalf("missing became %s", s.Status)
			}
		})
	}
	// Exercise the actual file export/seal boundary, never a captured PASS.
	out := filepath.Join(t.TempDir(), "summary")
	s, err := q.WriteQualificationSummary(out, f.Ledger, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(out, "report.txt"))
	if err != nil {
		t.Fatal(err)
	}
	first := strings.SplitN(string(data), "\n", 2)[0]
	if first != "NOT_COMPLETED selected=not_completed full=not_completed b3=not_completed" || s.Status != "not_completed" {
		t.Fatalf("scope lost: %s", first)
	}
	sealed, hash, err := q.ReadSummary(filepath.Join(out, "summary.json"))
	if err != nil || hash == "" || sealed.Identity != f.Ledger.Identity {
		t.Fatalf("sealed summary: %v", err)
	}
	if err := os.WriteFile(filepath.Join(out, "report.txt"), []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := q.ReadSummary(filepath.Join(out, "summary.json")); err == nil {
		t.Fatal("changed retained evidence accepted")
	}
}

func TestQualificationRerunNeedsCurrentCapture(t *testing.T) {
	f := qualificationfix.New(t, "groups")
	ledger := f.Ledger
	old := ledger.Identity
	old.Attempt = "old"
	ledger.Identity.Previous = "unit-seal"
	var oldSelection []string
	for _, phase := range q.Registry() {
		oldSelection = append(oldSelection, phase.ID)
	}
	ledger.History = map[string]q.AttemptAuthority{"old": {Identity: old, Selected: oldSelection, DialogCases: ledger.DialogCases, Allowance: 48}}
	ledger.Allowances["old"] = 48
	prior := q.QualificationSummary{Schema: q.Schema, Identity: old}
	// Synthetic aggregate inputs, not recordings or assertions of live success.
	for _, phase := range q.Registry() {
		var checks []runner.Result
		for _, id := range phase.Checks {
			checks = append(checks, runner.Result{SignalID: id, Status: "pass", Evidence: []string{"unit/old-result"}})
		}
		if phase.ID == "dialogs" {
			for _, key := range q.DialogDimensions() {
				checks = append(checks, runner.Result{SignalID: key.ID(), Status: "pass"})
			}
		}
		prior.Phases = append(prior.Phases, q.PhaseSummary{Phase: phase.ID, Attempt: "old", Verdict: runner.Verdict{Status: "pass", Results: checks}})
	}
	s, err := q.AggregateQualification(ledger, nil, &prior)
	if err != nil {
		t.Fatal(err)
	}
	if s.SelectedStatus != "not_completed" || s.Status != "not_completed" || s.Previous != &prior {
		t.Fatalf("selected rerun inherited old PASS: selected=%s status=%s", s.SelectedStatus, s.Status)
	}
}

func TestQualificationSelectedScopeResults(t *testing.T) {
	for _, kind := range []string{"complete-selected", "full-request", "missing-phase", "missing-signals", "missing-typed-pair", "missing-typed-recovery", "missing-resume-recovery", "missing-placement", "missing-notification", "missing-dialog-dimension", "missing-order-guards", "failed-measurement", "failed-prerequisite", "unresolved-cleanup"} {
		t.Run(kind, func(t *testing.T) {
			selected := []string{"typed-controls", "force-states", "dialogs", "groups", "text-limits-backlog", "esc-gap"}
			// Labelled unit outcomes at S1, never captures or provider success.
			// These independent inputs represent completed phase judgments;
			// their evidence/provenance checks are tested at the capture seams.
			ids := map[string][]string{
				"signals":             {"signal-capture", "p-b", "p-c", "turn-end-premise", "wake-order-point"},
				"typed-controls":      {"typed-long-tool", "typed-between-tools", "typed-idle", "steer-cap"},
				"force-states":        {"force-table", "force-pending", "force-group", "wake-caps"},
				"dialogs":             {"dialog-matrix", "pause-refusal"},
				"groups":              {"steer-group", "ordinary-group", "group-gaps", "small-backlog", "whole-corpus"},
				"text-limits-backlog": {"native-wake-ladder", "native-steer-ladder", "ad-caps", "ad-refusals", "large-backlog"},
				"esc-gap":             {"esc-typed-recovery", "esc-resume-recovery", "esc-notification", "esc-input-placement"},
			}
			unit := func(id, status string) runner.Result {
				return runner.Result{SignalID: id, Status: status, Reason: "synthetic unit aggregation outcome", Expected: "labelled unit result", Observed: "synthetic; no provider claim", Evidence: []string{"unit/S1"}}
			}
			phases := map[string][]runner.Result{}
			missing := map[string]string{"missing-typed-pair": "typed-between-tools", "missing-typed-recovery": "esc-typed-recovery", "missing-resume-recovery": "esc-resume-recovery", "missing-placement": "esc-input-placement", "missing-notification": "esc-notification"}
			for phase, checks := range ids {
				for _, id := range checks {
					status := "pass"
					if id == missing[kind] {
						status = "not_completed"
					}
					if kind == "failed-measurement" && id == "group-gaps" {
						status = "fail"
					}
					phases[phase] = append(phases[phase], unit(id, status))
				}
			}
			if kind == "missing-phase" {
				delete(phases, "groups")
			}
			if kind == "missing-signals" {
				delete(phases, "signals")
			}
			coverage := []q.CoverageEvidence{
				{CoverageRow: q.CoverageRow{ID: "o4/r-a", Kind: "named_premise"}, Status: "named_premise", Reason: "synthetic disclosed unit premise"},
				{CoverageRow: q.CoverageRow{ID: "claude/prompt-replacement", Kind: "claude_inapplicable"}, Status: "claude_inapplicable", Reason: "synthetic unit applicability outcome"},
				{CoverageRow: q.CoverageRow{ID: "scheduler/wake-order-guards", Kind: "scheduler"}, Status: "pass", Reason: "synthetic exact-test resolution outcome"},
			}
			if kind == "missing-order-guards" {
				coverage[2].Status = "not_completed"
			}
			for i, key := range q.DialogDimensions() {
				status := "pass"
				if kind == "missing-dialog-dimension" && i == 0 {
					status = "not_completed"
				}
				coverage = append(coverage, q.CoverageEvidence{CoverageRow: q.CoverageRow{ID: key.ID(), Kind: "required"}, Status: status, Reason: "synthetic unit Dialog resolution"})
			}
			prerequisites := []runner.Result{unit("accounting", "pass"), unit("cleanup", "pass")}
			if kind == "failed-prerequisite" {
				prerequisites[0].Status = "fail"
			}
			if kind == "unresolved-cleanup" {
				prerequisites[1].Status = "not_completed"
			}
			runKind := "shakedown"
			if kind == "full-request" {
				runKind = "full"
			}
			got := q.AggregatePhaseResults(runKind, selected, phases, coverage, prerequisites)
			wantSelected, wantFull, wantB3 := "pass", "not_completed", "pass"
			switch kind {
			case "complete-selected", "full-request":
			case "failed-measurement", "failed-prerequisite":
				wantSelected, wantFull, wantB3 = "fail", "fail", "fail"
			case "missing-order-guards":
				wantB3 = "not_completed"
			default:
				wantSelected, wantB3 = "not_completed", "not_completed"
			}
			wantStatus := wantSelected
			if runKind == "full" {
				wantStatus = wantFull
			}
			if got.Status != wantStatus || got.SelectedStatus != wantSelected || got.FullStatus != wantFull || got.MeasurementStatus != wantB3 {
				t.Fatalf("unit scopes: status=%s selected=%s full=%s b3=%s; want %s/%s/%s/%s", got.Status, got.SelectedStatus, got.FullStatus, got.MeasurementStatus, wantStatus, wantSelected, wantFull, wantB3)
			}
			// Selected success must retain all deferred obligations, including
			// alongside a mandatory failure, rather than dropping their rows.
			for _, id := range []string{"launch-setup", "lost-reply", "hook-block", "uncovered-agents"} {
				if row := namedResult(t, got.Results, id); row.Status != "not_completed" {
					t.Fatalf("deferred obligation lost: %+v", row)
				}
			}
		})
	}
}

// R14-3: coverage-only guards and independent nested GAPs must survive a
// failed combined judgment. This adapts evidence, never a live qualification.
func TestQualificationSummaryAllMeasurementGaps(t *testing.T) {
	f := qualificationfix.New(t, "groups")
	child := runner.Result{SignalID: "unit-independent-incomplete", Status: "not_completed", Reason: "unit measurement lacks its next input", Evidence: []string{"raw/qualification-observations.json"}}
	f.Manifest.Problems = []runner.Result{{SignalID: "unit-combined", Status: "fail", Reason: "unit independent failure retained", Observed: []runner.Result{{SignalID: "unit-failure", Status: "fail"}, child}}}
	c := f.Capture()
	s, err := q.AggregateQualification(f.Ledger, []q.Capture{c}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if s.Status != "fail" || s.MeasurementStatus != "fail" || s.SelectedStatus != "fail" {
		t.Fatalf("nested GAP changed failed aggregate: status=%s b3=%s selected=%s", s.Status, s.MeasurementStatus, s.SelectedStatus)
	}
	for _, id := range []string{"o4/r-a", "scheduler/wake-order-guards"} {
		if got := coverageRow(t, s.Coverage, id); got.Status != "not_completed" {
			t.Fatalf("missing guard citation must remain incomplete: %+v", got)
		}
	}
	for _, id := range []string{"o4/r-a", "scheduler/wake-order-guards", child.SignalID} {
		count := 0
		for _, gap := range s.MeasurementGaps {
			if gap.SignalID != id {
				continue
			}
			count++
			if gap.Status != "not_completed" || gap.Phase == "" {
				t.Errorf("GAP lost status or phase/coverage identity: %+v", gap)
			}
			if id == child.SignalID && !slices.Contains(gap.Evidence, c.Manifest.RunID+"/"+child.Evidence[0]) {
				t.Errorf("nested GAP lost evidence provenance: %+v", gap)
			}
		}
		if count != 1 {
			t.Errorf("b3_gaps must enumerate %s once, got %d", id, count)
		}
	}
}
