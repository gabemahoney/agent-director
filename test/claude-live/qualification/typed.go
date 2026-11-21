package qualification

import (
	"context"
	"errors"
	"github.com/gabemahoney/agent-director/test/claude-live/signals"
	"strings"
	"time"

	"github.com/gabemahoney/agent-director/test/claude-live/runner"
)

// The matched controls use six parent starts at most: one AD trigger,
// one native trigger, two busy typed inputs, and the two idle inputs. Both
// AD busy steers share its trigger; the long-tool typed input supplies the
// control's later between-tools window instead of spending another trigger.
func TypedControls(ctx context.Context, e *Exercise) error {
	longText, err := capText("TYPED-long-tool", "Acknowledge briefly. Then run this between-tools sequence exactly once: "+betweenTools, "steer_turn")
	if err != nil {
		return err
	}
	betweenText, err := capText("TYPED-between-tools", "Acknowledge in five words and finish after the original remaining foreground tool.", "steer_turn")
	if err != nil {
		return err
	}
	idleText, err := capText("TYPED-idle", "Acknowledge in five words and finish; use no tools.", "steer_turn")
	if err != nil {
		return err
	}
	adPrompt := strings.Replace(longTool, "Then finish with five words.", "Then run the between-tools sequence requested by the next qualification context exactly once.", 1)
	ad, err := e.spawn(ctx, "typed-ad-start", adPrompt)
	if err != nil {
		return err
	}
	control, err := e.spawn(ctx, "typed-control-start", longTool)
	if err != nil {
		return err
	}
	for _, iid := range []string{ad, control} {
		if _, err = e.wait(ctx, iid, ready); err != nil {
			return err
		}
	}
	if err = e.capability(ctx, ad); err != nil {
		return err
	}
	if err = e.reserveSlot("typed-idle-ad-input", "typed-long-tool-ad-input", "typed-between-tools-ad-input", "typed-idle-ad-input"); err != nil {
		return err
	}
	for _, step := range []struct {
		state, text string
		tool        int
		between     bool
	}{{"long-tool", longText, 1, false}, {"between-tools", betweenText, 2, true}, {"idle", idleText, 0, false}} {
		for _, method := range []string{"ad", "typed"} {
			iid := ad
			if method == "typed" {
				iid = control
			}
			id := "typed-" + step.state + "-" + method + "-input"
			trigger := ""
			if step.tool > 0 {
				err = e.waitForCase(ctx, iid, id, step.state, func() error {
					var er error
					trigger, er = e.toolWindow(ctx, iid, step.tool, step.between)
					return er
				})
				if err != nil {
					return err
				}
			} else {
				if _, err = e.wait(ctx, iid, idle); err != nil {
					return err
				}
				// The completed prior input, rather than a bare waiting state, is the
				// idle trigger; it prevents a queued typed prompt being overtaken.
				trigger = "raw/typed-between-tools-" + method + "-input-complete.json"
			}
			if method == "ad" && step.state == "between-tools" {
				previous, er := e.Run.Observe(ctx, iid)
				if er != nil {
					return er
				}
				if er = e.Run.Bind(previous); er != nil {
					return er
				}
				if er = e.fallback(previous); er != nil {
					return er
				}
				p, ok := promptFor(previous, longText)
				toolDelivered := false
				if ok && runner.Text(p, "transport") == "written" {
					for _, h := range previous.Tables["handovers"] {
						if runner.Text(h, "handover_id") == runner.Text(p, "handover_id") && runner.Text(h, "boundary") == "tool_boundary" {
							toolDelivered = true
						}
					}
				}
				if !toolDelivered {
					return e.workload("precondition-not-established", iid, id, "previous busy steer has no completed tool-boundary handover; second input withheld to keep one fallback start slot")
				}
			}
			if _, err = e.observe(ctx, iid, id+"-before"); err != nil {
				return err
			}
			if method == "typed" {
				if err = e.reserve(id); err != nil {
					return err
				}
			}
			if method == "ad" {
				err = e.send(ctx, iid, id, step.text, "steer_turn", true, false)
			} else {
				err = e.control(ctx, iid, id, step.text)
			}
			if err != nil {
				return err
			}
			e.Observations[len(e.Observations)-1].State = step.state
			e.Observations[len(e.Observations)-1].Trigger = trigger
			if method == "ad" && step.state != "idle" {
				if err = e.checkFallback(ctx, iid); err != nil {
					return err
				}
			}
		}
		// Submit both matched inputs before awaiting either completion;
		// waiting for one side must not consume the other's finite window.
		if step.state != "long-tool" {
			for _, method := range []string{"ad", "typed"} {
				iid := ad
				if method == "typed" {
					iid = control
				}
				id := "typed-" + step.state + "-" + method + "-input"
				if err = e.waitTypedComplete(ctx, iid, id, step.text, method); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (e *Exercise) waitTypedComplete(ctx context.Context, iid, id, text, method string) error {
	r := e.Run
	var workloadErr error
	var issued runner.Case
	for _, c := range r.Manifest.Cases {
		if c.ID == id {
			issued = c
			break
		}
	}
	_, err := e.wait(ctx, iid, func(s runner.Snapshot) bool {
		if !idle(s) {
			return false
		}
		o, er := e.owner(iid)
		if er != nil {
			return false
		}
		data, er := r.ReadTranscript(o)
		if er != nil {
			return false
		}
		t, er := signals.ParseTranscript(data, o.SessionID)
		if er != nil {
			return false
		}
		after := issued.Started.UnixMilli()
		want := text
		bodyMode := "typed"
		if method == "ad" {
			p, ok := promptFor(s, text)
			if !ok || runner.Text(p, "transport") != "written" {
				return false
			}
			body, er := PromptBody(s, p)
			if er != nil {
				return false
			}
			want = body
			bodyMode = "steer_turn"
			after = runner.Number(p, "transport_at_ms")
			for _, h := range s.Tables["handovers"] {
				if runner.Text(h, "handover_id") == runner.Text(p, "handover_id") && runner.Text(h, "boundary") != "tool_boundary" {
					want = body
					bodyMode = "wait_turn"
					after = runner.Number(p, "admitted_at_ms")
				}
			}
		}
		if strings.Contains(id, "between-tools") {
			facts, er := LiveHookFacts(r.Evidence.Root, r.Manifest.RunID, r.Manifest.RecordingHook.Events)
			if er != nil {
				return false
			}
			stopped, nextTool := false, false
			for _, h := range facts {
				if runner.Text(h.Payload, "session_id") != o.SessionID || runner.Text(h.Payload, "agent_id") != "" || h.Receipt.Received.Before(issued.Started) {
					continue
				}
				stopped = stopped || h.Receipt.Event == "Stop"
				nextTool = nextTool || h.Receipt.Event == "PostToolBatch"
			}
			if stopped && !nextTool {
				workloadErr = e.workload("missing-next-tool", iid, id, "parent ended after injection without the requested remaining tool; idle pair remains incomplete")
				return true
			}
		}
		var intake signals.Intake
		if bodyMode == "typed" {
			intake, er = t.NextTypedInput(after, want)
		} else {
			intake, er = t.NextBodyInput(after, want, bodyMode)
		}
		if er != nil {
			return false
		}
		facts, er := LiveHookFacts(r.Evidence.Root, r.Manifest.RunID, r.Manifest.RecordingHook.Events)
		if er != nil {
			return false
		}
		stopped := false
		for _, h := range facts {
			if h.Receipt.Event == "Stop" && runner.Text(h.Payload, "session_id") == o.SessionID && runner.Text(h.Payload, "agent_id") == "" && !h.Receipt.Received.Before(intake.Time) {
				stopped = true
			}
		}
		if !stopped {
			return false
		}
		if er = r.Evidence.Put("raw/"+id+"-complete.jsonl", data); er != nil {
			return false
		}
		return r.Evidence.JSON("raw/"+id+"-complete.json", map[string]any{"intake": intake, "snapshot": s, "stop_observed": true}) == nil
	})
	if workloadErr != nil {
		return workloadErr
	}
	return e.workloadTimeout(err, iid, id, "nonterminating-model")
}

type TypedPair struct {
	State           string        `json:"state"`
	TextAD          string        `json:"text_ad"`
	TextControl     string        `json:"text_control"`
	ADTrigger       string        `json:"ad_trigger"`
	ControlTrigger  string        `json:"control_trigger"`
	ADOffset        time.Duration `json:"ad_offset"`
	ControlOffset   time.Duration `json:"control_offset"`
	ADBoundary      int           `json:"ad_boundary"`
	ControlBoundary int           `json:"control_boundary"`
	ADWhole         bool          `json:"ad_whole"`
	ControlWhole    bool          `json:"control_whole"`
	Evidence        []string      `json:"evidence"`
}

func CheckTypedPair(p TypedPair) runner.Result {
	status, reason := "pass", "matched trigger, text and first eligible boundary"
	control := signals.ParseTypedInput(p.TextControl)
	if p.ADTrigger == "" || p.ControlTrigger == "" || p.ADBoundary == 0 || p.ControlBoundary == 0 {
		status, reason = "not_completed", "comparable injection/model-input evidence absent"
	} else if p.TextAD != control.Text || p.ADBoundary != p.ControlBoundary || p.ADBoundary != 1 || !p.ADWhole || !p.ControlWhole {
		status, reason = "fail", "typed pair differs in text, whole input or eligible boundary"
	}
	if p.ADOffset < 0 || p.ControlOffset < 0 {
		status, reason = "fail", errors.New("input predates observed trigger").Error()
	}
	return result("typed-"+p.State, status, reason, "equal text and comparable actual state/offset; first eligible whole input", p, p.Evidence...)
}
