package signals

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/gabemahoney/agent-director/test/claude-live/runner"
)

func (c capture) hasCase(id string) bool {
	for _, x := range c.m.Cases {
		if x.ID == id {
			return true
		}
	}
	return false
}

func (c capture) wakeText() string {
	for _, x := range c.m.Cases {
		if x.ID == "overlap-wake" && (x.Text == WakePA || x.Text == WakeOrder) {
			return x.Text
		}
	}
	return OverlapWake
}

func (c capture) toolTurnText() string {
	if c.wakeText() == WakePA || c.wakeText() == WakeOrder {
		return c.wakeText()
	}
	return Busy
}

func (c capture) message(text string) (runner.Row, error) {
	var found runner.Row
	count := 0
	for _, m := range c.s.Tables["messages"] {
		if runner.Text(m, "text") == text {
			found, count = m, count+1
		}
	}
	if count != 1 || runner.Text(found, "instance_id") != c.m.Owned[0].InstanceID || runner.Text(found, "text_sha256") != runner.Digest([]byte(text)) {
		return nil, errors.New("missing, ambiguous or foreign Message")
	}
	return found, nil
}

func (c capture) acceptedCase(id, text string) (runner.Row, error) {
	x, ok := c.caseRecord(id)
	m, err := c.message(text)
	if !ok || err != nil || x.Text != text || runner.Text(m, "send_mode") != x.Mode {
		return nil, errors.New("Message is not bound to its successful send case")
	}
	cmd := c.commands[x.Command]
	at := runner.Number(m, "accepted_at_ms")
	if at < cmd.Started.UnixMilli() || at > cmd.Ended.UnixMilli() {
		return nil, errors.New("Message acceptance lies outside its send command")
	}
	return m, nil
}

func sameMessage(a, b runner.Row) bool {
	return runner.Text(a, "message_id") != "" && runner.Text(a, "message_id") == runner.Text(b, "message_id") && runner.Number(a, "accepted_seq") == runner.Number(b, "accepted_seq") && runner.Number(a, "accepted_at_ms") == runner.Number(b, "accepted_at_ms") && runner.Text(a, "send_mode") == runner.Text(b, "send_mode")
}

// Every intermediate snapshot is indexed and bound just like the final store.
// Observation times order samples, never substitute for AD fact timestamps.
func (c capture) history() ([]runner.Snapshot, error) {
	var history []runner.Snapshot
	for name := range c.m.Artifacts {
		if !strings.HasSuffix(name, "-store.json") && !strings.HasPrefix(name, "raw/order-observations-") {
			continue
		}
		b, err := c.indexed(name)
		var s runner.Snapshot
		if err != nil || json.Unmarshal(b, &s) != nil {
			return nil, fmt.Errorf("invalid store observation %s", name)
		}
		if s.InstanceID != c.m.Owned[0].InstanceID {
			continue
		}
		if len(s.Tables["spawns"]) != 1 || runner.Text(s.Tables["spawns"][0], "session_id") != c.m.Owned[0].SessionID || runner.Text(s.Tables["spawns"][0], "launch_token") != c.m.Owned[0].LaunchToken || s.Observed.IsZero() || s.Observed.Before(c.m.Started) || s.Observed.After(c.m.Finished) {
			return nil, errors.New("unbound store observation")
		}
		// Older captures lack a read upper bound. They remain replayable but
		// cannot establish the new pre-Stop Held witness.
		if !s.ReadCompleted.IsZero() && (s.ReadCompleted.Before(s.Observed) || s.ReadCompleted.After(c.m.Finished)) {
			return nil, errors.New("invalid store observation read interval")
		}
		history = append(history, s)
	}
	sort.Slice(history, func(i, j int) bool { return history[i].Observed.Before(history[j].Observed) })
	return history, nil
}

