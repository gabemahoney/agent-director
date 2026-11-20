package qualification

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/gabemahoney/agent-director/test/claude-live/runner"
)

// These histories are synthetic accounting inputs, never live qualification evidence.
func TestAccountingApprovedBudgets(t *testing.T) {
	for _, tc := range []struct {
		name string
		kind string
		ci   int
		used int
		e8   int
	}{
		{"shakedown-ci-six", "shakedown", 6, 32, 12},
		{"shakedown-ci-seven", "shakedown", 7, 32, 11},
		{"separate-full-run", "full", 0, 47, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l, err := NewLedger(Identity{Run: "run", Attempt: "first", Candidate: "candidate", Product: "product", Provider: "claude-version", Kit: "kit"}, tc.kind,
				SignalCharge{Capture: "ci-capture", Product: "product", Provider: "claude-version", Actual: tc.ci})
			if err != nil {
				t.Fatal(err)
			}
			shares := []struct {
				phase string
				count int
			}{{"setup-launches", 4}, {"signals", 7}, {"typed-controls", 6}, {"force-states", 5}, {"dialogs", 6}, {"groups", 3}, {"text-limits-backlog", 8}, {"lifecycle", 2}, {"user-hooks", 2}, {"esc-gap", 4}, {"uncovered", 0}}
			wantSelected := []string{"typed-controls", "force-states", "dialogs", "groups", "text-limits-backlog", "esc-gap"}
			if tc.kind == "full" {
				wantSelected = []string{"setup-launches", "signals", "typed-controls", "force-states", "dialogs", "groups", "text-limits-backlog", "lifecycle", "user-hooks", "esc-gap", "uncovered"}
			}
			if !reflect.DeepEqual(l.Selected, wantSelected) || !reflect.DeepEqual(l.DialogCases, []string{"D1", "D2", "D3", "D4", "D5", "D6"}) || l.Allowances["first"] != tc.used {
				t.Fatalf("wrong authorized selection: %+v", l)
			}
			for _, share := range shares {
				if !slices.Contains(wantSelected, share.phase) {
					continue
				}
				for i := 0; i < share.count; i++ {
					id := fmt.Sprintf("%s-%d", share.phase, i)
					if err := Reserve(&l, Charge{ID: id, Phase: share.phase, Attempt: "first", Reserved: 1}); err != nil {
						t.Fatalf("reserve %s: %v", id, err)
					}
					if err := Reconcile(&l, id, "launch", id, 1, []string{"raw/" + id}); err != nil {
						t.Fatal(err)
					}
				}
			}
			a, err := AccountTurns(l)
			want := Accounting{Historical: 14, CI: tc.ci, Reserved: tc.used, Actual: tc.used, E8Remaining: tc.e8, ShakedownRemaining: 32 - tc.used}
			if err != nil || !reflect.DeepEqual(a, want) {
				t.Fatalf("accounting = %+v, %v; want %+v", a, err, want)
			}
			// Unused E8 headroom grants neither a retry nor the full run's contingency.
			l.Identity.Attempt = "repair"
			c := Charge{ID: "contingency", Phase: "typed-controls", Attempt: "repair", Reserved: 1}
			before, _ := json.Marshal(l)
			if err := Reserve(&l, c); err == nil {
				t.Fatal("unapproved repair received authority")
			}
			after, _ := json.Marshal(l)
			if !bytes.Equal(before, after) {
				t.Fatal("refusal changed ledger")
			}
			l.Allowances["repair"] = 2
			if tc.kind == "shakedown" {
				if err := Reserve(&l, c); err == nil {
					t.Fatal("explicit repair exceeded the 32-turn shakedown ceiling")
				}
				return
			}
			l.Allowances["repair"] = 1 // Separate MI authority for the one contingency.
			if err := Reserve(&l, c); err != nil {
				t.Fatal(err)
			}
			if err := Reconcile(&l, c.ID, "launch", c.ID, 1, []string{"raw/contingency"}); err != nil {
				t.Fatal(err)
			}
			a, err = AccountTurns(l)
			if err != nil || a.Actual != 48 || a.E8Remaining != 2 {
				t.Fatalf("separate 48-turn full authority: %+v, %v", a, err)
			}
			l.Allowances["repair"] = 2
			if err := Reserve(&l, Charge{ID: "extra", Phase: "typed-controls", Attempt: "repair", Reserved: 1}); err == nil {
				t.Fatal("full run exceeded 48 turns")
			}
		})
	}
}

