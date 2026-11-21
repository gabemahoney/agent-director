package runner_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/test/claude-live/internal/qualificationfix"
	q "github.com/gabemahoney/agent-director/test/claude-live/qualification"
	"github.com/gabemahoney/agent-director/test/claude-live/runner"
	"github.com/gabemahoney/agent-director/test/claude-live/signals"
)

// Complete synthetic producers for the replay's import boundary. The shared
// adapter still owns copying and sealing; its partial unit-test defaults stay
// intact. These files model evidence shapes, never live provider qualification.
type prerequisiteReplay struct {
	s            *setupReplay
	f            *qualificationfix.Fixture
	l            q.Ledger
	base         time.Time
	observations []q.Observation
	commands     []runner.Command
	owners       []*prerequisiteOwner
	serial       int
}
type prerequisiteOwner struct {
	o        runner.Owned
	snapshot runner.Snapshot
	journal  bytes.Buffer
	seq      int
}

func (b *prerequisiteReplay) at(ms int64) time.Time {
	return b.base.Add(time.Duration(ms) * time.Millisecond)
}

func (s *setupReplay) completePrerequisite(f *qualificationfix.Fixture, ledger q.Ledger) {
	s.t.Helper()
	// Discard the original probe's semantic evidence, retaining its installed
	// candidate/provenance records through the one shared adapter.
	if err := os.RemoveAll(f.Path("raw/hooks")); err != nil {
		s.t.Fatal(err)
	}
	for ref := range f.Manifest.Artifacts {
		if strings.HasPrefix(ref, "raw/hooks/") {
			delete(f.Manifest.Artifacts, ref)
		}
	}
	f.Manifest.Owned, f.Manifest.Cases, f.Manifest.Problems = nil, nil, nil
	f.Manifest.RecordingHook.Events = append(f.Manifest.RecordingHook.Events, "PreCompact", "PostCompact")
	f.Manifest.ObservedTurns, f.Manifest.TurnsReserved = 0, 0
	b := &prerequisiteReplay{s: s, f: f, l: ledger, base: f.Manifest.Finished.Add(time.Second)}
	for _, row := range f.Rows("commands.jsonl") {
		data, _ := json.Marshal(row)
		var cmd runner.Command
		if err := json.Unmarshal(data, &cmd); err != nil {
			s.t.Fatal(err)
		}
		b.commands = append(b.commands, cmd)
	}
	f.PutBytes("raw/caller-contract.md", replayRead(s.t, "../../../docs/caller-contract.md"))
	f.Put("raw/capabilities.json", s.capabilities)
	switch f.Manifest.Phase {
	case "force-states":
		b.forces()
	case "typed-controls":
		b.typed()
	case "groups":
		b.groups()
	default:
		s.t.Fatalf("no complete replay prerequisite for %s", f.Manifest.Phase)
	}
	for _, p := range b.owners {
		prefix := p.o.InstanceID + "-" + p.o.LaunchToken
		f.Manifest.Owned = append(f.Manifest.Owned, p.o)
		f.Put("raw/final-"+prefix+"-store.json", p.snapshot)
		f.PutBytes("raw/"+prefix+"-transcript.jsonl", p.journal.Bytes())
		trail, _ := json.Marshal(runner.Row{"instance_id": p.o.InstanceID, "event": "ad.hook.fired", "event_name": "UserPromptSubmit", "session_id": p.o.SessionID, "upsert_outcome": "updated"})
		f.PutBytes("raw/"+prefix+"-trail.jsonl", append(trail, '\n'))
	}
	f.Manifest.Finished = b.at(2000)
	f.Manifest.ObservedTurns = int64(len(b.l.Charges))
	f.Manifest.TurnsReserved = len(b.l.Charges)
	f.Put("raw/qualification-ledger-before.json", ledger)
	f.Put("raw/qualification-ledger-after.json", b.l)
	f.Put("raw/qualification-observations.json", b.observations)
	var commands bytes.Buffer
	for _, cmd := range b.commands {
		if err := json.NewEncoder(&commands).Encode(cmd); err != nil {
			s.t.Fatal(err)
		}
	}
	f.PutBytes("commands.jsonl", commands.Bytes())
	inv, err := runner.InspectRecordings(f.Root, f.Manifest.RunID, f.Manifest.RecordingHook.Events)
	if err != nil {
		s.t.Fatal(err)
	}
	f.Put("raw/hook-inventory.json", inv)
}

