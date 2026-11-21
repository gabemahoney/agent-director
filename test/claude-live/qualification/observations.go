package qualification

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/test/claude-live/runner"
	"github.com/gabemahoney/agent-director/test/claude-live/signals"
)

type HookFact struct {
	Receipt   runner.Receipt `json:"receipt"`
	Payload   runner.Row     `json:"payload"`
	Reference string         `json:"reference"`
}

// HookFacts uses the shared recorder's validation. Child/background facts are
// retained separately; only uniquely bound parent UPS events count as starts.
func HookFacts(root, run string, events []string) ([]HookFact, error) {
	return hookFacts(root, run, events, true)
}

// LiveHookFacts skips invocations still being written. Finalization calls the
// strict variant after draining and retains every incomplete recording.
func LiveHookFacts(root, run string, events []string) ([]HookFact, error) {
	return hookFacts(root, run, events, false)
}

func hookFacts(root, run string, events []string, strict bool) ([]HookFact, error) {
	inv, err := runner.InspectRecordings(root, run, events)
	if err != nil {
		return nil, err
	}
	var out []HookFact
	for _, item := range inv {
		if !item.Complete {
			if !strict {
				continue
			}
			return nil, fmt.Errorf("incomplete recorder invocation: %s", item.Directory)
		}
		b, err := runner.Read(root, item.Directory+"/receipt.json")
		if err != nil {
			return nil, err
		}
		var rec runner.Receipt
		if err = json.Unmarshal(b, &rec); err != nil {
			return nil, err
		}
		b, err = runner.Read(root, item.Directory+"/input.json")
		if err != nil {
			return nil, err
		}
		var p runner.Row
		if err = json.Unmarshal(b, &p); err != nil {
			return nil, err
		}
		out = append(out, HookFact{rec, p, item.Directory + "/input.json"})
	}
	slices.SortFunc(out, func(a, b HookFact) int { return a.Receipt.Received.Compare(b.Receipt.Received) })
	return out, nil
}

func PromptBody(s runner.Snapshot, p runner.Row) (string, error) {
	var members []runner.Row
	for _, m := range s.Tables["members"] {
		if runner.Text(m, "handover_id") == runner.Text(p, "handover_id") && runner.Number(m, "prompt_index") == runner.Number(p, "prompt_index") {
			members = append(members, m)
		}
	}
	slices.SortFunc(members, func(a, b runner.Row) int {
		aa, bb := runner.Number(a, "member_index"), runner.Number(b, "member_index")
		if aa < bb {
			return -1
		}
		if aa > bb {
			return 1
		}
		return 0
	})
	var texts [][]byte
	for _, m := range members {
		found := false
		for _, msg := range s.Tables["messages"] {
			if runner.Text(msg, "message_id") == runner.Text(m, "message_id") {
				texts = append(texts, []byte(runner.Text(msg, "text")))
				found = true
				break
			}
		}
		if !found {
			return "", errors.New("prompt member text absent")
		}
	}
	if len(texts) == 0 {
		return "", errors.New("prompt has no recorded members")
	}
	return string(store.RenderBusySendPromptBody(texts)), nil
}

func Transform(body, mode string) string {
	return signals.RenderBody(body, mode)
}

func (e *Exercise) stop(ctx context.Context, iid, label string, pause bool) error {
	r := e.Run
	o, err := e.owner(iid)
	if err != nil {
		return err
	}
	if _, err = e.observe(ctx, iid, label+"-before"); err != nil {
		return err
	}
	o, err = e.owner(iid)
	if err != nil {
		return err
	}
	verb := "kill"
	if pause {
		verb = "pause"
	}
	if _, _, err = r.AD(ctx, label, verb, "--instance-id", iid); err != nil {
		return err
	}
	_, err = r.AwaitState(ctx, iid, 20*time.Second, true, func(s runner.Snapshot) bool {
		gone, er := runner.ProcessGone(o.PID, o.ProcessStart)
		return er == nil && gone
	})
	if err != nil {
		return err
	}
	if err = r.CollectFiles(o); err != nil {
		return err
	}
	for i := range r.Manifest.Owned {
		if r.Manifest.Owned[i].InstanceID == iid && r.Manifest.Owned[i].LaunchToken == o.LaunchToken {
			r.Manifest.Owned[i].Exited = true
			r.Manifest.Owned[i].StoppedAt = r.Now().UTC()
		}
	}
	s, err := e.observe(ctx, iid, label+"-after")
	if err == nil {
		err = r.Evidence.JSON("raw/final-"+r.OwnedPrefix(o)+"-store.json", s)
	}
	return err
}

