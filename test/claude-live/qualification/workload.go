package qualification

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/test/claude-live/runner"
	"github.com/gabemahoney/agent-director/test/claude-live/signals"
)

// WorkloadOutcome is an incomplete measurement, never a delivery verdict or
// permission to import a partial phase. Reuse it for bounded workload branches.
type WorkloadOutcome struct {
	Code              string    `json:"code"`
	Case              string    `json:"case"`
	Instance          string    `json:"instance"`
	Launch            string    `json:"launch"`
	Session           string    `json:"session"`
	Request           string    `json:"request,omitempty"`
	Tool              string    `json:"tool_use_id,omitempty"`
	InputSHA256       string    `json:"input_sha256,omitempty"`
	At                time.Time `json:"at"`
	TeardownUncertain bool      `json:"teardown_uncertain,omitempty"`
}

type workloadError struct {
	Outcome WorkloadOutcome
	Cause   string
	Err     error
}

func (w *workloadError) Error() string { return w.Outcome.Code + ": " + w.Cause }

func (w *workloadError) Unwrap() error { return w.Err }

func (e *Exercise) workload(code, iid, id, cause string, refs ...string) error {
	o, _ := e.owner(iid)
	w := WorkloadOutcome{Code: code, Case: id, Instance: iid, Launch: o.LaunchToken, Session: o.SessionID, At: e.Run.Now().UTC()}
	for _, x := range e.Run.Manifest.Cases {
		if x.InstanceID == iid && !x.Started.Before(o.Started) && (x.ID == id || id == "") {
			w.Case = x.ID
			w.InputSHA256 = runner.Digest([]byte(x.Text))
		}
	}
	if facts, er := LiveHookFacts(e.Run.Evidence.Root, e.Run.Manifest.RunID, e.Run.Manifest.RecordingHook.Events); er == nil {
		for _, h := range facts {
			if runner.Text(h.Payload, "session_id") == o.SessionID && runner.Text(h.Payload, "agent_id") == "" && !h.Receipt.Received.Before(o.Started) && (o.StoppedAt.IsZero() || !h.Receipt.Received.After(o.StoppedAt)) {
				if h.Receipt.Event == "PreToolUse" {
					w.Tool = runner.Text(h.Payload, "tool_use_id")
					input, _ := h.Payload["tool_input"].(map[string]any)
					bg, _ := input["run_in_background"].(bool)
					outer, _ := h.Payload["is_background"].(bool)
					w.TeardownUncertain = w.TeardownUncertain || bg || outer
				}

				if h.Receipt.Event == "Stop" || h.Receipt.Event == "PreToolUse" || h.Receipt.Event == "UserPromptSubmit" {
					refs = append(refs, h.Reference)
				}
			}
		}
	}
	if data, er := e.Run.ReadTranscript(o); er == nil {
		if tr, er := signals.ParseTranscript(data, o.SessionID); er == nil && len(tr.Requests) > 0 {
			for _, q := range tr.Requests {
				if !q.Time.Before(o.Started) && !q.Time.After(w.At) {
					w.Request = q.ID
				}
			}
			refs = append(refs, "raw/"+e.Run.OwnedPrefix(o)+"-transcript.jsonl")
		}
	}

	err := &workloadError{Outcome: w, Cause: cause}
	for _, prior := range e.Run.Manifest.Problems {
		if prior.SignalID == "workload/"+code && strings.Contains(prior.Reason, cause) {
			return err
		}
	}
	e.Run.Manifest.Problems = append(e.Run.Manifest.Problems, result("workload/"+code, "not_completed", err.Error(), "actual requested workload precondition", w, refs...))
	return err
}