func (b *prerequisiteReplay) owner(name string, native bool) *prerequisiteOwner {
	o := runner.Owned{InstanceID: "live-" + b.f.Manifest.RunID + "-synthetic-" + name, SessionID: "synthetic-" + name, LaunchID: "launch-" + name, LaunchToken: "token-" + name, CWD: "/synthetic/" + name, Transcript: "/synthetic/" + name + "/journal", PID: 1, ProcessStart: "synthetic-process", Native: native, Started: b.at(0), StoppedAt: b.at(1500), Exited: true}
	p := &prerequisiteOwner{o: o, snapshot: runner.Snapshot{InstanceID: o.InstanceID, Observed: b.at(1400), ReadCompleted: b.at(1401), Tables: map[string][]runner.Row{
		"spawns":   {{"session_id": o.SessionID, "launch_token": o.LaunchToken, "state": "waiting"}},
		"launches": {{"launch_id": o.LaunchID, "launch_token": o.LaunchToken, "provider": "claude", "ended_at_ms": b.at(1500).UnixMilli()}},
	}}}
	b.owners = append(b.owners, p)
	return p
}
func (b *prerequisiteReplay) hook(p *prerequisiteOwner, event string, ms int64, extra runner.Row) string {
	b.serial++
	dir := fmt.Sprintf("raw/hooks/invocation-synthetic-%04d", b.serial)
	payload := runner.Row{"session_id": p.o.SessionID, "cwd": p.o.CWD, "hook_event_name": event, "transcript_path": p.o.Transcript}
	for k, v := range extra {
		payload[k] = v
	}
	b.f.Put(dir+"/input.json", payload)
	data := b.f.Read(dir + "/input.json")
	b.f.Put(dir+"/receipt.json", runner.Receipt{Schema: runner.Schema, RunID: b.f.Manifest.RunID, Event: event, Received: b.at(ms), Finished: b.at(ms).Add(time.Microsecond), Input: "input.json", SHA256: runner.Digest(data), Bytes: len(data), Complete: true})
	return dir + "/input.json"
}
func (b *prerequisiteReplay) request(p *prerequisiteOwner, role, text string, ms int64) {
	b.serial++
	message := runner.Row{"role": role, "content": []runner.Row{{"type": "text", "text": text}}}
	var canonical bytes.Buffer
	enc := json.NewEncoder(&canonical)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(message); err != nil {
		b.s.t.Fatal(err)
	}
	hash := runner.Digest(bytes.TrimSuffix(canonical.Bytes(), []byte("\n")))
	for _, row := range []runner.Row{
		{"type": "api-request-blob", "sessionId": p.o.SessionID, "hash": hash, "message": message},
		{"type": "api-request", "sessionId": p.o.SessionID, "id": fmt.Sprintf("synthetic-request-%d", b.serial), "timestamp": b.at(ms), "querySource": "repl_main_thread", "keep": 0, "tail": []string{hash}},
	} {
		if err := json.NewEncoder(&p.journal).Encode(row); err != nil {
			b.s.t.Fatal(err)
		}
	}
}
func (b *prerequisiteReplay) addCase(p *prerequisiteOwner, id, text, mode string, ms int64) {
	b.f.Manifest.Cases = append(b.f.Manifest.Cases, runner.Case{ID: id, InstanceID: p.o.InstanceID, Text: text, Mode: mode, Started: b.at(ms), Ended: b.at(ms + 1)})
}
func (b *prerequisiteReplay) charge(p *prerequisiteOwner, id, prompt string, ms int64) {
	pid := "synthetic-prompt-" + id
	ref := b.hook(p, "UserPromptSubmit", ms, runner.Row{"prompt_id": pid, "prompt": prompt})
	b.l.Charges = append(b.l.Charges, q.Charge{ID: id, Phase: b.f.Manifest.Phase, Attempt: b.l.Identity.Attempt, Launch: p.o.LaunchToken, Prompt: pid + "@" + ref, Reserved: 1, Actual: 1, Reconciled: true, Evidence: []string{ref}})
}
func (b *prerequisiteReplay) snapshot(p *prerequisiteOwner, id string, ms int64, state string, held []runner.Row) string {
	ref := "raw/synthetic-" + id + "-store.json"
	s := runner.Snapshot{InstanceID: p.o.InstanceID, Observed: b.at(ms), ReadCompleted: b.at(ms + 1), Tables: map[string][]runner.Row{
		"spawns": {{"state": state, "session_id": p.o.SessionID, "launch_token": p.o.LaunchToken}}, "launches": p.snapshot.Tables["launches"], "messages": held,
	}}
	b.f.Put(ref, s)
	b.observations = append(b.observations, q.Observation{Case: id, Kind: "snapshot", Instance: p.o.InstanceID, Snapshot: ref})
	return ref
}