func snapshotPrompt(s runner.Snapshot, p runner.Row) runner.Row {
	for _, row := range s.Tables["prompts"] {
		if runner.Text(row, "handover_id") == runner.Text(p, "handover_id") && runner.Number(row, "prompt_index") == runner.Number(p, "prompt_index") {
			return row
		}
	}
	return nil
}

// The second fact writer uses its own captured timestamp for order_at_ms.
// This is not a new host-clock stamp and need not be max(first, second) when
// writers enter concurrently. Retained single-fact observations disambiguate
// that case (and equal millisecond clocks); Stop supplies no fact.
func (c capture) wakeOrder(text string) (int64, error) {
	p, h, err := c.prompt(text)
	if err != nil {
		return 0, err
	}
	history, err := c.history()
	if err != nil {
		return 0, err
	}
	return WakeOrderBody(c.m.Owned[0], c.s, p, h, Prompt(text), c.hooks["UserPromptSubmit"], c.trail, history)
}

// WakeOrderBody shares ci's exact fact/sighting/Launch/callback/causal checks
// with multi-Message qualification. Captured writer timestamps can reverse;
// order is the second fact transaction, never a fabricated max timestamp.
func WakeOrderBody(owner runner.Owned, snapshot runner.Snapshot, p, h runner.Row, body string, hooks, trail []runner.Row, history []runner.Snapshot) (int64, error) {
	c := capture{m: runner.Manifest{Owned: []runner.Owned{owner}}, s: snapshot, hooks: map[string][]runner.Row{"UserPromptSubmit": hooks}, trail: trail}
	for _, s := range history {
		if s.InstanceID != owner.InstanceID || len(s.Tables["spawns"]) != 1 || runner.Text(s.Tables["spawns"][0], "launch_token") != owner.LaunchToken || runner.Text(s.Tables["spawns"][0], "session_id") != owner.SessionID {
			return 0, errors.New("unbound wake fact observation")
		}
	}
	issued, written, sighted, order := runner.Number(p, "issued_at_ms"), runner.Number(p, "transport_at_ms"), runner.Number(p, "admitted_at_ms"), runner.Number(p, "order_at_ms")
	if runner.Text(h, "boundary") != "force" && runner.Text(h, "boundary") != "turn_end" {
		return 0, errors.New("not a Claude wake")
	}
	if runner.Text(p, "admission_kind") != "prompt_sighting" || runner.Text(p, "order_kind") != "ordered_acceptance" || sighted < issued || sighted == 0 || (order != written && order != sighted) {
		return 0, errors.New("wake order is not its written plus prompt_sighting fact transaction")
	}
	launches := 0
	for _, l := range c.s.Tables["launches"] {
		if runner.Text(l, "launch_id") != c.m.Owned[0].LaunchID {
			continue
		}
		launches++
		end := runner.Number(l, "ended_at_ms")
		if runner.Text(l, "launch_token") != c.m.Owned[0].LaunchToken || runner.Text(l, "provider") != "claude" || (end > 0 && (written >= end || sighted >= end || order >= end)) {
			return 0, errors.New("wake fact/order is stale or after Launch End")
		}
	}
	if launches != 1 {
		return 0, errors.New("missing unique live Launch for wake facts")
	}
	if runner.Text(h, "boundary") == "force" {
		forces := 0
		for _, f := range c.s.Tables["forces"] {
			if runner.Text(f, "force_id") == runner.Text(h, "force_id") && runner.Text(f, "handover_id") == runner.Text(h, "handover_id") && runner.Text(f, "instance_id") == c.m.Owned[0].InstanceID && runner.Text(f, "launch_id") == c.m.Owned[0].LaunchID {
				forces++
			}
		}
		if forces != 1 {
			return 0, errors.New("wake lacks its unique owned force record")
		}
	}
	// One exact bound callback corroborates AD's uniquely selected sighting.
	sightings := 0
	for _, row := range c.hooks["UserPromptSubmit"] {
		if c.parent(row, "UserPromptSubmit") && runner.Text(row, "prompt") == RenderBody(body, "wait_turn") && runner.Text(row, "prompt_id") != "" {
			sightings++
		}
	}
	if sightings != 1 || !c.fired("UserPromptSubmit") {
		return 0, errors.New("missing or ambiguous exact parent wake sighting")
	}
	causal := false
	for _, s := range history {
		row := snapshotPrompt(s, p)
		if row == nil {
			continue
		}
		w, a := runner.Text(row, "transport") == "written", runner.Text(row, "admission_kind") == "prompt_sighting"
		if (w && runner.Number(row, "transport_at_ms") != written) || (a && runner.Number(row, "admitted_at_ms") != sighted) || (runner.Text(row, "order_kind") != "" && (runner.Text(row, "order_kind") != "ordered_acceptance" || runner.Number(row, "order_at_ms") != order)) {
			return 0, errors.New("immutable wake facts differ across observations")
		}
		if runner.Text(row, "order_kind") != "" && (!w || !a) {
			return 0, errors.New("wake order observed before both facts")
		}
		if runner.Text(row, "order_kind") == "" && ((order == sighted && w && !a) || (order == written && a && !w)) {
			causal = true
		}
	}
	if order <= min(written, sighted) && !causal {
		return 0, errors.New("coarse or reversed fact timestamps lack causal observation")
	}
	return order, nil
}