func TestAccountingRefusesUnauthorizedReservation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Ledger, *Charge)
	}{
		{"unknown-phase", func(l *Ledger, c *Charge) { l.Selected = append(l.Selected, "invented") }},
		{"duplicate-selection", func(l *Ledger, c *Charge) { l.Selected = append(l.Selected, "groups") }},
		{"deferred-phase", func(l *Ledger, c *Charge) { l.Selected = append(l.Selected, "setup-launches") }},
		{"fresh-signals", func(l *Ledger, c *Charge) { c.Phase = "signals" }},
		{"unselected-phase", func(l *Ledger, c *Charge) { l.Selected = []string{"groups"} }},
		{"attempt-exhausted", func(l *Ledger, c *Charge) { l.Allowances["first"] = 1 }},
		{"phase-exhausted", func(l *Ledger, c *Charge) {
			l.Charges[0].Actual = 5
		}},
		{"new-attempt-needs-authority", func(l *Ledger, c *Charge) { c.Attempt = "repair" }},
		{"wrong-force-owner", func(l *Ledger, c *Charge) { c.Phase = "groups" }},
		{"wrong-group-owner", func(l *Ledger, c *Charge) { c.ID = "O1" }},
		{"wrong-dialog-force-owner", func(l *Ledger, c *Charge) { c.ID = "D2" }},
		{"wrong-native-owner", func(l *Ledger, c *Charge) { c.ID = "N1" }},
		{"mid-turn-S1", func(l *Ledger, c *Charge) { c.ID = "S1"; c.Phase = "groups" }},
		{"mid-turn-S2", func(l *Ledger, c *Charge) { c.ID = "S2"; c.Phase = "groups" }},
		{"duplicate-case", func(l *Ledger, c *Charge) { c.ID = "F1" }},
		{"duplicate-physical-start", func(l *Ledger, c *Charge) { c.Launch = "launch"; c.Prompt = "prompt" }},
		{"missing-evidence", func(l *Ledger, c *Charge) { l.Charges[0].Evidence = nil }},
		{"missing-launch", func(l *Ledger, c *Charge) { l.Charges[0].Launch = "" }},
		{"missing-prompt", func(l *Ledger, c *Charge) { l.Charges[0].Prompt = "" }},
		{"not-one-reservation", func(l *Ledger, c *Charge) { c.Reserved = 2 }},
		{"already-actual", func(l *Ledger, c *Charge) { c.Actual = 1 }},
		{"already-reconciled", func(l *Ledger, c *Charge) { c.Reconciled = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l, err := NewLedger(Identity{Run: "run", Attempt: "first", Candidate: "candidate", Product: "product", Provider: "provider", Kit: "kit"}, "shakedown",
				SignalCharge{Capture: "ci", Product: "product", Provider: "provider", Actual: 6})
			if err != nil {
				t.Fatal(err)
			}
			l.Charges = []Charge{{ID: "F1", Phase: "force-states", Attempt: "first", Reserved: 1, Actual: 1, Reconciled: true, Launch: "launch", Prompt: "prompt", Evidence: []string{"raw/F1"}}}
			c := Charge{ID: "F2", Phase: "force-states", Attempt: "first", Reserved: 1}
			tc.change(&l, &c)
			before, _ := json.Marshal(l)
			if err := Reserve(&l, c); err == nil {
				t.Fatal("unauthorized input received a reservation")
			}
			after, _ := json.Marshal(l)
			if !bytes.Equal(before, after) {
				t.Fatal("refusal changed prior accounting")
			}
		})
	}
}