// Only the provider's known Stop envelope is allowed around an exact frozen AD
// body; marker deletion, substitutions, another session and repeated starts do
// not become matches. This normalization is never used for model intake.
func stopPromptMatches(got, want string) bool {
	if got == want || got == "<task-notification>\n<summary>Stop hook feedback</summary>\n</task-notification>\n"+want || got == "<summary>Stop hook feedback</summary>\n"+want {
		return true
	}
	// UPS records the Stop callback body before the request renderer adds its
	// fixed FileChanged error label. Only this exact measured form is accepted;
	// the AD separator, escaped contents and enclosing tags remain mandatory.
	const lead = "<task-notification>\n<summary>Stop hook feedback</summary>\n</task-notification>\n<system-reminder>\n"
	const label = `Stop hook blocking error from command "FileChanged": `
	if strings.HasPrefix(want, lead+label) {
		return got == lead+strings.TrimPrefix(want, lead+label)
	}
	return false
}

func (e *Exercise) reserveSlot(id string, alternatives ...string) error {
	if err := UpdateLedger(e.Ledger, func(l *Ledger) error {
		return Reserve(l, Charge{ID: id, Phase: e.Run.Manifest.Phase, Attempt: l.Identity.Attempt, Reserved: 1, Alternatives: alternatives})
	}); err != nil {
		return err
	}
	return e.Run.ReserveTurn()
}

func (e *Exercise) fallback(s runner.Snapshot) error {
	if e.Run.Manifest.Phase == "force-states" {
		for _, x := range e.Run.Manifest.Cases {
			if x.ID == "F3" && x.InstanceID == s.InstanceID {
				if p, ok := promptFor(s, x.Text); ok && runner.Text(p, "transport") == "written" {
					for _, h := range s.Tables["handovers"] {
						if runner.Text(h, "handover_id") == runner.Text(p, "handover_id") && runner.Text(h, "boundary") == "turn_end" {
							return e.workload("ordinary-early-F3", x.InstanceID, x.ID, "F3 started ordinarily before its planned force group; F3/F4 remains a GAP", x.Command)
						}
					}
				}
			}
		}
		return nil
	}
	if !slices.Contains([]string{"typed-controls", "groups", "esc-gap", "dialogs"}, e.Run.Manifest.Phase) {
		return nil
	}
	for _, x := range e.Run.Manifest.Cases {
		dialogProbe := e.Run.Manifest.Phase == "dialogs" && strings.HasPrefix(x.ID, "dialog-")
		if x.InstanceID != s.InstanceID || (!dialogProbe && x.Mode != "steer_turn") || strings.HasPrefix(x.ID, "typed-idle-") {
			continue
		}
		p, ok := promptFor(s, x.Text)
		if !ok || runner.Text(p, "emission") != "issued" || runner.Text(p, "transport") != "written" {
			continue
		}
		for _, h := range s.Tables["handovers"] {
			if runner.Text(h, "handover_id") == runner.Text(p, "handover_id") && runner.Text(h, "boundary") == "turn_end" {
				cause := "no tool boundary after injection; delivered at Stop; intended measurement and later branch remain incomplete"
				if dialogProbe {
					cause = "requested Dialog workload ended before the next required origin/force window; probes delivered at Stop; intended matrix and later branch remain incomplete"
				}
				return e.workload("early-Stop-fallback", x.InstanceID, x.ID, cause, x.Command)
			}
		}
	}
	return e.passiveTypedFallback(s)
}

func (e *Exercise) checkFallback(ctx context.Context, iid string) error {
	s, err := e.Run.Observe(ctx, iid)
	if err != nil {
		return err
	}
	if err = e.Run.Bind(s); err != nil {
		return err
	}
	return e.fallback(s)
}

func (e *Exercise) earlyF3(ctx context.Context, iid, text string) error {
	s, err := e.observe(ctx, iid, "F3-schedule")
	if err != nil {
		return err
	}
	p, ok := promptFor(s, text)
	if !ok {
		return nil
	}
	for _, h := range s.Tables["handovers"] {
		if runner.Text(h, "handover_id") == runner.Text(p, "handover_id") && runner.Text(h, "boundary") == "turn_end" && runner.Text(p, "transport") == "written" {
			return e.workload("ordinary-early-F3", iid, "F3", "planned F3 delivered ordinarily before its force-group window; F3/F4 measurement GAP", "raw/qualification-observations.json")
		}
	}
	return nil
}

