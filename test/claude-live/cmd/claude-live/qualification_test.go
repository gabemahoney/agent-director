package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/test/claude-live/internal/qualificationfix"
	q "github.com/gabemahoney/agent-director/test/claude-live/qualification"
	"github.com/gabemahoney/agent-director/test/claude-live/runner"
)

func TestQualificationOfflineCommand(t *testing.T) {
	for _, kind := range []string{"missing-scope", "explicit-failure", "missing-capture"} {
		t.Run(kind, func(t *testing.T) {
			f := qualificationfix.New(t, "groups")
			if kind == "explicit-failure" {
				f.Manifest.Problems = append(f.Manifest.Problems, runner.Result{SignalID: "unit-explicit-failure", Status: "fail", Reason: "synthetic command-boundary failure"})
				f.Reindex()
			}
			var stdout, stderr bytes.Buffer
			if kind == "missing-capture" {
				root := filepath.Join(t.TempDir(), "absent")
				code := run([]string{"qualification-analyze", "--evidence", root}, &stdout, &stderr)
				var got runner.Verdict
				if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
					t.Fatal(err)
				}
				want, _ := json.Marshal(q.AnalyzePhase(root))
				actual, _ := json.Marshal(got)
				if code != 1 || stderr.Len() != 0 || !bytes.Equal(actual, want) {
					t.Fatalf("exit=%d stderr=%s stdout=%s", code, &stderr, &stdout)
				}
				return
			}
			ledgerPath := filepath.Join(t.TempDir(), "ledger.json")
			b, _ := json.Marshal(f.Ledger)
			if err := os.WriteFile(ledgerPath, b, 0600); err != nil {
				t.Fatal(err)
			}
			var captures []q.Capture
			var roots []string
			if kind == "explicit-failure" {
				captures = []q.Capture{f.Capture()}
				roots = []string{f.Root}
			}
			want, err := q.AggregateQualification(f.Ledger, captures, nil)
			if err != nil {
				t.Fatal(err)
			}
			out := filepath.Join(t.TempDir(), "export")
			args := []string{"qualification-summary", "--ledger", ledgerPath, "--out", out}
			if len(roots) > 0 {
				args = append(args, "--captures", strings.Join(roots, ","))
			}
			code := run(args, &stdout, &stderr)
			var got q.QualificationSummary
			if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
				t.Fatalf("summary: %v stderr=%s", err, &stderr)
			}
			canonical := func(v any) any { b, _ := json.Marshal(v); var out any; _ = json.Unmarshal(b, &out); return out }
			if code != 1 || stderr.Len() != 0 || !reflect.DeepEqual(canonical(got), canonical(want)) {
				t.Fatalf("CLI/callable differ: exit=%d stderr=%s", code, &stderr)
			}
			wantStatus := "not_completed"
			if kind == "explicit-failure" {
				wantStatus = "fail"
			}
			if got.Status != wantStatus || got.FullStatus == "pass" || got.MeasurementStatus == "pass" {
				t.Fatalf("blanket qualification claim: selected=%s full=%s b3=%s", got.Status, got.FullStatus, got.MeasurementStatus)
			}
			file, err := os.ReadFile(filepath.Join(out, "summary.json"))
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(canonical(got), canonical(json.RawMessage(file))) {
				t.Fatal("machine-readable file differs")
			}
			report, err := os.ReadFile(filepath.Join(out, "report.txt"))
			if err != nil {
				t.Fatal(err)
			}
			first := strings.SplitN(string(report), "\n", 2)[0]
			wantLine := strings.ToUpper(want.Status) + " selected=" + want.SelectedStatus + " full=" + want.FullStatus + " b3=" + want.MeasurementStatus
			if first != wantLine {
				t.Fatalf("report scope = %q; want %q", first, wantLine)
			}
		})
	}
}