func (e *Exercise) resume(ctx context.Context, iid, id string, reuse bool) error {
	o, err := e.owner(iid)
	if err != nil {
		return err
	}
	if !o.Exited {
		return errors.New("actual old process exit required")
	}
	r := e.Run
	r.Manifest.Owned = append(r.Manifest.Owned, runner.Owned{InstanceID: iid, CWD: o.CWD, Socket: r.Socket, Started: r.Now().UTC()})
	args := []string{"resume", "--instance-id", iid}
	if reuse {
		args = []string{"spawn", "--reuse-finished", "--agent", "claude", "--cwd", o.CWD, "--instance-id", iid, "--tmux-session-name", iid, "--", "--model", "claude-sonnet-5-5", "--effort", "medium"}
	}
	c, _, err := r.AD(ctx, id, args...)
	e.Observations = append(e.Observations, Observation{Case: id, Instance: iid, Kind: "resume", Command: c.Stdout, Started: c.Started, Ended: c.Ended})
	return err
}

func (e *Exercise) finalize(ctx context.Context) error {
	r := e.Run
	facts, err := HookFacts(r.Evidence.Root, r.Manifest.RunID, r.Manifest.RecordingHook.Events)
	if err != nil {
		return err
	}
	type start struct {
		fact  HookFact
		owner runner.Owned
		used  bool
	}
	var starts []start
	var activities []Activity
	agents := map[string]bool{}
	seen := map[string]bool{}
	for _, h := range facts {
		if agent := runner.Text(h.Payload, "agent_id"); agent != "" {
			if !agents[agent] {
				activity, er := e.collectActivity(h)
				if er != nil {
					r.Problem("activity-evidence", "not_completed", er)
				}
				activities = append(activities, activity)
				agents[agent] = true
			}
			continue
		}
		if h.Receipt.Event != "UserPromptSubmit" {
			continue
		}
		for _, o := range r.Manifest.Owned {
			if o.Agent != "" && o.Agent != "claude" {
				continue
			}
			if runner.Text(h.Payload, "session_id") != o.SessionID || h.Receipt.Received.Before(o.Started) || (!o.StoppedAt.IsZero() && h.Receipt.Received.After(o.StoppedAt)) {
				continue
			}
			pid := runner.Text(h.Payload, "prompt_id")
			if pid == "" {
				return errors.New("parent start lacks prompt_id")
			}
			key := o.LaunchToken + "/" + pid + "@" + h.Reference
			if seen[key] {
				return errors.New("duplicate parent start identity")
			}
			seen[key] = true
			starts = append(starts, start{fact: h, owner: o})
			break
		}
	}
	// The passive parent occurrences also cover the released pre-counter
	// runtime used by setup. Child/background callbacks remain uncharged.
	var observed int64
	for i, o := range r.Manifest.Owned {
		if o.Agent != "" && o.Agent != "claude" {
			continue
		}
		var count int64
		for _, st := range starts {
			if st.owner.LaunchToken == o.LaunchToken {
				count++
			}
		}
		r.Manifest.Owned[i].ObservedTurns = max(o.ObservedTurns, count)
		observed += r.Manifest.Owned[i].ObservedTurns
	}
	r.Manifest.ObservedTurns = max(r.Manifest.ObservedTurns, observed)
	if er := r.CheckTurnBudget(); er != nil {
		r.Problem("turn-accounting", "fail", er)
	}
	err = UpdateLedger(e.Ledger, func(l *Ledger) error {
		for i := range l.Charges {
			c := &l.Charges[i]
			if c.Attempt != l.Identity.Attempt || c.Phase != r.Manifest.Phase || c.Reconciled {
				continue
			}
			var candidates []runner.Case
			for _, x := range r.Manifest.Cases {
				if x.Text != "" && (x.ID == c.ID || slices.Contains(c.Alternatives, x.ID)) {
					candidates = append(candidates, x)
				}
			}
			for j := range starts {
				st := &starts[j]
				if st.used {
					continue
				}
				for _, x := range candidates {
					if x.InstanceID != st.owner.InstanceID || st.fact.Receipt.Received.Before(x.Started) {
						continue
					}
					want := x.Text
					got := runner.Text(st.fact.Payload, "prompt")
					stopRoute := false
					if x.Mode == "typed" || x.Mode == "native" {
						got = signals.ParseTypedInput(got).Text
					}
					if x.Mode != "typed" && x.Mode != "native" && x.Mode != "" {
						snap, er := e.finalSnapshot(st.owner)
						if er != nil {
							continue
						}
						p, ok := promptFor(snap, x.Text)
						if !ok {
							continue
						}
						body, er := PromptBody(snap, p)
						if er != nil {
							continue
						}
						if r.Manifest.Phase == "groups" && (c.ID == "O1" || c.ID == "O2") {
							// A partially admitted prefix can start physically, but
							// cannot inherit the planned whole-partition identity.
							// Leave it unmatched so its actual start is still charged.
							planned, ok := plannedGroupBody(x.Mode, x.ID)
							if !ok || body != planned {
								continue
							}
						}
						want = Transform(body, "wait_turn")
						for _, h := range snap.Tables["handovers"] {
							if runner.Text(h, "handover_id") == runner.Text(p, "handover_id") && runner.Text(h, "boundary") == "turn_end" {
								stopRoute = true
							}
						}
						if len(c.Alternatives) > 0 && x.ID != c.ID && !stopRoute {
							continue
						}
					}
					if got == want || (stopRoute && stopPromptMatches(got, want)) || (x.Mode == "" && !st.fact.Receipt.Received.Before(x.Started)) {
						st.used = true
						c.Launch = st.owner.LaunchToken
						c.Prompt = runner.Text(st.fact.Payload, "prompt_id") + "@" + st.fact.Reference
						c.Actual = 1
						c.Reconciled = true
						c.Evidence = []string{st.fact.Reference}
						if len(c.Alternatives) > 0 {
							c.SelectedCase = x.ID
						}
						break
					}
				}
				if c.Reconciled {
					break
				}
			}
			if !c.Reconciled {
				c.Actual = 0
				c.Reconciled = true
				c.Launch = "not-issued"
				c.Prompt = c.Attempt + "/" + c.ID
				c.Evidence = []string{"raw/qualification-observations.json"}
			}
		}
		for _, st := range starts {
			if st.used {
				continue
			}
			l.Charges = append(l.Charges, Charge{ID: "unexpected-" + runner.Text(st.fact.Payload, "prompt_id") + "-" + runner.Digest([]byte(st.fact.Reference))[:12], Phase: r.Manifest.Phase, Attempt: l.Identity.Attempt, Launch: st.owner.LaunchToken, Prompt: runner.Text(st.fact.Payload, "prompt_id") + "@" + st.fact.Reference, Actual: 1, Reconciled: true, Evidence: []string{st.fact.Reference}})
		}

		for _, x := range r.Manifest.Cases {
			if x.Mode == "steer_turn" {
				key := l.Identity.Attempt + "/" + x.ID
				if !slices.Contains(l.MidTurn, key) {
					l.MidTurn = append(l.MidTurn, key)
				}
			}
		}
		for _, h := range facts {
			if h.Receipt.Event == "PreCompact" {
				key := l.Identity.Attempt + "/" + h.Reference
				if !slices.Contains(l.Compactions, key) {
					l.Compactions = append(l.Compactions, key)
				}
			}
		}
		var retained []ImportRef
		if b, er := runner.Read(r.Evidence.Root, "raw/import-index.json"); er == nil {
			if er = json.Unmarshal(b, &retained); er != nil {
				return er
			}
		}
		for i := range l.Charges {
			charge := &l.Charges[i]
			for _, ref := range retained {
				if charge.Phase == ref.Phase {
					consumer := r.Manifest.Phase + "@" + r.Manifest.RunID + "/" + ref.ManifestSHA256
					if !slices.Contains(charge.Consumers, consumer) {
						charge.Consumers = append(charge.Consumers, consumer)
					}
				}
			}
		}
		l.Activities = append(l.Activities, activities...)
		for _, o := range r.Manifest.Owned {
			data, er := runner.Read(r.Evidence.Root, "raw/"+r.OwnedPrefix(o)+"-transcript.jsonl")
			if er != nil {
				continue
			}
			t, er := signals.ParseTranscript(data, o.SessionID)
			if er != nil {
				continue
			}
			for _, request := range t.Requests {
				if !slices.Contains(l.APIRequests, request.ID) {
					l.APIRequests = append(l.APIRequests, request.ID)
				}
			}
		}
		return nil
	})
	allExited := true
	for _, o := range r.Manifest.Owned {
		allExited = allExited && o.Exited
	}
	if !allExited {
		return errors.Join(err, errors.New("owned process still live; fixture registrations retained for inspection"))
	}
	if er := e.finishGateProcesses(ctx); er != nil {
		return errors.Join(err, er)
	}
	for _, ref := range e.fixturePaths {
		path, er := runner.Confined(r.Evidence.Root, ref)
		if er == nil {
			er = os.Remove(path)
		}
		err = errors.Join(err, er)
	}
	b, er := runner.Read(filepath.Dir(e.Ledger), filepath.Base(e.Ledger))
	if er == nil {
		er = r.Evidence.Put("raw/qualification-ledger-after.json", b)
	}
	return errors.Join(err, er)
}