// One candidate-partitioned handover with independently recorded whole inputs
// and physical UPS starts (steers deliberately add no parent starts).
func (b *prerequisiteReplay) handover(p *prerequisiteOwner, id, boundary string, ids, texts []string, ms int64) {
	mode := "wait_turn"
	if boundary == "tool_boundary" {
		mode = "steer_turn"
	}
	limits, _ := store.BusySendCapabilitiesFor("claude").Mode(store.BusySendMode(mode))
	var raw [][]byte
	for _, text := range texts {
		raw = append(raw, []byte(text))
	}
	parts, err := store.PartitionBusySendPrompts(raw, limits, nil)
	if err != nil {
		b.s.t.Fatal(err)
	}
	h := runner.Row{"handover_id": id, "instance_id": p.o.InstanceID, "launch_token": p.o.LaunchToken, "session_id": p.o.SessionID, "boundary": boundary}
	if boundary == "force" {
		h["force_id"] = id
		p.snapshot.Tables["forces"] = append(p.snapshot.Tables["forces"], runner.Row{"force_id": id, "handover_id": id, "instance_id": p.o.InstanceID, "launch_id": p.o.LaunchID, "outcome": "handed_over"})
	}
	p.snapshot.Tables["handovers"] = append(p.snapshot.Tables["handovers"], h)
	for i, text := range texts {
		part := 0
		for j, x := range parts {
			if i >= x.First {
				part = j
			}
		}
		p.seq++
		p.snapshot.Tables["messages"] = append(p.snapshot.Tables["messages"], runner.Row{"message_id": ids[i], "instance_id": p.o.InstanceID, "accepted_seq": p.seq, "text": text, "text_sha256": runner.Digest([]byte(text)), "place": "handover"})
		p.snapshot.Tables["members"] = append(p.snapshot.Tables["members"], runner.Row{"handover_id": id, "message_id": ids[i], "member_index": i, "prompt_index": part})
		b.addCase(p, ids[i], text, mode, ms-5)
	}
	for i, part := range parts {
		at := ms + int64(i)*30
		prompt := runner.Row{"handover_id": id, "prompt_index": i, "message_count": part.Count, "emission": "issued", "transport": "written", "issued_at_ms": b.at(at).UnixMilli(), "transport_at_ms": b.at(at + 1).UnixMilli(), "admission_kind": "prompt_sighting", "admitted_at_ms": b.at(at + 2).UnixMilli(), "order_kind": "ordered_acceptance", "order_at_ms": b.at(at + 2).UnixMilli()}
		body := string(store.RenderBusySendPromptBody(raw[part.First : part.First+part.Count]))
		role, input := "user", signals.NativeWake(q.Transform(body, "wait_turn"))
		if mode == "steer_turn" {
			prompt["order_kind"], prompt["order_at_ms"] = "emission_complete", b.at(at+1).UnixMilli()
			role, input = "system", signals.NativeContextBody(body)
		} else {
			b.charge(p, ids[part.First], q.Transform(body, "wait_turn"), at+2)
		}
		p.snapshot.Tables["prompts"] = append(p.snapshot.Tables["prompts"], prompt)
		b.request(p, role, input, at+3)
	}
}
func (b *prerequisiteReplay) force(p *prerequisiteOwner, name, id, outcome, reason string, start, end int64) {
	ref := "raw/synthetic-" + name + ".json"
	ended := b.at(end)
	b.f.Put(ref, runner.Row{"force_result": api.ForceResult{ForceID: id, Outcome: outcome, Reason: reason, StartedAt: b.at(start), EndedAt: &ended}})
	b.commands = append(b.commands, runner.Command{Name: name, Stdout: ref, Argv: []string{"ad", "force-delivery", "--instance-id", p.o.InstanceID}, Started: b.at(start), Ended: ended})
	if outcome == "refused" {
		p.snapshot.Tables["forces"] = append(p.snapshot.Tables["forces"], runner.Row{"force_id": id, "instance_id": p.o.InstanceID, "launch_id": p.o.LaunchID, "outcome": outcome, "reason": reason})
	}
}
func (b *prerequisiteReplay) forces() {
	p := b.owner("forces", false)
	limit, _ := store.BusySendCapabilitiesFor("claude").Mode(store.BusySendWaitTurn)
	f1, f2 := "F1"+strings.Repeat("a", int(limit.MessageBytes)-2), "F2"+strings.Repeat("b", int(limit.MessageBytes)-2)
	held := []runner.Row{{"message_id": "F1", "instance_id": p.o.InstanceID, "accepted_seq": 1, "text": f1, "text_sha256": runner.Digest([]byte(f1)), "place": "held"}}
	b.snapshot(p, "starting-before", 1, "pending", held)
	b.force(p, "not-started-force", "starting", "refused", "agent_starting", 3, 4)
	b.snapshot(p, "starting-after", 5, "pending", held)
	b.hook(p, "SessionStart", 6, nil)
	b.snapshot(p, "idle-before", 10, "waiting", held)
	b.handover(p, "idle-force", "force", []string{"F1"}, []string{f1}, 20)
	b.force(p, "F1-idle-force", "idle-force", "handed_over", "admitted", 15, 25)
	b.hook(p, "PreToolUse", 30, runner.Row{"tool_name": "Bash", "tool_use_id": "long"})
	b.handover(p, "busy-force", "force", []string{"F2"}, []string{f2}, 50)
	b.force(p, "F2-force", "busy-force", "pending", "awaiting_admission", 40, 45)
	b.force(p, "second-force-refusal", "second", "refused", "handover_in_progress", 46, 47)
	b.hook(p, "PostToolBatch", 60, nil)
	f3, f4 := strings.Repeat("c", int(limit.PromptBytes/2)+128), strings.Repeat("d", int(limit.PromptBytes/2)+128)
	b.handover(p, "group-force", "force", []string{"F3", "F4"}, []string{f3, f4}, 80)
	b.force(p, "F3-F4-force", "group-force", "handed_over", "admitted", 70, 115)
	b.hook(p, "PreToolUse", 120, runner.Row{"tool_name": "Bash", "tool_use_id": "next"})
	tail := []runner.Row{{"message_id": "F3", "instance_id": p.o.InstanceID, "text": f3, "place": "held"}}
	b.snapshot(p, "F2-tail-return", 65, "working", tail)
	b.hook(p, "PreCompact", 130, nil)
	b.handover(p, "compact-force", "force", []string{"F5"}, []string{"synthetic compact input"}, 150)
	b.force(p, "F5", "compact-force", "handed_over", "admitted", 140, 155)
	b.hook(p, "PostCompact", 160, nil)
	b.snapshot(p, "ended-before", 1600, "ended", tail)
	b.force(p, "ended-force", "ended", "refused", "launch_ended", 1610, 1611)
	b.snapshot(p, "ended-after", 1620, "ended", tail)
}
func (b *prerequisiteReplay) typed() {
	ad, control := b.owner("typed-ad", false), b.owner("typed-control", true)
	for _, p := range []*prerequisiteOwner{ad, control} {
		id := "typed-ad-start"
		if p == control {
			id = "typed-control-start"
		}
		b.addCase(p, id, "synthetic startup", "", 2)
		b.charge(p, id, "synthetic startup", 3)
	}
	limit, _ := store.BusySendCapabilitiesFor("claude").Mode(store.BusySendSteerTurn)
	for i, state := range []string{"long-tool", "between-tools", "idle"} {
		ms := int64(100 + i*100)
		text := "synthetic " + state
		if i == 0 {
			text += strings.Repeat("a", int(limit.MessageBytes)-len(text))
		}
		for _, p := range []*prerequisiteOwner{ad, control} {
			kind, suffix, mode := "send", "ad-input", "steer_turn"
			if p == control {
				kind, suffix, mode = "typed-control", "typed-input", "typed"
			}
			id := "typed-" + state + "-" + suffix
			before := b.snapshot(p, id+"-before", ms-5, "waiting", nil)
			var trigger string
			if state == "idle" {
				var snap runner.Snapshot
				b.f.Decode(before, &snap)
				trigger = "raw/synthetic-" + id + "-trigger.json"
				b.f.Put(trigger, runner.Row{"snapshot": snap})
			} else {
				event := "PreToolUse"
				if state == "between-tools" {
					event = "PostToolBatch"
				}
				trigger = b.hook(p, event, ms-3, runner.Row{"tool_name": "Bash"})
				if state == "between-tools" {
					b.hook(p, "PreToolUse", ms+3, runner.Row{"tool_name": "Bash"})
				}
				b.hook(p, "PostToolBatch", ms+5, nil)
			}
			b.observations = append(b.observations, q.Observation{Case: id, Instance: p.o.InstanceID, Kind: kind, State: state, Trigger: trigger, Started: b.at(ms), Ended: b.at(ms + 1)})
			if p == control {
				b.addCase(p, id, text, mode, ms)
				b.charge(p, id, text, ms+2)
				b.request(p, "user", text, ms+8)
			} else {
				boundary := "tool_boundary"
				if state == "idle" {
					boundary = "turn_end"
				}
				b.handover(p, id, boundary, []string{id}, []string{text}, ms+6)
				// Injection precedes its eligible boundary, independently of output.
				b.f.Manifest.Cases[len(b.f.Manifest.Cases)-1].Started = b.at(ms)
				b.f.Manifest.Cases[len(b.f.Manifest.Cases)-1].Ended = b.at(ms + 1)
			}
		}
	}
}
func (b *prerequisiteReplay) groups() {
	p := b.owner("groups", false)
	b.addCase(p, "G0", "synthetic group startup", "", 2)
	b.charge(p, "G0", "synthetic group startup", 3)
	var plans []q.GroupPlan
	for _, mode := range []string{"steer_turn", "wait_turn"} {
		plan, err := q.PlanGroup(mode)
		if err != nil {
			b.s.t.Fatal(err)
		}
		plans = append(plans, plan)
	}
	b.hook(p, "PostToolBatch", 95, nil)
	b.hook(p, "PostToolBatch", 125, nil)
	b.handover(p, "steer-group", "tool_boundary", plans[0].Cases, plans[0].Texts, 100)
	b.hook(p, "Stop", 150, nil)
	b.hook(p, "Stop", 180, nil)
	b.handover(p, "ordinary-group", "turn_end", plans[1].Cases, plans[1].Texts, 160)
	b.f.Put("raw/group-plans.json", plans)
	// The groups analyzer consumes the complete force producer as a retained,
	// independently sealed import, exactly as the real phase does.
	root := b.s.prerequisite("force-states", b.l)
	manifest := replayRead(b.s.t, filepath.Join(root, "manifest.json"))
	hash := runner.Digest(manifest)
	var m runner.Manifest
	replayDecode(b.s.t, filepath.Join(root, "manifest.json"), &m)
	rel := "raw/imports/" + hash
	for ref := range m.Artifacts {
		b.f.PutBytes(rel+"/"+ref, replayRead(b.s.t, filepath.Join(root, filepath.FromSlash(ref))))
	}
	b.f.PutBytes(rel+"/manifest.json", manifest)
	b.f.Put("raw/import-index.json", []q.ImportRef{{Relative: rel, ManifestSHA256: hash, Phase: m.Phase, Run: m.RunID, Candidate: m.Candidate, Provider: m.ProviderVersion, Kit: m.KitSHA256}})
}
