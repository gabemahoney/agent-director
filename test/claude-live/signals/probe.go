package signals

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gabemahoney/agent-director/test/claude-live/runner"
)

func replace(s, old, new string) string { return strings.ReplaceAll(s, old, new) }

func Probe(ctx context.Context, r *runner.Run) (err error) {
	r.Manifest.ExpectedChecks = append([]string(nil), CheckIDs...)
	r.Manifest.TurnBudget = min(r.Manifest.TurnBudget, 7)
	r.Manifest.ProbePlan = &runner.ProbePlan{Version: PlanVersion, PrimaryTurns: 4, ContinuationTurns: 2, ScheduledTurns: 6, AggregateCap: 7}
	if r.Manifest.TurnBudget < 6 {
		return runner.ErrTurnBudget
	}
	defer func() {
		// Bound every attempted observation before cleanup starts. The offline
		// analyzer independently derives progress from the retained transcript.
		for i := range r.Manifest.Cases {
			if !r.Manifest.Cases[i].Completed {
				r.Manifest.Cases[i].ObservedUntil = r.Now().UTC()
			}
		}
	}()
	if err := r.Preflight(ctx); err != nil {
		return err
	}
	if _, err := r.VerifyCandidate(ctx, true); err != nil {
		return err
	}
	// PreToolUse is needed only to schedule the concurrent exercise against a
	// real foreground tool. It is not used as output-completion evidence.
	events := []string{"SessionStart", "FileChanged", "PostToolBatch", "UserPromptSubmit", "Stop", "PreToolUse"}
	if err := r.StartRecorder(events); err != nil {
		return err
	}
	id := "live-" + r.Manifest.RunID + "-signals"
	work := filepath.Join(r.Evidence.Root, "work")
	if err := os.Mkdir(work, 0700); err != nil {
		return err
	}
	if err := reserveProbeTurn(r); err != nil {
		return err
	}
	r.Manifest.Owned = append(r.Manifest.Owned, runner.Owned{InstanceID: id, CWD: work, Socket: r.Socket, Started: r.Now().UTC()})
	cmd, _, err := r.AD(ctx, "spawn", "spawn", "--agent", "claude", "--cwd", work, "--instance-id", id, "--tmux-session-name", id, "--relay-mode", "on", "--send-mode", "wait_turn", "--confirmed-delivery", "--", "--model", "claude-sonnet-5-5", "--effort", "medium", "--allowedTools", "Bash", "--append-system-prompt", "You are a terse test agent. Follow the requested harmless probes literally. Never use subagents or background tools. No tools except the exact requested foreground Bash loops. Reply with at most ten words except when the launch prompt explicitly requests a longer text-only finish. Do not read other files.", StartupOrder)
	r.Manifest.Cases = append(r.Manifest.Cases, runner.Case{ID: "startup", Text: StartupOrder, InstanceID: id, Started: cmd.Started, Command: cmd.Stdout, Ready: err == nil})
	if err != nil {
		return err
	}
	// A launch prompt has no earlier AD wake barrier blocking this force.
	if err = waitTool(ctx, r, 0, 1); err != nil {
		return err
	}
	if err = waitTextFinish(ctx, r, 0); err != nil {
		return err
	}
	// A forced wake is reserved as a full turn even if Claude takes it inside
	// the current turn. The phase is capped at seven turns, with no retry.
	if err = reserveProbeTurn(r); err != nil {
		return err
	}
	if err = send(ctx, r, id, "overlap-wake", WakeOrder, "wait_turn"); err != nil {
		return err
	}
	force := startForce(ctx, r, id)
	defer func() { err = errors.Join(err, force.wait()) }()
	// Wait for actual complete wake output, not a command start or recorder callback.
	pending, observeErr := waitForcedIssue(ctx, r, 0, WakeOrder, force)
	if observeErr == nil {
		if err = r.Evidence.JSON("raw/pending-wake-store.json", pending); err == nil {
			err = send(ctx, r, id, "overlap-steer", OverlapSteer, "steer_turn")
		}
	} else {
		err = observeErr
	}
	if err != nil {
		return err
	}
	alias := r.Manifest.Cases[len(r.Manifest.Cases)-1]
	alias.ID = "steer-turn-end"
	r.Manifest.Cases = append(r.Manifest.Cases, alias)
	// Queue Plain during the SAME Held interval. Its Wait Turn mode prevents
	// it from batching with STEER at the wake's post-sighting tool boundary.
	if err = reserveProbeTurn(r); err != nil {
		return err
	}
	if err = send(ctx, r, id, "idle-wake", Plain, "wait_turn"); err != nil {
		return err
	}
	if err = recordHeld(ctx, r, 0, "raw/overlap-held-store.json", WakeOrder, OverlapSteer, Plain); err != nil {
		return err
	}
	if err = waitOrderHistory(ctx, r, 0, Plain); err != nil {
		return err
	}
	if err = force.wait(); err != nil {
		return err
	}
	if err = waitExercise(ctx, r, "steer-turn-end"); err != nil {
		return err
	}
	if err = waitExercise(ctx, r, "overlap-wake"); err != nil {
		return err
	}
	// Ordinary subsequent wakes require every earlier prompt to be admitted.
	_, err = r.Await(ctx, id, 5*time.Second, func(s runner.Snapshot) bool { return noUnresolved(s) })
	if err != nil {
		return err
	}
	if err = r.Collect(ctx, id, "overlap-complete"); err != nil {
		return err
	}
	if err = waitExercise(ctx, r, "idle-wake"); err != nil {
		return err
	}
	if err = r.Collect(ctx, id, "idle-wake"); err != nil {
		return err
	}
	// Reserved tags intentionally yield no sighting and leave the wake's
	// awaiting gate set until Launch End. No ordinary wake follows this case.
	if err = sendTurn(ctx, r, id, "reserved-tags", Reserved); err != nil {
		return err
	}
	if err = r.Collect(ctx, id, "exercise-complete"); err != nil {
		return err
	}
	return probeContinuation(ctx, r)
}

