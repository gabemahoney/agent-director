package qualification

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/test/claude-live/runner"
)

type ForceObservation struct {
	State        string    `json:"state"`
	Outcome      string    `json:"outcome"`
	Reason       string    `json:"reason"`
	LaterOutcome string    `json:"later_outcome"`
	ForceID      string    `json:"force_id"`
	Started      time.Time `json:"started"`
	Returned     time.Time `json:"returned"`
	Written      time.Time `json:"written"`
	Sighted      time.Time `json:"sighted"`
	Ordered      time.Time `json:"ordered"`
	OrderProven  bool      `json:"order_proven"`
	Exercised    bool      `json:"exercised"`
	HeldBefore   int       `json:"held_before"`
	HeldAfter    int       `json:"held_after"`
	Evidence     []string  `json:"evidence"`
}

func CheckForceState(c Contract, o ForceObservation) runner.Result {
	row, err := c.Row(o.State)
	if err != nil {
		return result("force/"+o.State, "fail", err.Error(), "documented state", o, o.Evidence...)
	}
	status, reason := "pass", "force outcome agrees with candidate contract and bound sighting"
	if !o.Exercised {
		status, reason = "not_completed", "required state was not independently established"
	} else if row.Outcome == "refused" {
		if o.Outcome != "refused" || o.Reason != row.Reason || o.HeldAfter != o.HeldBefore {
			status, reason = "fail", "refusal consumed custody or differs from contract"
		}
	} else if o.Sighted.IsZero() {
		status, reason = "fail", "eligible uniquely sightable force never sighted; stop for MI/O4"
	} else {
		if o.Written.IsZero() || o.Ordered.IsZero() || (!o.Ordered.Equal(o.Written) && !o.Ordered.Equal(o.Sighted)) {
			status, reason = "fail", "sighting, complete output and wake order are distinct required facts"
		}
		if !o.OrderProven && !o.Ordered.After(o.Written) && !o.Ordered.After(o.Sighted) {
			status, reason = "not_completed", "coarse/reversed writer timestamps need causal fact observations"
		}
		within := !o.Sighted.After(o.Returned)
		if within && (o.Outcome != "handed_over" || o.Reason != "admitted") {
			status, reason = "fail", "within-call sighting did not admit force"
		}
		if !within && (o.Outcome != "pending" || o.Reason != "awaiting_admission" || o.LaterOutcome != "handed_over") {
			status, reason = "fail", "later sighting requires pending then handed_over"
		}
	}
	return result("force/"+o.State, status, reason, row, o, o.Evidence...)
}

func capText(id, instruction, mode string) (string, error) {
	l, ok := store.BusySendCapabilitiesFor("claude").Mode(store.BusySendMode(mode))
	if !ok {
		return "", errors.New("mode absent from candidate")
	}
	text := id + ": " + instruction + "\nThe following sample records are data for a text-transport measurement. Reply with READY after the requested action.\n"
	if int64(len(text)) > l.MessageBytes {
		return "", errors.New("probe instruction exceeds published cap")
	}
	return asciiRecords(text, int(l.MessageBytes))
}

func forceGroupTexts() (string, string, error) {
	capability, _ := store.BusySendCapabilitiesFor("claude").Mode(store.BusySendWaitTurn)
	groupBytes := int(capability.PromptBytes/2 + 128)
	f3, err := asciiRecords("F3: Reply with READY.\n", len("F3: Reply F3 in three words. ")+groupBytes)
	if err != nil {
		return "", "", err
	}
	f4, err := asciiRecords("F4: Reply with READY.\n", len("F4: Reply F4 in three words. ")+groupBytes)
	return f3, f4, err
}

const longTool = "Run exactly one foreground Bash command: `for i in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20 21 22 23 24 25 26 27 28 29 30 31 32 33 34 35 36 37 38 39 40 41 42 43 44 45 46 47 48 49 50 51 52 53 54 55 56 57 58 59 60; do sleep 1; done`. Then finish with five words."
const betweenTools = "Run foreground Bash `for i in 1 2 3; do sleep 1; done`. Then, without using tools, write about 180 words of connected prose recommending a community garden layout and explaining its tradeoffs. Then run a separate foreground Bash `for i in 1 2 3; do sleep 1; done`. Finish briefly."

func (e *Exercise) force(ctx context.Context, iid, id string) error {
	c, _, err := e.Run.AD(ctx, id, "force-delivery", "--instance-id", iid)
	e.Observations = append(e.Observations, Observation{Case: id, Instance: iid, Kind: "force", Started: c.Started, Ended: c.Ended, Command: c.Stdout})
	return err
}