func TestAccountingSharedPhysicalStarts(t *testing.T) {
	l, err := NewLedger(Identity{Run: "run", Attempt: "first", Candidate: "candidate", Product: "product", Provider: "provider", Kit: "kit"}, "shakedown",
		SignalCharge{Capture: "ci", Product: "product", Provider: "provider", Actual: 6})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		phase string
		ids   []string
	}{
		{"force-states", []string{"F1", "F2", "F3", "F4", "F5"}},
		{"groups", []string{"G0", "O1", "O2"}},
		{"text-limits-backlog", []string{"N1", "N2", "N3", "N4", "N5", "N6", "N7", "N8"}},
		{"typed-controls", []string{"typed-cap"}},
		{"dialogs", []string{"D2", "D3"}},
	} {
		for _, id := range tc.ids {
			if err := Reserve(&l, Charge{ID: id, Phase: tc.phase, Attempt: "first", Reserved: 1, Consumers: []string{"groups", "text-limits-backlog", "cap-check"}}); err != nil {
				t.Fatal(err)
			}
			// F2's never-issued tail is membership, not a turn. F3 later issues it.
			if id == "F3" {
				a, err := AccountTurns(l)
				if err != nil || a.Actual != 2 || !reflect.DeepEqual(a.Unreconciled, []string{"F3"}) {
					t.Fatalf("unissued member counted: %+v, %v", a, err)
				}
			}
			// The same prompt ID in different Launches denotes distinct starts.
			if err := Reconcile(&l, id, "launch-"+id, "prompt", 1, []string{"raw/" + id}); err != nil {
				t.Fatal(err)
			}
		}
	}
	l.MidTurn = []string{"S1", "S2", "steer-cap"}
	l.Compactions = []string{"inside-F5"}
	l.APIRequests = []string{"request-1", "request-2", "request-3"}
	l.Activities = []Activity{{Origin: "subagent", Agent: "child", Session: "parent", Requests: []string{"child-request"}}, {Origin: "background", Agent: "background", Session: "parent"}, {Origin: "codex", Agent: "other-agent"}}
	want := Accounting{Historical: 14, CI: 6, Reserved: 19, Actual: 19, E8Remaining: 25, ShakedownRemaining: 13}
	for range 2 { // Re-reading/importing immutable facts adds no charge.
		a, err := AccountTurns(l)
		if err != nil || !reflect.DeepEqual(a, want) {
			t.Fatalf("shared observations charged again: %+v, %v; want %+v", a, err, want)
		}
	}
	before, _ := json.Marshal(l)
	if err := Reconcile(&l, "F3", "launch-F3", "prompt", 1, []string{"raw/F3"}); err == nil {
		t.Fatal("repeated report reconciled the same start again")
	}
	after, _ := json.Marshal(l)
	if !bytes.Equal(before, after) {
		t.Fatal("repeated reconciliation changed usage")
	}
}

