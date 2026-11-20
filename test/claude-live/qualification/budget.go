package qualification

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/gabemahoney/agent-director/test/claude-live/runner"
)

const E8Budget = 64
const HistoricalTurns = 14
const ShakedownBudget = 32
const FullRunBudget = 48 // includes one contingency turn, which requires separate authority

type Charge struct {
	ID           string   `json:"id"`
	Phase        string   `json:"phase"`
	Attempt      string   `json:"attempt"`
	Launch       string   `json:"launch"`
	Prompt       string   `json:"prompt"`
	Reserved     int      `json:"reserved"`
	Actual       int      `json:"actual"`
	Reconciled   bool     `json:"reconciled"`
	Consumers    []string `json:"consumers"`
	Evidence     []string `json:"evidence"`
	Alternatives []string `json:"alternatives,omitempty"`
	SelectedCase string   `json:"selected_case,omitempty"`
}
type SignalCharge struct {
	Capture  string `json:"capture"`
	Product  string `json:"product"`
	Provider string `json:"provider"`
	Actual   int    `json:"actual"`
}
type AttemptAuthority struct {
	Identity    Identity `json:"identity"`
	Selected    []string `json:"selected"`
	DialogCases []string `json:"dialog_cases"`
	Allowance   int      `json:"allowance"`
	Sequence    int      `json:"sequence"`
}

type Ledger struct {
	Schema   int          `json:"schema"`
	Identity Identity     `json:"identity"`
	Kind     string       `json:"kind"`
	Selected []string     `json:"selected"`
	Signals  SignalCharge `json:"signals"`
	Charges  []Charge     `json:"charges"`
	// Reruns never reset prior charges. An explicit attempt allowance is
	// necessary even when unused E8 authority remains.
	Allowances  map[string]int              `json:"allowances"`
	MidTurn     []string                    `json:"mid_turn"`
	Compactions []string                    `json:"compactions"`
	APIRequests []string                    `json:"api_requests"`
	DialogCases []string                    `json:"dialog_cases,omitempty"`
	Activities  []Activity                  `json:"activities,omitempty"`
	History     map[string]AttemptAuthority `json:"history,omitempty"`
	// A changed kit starts a new qualification, but cannot reset settled usage
	// from its predecessor. Preserve the original ledger bytes as evidence.
	Prior *PriorLedger `json:"prior,omitempty"`
}

type PriorLedger struct {
	SHA256 string `json:"sha256"`
	Ledger []byte `json:"ledger"`
}

func samePriorLedger(a, b *PriorLedger) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.SHA256 == b.SHA256 && string(a.Ledger) == string(b.Ledger)
}

type Accounting struct {
	Historical         int      `json:"historical"`
	CI                 int      `json:"ci"`
	Reserved           int      `json:"reserved"`
	Actual             int      `json:"actual"`
	E8Remaining        int      `json:"e8_remaining"`
	ShakedownRemaining int      `json:"shakedown_remaining"`
	Unreconciled       []string `json:"unreconciled"`
	PriorActual        int      `json:"prior_actual,omitempty"`
}

// AccountTurns is the shared authority calculation. Future qualification
// accounting must reuse it and the ledger operations below; see docs/architecture.md.
func AccountTurns(l Ledger) (Accounting, error) {
	return accountTurns(l, 0)
}

