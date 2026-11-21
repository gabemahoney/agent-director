package qualification

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/test/claude-live/runner"
	"github.com/gabemahoney/agent-director/test/claude-live/signals"
)

type PackingEvidence struct {
	Owner      runner.Owned
	Snapshot   runner.Snapshot
	Transcript signals.Transcript
	Hooks      []runner.Row
	Trail      []runner.Row
	History    []runner.Snapshot
	References []string
}

// CheckPackingAndOrder counts actual issued output only. Released unissued
// memberships can be reclaimed later without becoming duplicate emissions.
func CheckPackingAndOrder(in PackingEvidence) runner.Result {
	fail := func(reason string, observed any) runner.Result {
		return result("packing-order", "fail", reason, "whole minimal packing, accepted order and proper successor gates", observed, in.References...)
	}
	missing := func(reason string) runner.Result {
		return result("packing-order", "not_completed", reason, "complete frozen membership/output/intake facts", nil, in.References...)
	}
	if err := validateSnapshot(in.Snapshot, in.Owner); err != nil {
		return fail(err.Error(), nil)
	}
	messages := map[string]runner.Row{}
	for _, m := range in.Snapshot.Tables["messages"] {
		id := runner.Text(m, "message_id")
		if id == "" || messages[id] != nil || runner.Text(m, "text_sha256") != runner.Digest([]byte(runner.Text(m, "text"))) {
			return fail("Message identity/text hash changed", m)
		}
		if runner.Text(m, "result_class") == "delivered_confirmed" {
			return fail("Claude Result falsely claims confirmed delivery", m)
		}
		messages[id] = m
	}
	emitted := map[string]int{}
	issuedCount := 0
	var sequence []physicalEmission
	for _, h := range in.Snapshot.Tables["handovers"] {
		if runner.Text(h, "launch_token") != in.Owner.LaunchToken {
			continue
		}
		hid := runner.Text(h, "handover_id")
		var members []runner.Row
		for _, member := range in.Snapshot.Tables["members"] {
			if runner.Text(member, "handover_id") == hid {
				members = append(members, member)
			}
		}
		slices.SortFunc(members, func(a, b runner.Row) int {
			return int(runner.Number(a, "member_index") - runner.Number(b, "member_index"))
		})
		var texts [][]byte
		var previous int64
		for _, member := range members {
			msg := messages[runner.Text(member, "message_id")]
			if msg == nil {
				return fail("frozen membership lacks its Message", member)
			}
			seq := runner.Number(msg, "accepted_seq")
			if seq <= previous {
				return fail("handover crosses its accepted-order cutoff", msg)
			}
			previous = seq
			texts = append(texts, []byte(runner.Text(msg, "text")))
		}
		if len(texts) == 0 {
			continue
		}
		mode := "wait_turn"
		if runner.Text(h, "boundary") == "tool_boundary" {
			mode = "steer_turn"
		}
		limits, _ := store.BusySendCapabilitiesFor("claude").Mode(store.BusySendMode(mode))
		parts, err := store.PartitionBusySendPrompts(texts, limits, nil)
		if err != nil {
			return fail(err.Error(), nil)
		}
		prompts := make([]runner.Row, len(parts))
		for _, p := range in.Snapshot.Tables["prompts"] {
			if runner.Text(p, "handover_id") != hid {
				continue
			}
			index := int(runner.Number(p, "prompt_index"))
			if index < 0 || index >= len(parts) || prompts[index] != nil {
				return fail("invalid or duplicate prompt partition index", p)
			}
			prompts[index] = p
		}
		for index, part := range parts {
			p := prompts[index]
			if p == nil {
				return missing("frozen prompt partition missing")
			}
			if runner.Number(p, "message_count") != int64(part.Count) {
				return fail("prompt does not use the candidate's longest fitting prefix", p)
			}
			body := string(store.RenderBusySendPromptBody(texts[part.First : part.First+part.Count]))
			actual, err := PromptBody(in.Snapshot, p)
			if err != nil {
				return fail(err.Error(), p)
			}
			if actual != body || limits.Measure([]byte(body)) != part.RenderedBytes {
				return fail("whole body or separator widths differ from minimal partition", p)
			}
			if runner.Text(p, "emission") != "issued" {
				if runner.Text(p, "transport") == "written" {
					return fail("released/unissued row claims physical complete output", p)
				}
				continue
			}
			issuedCount++
			if runner.Text(p, "transport") != "written" {
				return missing("issued prompt lacks complete physical output")
			}
			if runner.Text(h, "instance_id") != in.Owner.InstanceID || runner.Text(h, "launch_token") != in.Owner.LaunchToken || runner.Text(h, "session_id") != in.Owner.SessionID {
				return fail("physical prompt belongs to another target/Launch/session", h)
			}
			if mode == "wait_turn" {
				if _, err = signals.WakeOrderBody(in.Owner, in.Snapshot, p, h, body, in.Hooks, in.Trail, in.History); err != nil {
					return fail(err.Error(), p)
				}
			} else if runner.Text(p, "order_kind") != "emission_complete" || runner.Number(p, "order_at_ms") != runner.Number(p, "transport_at_ms") {
				return fail("steer order is not complete-output order", p)
			}
			after := runner.Number(p, "transport_at_ms")
			if mode == "wait_turn" {
				after = runner.Number(p, "admitted_at_ms")
			}
			whole := CheckWholeInput(WholeInput{body, mode, after, in.Transcript, in.References})
			if whole.Status != "pass" {
				whole.SignalID = "packing-order"
				return whole
			}
			var seqs []int64
			for _, member := range members {
				if runner.Number(member, "prompt_index") == int64(index) {
					emitted[runner.Text(member, "message_id")]++
					seqs = append(seqs, runner.Number(messages[runner.Text(member, "message_id")], "accepted_seq"))
				}
			}
			intake, _ := intakeFromResult(whole)
			sequence = append(sequence, physicalEmission{h, p, mode, seqs, intake.Time.UnixMilli()})
			if index > 0 && runner.Text(prompts[index-1], "emission") == "issued" {
				prior := prompts[index-1]
				if runner.Number(p, "issued_at_ms") < runner.Number(prior, "order_at_ms") {
					return fail("successor preceded its predecessor's recorded order point", p)
				}
				if mode == "wait_turn" && runner.Text(h, "boundary") == "turn_end" {
					stopped := false
					// Stop witnesses are included in Hooks alongside UPS by the caller.
					for _, hook := range in.Hooks {
						if runner.Text(hook, "hook_event_name") == "Stop" && runner.Number(hook, "_received_ms") > runner.Number(prior, "admitted_at_ms") && runner.Number(hook, "_received_ms") <= runner.Number(p, "issued_at_ms") {
							stopped = true
						}
					}
					if !stopped {
						return missing("ordinary successor lacks the previous turn's independent Stop witness")
					}
				}
			}
		}
	}
	if r := checkLaunchSequence(in, sequence); r.Status != "pass" {
		return r
	}
	if issuedCount == 0 {
		return missing("no physical prompt emission")
	}
	for id, count := range emitted {
		if count != 1 {
			return fail("Message has more than one physical emission", map[string]any{"message": id, "emissions": count})
		}
	}
	return result("packing-order", "pass", "candidate minimal packing, whole input, accepted order and physical once-only gates agree", "all issued outputs", issuedCount, in.References...)
}