func noUnresolved(s runner.Snapshot) bool {
	for _, p := range s.Tables["prompts"] {
		if runner.Text(p, "emission") == "issued" && (runner.Text(p, "admission_kind") == "" || runner.Text(p, "order_kind") == "") {
			return false
		}
	}
	return true
}

func send(ctx context.Context, r *runner.Run, id, name, text, mode string) error {
	if err := r.CheckTurnBudget(); err != nil {
		return err
	}
	c := runner.Case{ID: name, Text: text, Mode: mode, RequestID: name, InstanceID: id, Started: r.Now().UTC(), Ready: true}
	cmd, _, err := r.AD(ctx, name, "send-keys", "--instance-id", id, "--text", text, "--send-mode", mode, "--confirmed-delivery", "--caller-uuid", r.Manifest.RunID, "--delivery-request-id", name)
	c.Command = cmd.Stdout
	c.Ended = r.Now().UTC()
	if err != nil {
		c.Reason = err.Error()
	}
	r.Manifest.Cases = append(r.Manifest.Cases, c)
	if name == "idle-wake" {
		c.ID = "plain-sighting"
		r.Manifest.Cases = append(r.Manifest.Cases, c)
	}
	return err
}

func sendTurn(ctx context.Context, r *runner.Run, id, name, text string) error {
	if err := reserveProbeTurn(r); err != nil {
		return err
	}
	if err := send(ctx, r, id, name, text, "wait_turn"); err != nil {
		return err
	}
	if err := waitExercise(ctx, r, name); err != nil {
		return err
	}
	return r.Collect(ctx, id, name)
}

type forceCall struct {
	done   <-chan error
	result error
}

func startForce(ctx context.Context, r *runner.Run, id string) *forceCall {
	done := make(chan error, 1)
	go func() {
		_, b, err := r.AD(ctx, "overlap-force", "force-delivery", "--instance-id", id)
		if err == nil {
			var result struct {
				Force struct {
					Outcome string `json:"outcome"`
					Reason  string `json:"reason"`
				} `json:"force_result"`
			}
			if json.Unmarshal(b, &result) != nil {
				err = errors.New("invalid force outcome")
			} else {
				switch result.Force.Outcome {
				case "pending", "handed_over":
				default:
					err = fmt.Errorf("force %s: %s", result.Force.Outcome, result.Force.Reason)
				}
			}
		}
		done <- err
	}()
	return &forceCall{done: done}
}

func (f *forceCall) check() error {
	select {
	case f.result = <-f.done:
		f.done = nil
	default:
	}
	return f.result
}
func (f *forceCall) wait() error {
	if f.done != nil {
		f.result = <-f.done
		f.done = nil
	}
	return f.result
}

