package qualification

import (
	"encoding/json"
	"slices"
	"strings"
	"time"

	"github.com/gabemahoney/agent-director/test/claude-live/runner"
	"github.com/gabemahoney/agent-director/test/claude-live/signals"
)

type Timing struct {
	Case              string              `json:"case"`
	Mode              string              `json:"mode"`
	State             string              `json:"state,omitempty"`
	Provider          string              `json:"provider"`
	Instance          string              `json:"instance"`
	Launch            string              `json:"launch"`
	Send              time.Time           `json:"send"`
	Returned          time.Time           `json:"returned"`
	Issue             time.Time           `json:"issue"`
	Complete          time.Time           `json:"complete"`
	Sighting          time.Time           `json:"sighting"`
	Order             time.Time           `json:"order"`
	Intake            time.Time           `json:"intake"`
	FirstResponseText time.Time           `json:"first_response_text"`
	ResponseMeaning   string              `json:"response_meaning"`
	Request           string              `json:"request"`
	BoundaryStatus    string              `json:"boundary_status,omitempty"`
	BoundaryReason    string              `json:"boundary_reason,omitempty"`
	Boundary          int                 `json:"boundary"`
	Whole             bool                `json:"whole"`
	Evidence          []string            `json:"evidence"`
	TypedInput        *signals.TypedInput `json:"typed_input,omitempty"`
}

type WholeInput struct {
	Body       string             `json:"body"`
	Mode       string             `json:"mode"`
	After      int64              `json:"after"`
	Transcript signals.Transcript `json:"-"`
	Evidence   []string           `json:"evidence"`
}

func CheckWholeInput(in WholeInput) runner.Result {
	if in.Body == "" || in.After <= 0 || len(in.Transcript.Requests) == 0 {
		return result("whole-input", "not_completed", "known body or request journal missing", "one exact whole block in the next eligible request", nil, in.Evidence...)
	}
	if in.Mode != "wait_turn" && in.Mode != "steer_turn" && in.Mode != "typed" {
		return result("whole-input", "fail", "unknown input mode", nil, in.Mode, in.Evidence...)
	}
	var intake signals.Intake
	var err error
	if in.Mode == "typed" {
		intake, err = in.Transcript.NextTypedInput(in.After, in.Body)
	} else {
		intake, err = in.Transcript.NextBodyInput(in.After, in.Body, in.Mode)
	}
	if err != nil {
		status := "not_completed"
		for _, q := range in.Transcript.Requests {
			if q.Time.UnixMilli() >= in.After {
				status = "fail"
				break
			}
		}
		return result("whole-input", status, err.Error(), "complete known input exactly once in the first eligible model request", intake, in.Evidence...)
	}
	return result("whole-input", "pass", "exact whole input in the first eligible model request", "complete known input once", intake, in.Evidence...)
}

// Injection state and triggers come from the actual input operation. The
// same-case wait remains separate evidence for provider-refusal diagnostics.
func (c Capture) injectionObservation(x runner.Case) (Observation, bool) {
	kind := "send"
	if x.Mode == "typed" || x.Mode == "native" {
		kind = "typed-control"
	}
	return c.observation(x.ID, kind)
}

func ExtractTimings(c Capture) []Timing {
	var timings []Timing
	for _, x := range c.Manifest.Cases {
		if x.Text == "" {
			continue
		}
		t := Timing{Case: x.ID, Mode: x.Mode, Provider: c.Manifest.ProviderVersion, Instance: x.InstanceID, Send: x.Started, Returned: x.Ended, ResponseMeaning: "first assistant text only; semantic acknowledgment and delivery confirmation are not inferred"}
		if o, ok := c.injectionObservation(x); ok {
			t.State = o.State
		}
		var owner OwnerEvidence
		var in WholeInput
		if x.Mode != "typed" && x.Mode != "native" && x.Mode != "" {
			own, p, h, err := c.prompt(x.Text)
			if err != nil {
				timings = append(timings, t)
				continue
			}
			owner = own
			body, err := PromptBody(own.Snapshot, p)
			if err != nil {
				timings = append(timings, t)
				continue
			}
			t.Mode = "wait_turn"
			if runner.Text(h, "boundary") == "tool_boundary" {
				t.Mode = "steer_turn"
			}
			t.Issue = timeFromMillis(runner.Number(p, "issued_at_ms"))
			t.Complete = timeFromMillis(runner.Number(p, "transport_at_ms"))
			t.Order = timeFromMillis(runner.Number(p, "order_at_ms"))
			if runner.Text(p, "admission_kind") == "prompt_sighting" {
				t.Sighting = timeFromMillis(runner.Number(p, "admitted_at_ms"))
			}
			after := t.Complete.UnixMilli()
			if t.Mode == "wait_turn" {
				after = t.Sighting.UnixMilli()
			}
			in = WholeInput{body, t.Mode, after, own.Transcript, []string{x.Command, own.SnapshotRef, own.TranscriptRef}}
		} else {
			var ok bool
			owner, ok = c.ownerDuring(x.InstanceID, x.Started, x.Ended)
			if !ok {
				timings = append(timings, t)
				continue
			}
			in = WholeInput{Body: x.Text, Mode: "typed", After: x.Started.UnixMilli(), Transcript: owner.Transcript, Evidence: []string{x.Command, owner.TranscriptRef}}
		}
		t.Launch = owner.Owner.LaunchToken
		check := CheckWholeInput(in)
		t.Whole = check.Status == "pass"
		t.Evidence = check.Evidence
		if intake, ok := check.Observed.(signals.Intake); ok && t.Whole {
			t.TypedInput = intake.Typed
			t.Intake = intake.Time
			if intake.Request >= 0 && intake.Request < len(in.Transcript.Requests) {
				t.Request = in.Transcript.Requests[intake.Request].ID
			}
			t.FirstResponseText = firstResponseText(c, owner, t.Request, t.Intake)
		}
		if strings.HasPrefix(x.ID, "typed-") && strings.HasSuffix(x.ID, "-input") {
			deriveTypedBoundary(c, x, owner, &t)
		}
		timings = append(timings, t)
	}
	return timings
}