func (e *Exercise) collectActivity(h HookFact) (Activity, error) {
	a := Activity{Origin: "subagent", Session: runner.Text(h.Payload, "session_id"), Agent: runner.Text(h.Payload, "agent_id"), Evidence: []string{h.Reference}}
	if background, _ := h.Payload["is_background"].(bool); background {
		a.Origin = "background"
	}
	for _, c := range a.Agent {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return a, errors.New("invalid child activity identity")
		}
	}
	var owner runner.Owned
	for _, o := range e.Run.Manifest.Owned {
		if o.SessionID == a.Session {
			owner = o
			break
		}
	}
	if owner.SessionID == "" {
		return a, errors.New("child activity has no owned parent session")
	}
	parentPath, err := runner.TranscriptPath(e.Run.Home, owner.CWD, owner.SessionID, owner.Transcript)
	if err != nil {
		return a, err
	}
	base := filepath.Dir(parentPath)
	expected := filepath.Join(base, owner.SessionID, "subagents", "agent-"+a.Agent+".jsonl")
	if provided := runner.Text(h.Payload, "agent_transcript_path"); provided != "" && provided != expected {
		return a, errors.New("foreign child transcript path")
	}
	rel, err := filepath.Rel(base, expected)
	if err != nil {
		return a, err
	}
	data, err := runner.Read(base, rel)
	if err != nil {
		return a, err
	}
	ref := "raw/activity-" + a.Agent + ".jsonl"
	if err = e.Run.Evidence.Put(ref, data); err != nil {
		return a, err
	}
	a.Evidence = append(a.Evidence, ref)
	scan := bufio.NewScanner(bytes.NewReader(data))
	scan.Buffer(make([]byte, 4096), runner.MaxArtifactBytes)
	for scan.Scan() {
		var row runner.Row
		if err = json.Unmarshal(scan.Bytes(), &row); err != nil {
			return a, err
		}
		if runner.Text(row, "type") == "api-request" {
			if id := runner.Text(row, "id"); id != "" && !slices.Contains(a.Requests, id) {
				a.Requests = append(a.Requests, id)
			}
		}
	}
	return a, scan.Err()
}