func (c capture) heldOverlap(wakeText string, later ...string) (bool, error) {
	wake, _, err := c.prompt(wakeText)
	if err != nil {
		return false, err
	}
	wakeMessage, err := c.message(wakeText)
	if err != nil {
		return false, err
	}
	seq := runner.Number(wakeMessage, "accepted_seq")
	for _, text := range later {
		m, err := c.message(text)
		if err != nil || runner.Number(m, "accepted_seq") <= seq {
			return false, errors.New("accepted sequence is not wake then later Messages")
		}
		seq = runner.Number(m, "accepted_seq")
	}
	history, err := c.history()
	if err != nil {
		return false, err
	}
	for _, s := range history {
		row := snapshotPrompt(s, wake)
		if runner.Text(row, "transport") != "written" || runner.Text(row, "admission_kind") != "" || runner.Text(row, "order_kind") != "" {
			continue
		}
		copy := c
		copy.s = s
		all := true
		for _, text := range later {
			m, e := copy.message(text)
			final, fe := c.message(text)
			all = all && e == nil && fe == nil && sameMessage(m, final) && runner.Text(m, "place") == "held"
		}
		if all {
			return true, nil
		}
	}
	return false, nil
}

func appendClause(v *runner.Verdict, id string, exercised, ok bool, expected string, observed any, refs []string) {
	r := runner.Result{SignalID: id, Status: "pass", Reason: "expected signal observed", Expected: expected, Observed: observed, Evidence: refs}
	if !exercised {
		r.Status, r.Reason = "not_completed", "required overlapping exercise is not established"
	} else if !ok {
		r.Status, r.Reason = "fail", "signal differs after the completed overlapping exercise"
	}
	v.Results = append(v.Results, r)
}