// A response may make tool calls before emitting text. Follow only owned main
// requests within this parent turn; a Stop or a new parent start closes it.
func firstResponseText(c Capture, owner OwnerEvidence, request string, intake time.Time) time.Time {
	var end time.Time
	for _, h := range c.Hooks {
		if runner.Text(h.Payload, "session_id") != owner.Owner.SessionID || runner.Text(h.Payload, "agent_id") != "" || !h.Receipt.Received.After(intake) {
			continue
		}
		if (h.Receipt.Event == "Stop" || h.Receipt.Event == "UserPromptSubmit") && (end.IsZero() || h.Receipt.Received.Before(end)) {
			end = h.Receipt.Received
		}
	}
	eligible := map[string]bool{request: true}
	if !end.IsZero() {
		for _, q := range owner.Transcript.Requests {
			if !q.Time.Before(intake) && q.Time.Before(end) {
				eligible[q.ID] = true
			}
		}
	}
	var first time.Time
	for _, row := range owner.Transcript.Rows {
		if row.Type != "assistant" || !eligible[row.RequestRef] || row.Timestamp.Before(intake) || (!end.IsZero() && !row.Timestamp.Before(end)) {
			continue
		}
		texts, err := signals.Texts(row.Message)
		if err != nil {
			continue
		}
		for _, text := range texts {
			if text != "" && (first.IsZero() || row.Timestamp.Before(first)) {
				first = row.Timestamp
			}
		}
	}
	return first
}

type Gap struct {
	ID         string `json:"id"`
	Schedule   string `json:"schedule"`
	First      Timing `json:"first"`
	Second     Timing `json:"second"`
	CompleteMS *int64 `json:"complete_ms,omitempty"`
	SightingMS *int64 `json:"sighting_ms,omitempty"`
	IntakeMS   *int64 `json:"intake_ms,omitempty"`
}

func difference(a, b time.Time) *int64 {
	if a.IsZero() || b.IsZero() {
		return nil
	}
	n := b.Sub(a).Milliseconds()
	return &n
}
func NewGap(id, schedule string, first, second Timing) Gap {
	return Gap{id, schedule, first, second, difference(first.Complete, second.Complete), difference(first.Sighting, second.Sighting), difference(first.Intake, second.Intake)}
}
func timingByID(ts []Timing, id string) (Timing, bool) {
	i := slices.IndexFunc(ts, func(t Timing) bool { return t.Case == id })
	if i < 0 {
		return Timing{}, false
	}
	return ts[i], true
}
func intakeFromResult(r runner.Result) (signals.Intake, bool) {
	if in, ok := r.Observed.(signals.Intake); ok {
		return in, true
	}
	b, err := json.Marshal(r.Observed)
	if err != nil {
		return signals.Intake{}, false
	}
	var in signals.Intake
	err = json.Unmarshal(b, &in)
	return in, err == nil
}