func accountTurns(l Ledger, depth int) (Accounting, error) {
	a := Accounting{Historical: HistoricalTurns, CI: l.Signals.Actual}
	if depth > 8 {
		return a, errors.New("prior qualification chain exceeds its bounded depth")
	}
	if l.Schema != Schema {
		return a, errors.New("unknown turn ledger schema")
	}
	if err := l.Identity.Validate(); err != nil {
		return a, err
	}
	if l.Kind != "shakedown" && l.Kind != "full" {
		return a, errors.New("explicit shakedown or full authority required")
	}
	if len(l.Selected) == 0 {
		return a, errors.New("at least one explicitly authorized phase is required")
	}
	if l.Signals.Actual < 0 || l.Signals.Actual > 7 {
		return a, errors.New("ci actual turn count is outside its allowance")
	}
	if l.Kind == "shakedown" && (l.Signals.Actual < 0 || l.Signals.Actual > 7 || l.Signals.Capture == "" || l.Signals.Product != l.Identity.Product || l.Signals.Provider != l.Identity.Provider) {
		return a, errors.New("compatible ci capture and actual turn charge required")
	}
	if l.Prior != nil {
		if len(l.Prior.Ledger) == 0 || len(l.Prior.Ledger) > runner.MaxArtifactBytes || runner.Digest(l.Prior.Ledger) != l.Prior.SHA256 {
			return a, errors.New("prior qualification ledger hash or size differs")
		}
		var prior Ledger
		if err := json.Unmarshal(l.Prior.Ledger, &prior); err != nil {
			return a, err
		}
		if l.Kind != "shakedown" || prior.Kind != l.Kind || prior.Identity.Run == l.Identity.Run || prior.Signals != l.Signals {
			return a, errors.New("prior qualification identity or ci charge conflicts")
		}
		for _, charge := range prior.Charges {
			if !charge.Reconciled {
				return a, errors.New("prior qualification still has in-flight reservations")
			}
		}
		previous, err := accountTurns(prior, depth+1)
		if err != nil {
			return a, err
		}
		// Closed, never-issued reservations consumed no parent start. Keep them
		// in the original ledger while carrying every actual start, including
		// starts the earlier kit attributed as unexpected.
		a.PriorActual = previous.PriorActual + previous.Actual
	}
	if len(l.DialogCases) > 0 {
		if err := DialogSelection(l.DialogCases); err != nil {
			return a, err
		}
	}
	selected := map[string]bool{}
	for _, id := range l.Selected {
		p, e := FindPhase(id)
		if e != nil {
			return a, e
		}
		if selected[id] {
			return a, errors.New("duplicate phase selection")
		}
		selected[id] = true
		if l.Kind == "shakedown" && !p.Shakedown {
			return a, fmt.Errorf("phase %s has no shakedown authority", id)
		}
	}
	for id, authority := range l.History {
		if id == l.Identity.Attempt || authority.Identity.Attempt != id || !authority.Identity.Compatible(l.Identity) || authority.Identity.Kit != l.Identity.Kit || authority.Allowance != l.Allowances[id] {
			return a, errors.New("historical attempt authority changed")
		}
		if err := validateSelection(authority.Selected, l.Kind); err != nil {
			return a, err
		}
		if err := DialogSelection(authority.DialogCases); err != nil {
			return a, err
		}
	}
	seen, physical := map[string]bool{}, map[string]bool{}
	byPhase, byAttempt := map[string]int{}, map[string]int{}
	for _, c := range l.Charges {
		key := c.Attempt + "/" + c.ID
		allowed := selected[c.Phase]
		if c.Attempt != l.Identity.Attempt {
			if authority, ok := l.History[c.Attempt]; ok {
				allowed = slices.Contains(authority.Selected, c.Phase)
			}
		}
		if c.ID == "" || seen[key] || c.Attempt == "" || !allowed || c.Reserved < 0 || c.Actual < 0 {
			return a, errors.New("invalid or duplicate turn charge")
		}
		seen[key] = true
		if !validAlternatives(c) {
			return a, errors.New("invalid mutually exclusive physical-start slot")
		}
		if c.Reconciled && (c.Launch == "" || c.Prompt == "" || len(c.Evidence) == 0) {
			return a, errors.New("reconciled start lacks physical prompt/Launch/evidence")
		}
		if c.Prompt != "" {
			key := c.Launch + "/" + c.Prompt
			if physical[key] {
				return a, errors.New("physical prompt charged twice")
			}
			physical[key] = true
		}
		if owner, ok := CaseOwner(c.ID); ok && owner != c.Phase {
			return a, errors.New("physical case charged to wrong phase")
		}
		if c.ID == "S1" || c.ID == "S2" {
			return a, errors.New("mid-turn steers are not wake starts")
		}
		n := max(c.Reserved, c.Actual)
		a.Reserved += c.Reserved
		a.Actual += c.Actual
		byPhase[c.Attempt+"/"+c.Phase] += n
		byAttempt[c.Attempt] += n
		if !c.Reconciled {
			a.Unreconciled = append(a.Unreconciled, c.ID)
		}
	}
	for attempt, n := range byAttempt {
		allow, ok := l.Allowances[attempt]
		if !ok || allow < 0 || n > allow {
			return a, fmt.Errorf("attempt %s exceeds explicit allowance", attempt)
		}
	}
	for id, n := range byPhase {
		attempt, phase, _ := strings.Cut(id, "/")
		p, _ := FindPhase(phase)
		limit := p.Turns
		cases := l.DialogCases
		if authority, ok := l.History[attempt]; ok && attempt != l.Identity.Attempt {
			cases = authority.DialogCases
		}
		if phase == "dialogs" && len(cases) > 0 {
			limit = len(cases)
		}
		if n > limit {
			return a, fmt.Errorf("phase %s exceeds turn share; no automatic retry", id)
		}
	}
	used := 0
	for _, c := range l.Charges {
		used += max(c.Reserved, c.Actual)
	}
	a.E8Remaining = E8Budget - HistoricalTurns - l.Signals.Actual - a.PriorActual - used
	a.ShakedownRemaining = ShakedownBudget - used
	if l.Kind == "shakedown" && (a.E8Remaining < 0 || a.ShakedownRemaining < 0) {
		return a, errors.New("shakedown or E8 ceiling exceeded")
	}
	if l.Kind == "full" && used+l.Signals.Actual > FullRunBudget {
		return a, errors.New("MI full-run allowance exceeded")
	}
	return a, nil
}