func (c capture) overlap(v *runner.Verdict) {
	wake, wh, we := c.prompt(c.wakeText())
	steer, sh, se := c.prompt(Steer)
	wi, wie := c.intake(c.wakeText(), "wait_turn")
	si, sie := c.intake(Steer, "steer_turn")
	order, oe := c.wakeOrder(c.wakeText())
	held, he := c.heldOverlap(c.wakeText(), Steer)
	wm, wme := c.acceptedCase("overlap-wake", c.wakeText())
	sm, sme := c.acceptedCase("overlap-steer", Steer)
	// Older captures did not retain the Held snapshot. Their authenticated
	// acceptance interval can still expose an early order/issue regression;
	// it cannot qualify the new P-b/P-c snapshot-based exercise.
	accepted := runner.Number(sm, "accepted_at_ms")
	legacyInterval := we == nil && wme == nil && sme == nil && runner.Number(wm, "accepted_seq") < runner.Number(sm, "accepted_seq") && runner.Number(wake, "transport_at_ms") < accepted && accepted < runner.Number(wake, "admitted_at_ms")
	exercised := c.caseDone("wake-steer-overlap") && (held || legacyInterval) && runner.Text(wh, "boundary") == "force"
	barrier := oe == nil && se == nil && runner.Number(steer, "issued_at_ms") > max(order, runner.Number(wake, "admitted_at_ms"), runner.Number(wake, "transport_at_ms"))
	steerOrder := se == nil && runner.Text(sh, "boundary") == "tool_boundary" && runner.Text(steer, "order_kind") == "emission_complete" && runner.Number(steer, "order_at_ms") == runner.Number(steer, "transport_at_ms")
	modelOrder := wie == nil && sie == nil && earlier(wi, si)
	observed := map[string]any{"wake": facts(wake, we), "steer": facts(steer, se), "wake_input": wi, "steer_input": si, "wake_input_error": errorText(wie), "steer_input_error": errorText(sie), "wake_order_ms": order, "wake_order_error": errorText(oe), "held_interval": held, "held_error": errorText(he), "legacy_acceptance_interval": legacyInterval, "steer_after_wake_order": barrier, "wake_before_steer": modelOrder, "steer_emission_order": steerOrder}
	appendClause(v, "wake-steer-overlap", exercised, he == nil && wme == nil && sme == nil, "later STEER accepted while the completed wake remains unsighted; no Stop release", observed, c.refs)
	// A completed old exercise may refute the AD barrier even if it cannot
	// qualify P-b. Keep the actual reversed input offsets in this result.
	appendClause(v, "hook-completion-order", exercised, barrier && steerOrder && modelOrder, "wake written+sighted order point; exactly one later tool-boundary steer after it; whole wake before steer", observed, c.refs)
	appendClause(v, PB, exercised && held && barrier && steerOrder, modelOrder && wme == nil && sme == nil, "P-b: a sighted wake is taken before the later issued STEER; overlapping Held custody required", observed, c.refs)
	if !barrier {
		v.Results[len(v.Results)-1].Reason = "P-b not measured: AD issued before a valid wake order point, or its evidence is missing"
	}
	c.steerPlain(v)
}

func (c capture) steerPlain(v *runner.Verdict) {
	held, he := c.heldOverlap(c.wakeText(), Steer, Plain)
	steer, sh, se := c.prompt(Steer)
	plain, ph, pe := c.prompt(Plain)
	si, sie := c.intake(Steer, "steer_turn")
	pi, pie := c.intake(Plain, "wait_turn")
	sm, sme := c.acceptedCase("overlap-steer", Steer)
	pm, pme := c.acceptedCase("idle-wake", Plain)
	pendingTake, pendingErr := c.heldDuringTake(steer, Plain)
	plainOrder, plainOrderErr := c.wakeOrder(Plain)
	ordered := se == nil && pe == nil && sme == nil && pme == nil && runner.Text(sh, "boundary") == "tool_boundary" && runner.Text(pm, "send_mode") == "wait_turn" && runner.Text(steer, "order_kind") == "emission_complete" && runner.Number(steer, "order_at_ms") == runner.Number(steer, "transport_at_ms") && runner.Number(pm, "accepted_seq") > runner.Number(sm, "accepted_seq") && runner.Number(plain, "issued_at_ms") > runner.Number(steer, "transport_at_ms") && runner.Text(ph, "boundary") == "turn_end" && runner.Text(steer, "admission_kind") == "native_signal" && runner.Number(plain, "issued_at_ms") >= runner.Number(steer, "admitted_at_ms")
	observed := map[string]any{"held_interval": held, "held_error": errorText(he), "steer": facts(steer, se), "plain": facts(plain, pe), "steer_input": si, "plain_input": pi, "steer_input_error": errorText(sie), "plain_input_error": errorText(pie), "issue_and_take_order": ordered, "plain_held_during_steer_take": pendingTake, "pending_take_error": errorText(pendingErr), "plain_order_ms": plainOrder, "plain_order_error": errorText(plainOrderErr)}
	appendClause(v, PC, held && he == nil && c.caseDone("steer-turn-end") && c.caseDone("idle-wake"), ordered && pendingTake && pendingErr == nil && plainOrderErr == nil && sie == nil && pie == nil && si.Time.UnixMilli() < runner.Number(plain, "issued_at_ms") && earlier(si, pi), "P-c: overlapping STEER/Wait Turn Plain; Plain held through pending steer take; whole steer context before later Plain issue and whole input", observed, c.refs)
}