func (c Capture) packingInput(o OwnerEvidence) PackingEvidence {
	in := PackingEvidence{Owner: o.Owner, Snapshot: o.Snapshot, Transcript: o.Transcript, Trail: o.Trail, References: []string{o.SnapshotRef, o.TranscriptRef}}
	for _, h := range c.Hooks {
		if runner.Text(h.Payload, "session_id") != o.Owner.SessionID || runner.Text(h.Payload, "agent_id") != "" {
			continue
		}
		copy := runner.Row{}
		for k, v := range h.Payload {
			copy[k] = v
		}
		copy["_received_ms"] = h.Receipt.Received.UnixMilli()
		in.Hooks = append(in.Hooks, copy)
	}
	for _, s := range c.Snapshots {
		if s.InstanceID == o.Owner.InstanceID && len(s.Tables["spawns"]) == 1 && runner.Text(s.Tables["spawns"][0], "launch_token") == o.Owner.LaunchToken {
			in.History = append(in.History, s)
		}
	}
	return in
}
func requirePhysicalCase(c Capture, id, phase string) error {
	count := 0
	for _, charge := range c.After.Charges {
		if (charge.ID == id && (charge.SelectedCase == "" || charge.SelectedCase == id) || charge.SelectedCase == id) && charge.Phase == phase && charge.Attempt == c.After.Identity.Attempt && charge.Reconciled && charge.Actual == 1 {
			x, ok := c.caseByID(id)
			if !ok {
				return absent("physical case %s lacks its frozen corpus", id)
			}
			at := strings.LastIndex(charge.Prompt, "@")
			ref := ""
			if at >= 0 {
				ref = charge.Prompt[at+1:]
			}
			if at < 0 {
				return corrupt("physical case %s lacks its raw parent reference", id)
			}
			matched := false
			for _, fact := range c.Hooks {
				if fact.Reference != ref || fact.Receipt.Event != "UserPromptSubmit" || runner.Text(fact.Payload, "agent_id") != "" {
					continue
				}
				owner, owned := c.owner(x.InstanceID, charge.Launch)
				if !owned || owner.Owner.SessionID != runner.Text(fact.Payload, "session_id") {
					return corrupt("physical case %s is charged to another target/Launch", id)
				}
				want := x.Text
				got := runner.Text(fact.Payload, "prompt")
				if x.Mode == "typed" || x.Mode == "native" {
					got = signals.ParseTypedInput(got).Text
				}
				if x.Mode != "" && x.Mode != "typed" && x.Mode != "native" {
					p, ok := promptFor(owner.Snapshot, x.Text)
					if !ok {
						return absent("physical case %s lacks its issued membership", id)
					}
					body, err := PromptBody(owner.Snapshot, p)
					if err != nil {
						return err
					}
					want = Transform(body, "wait_turn")
				}
				stopRoute := false
				if x.Mode == "steer_turn" {
					for _, h := range owner.Snapshot.Tables["handovers"] {
						if p, ok := promptFor(owner.Snapshot, x.Text); ok && runner.Text(h, "handover_id") == runner.Text(p, "handover_id") && runner.Text(h, "boundary") == "turn_end" {
							stopRoute = true
						}
					}
				}
				if x.Mode != "" && got != want && !(stopRoute && stopPromptMatches(got, want)) {
					return corrupt("physical case %s charged another prompt body", id)
				}
				if x.Mode == "" {
					for _, prior := range c.Hooks {
						if prior.Receipt.Event == "UserPromptSubmit" && runner.Text(prior.Payload, "agent_id") == "" && runner.Text(prior.Payload, "session_id") == owner.Owner.SessionID && !prior.Receipt.Received.Before(owner.Owner.Started) && prior.Receipt.Received.Before(fact.Receipt.Received) {
							return corrupt("startup case %s was assigned a later parent start", id)
						}
					}
				}
				matched = true
			}
			if !matched {
				return corrupt("physical case %s has no matching owned parent fact", id)
			}
			count++
		}
	}
	if count != 1 {
		return errors.New(fmt.Sprintf("case %s needs exactly one owned physical start", id))
	}
	return nil
}