// The eligible request is anchored to the owned injection window, before
// looking at AD output. Eventual output can never move this lower bound.
func deriveTypedBoundary(c Capture, x runner.Case, owner OwnerEvidence, t *Timing) {
	missing := func(reason string) {
		t.State = ""
		t.Boundary = 0
		t.BoundaryStatus = "not_completed"
		t.BoundaryReason = reason
	}
	ob, ok := c.injectionObservation(x)
	if !ok {
		missing("owned injection observation missing")
		return
	}
	before, ref, ok := c.snapshotCase(x.ID + "-before")
	if !ok || validateSnapshot(before, owner.Owner) != nil || before.ReadCompleted.After(x.Started) {
		missing("owned pre-injection snapshot missing or overlaps send")
		return
	}
	t.Evidence = append(t.Evidence, ref, ob.Trigger)
	var hooks []HookFact
	for _, h := range c.Hooks {
		if runner.Text(h.Payload, "session_id") == owner.Owner.SessionID && runner.Text(h.Payload, "agent_id") == "" && !h.Receipt.Received.Before(owner.Owner.Started) {
			hooks = append(hooks, h)
		}
	}
	slices.SortFunc(hooks, func(a, b HookFact) int { return a.Receipt.Received.Compare(b.Receipt.Received) })
	var pre, post, trigger *HookFact
	for i := range hooks {
		h := &hooks[i]
		if h.Reference == ob.Trigger {
			trigger = h
		}
		if h.Receipt.Received.After(x.Started) {
			continue
		}
		if h.Receipt.Event == "PreToolUse" && runner.Text(h.Payload, "tool_name") == "Bash" {
			pre = h
		}
		if h.Receipt.Event == "PostToolBatch" {
			post = h
		}
	}
	requestAfter := x.Started.UnixMilli()
	switch ob.State {
	case "idle":
		if !idle(before) {
			missing("idle injection was not independently waiting")
			return
		}
		var prior struct {
			Snapshot runner.Snapshot `json:"snapshot"`
		}
		if c.json(ob.Trigger, &prior) != nil || validateSnapshot(prior.Snapshot, owner.Owner) != nil || prior.Snapshot.ReadCompleted.After(x.Started) {
			missing("idle completion trigger is not owned or precedes no send")
			return
		}
		t.State = "idle"
	case "long-tool":
		if trigger == nil || trigger.Receipt.Event != "PreToolUse" || pre != trigger || (post != nil && !post.Receipt.Received.Before(pre.Receipt.Received)) {
			missing("send missed its trigger tool interval")
			return
		}
		t.State = "long-tool"
	case "between-tools":
		if trigger == nil || trigger.Receipt.Event != "PostToolBatch" || post != trigger || (pre != nil && !pre.Receipt.Received.Before(post.Receipt.Received)) {
			missing("send missed the trigger-to-next-tool interval")
			return
		}
		t.State = "between-tools"
	default:
		missing("unknown observed injection window")
		return
	}
	if t.State == "between-tools" {
		for _, h := range hooks {
			if h.Receipt.Event == "PreToolUse" && h.Receipt.Received.After(x.Started) && h.Receipt.Received.Before(x.Ended) {
				missing("injection command overlapped the next tool")
				return
			}
		}
	}
	if t.State != "idle" {
		found := false
		for _, h := range hooks {
			if h.Receipt.Event == "PostToolBatch" && !h.Receipt.Received.Before(x.Started) {
				if h.Receipt.Received.Before(x.Ended) {
					missing("injection command overlapped its eligible tool boundary")
					return
				}
				requestAfter = h.Receipt.Received.UnixMilli()
				found = true
				t.Evidence = append(t.Evidence, h.Reference)
				break
			}
		}
		if !found {
			missing("no eligible parent PostToolBatch after injection")
			return
		}
	}
	first := -1
	actual := -1
	for i, q := range owner.Transcript.Requests {
		if q.Time.UnixMilli() >= requestAfter && first < 0 {
			first = i
		}
		if q.ID == t.Request && t.Request != "" {
			actual = i
		}
	}
	if first < 0 {
		missing("no first eligible model request after the owned boundary")
		return
	}
	t.Boundary = 1
	if actual >= first {
		t.Boundary = actual - first + 1
	}
	if t.State != "idle" && !t.Intake.IsZero() {
		n := 0
		for _, h := range hooks {
			if h.Receipt.Event == "PostToolBatch" && !h.Receipt.Received.Before(x.Started) && !h.Receipt.Received.After(t.Intake) {
				n++
			}
		}
		t.Boundary = max(t.Boundary, n)
	}
	// ExtractTimings validated whole input with the original injection bound,
	// including any human attachment queued while the tool was running. Keep
	// that provenance bound; only request eligibility advances to the boundary.
	t.Whole = t.Whole && actual == first
	t.BoundaryStatus = "pass"
	t.BoundaryReason = "whole input in the first request of the owned first eligible interval"
	if t.Boundary != 1 || !t.Whole {
		t.BoundaryStatus = "fail"
		t.BoundaryReason = "input missed the first eligible model request/boundary derived from the injection trigger"
	}
}