func readSnapshot(root, ref string) (runner.Snapshot, error) {
	var s runner.Snapshot
	b, err := runner.Read(root, ref)
	if err == nil {
		err = json.Unmarshal(b, &s)
	}
	return s, err
}

func timeFromMillis(n int64) time.Time {
	if n == 0 {
		return time.Time{}
	}
	return time.UnixMilli(n).UTC()
}

func (e *Exercise) conditionalPauseProbe(ctx context.Context, iid, id string) error {
	before, err := e.observe(ctx, iid, id+"-before")
	if err != nil {
		return err
	}
	command, _, callErr := e.Run.AD(ctx, id, "pause", "--instance-id", iid, "--only-if-waiting")
	after, err := e.observe(ctx, iid, id+"-after")
	if err != nil {
		return err
	}
	record := struct {
		Command       runner.Command `json:"command"`
		Before, After runner.Snapshot
		Error         string `json:"error,omitempty"`
	}{Command: command, Before: before, After: after}
	if callErr != nil {
		record.Error = callErr.Error()
	}
	if err = e.Run.Evidence.JSON("raw/"+id+".json", record); err != nil {
		return err
	}
	if callErr == nil {
		return errors.New("conditional pause unexpectedly succeeded in the held/working safety probe; stop for MI")
	}
	return nil
}

// Reconciliation also reads retained captures whose owner-qualified artifact
// names predate the current Run options. No wildcard or foreign Launch is used.
func (e *Exercise) finalSnapshot(o runner.Owned) (runner.Snapshot, error) {
	refs := []string{"raw/final-" + e.Run.OwnedPrefix(o) + "-store.json"}
	qualified := "raw/final-" + o.InstanceID + "-" + o.LaunchToken + "-store.json"
	if o.LaunchToken != "" && refs[0] != qualified {
		refs = append(refs, qualified)
	}
	var s runner.Snapshot
	var err error
	for _, ref := range refs {
		s, err = readSnapshot(e.Run.Evidence.Root, ref)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return s, err
		}
		if s.InstanceID != o.InstanceID || (len(s.Tables["spawns"]) > 0 && (len(s.Tables["spawns"]) != 1 || runner.Text(s.Tables["spawns"][0], "launch_token") != o.LaunchToken || runner.Text(s.Tables["spawns"][0], "session_id") != o.SessionID)) {
			return s, errors.New("final snapshot is not the exact owned Launch")
		}
		return s, nil
	}
	return s, err
}