func waitForcedIssue(ctx context.Context, r *runner.Run, owner int, text string, force *forceCall) (runner.Snapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	for {
		if err := force.check(); err != nil {
			return runner.Snapshot{}, err
		}
		s, err := r.Observe(ctx, r.Manifest.Owned[owner].InstanceID)
		if err != nil {
			return s, err
		}
		if err = bindProbe(ctx, r, s); err != nil {
			return s, err
		}
		m := r.Manifest
		m.Owned = []runner.Owned{r.Manifest.Owned[owner]}
		if p, _, err := (capture{m: m, s: s}).prompt(text); err == nil && runner.Number(p, "transport_at_ms") > 0 {
			if runner.Text(p, "admission_kind") != "" || runner.Text(p, "order_kind") != "" {
				return s, errors.New("forced wake already sighted or ordered; overlap window missed, no retry")
			}
			return s, nil
		}
		if force.done == nil {
			return s, errors.New("force ended without a completely written wake")
		}
		if err = r.Sleep(ctx, 200*time.Millisecond); err != nil {
			return s, err
		}
	}
}

func waitTool(ctx context.Context, r *runner.Run, owner, want int) error {
	ctx, c := context.WithTimeout(ctx, 100*time.Second)
	defer c()
	for {
		entries, err := os.ReadDir(filepath.Join(r.Evidence.Root, "raw/hooks"))
		if err != nil {
			return err
		}
		ids := map[string]bool{}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			base := "raw/hooks/" + entry.Name() + "/"
			b, err := runner.Read(r.Evidence.Root, base+"receipt.json")
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				return err
			}
			var rec runner.Receipt
			if json.Unmarshal(b, &rec) != nil || !rec.Complete || rec.Event != "PreToolUse" {
				continue
			}
			b, err = runner.Read(r.Evidence.Root, base+"input.json")
			if err != nil {
				return err
			}
			var p runner.Row
			if json.Unmarshal(b, &p) != nil {
				return errors.New("invalid recorded tool payload")
			}
			if runner.Text(p, "session_id") == r.Manifest.Owned[owner].SessionID && runner.Text(p, "tool_name") == "Bash" && runner.Text(p, "agent_id") == "" {
				if id := runner.Text(p, "tool_use_id"); id != "" {
					ids[id] = true
				}
			}
		}
		if len(ids) >= want {
			return nil
		}
		s, err := r.Observe(ctx, r.Manifest.Owned[owner].InstanceID)
		if err != nil {
			return err
		}
		if err = bindProbe(ctx, r, s); err != nil {
			return err
		}
		if len(s.Tables["spawns"]) == 1 {
			state := runner.Text(s.Tables["spawns"][0], "state")
			if state == "check_permission" || state == "ask_user" {
				return errors.New("unexpected dialog during tool probe; intelligence required")
			}
		}
		if err = r.Sleep(ctx, 200*time.Millisecond); err != nil {
			return err
		}
	}
}

// Wait only for the initial tool result's next model request, while it is
// still running. A too-fast finish ends the exercise without another turn.
func waitTextFinish(ctx context.Context, r *runner.Run, owner int) error {
	ctx, cancel := context.WithTimeout(ctx, ExerciseWait)
	defer cancel()
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
			return errors.New("unexpected dialog before text-only window")
		}
		b, err := r.ReadTranscript(r.Manifest.Owned[owner])
		if err == nil {
			t, parseErr := ParseTranscript(b, r.Manifest.Owned[owner].SessionID)
			if parseErr == nil && len(t.Requests) >= 2 {
				if len(t.Requests) != 2 {
					return errors.New("unexpected extra request before text-only window")
				}
				req, tool := t.Requests[1], false
				for _, row := range t.Rows {
					if row.Line < req.Line && hasBlock(row, "tool_result") {
						tool = true
					}
					if row.Type == "assistant" && row.RequestRef == req.ID && (row.Message.StopReason != "" || hasBlock(row, "tool_use")) {
						return errors.New("text-only window already ended; no automatic retry")
					}
				}
				if tool && state == "working" {
					return r.Evidence.JSON(fmt.Sprintf("raw/text-finish-window-%d.json", owner), map[string]any{"request_line": req.Line, "request_id": req.ID, "request_time": req.Time, "observation": s})
				}
			}
		}
		if err = r.Sleep(ctx, 100*time.Millisecond); err != nil {
			return err
		}
	}
}

// Reserve against both the plan and already observed provider turns. At the
// observed cap, no further explicit input is authorized by this probe.
func reserveProbeTurn(r *runner.Run) error {
	r.Manifest.TurnBudget = min(r.Manifest.TurnBudget, 7)
	if r.Manifest.ObservedTurns >= int64(r.Manifest.TurnBudget) || r.Manifest.TurnsReserved >= 6 {
		return runner.ErrTurnBudget
	}
	return r.ReserveTurn()
}
