package signals

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/gabemahoney/agent-director/test/claude-live/runner"
)

// Progress is independent of the signal assertions: a model request followed
// by an end_turn response completes a wake exercise even without UPS/Stop.
// A steer additionally needs a real completed tool before that request.
type Progress struct {
	RequestLine  int
	ToolLine     int
	TerminalLine int
	Finished     time.Time
}

func ExerciseProgress(x runner.Case, s runner.Snapshot, t Transcript) Progress {
	var progress Progress
	if !x.Ready || x.ObservedUntil.IsZero() {
		return progress
	}
	anchor := x.Started
	if x.Mode != "steer_turn" && x.ID != "startup" {
		issued := issuedAt(s, x.Text)
		if issued == 0 {
			return progress
		}
		anchor = time.UnixMilli(issued)
	}
	if x.Mode == "steer_turn" {
		// This is the tool result in the transcript, not the PostToolBatch
		// callback being tested or a model acknowledgment of the steer.
		for _, row := range t.Rows {
			if row.Type != "user" || row.Timestamp.Before(anchor) || row.Timestamp.After(x.ObservedUntil) {
				continue
			}
			var blocks []Block
			if json.Unmarshal(row.Message.Content, &blocks) != nil {
				continue
			}
			for _, b := range blocks {
				if b.Type == "tool_result" {
					progress.ToolLine = row.Line
					anchor = row.Timestamp
					break
				}
			}
			if progress.ToolLine != 0 {
				break
			}
		}
		if progress.ToolLine == 0 {
			return progress
		}
	}
	for _, req := range t.Requests {
		if req.Time.Before(anchor) || req.Time.After(x.ObservedUntil) {
			continue
		}
		progress.RequestLine = req.Line
		for _, row := range t.Rows {
			if row.Line > req.Line && row.Type == "assistant" && row.Message.StopReason == "end_turn" && !row.Timestamp.Before(req.Time) && !row.Timestamp.After(x.ObservedUntil) {
				progress.TerminalLine, progress.Finished = row.Line, row.Timestamp
				return progress
			}
		}
		return progress
	}
	return progress
}

// issuedAt needs only the actual issuance relation. Wrapping, cardinality,
// order and admission are separate assertions, never completion prerequisites.
func issuedAt(s runner.Snapshot, text string) int64 {
	for _, m := range s.Tables["messages"] {
		if runner.Text(m, "text") != text {
			continue
		}
		for _, member := range s.Tables["members"] {
			if runner.Text(member, "message_id") != runner.Text(m, "message_id") {
				continue
			}
			for _, p := range s.Tables["prompts"] {
				if runner.Text(p, "handover_id") == runner.Text(member, "handover_id") && runner.Number(p, "prompt_index") == runner.Number(member, "prompt_index") && runner.Text(p, "emission") == "issued" {
					return runner.Number(p, "issued_at_ms")
				}
			}
		}
	}
	return 0
}

func observeProgress(ctx context.Context, r *runner.Run, owner int) (runner.Snapshot, error) {
	id := r.Manifest.Owned[owner].InstanceID
	s, err := r.Observe(ctx, id)
	if err != nil {
		return s, err
	}
	if err = bindProbe(ctx, r, s); err != nil {
		return s, err
	}
	b, err := r.ReadTranscript(r.Manifest.Owned[owner])
	if err != nil {
		return s, err
	}
	t, err := ParseTranscript(b, r.Manifest.Owned[owner].SessionID)
	if err != nil {
		return s, err
	}
	for i := range r.Manifest.Cases {
		x := &r.Manifest.Cases[i]
		if x.Completed || x.InstanceID != id {
			continue
		}
		x.ObservedUntil = r.Now().UTC()
		p := ExerciseProgress(*x, s, t)
		x.Progress = nil
		if p.RequestLine != 0 {
			x.Progress = append(x.Progress, fmt.Sprintf("request:%d", p.RequestLine))
		}
		if p.ToolLine != 0 {
			x.Progress = append(x.Progress, fmt.Sprintf("tool-result:%d", p.ToolLine))
		}
		if p.TerminalLine != 0 {
			x.Progress = append(x.Progress, fmt.Sprintf("end-turn:%d", p.TerminalLine))
			x.Completed = true
			x.Ended = p.Finished
		}
	}
	return s, nil
}

const ExerciseWait = 100 * time.Second

// An unexpected turn on the first Launch counts while the second is active.
// Bind retains each Launch's monotonic maximum and sums both, even if a later
// store snapshot reports a smaller count. Cleanup retains its separate guard.
func bindProbe(ctx context.Context, r *runner.Run, s runner.Snapshot) error {
	if err := r.Bind(s); err != nil {
		return err
	}
	for _, o := range r.Manifest.Owned {
		if o.InstanceID == s.InstanceID || o.Exited {
			continue
		}
		other, err := r.Observe(ctx, o.InstanceID)
		if err != nil {
			return err
		}
		if err = r.Bind(other); err != nil {
			return err
		}
	}
	return nil
}