func (c capture) heldDuringTake(earlier runner.Row, laterText string) (bool, error) {
	history, err := c.history()
	if err != nil {
		return false, err
	}
	found := false
	for _, s := range history {
		p := snapshotPrompt(s, earlier)
		if runner.Text(p, "transport") != "written" || runner.Text(p, "admission_kind") != "" {
			continue
		}
		copy := c
		copy.s = s
		m, err := copy.message(laterText)
		if err != nil {
			continue
		}
		final, fe := c.message(laterText)
		if fe != nil || !sameMessage(m, final) {
			return false, errors.New("later Message identity changed across observations")
		}
		if runner.Text(m, "place") != "held" {
			return false, errors.New("later Wait Turn Message left Held before earlier steer take")
		}
		found = true
	}
	return found, nil
}

func hasBlock(row TranscriptRow, kind string) bool {
	var blocks []Block
	if json.Unmarshal(row.Message.Content, &blocks) != nil {
		return false
	}
	for _, b := range blocks {
		if b.Type == kind {
			return true
		}
	}
	return false
}

// textWindow identifies the model request running at complete output, its
// own terminal response, and the absence of later tools through Stop. It does
// not use the manifest's Completed flag or an assistant acknowledgment.
func (c capture) textWindow(complete, stop int64) (Request, int, error) {
	var req Request
	if c.transcriptErr != nil {
		return req, 0, c.transcriptErr
	}
	for _, r := range c.t.Requests {
		if r.Time.UnixMilli() < complete && (req.Line == 0 || r.Time.After(req.Time)) {
			req = r
		}
	}
	if req.Line == 0 {
		return req, 0, errors.New("no parent request before wake completion")
	}
	terminal, count := 0, 0
	for _, row := range c.t.Rows {
		if row.Type == "assistant" && row.RequestRef == req.ID {
			if hasBlock(row, "tool_use") {
				return req, 0, errors.New("active request used a tool instead of a text-only finish")
			}
			if row.Message.StopReason == "end_turn" {
				if row.Timestamp.UnixMilli() <= complete || row.Timestamp.UnixMilli() >= stop {
					return req, 0, errors.New("request did not finish strictly between wake completion and releasing Stop")
				}
				terminal, count = row.Line, count+1
			}
		}
		if row.Timestamp.UnixMilli() >= complete && row.Timestamp.UnixMilli() <= stop && (hasBlock(row, "tool_use") || hasBlock(row, "tool_result")) {
			return req, 0, errors.New("tool boundary after wake completion and before releasing Stop")
		}
	}
	if count != 1 {
		return req, 0, errors.New("missing or ambiguous end_turn for the running request")
	}
	for _, event := range []string{"PreToolUse", "PostToolBatch"} {
		for _, h := range c.hooks[event] {
			at, err := time.Parse(time.RFC3339Nano, runner.Text(h, "_received"))
			if err == nil && c.parent(h, event) && at.UnixMilli() >= complete && at.UnixMilli() <= stop {
				return req, terminal, errors.New("recorded tool boundary during the text-only window")
			}
		}
	}
	return req, terminal, nil
}