func (e *Exercise) workloadTimeout(err error, iid, id, code string) error {
	var named *workloadError
	if errors.As(err, &named) {
		return err
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	owner, er := e.owner(iid)
	if er != nil {
		return errors.Join(err, er)
	}
	// A missing recorder/owner binding is a setup fault, not model behavior.
	// Keep its bounded deadline and independent binding error intact.
	if er = runner.CheckTranscriptRecordings(e.Run.Evidence.Root, e.Run.Manifest.RunID, owner, e.Run.Manifest.Owned...); er != nil {
		if errors.Is(er, runner.ErrTranscriptBindingPending) {
			return err
		}
		return errors.Join(err, er)
	}
	namedErr := e.workload(code, iid, id, "bounded workload wait ended without its required parent event; no replacement input sent")
	if errors.As(namedErr, &named) {
		named.Err = err
	}
	return namedErr
}

func shellCommands(text string) []string {
	var out []string
	parts := strings.Split(text, "`")
	for i := 1; i < len(parts); i += 2 {
		out = append(out, strings.TrimSpace(parts[i]))
	}
	return out
}

func (e *Exercise) expectedTools(iid string) []string {
	var out []string
	seen := map[string]bool{}
	for _, x := range e.Run.Manifest.Cases {
		if x.InstanceID == iid && !seen[x.Text] {
			seen[x.Text] = true
			out = append(out, shellCommands(x.Text)...)
		}
	}
	return out
}

// toolIDs joins actual parent batch membership; it does not equate a callback
// count with a number of tools or infer foreground execution from instructions.
func toolIDs(p runner.Row) []string {
	var v struct {
		Calls []struct {
			ID string `json:"tool_use_id"`
		} `json:"tool_calls"`
	}
	b, _ := json.Marshal(p)
	_ = json.Unmarshal(b, &v)
	var ids []string
	for _, c := range v.Calls {
		ids = append(ids, c.ID)
	}
	return ids
}

func (e *Exercise) selectToolWindow(o runner.Owned, facts []HookFact, n int, between bool) (string, error) {
	var pre []HookFact
	completed := map[string]HookFact{}
	open := map[string]bool{}
	for _, h := range facts {
		if runner.Text(h.Payload, "session_id") != o.SessionID || runner.Text(h.Payload, "agent_id") != "" || h.Receipt.Received.Before(o.Started) {
			continue
		}
		if h.Receipt.Event == "PreToolUse" {
			if runner.Text(h.Payload, "tool_name") != "Bash" {
				return "", e.workload("precondition-not-established", o.InstanceID, "", "requested foreground Bash was replaced by another tool", h.Reference)
			}
			var v struct {
				Input struct {
					Command    string `json:"command"`
					Background bool   `json:"run_in_background"`
				} `json:"tool_input"`
				Background bool `json:"is_background"`
			}
			b, _ := json.Marshal(h.Payload)
			_ = json.Unmarshal(b, &v)
			if v.Background || v.Input.Background {
				return "", e.workload("precondition-not-established", o.InstanceID, "", "background fixture requested; exact descendant teardown remains uncertain", h.Reference)
			}
			id := runner.Text(h.Payload, "tool_use_id")
			if id == "" || open[id] || len(open) > 0 {
				return "", e.workload("precondition-not-established", o.InstanceID, "", "parallel or unidentified parent tool calls", h.Reference)
			}
			expected := e.expectedTools(o.InstanceID)
			// A standalone observer may supply only the known short fixture.
			want := "for i in 1 2 3; do sleep 1; done"
			if len(expected) > len(pre) {
				want = expected[len(pre)]
			} else if len(expected) > 0 {
				return "", e.workload("precondition-not-established", o.InstanceID, "", "extra parent tool shifted the requested ordinal", h.Reference)
			}
			if v.Input.Command != want {
				return "", e.workload("precondition-not-established", o.InstanceID, "", "parent tool command differs from its frozen foreground fixture (combined or unrelated call)", h.Reference)
			}
			open[id] = true
			pre = append(pre, h)
		}
		if h.Receipt.Event == "PostToolBatch" {
			ids := toolIDs(h.Payload)
			if len(ids) != 1 || !open[ids[0]] {
				return "", e.workload("precondition-not-established", o.InstanceID, "", "batch is not one uniquely outstanding foreground tool", h.Reference)
			}
			completed[ids[0]] = h
			delete(open, ids[0])
		}
	}
	if len(pre) >= n {
		p := pre[n-1]
		post, done := completed[runner.Text(p.Payload, "tool_use_id")]
		if !between && !done && len(pre) == n {
			return p.Reference, nil
		}
		if between && done {
			if len(pre) > n {
				return "", e.workload("missed-prose-window", o.InstanceID, "", "precondition-not-established: next tool started before injection into the requested prose interval", post.Reference)
			}
			data, err := e.Run.ReadTranscript(o)
			if err != nil {
				return "", err
			}
			tr, err := signals.ParseTranscript(data, o.SessionID)
			if err != nil {
				return "", err
			}
			for _, q := range tr.Requests {
				if q.Time.Before(post.Receipt.Received) {
					continue
				}
				terminal := false
				for _, row := range tr.Rows {
					if row.RequestRef == q.ID && row.Type == "assistant" && row.Message.StopReason != "" {
						terminal = true
					}
				}
				if !terminal {
					return post.Reference, nil
				}
			}
		}
	}
	lastActivity := o.Started
	for _, h := range facts {
		if runner.Text(h.Payload, "session_id") == o.SessionID && runner.Text(h.Payload, "agent_id") == "" && (h.Receipt.Event == "PreToolUse" || h.Receipt.Event == "UserPromptSubmit") && h.Receipt.Received.After(lastActivity) {
			lastActivity = h.Receipt.Received
		}
	}
	for i := len(facts) - 1; i >= 0; i-- {
		h := facts[i]
		if h.Receipt.Event == "Stop" && runner.Text(h.Payload, "session_id") == o.SessionID && runner.Text(h.Payload, "agent_id") == "" && !h.Receipt.Received.Before(lastActivity) {
			code := "missing-next-tool"
			if between && len(pre) >= n {
				code = "missed-prose-window"
			}
			return "", e.workload(code, o.InstanceID, "", "precondition-not-established: parent stopped without the required next tool/prose window", h.Reference)
		}
	}
	return "", nil
}

func workloadRows(c Capture) []runner.Result {
	var rows []runner.Result
	for _, r := range c.Manifest.Problems {
		if strings.HasPrefix(r.SignalID, "workload/") && r.Status == "not_completed" {
			rows = append(rows, r)
		}
	}
	return rows
}

func phaseWorkload(c Capture) bool { return len(workloadRows(c)) > 0 }

func workloadMissing(c Capture, id string) runner.Result {
	return result(id, "not_completed", "workload branch closed; measurement GAP retained", "complete intended schedule", workloadRows(c))
}

func validAlternatives(c Charge) bool {
	if len(c.Alternatives) == 0 {
		return c.SelectedCase == ""
	}
	var allowed []string
	switch c.Phase + "/" + c.ID {
	case "typed-controls/typed-idle-ad-input":
		allowed = []string{"typed-long-tool-ad-input", "typed-between-tools-ad-input", "typed-idle-ad-input"}
	case "dialogs/D2":
		allowed = dialogAlternatives()
	case "esc-gap/E2":
		allowed = []string{"Esc-steer-1", "E2"}
	case "esc-gap/E4":
		allowed = []string{"Esc-steer-2", "E4"}
	case "groups/O1", "groups/O2":
		p, err := PlanGroup("steer_turn")
		if err != nil {
			return false
		}
		i := 0
		if c.ID == "O2" {
			i = 1
		}
		allowed = []string{c.ID, p.Cases[p.Parts[i].First]}
	default:
		return false
	}
	return slices.Equal(c.Alternatives, allowed) && (c.SelectedCase == "" || slices.Contains(allowed, c.SelectedCase))
}

func typedStopFallback(c Capture) bool {
	for _, t := range ExtractTimings(c) {
		if strings.HasPrefix(t.Case, "typed-") && !strings.Contains(t.Case, "idle") && t.Mode == "wait_turn" && t.Whole {
			return true
		}
	}
	return false
}

func forceToolEnded(c Capture, command runner.Command) bool {
	var selected HookFact
	var session string
	for _, o := range c.Owners {
		if containsArg(command.Argv, "--instance-id", o.Owner.InstanceID) {
			session = o.Owner.SessionID
		}
	}
	if session == "" {
		return false
	}
	for _, h := range c.Hooks {
		if runner.Text(h.Payload, "session_id") == session && runner.Text(h.Payload, "agent_id") == "" && h.Receipt.Event == "PreToolUse" && runner.Text(h.Payload, "tool_name") == "Bash" && !h.Receipt.Received.After(command.Started) && (selected.Reference == "" || h.Receipt.Received.After(selected.Receipt.Received)) {
			selected = h
		}
	}
	id := runner.Text(selected.Payload, "tool_use_id")
	if id == "" {
		return false
	}
	for _, h := range c.Hooks {
		if h.Receipt.Event == "PostToolBatch" && runner.Text(h.Payload, "session_id") == session && runner.Text(h.Payload, "agent_id") == "" && !h.Receipt.Received.After(command.Ended) && slices.Contains(toolIDs(h.Payload), id) {
			return true
		}
	}
	return false
}

// All 24 Dialog probes and D2 fit one bounded ordinary prompt. D2 can own
// that first unexpected Stop route instead of forcing another parent start.
func dialogAlternatives() []string {
	ids := []string{"D2"}
	for i := 1; i <= 24; i++ {
		ids = append(ids, fmt.Sprintf("dialog-%04d", i))
	}
	return ids
}

func workloadCaseUnspent(c Capture, id string) bool {
	for _, charge := range c.After.Charges {
		if charge.Phase == c.Manifest.Phase && charge.Attempt == c.After.Identity.Attempt && charge.Actual > 0 && ((charge.ID == id && (charge.SelectedCase == "" || charge.SelectedCase == id)) || charge.SelectedCase == id) {
			return false
		}
	}
	return true
}

// A completed passive Stop/UPS can precede the next store observation. It is
// enough to close the branch safely; final accounting still requires actual
// stored membership and never imports this preliminary classification.
func (e *Exercise) passiveTypedFallback(s runner.Snapshot) error {
	if e.Run.Manifest.Phase != "typed-controls" {
		return nil
	}
	owner, err := e.owner(s.InstanceID)
	if err != nil {
		return err
	}
	facts, err := LiveHookFacts(e.Run.Evidence.Root, e.Run.Manifest.RunID, e.Run.Manifest.RecordingHook.Events)
	if err != nil {
		return err
	}
	for _, x := range e.Run.Manifest.Cases {
		if x.InstanceID != owner.InstanceID || x.Mode != "steer_turn" || strings.HasPrefix(x.ID, "typed-idle-") {
			continue
		}
		want := Transform(string(store.RenderBusySendPromptBody([][]byte{[]byte(x.Text)})), "wait_turn")
		stopped := false
		for _, h := range facts {
			if runner.Text(h.Payload, "session_id") != owner.SessionID || runner.Text(h.Payload, "cwd") != owner.CWD || runner.Text(h.Payload, "agent_id") != "" || h.Receipt.Received.Before(x.Started) {
				continue
			}
			if h.Receipt.Event == "Stop" {
				stopped = true
			}
			if stopped && h.Receipt.Event == "UserPromptSubmit" && runner.Text(h.Payload, "prompt_id") != "" && stopPromptMatches(runner.Text(h.Payload, "prompt"), want) {
				return e.workload("early-Stop-fallback", owner.InstanceID, x.ID, "no tool boundary after injection; delivered at Stop; intended measurement and later branch remain incomplete", h.Reference)
			}
		}
	}
	return nil
}
