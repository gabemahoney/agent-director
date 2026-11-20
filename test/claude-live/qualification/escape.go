package qualification

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/test/claude-live/runner"
	"github.com/gabemahoney/agent-director/test/claude-live/signals"
)

type NotificationWindow struct {
	Session       string     `json:"session"`
	Started       time.Time  `json:"started"`
	Ended         time.Time  `json:"ended"`
	Observed      bool       `json:"observed"`
	Notifications []HookFact `json:"notifications"`
}
type EscObservation struct {
	ID           string             `json:"id"`
	Instance     string             `json:"instance"`
	Steer        string             `json:"steer"`
	Escape       time.Time          `json:"escape"`
	Held         string             `json:"held"`
	Delivery     api.DeliveryFacts  `json:"delivery"`
	Result       api.DeliveryResult `json:"result"`
	Notification NotificationWindow `json:"notification"`
	Stops        []HookFact         `json:"stops"`
	Force        string             `json:"force"`
}

func twoTools(first, second string) string {
	return "This local timing fixture waits for files created by the test runner in this run directory. Run EXACTLY two separate foreground Bash calls in order, one per response: first `" + controlledTool(first) + "`, then `" + controlledTool(second) + "`. Acknowledge mid-turn input briefly and preserve this exact sequence. After the second call finish briefly. Do not background calls."
}