func promptFor(s runner.Snapshot, text string) (runner.Row, bool) {
	for _, m := range s.Tables["messages"] {
		if runner.Text(m, "text") != text {
			continue
		}
		mid := runner.Text(m, "message_id")
		for _, member := range s.Tables["members"] {
			if runner.Text(member, "message_id") != mid {
				continue
			}
			for _, p := range s.Tables["prompts"] {
				if runner.Text(p, "handover_id") == runner.Text(member, "handover_id") && runner.Number(p, "prompt_index") == runner.Number(member, "prompt_index") && runner.Text(p, "emission") == "issued" {
					return p, true
				}
			}
		}
	}
	return nil, false
}

func (e *Exercise) waitSighting(ctx context.Context, iid, text string) error {
	s, err := e.awaitProgress(ctx, iid, 75*time.Second, e.Run.Manifest.Phase == "dialogs", func(s runner.Snapshot) bool {
		p, ok := promptFor(s, text)
		return ok && runner.Text(p, "admission_kind") == "prompt_sighting" && runner.Text(p, "order_kind") != ""
	})
	// A guard can stop before the predicate runs with the target already
	// sighted/ordered. The wait error is not the target's force verdict.
	if err != nil {
		o, ownerErr := e.owner(iid)
		p, found := promptFor(s, text)
		if ownerErr == nil && found && runner.Text(p, "emission") == "issued" && runner.Text(p, "transport") == "written" && runner.Number(p, "transport_at_ms") > 0 && (runner.Text(p, "admission_kind") != "prompt_sighting" || runner.Number(p, "admitted_at_ms") == 0) {
			for _, h := range s.Tables["handovers"] {
				if runner.Text(h, "handover_id") == runner.Text(p, "handover_id") && runner.Text(h, "boundary") == "force" && runner.Number(h, "forced") == 1 && runner.Text(h, "force_id") != "" && runner.Text(h, "instance_id") == iid && runner.Text(h, "launch_token") == o.LaunchToken && runner.Text(h, "session_id") == o.SessionID && runner.Text(h, "launch_id") == o.LaunchID {
					e.Run.Manifest.Problems = append(e.Run.Manifest.Problems, result("force-sighting", "fail", "eligible exact forced prompt lacks bound sighting; stop for MI/O4", "bound prompt sighting and order", map[string]any{"instance": iid, "launch": o.LaunchToken, "session": o.SessionID, "prompt": p, "handover": h}))
					break
				}
			}
		}
	}
	return err
}