// AttachPriorLedger is an explicit initialization operation for a changed kit.
// It carries actual usage, preserving the predecessor rather than rewriting its
// case attribution or pretending it ran on the current candidate.
// Future changed-kit initialization must reuse this carry gate (docs/architecture.md).
func AttachPriorLedger(l *Ledger, raw []byte) error {
	if l.Prior != nil || len(l.Charges) != 0 || len(l.History) != 0 {
		return errors.New("prior usage can only be attached to a fresh qualification")
	}
	l.Prior = &PriorLedger{SHA256: runner.Digest(raw), Ledger: append([]byte(nil), raw...)}
	a, err := AccountTurns(*l)
	if err == nil && l.Allowances[l.Identity.Attempt] > min(a.E8Remaining, a.ShakedownRemaining) {
		err = errors.New("new qualification allowance exceeds remaining authority after prior usage")
	}
	if err != nil {
		l.Prior = nil
	}
	return err
}

func CaseOwner(id string) (string, bool) {
	if slices.Contains([]string{"L1", "L2"}, id) {
		return "lifecycle", true
	}
	if slices.Contains([]string{"H1", "H2"}, id) {
		return "user-hooks", true
	}
	if slices.Contains([]string{"E1", "E2", "E3", "E4"}, id) {
		return "esc-gap", true
	}

	if slices.Contains([]string{"N1", "N2", "N3", "N4", "N5", "N6", "N7", "N8"}, id) {
		return "text-limits-backlog", true
	}
	if slices.Contains([]string{"D1", "D2", "D3", "D4", "D5", "D6"}, id) {
		return "dialogs", true
	}
	if slices.Contains([]string{"F1", "F2", "F3", "F4", "F5"}, id) {
		return "force-states", true
	}
	if slices.Contains([]string{"G0", "O1", "O2"}, id) {
		return "groups", true
	}
	return "", false
}

// UpdateLedger serializes reservations across independently invoked phases.
// Failed writes never replace the last durable ledger; a held lock is an
// inspection stop, never authority to discard another invocation's reservation.
// Future phase writers must reuse this durable update boundary (docs/architecture.md).
func UpdateLedger(path string, change func(*Ledger) error) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("ledger requires an absolute clean path")
	}
	if _, err := runner.Confined(filepath.Dir(path), filepath.Base(path)); err != nil {
		return err
	}
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if err = lock.Close(); err != nil {
		return errors.Join(err, os.Remove(path+".lock"))
	}
	defer os.Remove(path + ".lock")
	var l Ledger
	b, err := runner.Read(filepath.Dir(path), filepath.Base(path))
	if err != nil {
		return err
	}
	if err = json.Unmarshal(b, &l); err != nil {
		return err
	}
	prior := l.Prior
	if prior != nil {
		copy := *prior
		copy.Ledger = append([]byte(nil), prior.Ledger...)
		prior = &copy
	}
	if err = change(&l); err != nil {
		return err
	}
	if !samePriorLedger(prior, l.Prior) {
		return errors.New("settled prior usage cannot change during a qualification")
	}
	// Reservations validate in Reserve before mutation. Reconciliation must
	// retain the provider's actual usage even when it reveals an overshoot.
	_, accountingErr := AccountTurns(l)
	b, err = json.MarshalIndent(l, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".qualification-ledger-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(append(b, '\n'))
	}
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	d, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	return errors.Join(accountingErr, d.Sync(), d.Close())
}

// Reserve validates authority before input. Future executors must reuse this
// reservation gate; see the accounting reuse rules in docs/architecture.md.
func Reserve(l *Ledger, c Charge) error {
	if c.Reconciled || c.Actual != 0 || c.Reserved != 1 {
		return errors.New("reserve exactly one physical start before input")
	}
	if owner, ok := CaseOwner(c.ID); ok && owner != c.Phase {
		return errors.New("physical case charged to wrong phase")
	}
	original := l.Charges
	l.Charges = append(l.Charges, c)
	_, err := AccountTurns(*l)
	if err != nil {
		l.Charges = original
	}
	return err
}