func (e *Exercise) wholeSteer(ctx context.Context, iid, id, text string) error {
	r := e.Run
	_, err := e.wait(ctx, iid, func(s runner.Snapshot) bool {
		p, ok := promptFor(s, text)
		if !ok || runner.Text(p, "transport") != "written" {
			return false
		}
		body, er := PromptBody(s, p)
		if er != nil {
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
		transcript, er := signals.ParseTranscript(data, o.SessionID)
		if er != nil {
			return false
		}
		intake, er := transcript.NextBodyInput(runner.Number(p, "transport_at_ms"), body, "steer_turn")
		if er != nil {
			return false
		}
		if er = r.Evidence.Put("raw/"+id+"-before-Escape.jsonl", data); er != nil {
			return false
		}
		return r.Evidence.JSON("raw/"+id+"-whole-steer.json", map[string]any{"body": body, "intake": intake, "prompt": p}) == nil
	})
	if err != nil {
		return fmt.Errorf("first eligible model input did not establish the whole mid-turn steer before Escape: %w", err)
	}
	return nil
}

func (e *Exercise) notificationWindow(ctx context.Context, iid string) (NotificationWindow, error) {
	o, err := e.owner(iid)
	if err != nil {
		return NotificationWindow{}, err
	}
	r := e.Run
	window := NotificationWindow{Session: o.SessionID, Started: r.Now().UTC()}
	err = r.AwaitEvidence(ctx, 20*time.Second, func(context.Context) (bool, error) {
		if er := e.checkProviderProgress(ctx, iid); er != nil {
			return false, er
		}
		facts, er := LiveHookFacts(r.Evidence.Root, r.Manifest.RunID, r.Manifest.RecordingHook.Events)
		if er != nil {
			return false, er
		}
		window.Notifications = nil
		idlePrompt := false
		for _, h := range facts {
			if runner.Text(h.Payload, "session_id") != o.SessionID || runner.Text(h.Payload, "agent_id") != "" || h.Receipt.Received.Before(window.Started) {
				continue
			}
			if h.Receipt.Event == "PermissionRequest" {
				return false, errors.New("unexpected Dialog during Esc observation; no answer sent")
			}
			if h.Receipt.Event == "Notification" {
				window.Notifications = append(window.Notifications, h)
				idlePrompt = idlePrompt || runner.Text(h.Payload, "notification_type") == "idle_prompt"
			}
		}
		if _, er = e.observe(ctx, iid, "esc-notification-window"); er != nil {
			return false, er
		}
		return idlePrompt, nil
	})
	window.Ended = r.Now().UTC()
	window.Observed = true
	if ctx.Err() != nil {
		return window, ctx.Err()
	}
	if err != nil && !errors.Is(err, context.DeadlineExceeded) {
		return window, err
	}
	return window, nil
}

func (e *Exercise) interrupt(ctx context.Context, iid, id, steerCase, heldCase, heldText string) (rec EscObservation, err error) {
	r := e.Run
	rec = EscObservation{ID: id, Instance: iid, Steer: steerCase, Held: heldCase}
	if _, err = e.observe(ctx, iid, id+"-before-Escape"); err != nil {
		return rec, err
	}
	if err = e.control(ctx, iid, id+"-Escape", "Escape"); err != nil {
		return rec, err
	}
	rec.Escape = e.Observations[len(e.Observations)-1].Started
	if heldCase != "E4" {
		if err = e.reserve(heldCase); err != nil {
			return rec, err
		}
	}
	if err = e.send(ctx, iid, heldCase, heldText, "wait_turn", true, false); err != nil {
		return rec, err
	}
	if _, err = e.observe(ctx, iid, id+"-Held-after-Escape"); err != nil {
		return rec, err
	}
	c, b, er := r.AD(ctx, id+"-gap-get", "get", "--instance-id", iid)
	if er != nil {
		return rec, er
	}
	var row api.SpawnRow
	if er = json.Unmarshal(b, &row); er != nil {
		return rec, er
	}
	if row.Delivery == nil {
		return rec, errors.New("Esc get lacks covered delivery facts")
	}
	rec.Delivery = *row.Delivery
	rec.Result, err = e.lookup(ctx, heldCase, id+"-held-lookup")
	if err != nil {
		return rec, err
	}
	if rec.Delivery.AwaitingTurnStart <= 0 || rec.Result.Class != "held" || rec.Result.Wait == nil || rec.Result.Wait.Reason != "readiness_unknown" {
		return rec, fmt.Errorf("Esc did not produce the documented unresolved-input gap; stop for MI; get %s", c.Stdout)
	}
	c, b, err = r.AD(ctx, id+"-force-with-Held", "force-delivery", "--instance-id", iid)
	if err != nil {
		return rec, err
	}
	rec.Force = c.Stdout
	f, err := ParseForce(b)
	if err != nil {
		return rec, err
	}
	if f.Outcome != "refused" || f.Reason != "input_unresolved" {
		return rec, errors.New("Esc force with Held input did not refuse input_unresolved; stop for MI")
	}
	rec.Notification, err = e.notificationWindow(ctx, iid)
	if err != nil {
		return rec, err
	}
	facts, err := LiveHookFacts(r.Evidence.Root, r.Manifest.RunID, r.Manifest.RecordingHook.Events)
	if err != nil {
		return rec, err
	}
	o, err := e.owner(iid)
	if err != nil {
		return rec, err
	}
	for _, h := range facts {
		if h.Receipt.Event == "Stop" && runner.Text(h.Payload, "session_id") == o.SessionID && !h.Receipt.Received.Before(rec.Escape) {
			rec.Stops = append(rec.Stops, h)
		}
	}
	return rec, r.Evidence.JSON("raw/"+id+"-gap.json", rec)
}

func EscGap(ctx context.Context, e *Exercise) error {
	r := e.Run
	first := filepath.Join(r.Evidence.Root, "work-E1", "release-E1-first")
	second := filepath.Join(r.Evidence.Root, "work-E1", "release-E1-second-never")
	third := filepath.Join(r.Evidence.Root, "work-E1", "release-E3-first")
	fourth := filepath.Join(r.Evidence.Root, "work-E1", "release-E3-second-never")
	iid, err := e.spawn(ctx, "E1", twoTools(first, second))
	if err != nil {
		return err
	}
	if _, err = e.wait(ctx, iid, ready); err != nil {
		return err
	}
	if err = e.capability(ctx, iid); err != nil {
		return err
	}
	for round := 1; round <= 2; round++ {
		pre := 1
		release := first
		heldID := "E3"
		heldText := twoTools(third, fourth)
		if round == 2 {
			pre = 3
			release = third
			heldID = "E4"
			heldText = "E4: acknowledge preserved Held custody after keyless pause and resume, then finish briefly."
		}
		if _, err = e.toolWindow(ctx, iid, pre, false); err != nil {
			return err
		}
		slot := "E2"
		if round == 2 {
			slot = "E4"
		}
		if err = e.reserveSlot(slot, fmt.Sprintf("Esc-steer-%d", round), slot); err != nil {
			return err
		}
		steerID := fmt.Sprintf("Esc-steer-%d", round)
		text, er := Corpus(steerID+": acknowledge briefly and continue the original two-tool sequence.", 1900)
		if er != nil {
			return er
		}
		if err = e.send(ctx, iid, steerID, text, "steer_turn", true, false); err != nil {
			return err
		}
		if err = os.WriteFile(release, []byte("release\n"), 0600); err != nil {
			return err
		}
		if _, err = e.toolWindow(ctx, iid, pre+1, false); err != nil {
			return err
		}
		if err = e.wholeSteer(ctx, iid, steerID, text); err != nil {
			return err
		}
		rec, er := e.interrupt(ctx, iid, fmt.Sprintf("esc-%d", round), steerID, heldID, heldText)
		if er != nil {
			return errors.Join(er, r.Evidence.JSON(fmt.Sprintf("raw/esc-%d-gap.json", round), rec))
		}
		if round == 1 {
			if err = e.control(ctx, iid, "E2", "Fresh typed recovery E2: reply with three words and finish this turn; use no tools."); err != nil {
				return err
			}
			if err = e.waitSighting(ctx, iid, heldText); err != nil {
				return err
			}
			if _, err = e.observe(ctx, iid, "E2-typed-recovery-E3-delivery"); err != nil {
				return err
			}
		} else {
			if err = e.stop(ctx, iid, "esc-keyless-pause", true); err != nil {
				return err
			}
			if err = e.resume(ctx, iid, "esc-keyless-resume", false); err != nil {
				return err
			}
			if err = e.waitSighting(ctx, iid, heldText); err != nil {
				return err
			}
			if _, err = e.wait(ctx, iid, idle); err != nil {
				return err
			}
			if _, err = e.observe(ctx, iid, "E4-resume-recovery-final"); err != nil {
				return err
			}
		}
	}
	return r.Evidence.JSON("raw/import-requests.json", e.Options)
}