// Keep intermediate AD facts, including unchanged custody after a sibling
// tool. This is a bounded observation of an in-flight exercise, never delivery.
func waitOrderHistory(ctx context.Context, r *runner.Run, owner int, lastText string) error {
	ctx, cancel := context.WithTimeout(ctx, ExerciseWait)
	defer cancel()
	var previous []byte
	index := 0
	for {
		s, err := r.Observe(ctx, r.Manifest.Owned[owner].InstanceID)
		if err != nil {
			return err
		}
		if err = bindProbe(ctx, r, s); err != nil {
			return err
		}
		state := runner.Text(s.Tables["spawns"][0], "state")
		if state == "check_permission" || state == "ask_user" {
			return errors.New("unexpected dialog during order observation")
		}
		// Retain every sample: the time of an unchanged Held snapshot after a
		// completed tool is itself necessary evidence for probe step 7.
		current, err := json.Marshal(s.Tables)
		if err != nil {
			return err
		}
		if !bytes.Equal(current, previous) || owner == 1 {
			index++
			if err = r.Evidence.JSON(fmt.Sprintf("raw/order-observations-%d/%04d.json", owner, index), s); err != nil {
				return err
			}
			previous = current
		}
		m := r.Manifest
		m.Owned = []runner.Owned{r.Manifest.Owned[owner]}
		if p, _, e := (capture{m: m, s: s}).prompt(lastText); e == nil && runner.Text(p, "admission_kind") != "" && runner.Text(p, "order_kind") != "" {
			return nil
		}
		if err = r.Sleep(ctx, 200*time.Millisecond); err != nil {
			return err
		}
	}
}

func recordHeld(ctx context.Context, r *runner.Run, owner int, ref, wakeText string, later ...string) error {
	s, err := r.Observe(ctx, r.Manifest.Owned[owner].InstanceID)
	if err != nil {
		return err
	}
	if err = bindProbe(ctx, r, s); err != nil {
		return err
	}
	if err = r.Evidence.JSON(ref, s); err != nil {
		return err
	}
	m := r.Manifest
	m.Owned = []runner.Owned{r.Manifest.Owned[owner]}
	c := capture{m: m, s: s}
	wake, _, err := c.prompt(wakeText)
	if err != nil || runner.Text(wake, "admission_kind") != "" || runner.Text(wake, "order_kind") != "" {
		return errors.New("wake no longer pending at Held overlap; no retry")
	}
	for _, text := range later {
		message, err := c.message(text)
		if err != nil || runner.Text(message, "place") != "held" {
			return errors.New("later Message not Held in intended overlap; no retry")
		}
	}
	return nil
}

func waitExercise(ctx context.Context, r *runner.Run, caseID string) error {
	ctx, cancel := context.WithTimeout(ctx, ExerciseWait)
	defer cancel()
	owner := 0
	for _, x := range r.Manifest.Cases {
		if x.ID == caseID {
			for i, o := range r.Manifest.Owned {
				if o.InstanceID == x.InstanceID {
					owner = i
				}
			}
		}
	}
	var observationErr error
	for {
		s, err := observeProgress(ctx, r, owner)
		if err == nil {
			state := runner.Text(s.Tables["spawns"][0], "state")
			if state == "check_permission" || state == "ask_user" {
				return errors.New("unexpected dialog; intelligence required")
			}
			for _, x := range r.Manifest.Cases {
				// Independent completion is retained even when a missing Stop
				// prevents readiness for the next exercise.
				if x.ID == caseID && x.Completed && state == "waiting" {
					return nil
				}
			}
		} else if errors.Is(err, runner.ErrTurnBudget) {
			return err
		}
		observationErr = err
		if err := r.Sleep(ctx, 200*time.Millisecond); err != nil {
			return fmt.Errorf("%s exercise observation (limit %s): %w", caseID, ExerciseWait, errors.Join(err, observationErr))
		}
	}
}

func (c capture) caseProgress(id string) Progress {
	x, ok := c.caseRecord(id)
	if !ok {
		return Progress{}
	}
	return ExerciseProgress(x, c.s, c.t)
}

func (c capture) caseRecord(id string) (runner.Case, bool) {
	for _, x := range c.m.Cases {
		if x.ID != id {
			continue
		}
		cmd, ok := c.commands[x.Command]
		if !ok || cmd.ExitCode != 0 || cmd.Timeout || cmd.SpawnError != "" {
			return runner.Case{}, false
		}
		if x.InstanceID != c.m.Owned[0].InstanceID || x.Started.After(cmd.Started) || x.ObservedUntil.Before(cmd.Ended) || x.ObservedUntil.After(c.m.Finished) {
			return runner.Case{}, false
		}
		if x.ID != "startup" {
			if cmd.Name != x.RequestID || !commandFlag(cmd.Argv, "--instance-id", x.InstanceID) || !commandFlag(cmd.Argv, "--text", x.Text) || !commandFlag(cmd.Argv, "--delivery-request-id", x.RequestID) {
				return runner.Case{}, false
			}
		}
		return x, true
	}
	return runner.Case{}, false
}

func commandFlag(argv []string, name, want string) bool {
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == name {
			return argv[i+1] == want
		}
	}
	return false
}