type physicalEmission struct {
	Handover, Prompt runner.Row
	Mode             string
	Sequences        []int64
	Intake           int64
}

func checkLaunchSequence(in PackingEvidence, items []physicalEmission) runner.Result {
	slices.SortStableFunc(items, func(a, b physicalEmission) int {
		aTime, bTime := runner.Number(a.Prompt, "issued_at_ms"), runner.Number(b.Prompt, "issued_at_ms")
		if aTime < bTime {
			return -1
		}
		if aTime > bTime {
			return 1
		}
		return 0
	})
	var lastSeq int64
	for i, item := range items {
		for _, seq := range item.Sequences {
			if seq <= lastSeq {
				return result("packing-order", "fail", "accepted order crossed between physical handovers", nil, item, in.References...)
			}
			lastSeq = seq
		}
		if i == 0 {
			continue
		}
		prior := items[i-1]
		issued := runner.Number(item.Prompt, "issued_at_ms")
		gate := runner.Number(prior.Prompt, "order_at_ms")
		if gate <= 0 || issued < gate {
			return result("packing-order", "fail", "successor handover preceded predecessor order", gate, item, in.References...)
		}
		if runner.Text(item.Handover, "boundary") == "turn_end" {
			stop := false
			for _, h := range in.Hooks {
				if runner.Text(h, "hook_event_name") == "Stop" && runner.Number(h, "_received_ms") >= prior.Intake && runner.Number(h, "_received_ms") <= issued {
					stop = true
				}
			}
			if !stop {
				return result("packing-order", "fail", "ordinary handover lacks predecessor turn progression across handovers", nil, item, in.References...)
			}
		}
	}
	return result("packing-order", "pass", "Launch-wide physical sequence preserves accepted order and predecessor gates", nil, len(items), in.References...)
}