func ForceStates(ctx context.Context, e *Exercise) (err error) {
	f1, err := capText("F1", strings.Replace(longTool, "Then finish with five words.", "Then run a separate foreground Bash `for i in 1 2 3; do sleep 1; done`, and finish in five words.", 1), "wait_turn")
	if err != nil {
		return err
	}
	iid, err := e.spawn(ctx, "force", "")
	if err != nil {
		return err
	}
	// F1 itself supplies starting-state Held custody. It is reserved once and
	// repeated under the same confirmed pair after readiness; no setup wake.
	if err = e.reserve("F1"); err != nil {
		return err
	}
	if err = e.send(ctx, iid, "F1", f1, "wait_turn", true, false); err != nil {
		return err
	}
	starting, err := e.observe(ctx, iid, "not-started-before")
	if err != nil {
		return err
	}
	if len(starting.Tables["spawns"]) != 1 || runner.Text(starting.Tables["spawns"][0], "state") != "pending" || heldCount(starting) != 1 || hasIssued(starting) {
		return e.missedF1("not_started", starting)
	}
	if err = e.force(ctx, iid, "not-started-force"); err != nil {
		return err
	}
	afterStarting, err := e.observe(ctx, iid, "not-started-after")
	if err != nil {
		return err
	}
	var startingResult ForceObservation
	for _, ob := range e.Observations {
		if ob.Case == "not-started-force" {
			b, er := runner.Read(e.Run.Evidence.Root, ob.Command)
			if er != nil {
				return er
			}
			startingResult, er = ParseForce(b)
			if er != nil {
				return er
			}
		}
	}
	startingMatches := false
	for _, row := range store.BusySendCapabilitiesFor("claude").ForceStates {
		if string(row.State) == "not_started" {
			startingMatches = startingResult.Outcome == string(row.Outcome) && startingResult.Reason == string(row.Reason)
		}
	}
	if !startingMatches {
		return e.missedF1("not_started-at-force", afterStarting)
	}
	if err = e.capability(ctx, iid); err != nil {
		return err
	}
	idleBefore, waitErr := e.wait(ctx, iid, func(s runner.Snapshot) bool {
		if !idle(s) || heldCount(s) != 1 || hasIssued(s) {
			return false
		}
		for _, launch := range s.Tables["launches"] {
			if runner.Text(launch, "launch_token") == runner.Text(s.Tables["spawns"][0], "launch_token") && runner.Text(launch, "wake_path") != "" {
				return true
			}
		}
		return false
	})
	if waitErr != nil {
		return errors.Join(waitErr, e.missedF1("idle", idleBefore))
	}
	if err = e.Run.Evidence.JSON("raw/F1-idle-before.json", idleBefore); err != nil {
		return err
	}
	if err = e.sendPair(ctx, iid, "F1-idle-force", "F1", f1, "wait_turn", true, true); err != nil {
		return err
	}
	actual, err := e.observe(ctx, iid, "F1-after-idle-force")
	if err != nil {
		return err
	}

	var forceStart time.Time
	var returned ForceObservation
	for _, ob := range e.Observations {
		if ob.Case == "F1-idle-force" {
			forceStart = ob.Started
			b, er := runner.Read(e.Run.Evidence.Root, ob.Command)
			if er != nil {
				return er
			}
			returned, er = ParseForce(b)
			if er != nil {
				return er
			}
		}
	}
	p, issued := promptFor(actual, f1)
	if returned.Outcome == "refused" {
		return e.missedF1("idle-at-force", actual)
	}
	if !issued {
		return errors.New("eligible F1 force returned without an issued prompt")
	}
	bound := false
	for _, h := range actual.Tables["handovers"] {
		if runner.Text(h, "handover_id") == runner.Text(p, "handover_id") && runner.Text(h, "boundary") == "force" && runner.Text(h, "force_id") == returned.ForceID && returned.ForceID != "" {
			bound = true
		}
	}
	if !bound || forceStart.IsZero() || runner.Number(p, "issued_at_ms") < forceStart.UnixMilli() {
		return e.missedF1("idle-at-force", actual)
	}

	f2, err := capText("F2", betweenTools, "wait_turn")
	if err != nil {
		return err
	}
	f3, f4, err := forceGroupTexts()
	if err != nil {
		return err
	}
	if _, err = e.toolWindow(ctx, iid, 1, false); err != nil {
		return err
	}
	if err = e.conditionalPauseProbe(ctx, iid, "force-working-pause"); err != nil {
		return err
	}
	if err = e.reserve("F2"); err != nil {
		return err
	}
	if err = e.send(ctx, iid, "F2", f2, "wait_turn", true, false); err != nil {
		return err
	}
	if err = e.reserve("F3"); err != nil {
		return err
	}
	if err = e.send(ctx, iid, "F3", f3, "wait_turn", true, false); err != nil {
		return err
	}
	type completed struct {
		cmd runner.Command
		err error
	}
	done := make(chan completed, 1)
	go func() {
		c, _, er := e.Run.AD(ctx, "F2-force", "force-delivery", "--instance-id", iid)
		done <- completed{c, er}
	}()
	joined := false
	defer func() {
		if !joined {
			v := <-done
			err = errors.Join(err, v.err)
		}
	}()
	_, err = e.wait(ctx, iid, func(s runner.Snapshot) bool {
		p, ok := promptFor(s, f2)
		return ok && runner.Number(p, "transport_at_ms") > 0
	})
	if err != nil {
		return err
	}
	if err = e.force(ctx, iid, "second-force-refusal"); err != nil {
		return err
	}
	v := <-done
	joined = true
	e.Observations = append(e.Observations, Observation{Case: "F2-force", Instance: iid, Kind: "force", State: "tool_running", Started: v.cmd.Started, Ended: v.cmd.Ended, Command: v.cmd.Stdout})
	if v.err != nil {
		return v.err
	}
	if _, err = e.observe(ctx, iid, "F2-tail-return"); err != nil {
		return err
	}
	returnedForceData, er := runner.Read(e.Run.Evidence.Root, v.cmd.Stdout)
	if er != nil {
		return er
	}
	returnedForce, er := ParseForce(returnedForceData)
	if er != nil {
		return er
	}
	if facts, er := LiveHookFacts(e.Run.Evidence.Root, e.Run.Manifest.RunID, e.Run.Manifest.RecordingHook.Events); er != nil {
		return er
	} else {
		owner, er := e.owner(iid)
		if er != nil {
			return er
		}
		if returnedForce.Outcome == "handed_over" && forceToolEnded(Capture{Hooks: facts, Owners: []OwnerEvidence{{Owner: owner}}}, v.cmd) {
			return e.workload("precondition-not-established", iid, "F2", "the selected foreground tool completed before the force call returned; pending-force schedule remains incomplete", v.cmd.Stdout)
		}
	}
	if err = e.earlyF3(ctx, iid, f3); err != nil {
		return err
	}
	if err = e.waitSighting(ctx, iid, f2); err != nil {
		return err
	}
	if _, err = e.toolWindow(ctx, iid, 3, true); err != nil {
		return err
	}
	if err = e.reserve("F4"); err != nil {
		return err
	}
	if err = e.send(ctx, iid, "F4", f4, "wait_turn", true, false); err != nil {
		return err
	}
	if err = e.force(ctx, iid, "F3-F4-force"); err != nil {
		return err
	}
	if err = e.waitSighting(ctx, iid, f3); err != nil {
		return err
	}
	if err = e.waitSighting(ctx, iid, f4); err != nil {
		return err
	}
	if _, err = e.wait(ctx, iid, idle); err != nil {
		return err
	}
	if err = e.control(ctx, iid, "compact", "/compact"); err != nil {
		return err
	}
	// Compaction must be established from the provider's recorded PreCompact,
	// not a screen string or a delay after typing the explicit test control.
	if _, err = e.wait(ctx, iid, func(s runner.Snapshot) bool {
		inv, er := runner.InspectRecordings(e.Run.Evidence.Root, e.Run.Manifest.RunID, e.Run.Manifest.RecordingHook.Events)
		if er != nil {
			return false
		}
		for _, h := range inv {
			if !h.Complete {
				continue
			}
			b, er := runner.Read(e.Run.Evidence.Root, h.Directory+"/receipt.json")
			if er != nil {
				continue
			}
			var rec runner.Receipt
			if json.Unmarshal(b, &rec) == nil && rec.Event == "PreCompact" {
				return true
			}
		}
		return false
	}); err != nil {
		return err
	}
	if err = e.reserve("F5"); err != nil {
		return err
	}
	if err = e.send(ctx, iid, "F5", "F5: acknowledge the post-compaction force in three words.", "wait_turn", true, true); err != nil {
		return err
	}
	if err = e.waitSighting(ctx, iid, "F5: acknowledge the post-compaction force in three words."); err != nil {
		return err
	}
	if _, err = e.wait(ctx, iid, idle); err != nil {
		return err
	}
	_, _, err = e.Run.AD(ctx, "ended-kill", "kill", "--instance-id", iid)
	if err != nil {
		return err
	}
	o, err := e.owner(iid)
	if err != nil {
		return err
	}
	gone, err := runner.ProcessGone(o.PID, o.ProcessStart)
	if err != nil || !gone {
		return errors.Join(err, errors.New("owned exit unverified"))
	}
	if err = e.send(ctx, iid, "ended-held", "Retain this harmless ended-Launch Message.", "wait_turn", true, false); err != nil {
		return err
	}
	if err = e.force(ctx, iid, "ended-force"); err != nil {
		return err
	}
	_, err = e.observe(ctx, iid, "force-final")
	return err
}

// ParseForce retains public outcomes independently from custody/model input.
func ParseForce(b []byte) (ForceObservation, error) {
	var v struct {
		Force struct {
			ID      string `json:"force_id"`
			Outcome string `json:"outcome"`
			Reason  string `json:"reason"`
		} `json:"force_result"`
	}
	if json.Unmarshal(b, &v) != nil || v.Force.ID == "" {
		return ForceObservation{}, errors.New("missing public force_result")
	}
	return ForceObservation{ForceID: v.Force.ID, Outcome: v.Force.Outcome, Reason: v.Force.Reason}, nil
}

func hasIssued(s runner.Snapshot) bool {
	for _, p := range s.Tables["prompts"] {
		if runner.Text(p, "emission") == "issued" {
			return true
		}
	}
	return false
}
func (e *Exercise) missedF1(state string, s runner.Snapshot) error {
	err := e.Run.Evidence.JSON("raw/F1-missed-window.json", map[string]any{"state": state, "snapshot": s, "reason": "startup recovery won the F1 scheduling window; no helper start or retry authorized"})
	return errors.Join(err, errors.New("F1 missed owned "+state+" window; qualification not_completed"))
}