func TestAccountingPersistedRepair(t *testing.T) {
	l, err := NewLedger(Identity{Run: "run", Attempt: "failed", Candidate: "candidate", Product: "product", Provider: "provider", Kit: "kit"}, "shakedown",
		SignalCharge{Capture: "ci", Product: "product", Provider: "provider", Actual: 6})
	if err != nil {
		t.Fatal(err)
	}
	l.Allowances["failed"] = 2
	path := filepath.Join(t.TempDir(), "ledger.json")
	b, err := json.Marshal(l)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"F1", "F2"} {
		if err := UpdateLedger(path, func(l *Ledger) error {
			return Reserve(l, Charge{ID: id, Phase: "force-states", Attempt: "failed", Reserved: 1})
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := UpdateLedger(path, func(l *Ledger) error {
		return Reconcile(l, "F1", "launch-old", "prompt", 1, []string{"raw/failed-window"})
	}); err != nil {
		t.Fatal(err)
	}
	// F2 never started. Its reservation still bounds the unfinished attempt.
	if err := UpdateLedger(path, func(l *Ledger) error {
		return Reconcile(l, "F2", "not-issued", "F2", 0, []string{"raw/no-start"})
	}); err != nil {
		t.Fatal(err)
	}
	if err := UpdateLedger(path, func(l *Ledger) error {
		l.Identity.Attempt = "repair"
		l.Identity.Kit = "fixed-kit"
		l.Selected = []string{"force-states"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	if err := UpdateLedger(path, func(l *Ledger) error {
		return Reserve(l, Charge{ID: "F1", Phase: "force-states", Attempt: "repair", Reserved: 1})
	}); err == nil {
		t.Fatal("retry implicitly reused the failed attempt's allowance")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("refused retry changed durable history")
	}
	if err := UpdateLedger(path, func(l *Ledger) error {
		l.Allowances["repair"] = 1
		return Reserve(l, Charge{ID: "F1", Phase: "force-states", Attempt: "repair", Reserved: 1})
	}); err != nil {
		t.Fatal(err)
	}
	if err := UpdateLedger(path, func(l *Ledger) error {
		return Reconcile(l, "F1", "launch-new", "prompt", 1, []string{"raw/repair"})
	}); err != nil {
		t.Fatal(err)
	}
	l, err = LoadLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	a, err := AccountTurns(l)
	want := Accounting{Historical: 14, CI: 6, Reserved: 3, Actual: 2, E8Remaining: 41, ShakedownRemaining: 29}
	if err != nil || !reflect.DeepEqual(a, want) || len(l.Charges) != 3 || l.Charges[0].Launch != "launch-old" || l.Charges[2].Launch != "launch-new" {
		t.Fatalf("repair reset or duplicated prior usage: %+v, %v; ledger %+v", a, err, l)
	}
}

func TestAccountingOvershootIsDurable(t *testing.T) {
	l, err := NewLedger(Identity{Run: "run", Attempt: "first", Candidate: "candidate", Product: "product", Provider: "provider", Kit: "kit"}, "shakedown",
		SignalCharge{Capture: "ci", Product: "product", Provider: "provider", Actual: 6})
	if err != nil {
		t.Fatal(err)
	}
	l.Allowances["first"] = 1
	if err := Reserve(&l, Charge{ID: "F1", Phase: "force-states", Attempt: "first", Reserved: 1}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "ledger.json")
	b, err := json.Marshal(l)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	if err := UpdateLedger(path, func(l *Ledger) error {
		return Reconcile(l, "F1", "launch", "prompt-and-unexpected-parent-READY", 2, []string{"raw/UPS", "raw/unexpected-parent"})
	}); err == nil {
		t.Fatal("unexpected parent start did not stop the attempt")
	}
	l, err = LoadLedger(path)
	if err == nil || len(l.Charges) != 1 || l.Charges[0].Actual != 2 || !l.Charges[0].Reconciled || !reflect.DeepEqual(l.Charges[0].Evidence, []string{"raw/UPS", "raw/unexpected-parent"}) {
		t.Fatalf("overshoot erased or clipped: %+v, %v", l, err)
	}
	a, err := AccountTurns(l)
	if err == nil || a.Actual != 2 || a.Reserved != 1 {
		t.Fatalf("overshoot hidden in accounting: %+v, %v", a, err)
	}
	before, _ := os.ReadFile(path)
	if err := UpdateLedger(path, func(l *Ledger) error {
		return Reserve(l, Charge{ID: "F2", Phase: "force-states", Attempt: "first", Reserved: 1})
	}); err == nil {
		t.Fatal("overspent attempt continued")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("refusal erased overshoot evidence")
	}
}

func TestAccountingRequiresCompatibleCI(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*SignalCharge)
	}{
		{"missing-capture", func(s *SignalCharge) { s.Capture = "" }},
		{"different-product", func(s *SignalCharge) { s.Product = "other" }},
		{"different-provider", func(s *SignalCharge) { s.Provider = "other" }},
		{"unknown-actual", func(s *SignalCharge) { s.Actual = -1 }},
		{"over-ci-allowance", func(s *SignalCharge) { s.Actual = 8 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := SignalCharge{Capture: "ci", Product: "product", Provider: "provider", Actual: 6}
			tc.change(&s)
			if _, err := NewLedger(Identity{Run: "run", Attempt: "first", Candidate: "candidate", Product: "product", Provider: "provider", Kit: "kit"}, "shakedown", s); err == nil {
				t.Fatal("unqualified ci evidence authorized live input")
			}
		})
	}
}

func TestAccountingPriorQualificationCarry(t *testing.T) {
	ci := SignalCharge{Capture: "ci", Product: "product", Provider: "provider", Actual: 6}
	prior, err := NewLedger(Identity{Run: "prior-run", Attempt: "a2", Candidate: "old-candidate", Product: "product", Provider: "provider", Kit: "kit13"}, "shakedown", ci)
	if err != nil {
		t.Fatal(err)
	}
	for i, id := range []string{"typed-ad-start", "typed-control-start", "typed-long-tool-typed-input"} {
		if err := Reserve(&prior, Charge{ID: id, Phase: "typed-controls", Attempt: "a2", Reserved: 1}); err != nil {
			t.Fatal(err)
		}
		actual := 1
		if i == 2 {
			actual = 0 // a2 kept this settled reservation even though it never issued.
		}
		if err := Reconcile(&prior, id, "old-launch", id+"@raw/"+id, actual, []string{"raw/" + id}); err != nil {
			t.Fatal(err)
		}
	}
	// The same physical paste was recorded as unexpected by kit13. Do not
	// rewrite that history or carry max(reserved,actual)=4 as four real starts.
	prior.Charges = append(prior.Charges, Charge{ID: "unexpected-paste", Phase: "typed-controls", Attempt: "a2", Launch: "old-launch", Prompt: "reused-prompt@raw/paste", Actual: 1, Reconciled: true, Evidence: []string{"raw/paste"}})
	raw, err := json.MarshalIndent(prior, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, '\n')
	current, err := NewLedger(Identity{Run: "new-run", Attempt: "a3", Candidate: "new-candidate", Product: "product", Provider: "provider", Kit: "kit14"}, "shakedown", ci)
	if err != nil {
		t.Fatal(err)
	}
	if err := AttachPriorLedger(&current, raw); err != nil {
		t.Fatal(err)
	}
	a, err := AccountTurns(current)
	want := Accounting{Historical: 14, CI: 6, PriorActual: 3, E8Remaining: 41, ShakedownRemaining: 32}
	if err != nil || !reflect.DeepEqual(a, want) || current.Allowances["a3"] != 32 {
		t.Fatalf("a2 actual usage not carried into E8: %+v, %v; want %+v and a3 allowance32", a, err, want)
	}
	if current.Prior == nil || current.Prior.SHA256 != runner.Digest(raw) || !bytes.Equal(current.Prior.Ledger, raw) {
		t.Fatal("carry rewrote the original a2 ledger bytes/hash")
	}
	path := filepath.Join(t.TempDir(), "ledger.json")
	data, err := json.Marshal(current)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadLedger(path)
	if err != nil || loaded.Prior == nil || !bytes.Equal(loaded.Prior.Ledger, raw) || loaded.Prior.SHA256 != runner.Digest(raw) {
		t.Fatalf("persisted carry lost original bytes/hash: %v", err)
	}
	// This replacement is independently valid, with the same three actual
	// starts and a correct digest: only immutable-history protection rejects it.
	prior.Charges[0].Prompt = "replacement-prompt@raw/typed-ad-start"
	replacement, err := json.Marshal(prior)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"remove", "replace"} {
		t.Run(kind, func(t *testing.T) {
			err := UpdateLedger(path, func(l *Ledger) error {
				l.Prior = nil
				if kind == "replace" {
					l.Prior = &PriorLedger{SHA256: runner.Digest(replacement), Ledger: replacement}
				}
				return nil
			})
			if err == nil || err.Error() != "settled prior usage cannot change during a qualification" {
				t.Errorf("%s prior usage was not refused by immutable carry: %v", kind, err)
			}
			after, readErr := os.ReadFile(path)
			if readErr != nil || !bytes.Equal(after, data) {
				t.Errorf("%s prior refusal changed durable history: %v", kind, readErr)
			}
		})
	}
}

// Preserve the original a3/a4/a5 ledgers and all nested prior bytes. These are
// recorded accounting facts, never a synthetic live PASS or retry authority.
func TestAccountingA4OriginalNestedCarry(t *testing.T) {
	if E8Budget != 64 || ShakedownBudget != 32 || FullRunBudget != 48 {
		t.Errorf("MI ceilings: E8=%d shakedown=%d full=%d; want 64/32/48", E8Budget, ShakedownBudget, FullRunBudget)
	}
	for _, tc := range []struct {
		attempt, priorFile, digest, kit string
		bytes, priorActual, remaining   int
	}{
		{"a4", "a3-ledger.json", "a19259a2ad9ed32099c9dd70275e3ad469d4312eb5bac0adc8f4180e31764136", "unit-kit17", 6613, 6, 38},
		{"a5", "a4-ledger.json", "b02e440bb5f4ae664c04cb429fb0b474bd9833570310624aa33b2fdbd1e3ba95", "unit-kit19", 10702, 7, 37},
		{"a6", "a5-ledger.json", "489f28f7416166c1bd3756c4746d0d2e20943b522af8b26d96d0a30c2e4c4121", "unit-kit20", 17907, 12, 32},
	} {
		t.Run(tc.attempt, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("testdata/recorded", tc.priorFile))
			if err != nil {
				t.Fatal(err)
			}
			if len(raw) != tc.bytes || runner.Digest(raw) != tc.digest {
				t.Fatalf("original %s ledger bytes changed", tc.priorFile)
			}
			var prior Ledger
			if err := json.Unmarshal(raw, &prior); err != nil {
				t.Fatal(err)
			}
			if tc.attempt == "a6" {
				chain := prior
				for i, want := range []struct {
					attempt string
					actual  int
				}{{"a5", 5}, {"a4", 1}, {"a3", 3}, {"a2", 3}} {
					actual := 0
					for _, charge := range chain.Charges {
						actual += charge.Actual
					}
					if chain.Identity.Attempt != want.attempt || actual != want.actual {
						t.Fatalf("original nested a5 usage: %s=%d; want %s=%d", chain.Identity.Attempt, actual, want.attempt, want.actual)
					}
					if i < 3 {
						if chain.Prior == nil || runner.Digest(chain.Prior.Ledger) != chain.Prior.SHA256 {
							t.Fatal("nested original prior bytes/hash changed")
						}
						var next Ledger
						if err := json.Unmarshal(chain.Prior.Ledger, &next); err != nil {
							t.Fatal(err)
						}
						chain = next
					} else if chain.Prior != nil {
						t.Fatal("unexpected additional prior usage")
					}
				}
			}
			current, err := NewLedger(Identity{Run: "unit-" + tc.attempt, Attempt: tc.attempt, Candidate: "unit-fixed-candidate", Product: prior.Identity.Product, Provider: prior.Identity.Provider, Kit: tc.kit}, "shakedown", prior.Signals)
			if err != nil {
				t.Fatal(err)
			}
			if err := AttachPriorLedger(&current, raw); err != nil {
				t.Fatalf("original %s actual%d must retain fresh32 under E8=64: %v", tc.priorFile, tc.priorActual, err)
			}
			a, err := AccountTurns(current)
			if err != nil || a.PriorActual != tc.priorActual || a.CI != 6 || a.E8Remaining != tc.remaining || a.ShakedownRemaining != 32 || len(current.Allowances) != 1 || current.Allowances[tc.attempt] != 32 {
				t.Fatalf("nested history or %s authority changed: %+v allowances=%v err=%v", tc.attempt, a, current.Allowances, err)
			}
			if current.Prior == nil || !bytes.Equal(current.Prior.Ledger, raw) || current.Prior.SHA256 != runner.Digest(raw) || prior.Prior == nil {
				t.Fatal("carry replaced original nested history")
			}
			before, _ := json.Marshal(current)
			if err := Reserve(&current, Charge{ID: "unit-retry", Phase: "typed-controls", Attempt: "retry", Reserved: 1}); err == nil {
				t.Fatalf("%s received an unauthorized retry allowance", tc.attempt)
			}
			after, _ := json.Marshal(current)
			if !bytes.Equal(before, after) {
				t.Fatal("refused retry changed nested ledger")
			}
		})
	}
}