// Reconcile records actual provider starts, including unexpected ones. The
// caller must retain this over-budget observation even when AccountTurns then
// returns an error; it must never erase usage to make an attempt fit.
// Future reconciliation consumers must reuse this operation (docs/architecture.md).
func Reconcile(l *Ledger, id, launch, prompt string, actual int, evidence []string) error {
	if actual < 0 {
		return errors.New("negative observed turns")
	}
	for i := range l.Charges {
		c := &l.Charges[i]
		if c.ID == id && c.Attempt == l.Identity.Attempt {
			if c.Reconciled {
				return errors.New("start already reconciled")
			}
			c.Launch, c.Prompt, c.Actual, c.Evidence, c.Reconciled = launch, prompt, actual, evidence, true
			return nil
		}
	}
	return errors.New("physical start has no reservation")
}

// NewLedger initializes shared qualification authority. Future initializers
// must reuse it under the accounting rules in docs/architecture.md.
func NewLedger(i Identity, kind string, signals SignalCharge) (Ledger, error) {
	if kind == "full" && signals.Capture == "" {
		signals.Actual = 0
	}
	l := Ledger{Schema: Schema, Identity: i, Kind: kind, Signals: signals, Allowances: map[string]int{}}
	allowance := 0
	for _, p := range Registry() {
		if kind == "full" || p.Shakedown {
			l.Selected = append(l.Selected, p.ID)
			allowance += p.Turns
		}
	}
	l.Allowances[i.Attempt] = allowance
	l.DialogCases = []string{"D1", "D2", "D3", "D4", "D5", "D6"}
	_, err := AccountTurns(l)
	return l, err
}

func validateSelection(selected []string, kind string) error {
	seen := map[string]bool{}
	for _, id := range selected {
		p, err := FindPhase(id)
		if err != nil {
			return err
		}
		if seen[id] || (kind == "shakedown" && !p.Shakedown) {
			return errors.New("invalid phase selection")
		}
		seen[id] = true
	}
	if len(seen) == 0 {
		return errors.New("empty phase selection")
	}
	return nil
}

// NewAttempt preserves prior selected shares and charges. Allowance and the
// previous sealed summary link must be explicitly supplied; no retry is free.
// Future rerun callers must reuse this gate (docs/architecture.md).
func NewAttempt(l *Ledger, id string, allowance int, selected, dialogs []string, previous string) error {
	if _, err := AccountTurns(*l); err != nil {
		return err
	}
	_, exists := l.Allowances[id]
	if _, ok := l.Allowances[l.Identity.Attempt]; !ok {
		return errors.New("prior attempt has no authority")
	}
	if id == "" || id == l.Identity.Attempt || exists || allowance <= 0 || previous == "" {
		return errors.New("rerun requires a new attempt, explicit allowance and previous summary link")
	}
	identity := l.Identity
	identity.Attempt = id
	identity.Previous = previous
	if err := identity.Validate(); err != nil {
		return err
	}
	if err := validateSelection(selected, l.Kind); err != nil {
		return err
	}
	if err := DialogSelection(dialogs); err != nil {
		return err
	}
	for _, c := range l.Charges {
		if !c.Reconciled {
			return errors.New("cannot replace an in-flight attempt with unreconciled reservations")
		}
	}
	original, err := json.Marshal(l)
	if err != nil {
		return err
	}
	var prior Ledger
	if err = json.Unmarshal(original, &prior); err != nil {
		return err
	}
	sequence := 1
	for _, a := range l.History {
		sequence = max(sequence, a.Sequence+1)
	}
	if l.History == nil {
		l.History = map[string]AttemptAuthority{}
	}
	l.History[l.Identity.Attempt] = AttemptAuthority{l.Identity, append([]string(nil), l.Selected...), append([]string(nil), l.DialogCases...), l.Allowances[l.Identity.Attempt], sequence}
	l.Identity = identity
	l.Selected = append([]string(nil), selected...)
	l.DialogCases = append([]string(nil), dialogs...)
	l.Allowances[id] = allowance
	accounting, err := AccountTurns(*l)
	remaining := FullRunBudget - l.Signals.Actual
	for _, c := range l.Charges {
		remaining -= max(c.Reserved, c.Actual)
	}
	if l.Kind == "shakedown" {
		remaining = min(accounting.ShakedownRemaining, accounting.E8Remaining)
	}
	if err != nil || allowance > remaining {
		*l = prior
		return errors.New("explicit rerun allowance exceeds remaining hard ceiling")
	}
	return nil
}
