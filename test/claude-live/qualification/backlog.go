package qualification

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/test/claude-live/runner"
)

type AdmissionAttempt struct {
	ID          string             `json:"id"`
	TextBytes   int                `json:"text_bytes"`
	Started     time.Time          `json:"started"`
	Ended       time.Time          `json:"ended"`
	Output      string             `json:"output"`
	ErrorOutput string             `json:"error_output"`
	Result      api.DeliveryResult `json:"result"`
}

func (e *Exercise) lookup(ctx context.Context, id, label string) (api.DeliveryResult, error) {
	_, b, err := e.Run.AD(ctx, label, "get-delivery", "--caller-uuid", e.Run.Manifest.RunID, "--delivery-request-id", id)
	var v struct {
		Lookup string              `json:"lookup"`
		Result *api.DeliveryResult `json:"result"`
	}
	if err != nil {
		return api.DeliveryResult{}, err
	}
	if err = json.Unmarshal(b, &v); err != nil {
		return api.DeliveryResult{}, err
	}
	if v.Lookup != "found" || v.Result == nil {
		return api.DeliveryResult{}, errors.New("confirmed pair Lookup is not found")
	}
	return *v.Result, nil
}

func LargeBacklog(ctx context.Context, e *Exercise) (err error) {
	r := e.Run
	release := filepath.Join(r.Evidence.Root, "work-N8", "release-never")
	prompt := "This local timing fixture waits for a file controlled by the test runner in this run directory. Run exactly this one foreground Bash call and no other tool: `" + controlledTool(release) + "`. Remain in that call until it exits. Never create background work."
	iid, err := e.spawnConfigured(ctx, "N8", prompt, LaunchOptions{Fixture: gateFixture(r)})
	if err != nil {
		return err
	}
	if _, err = e.wait(ctx, iid, ready); err != nil {
		return err
	}
	if err = e.capability(ctx, iid); err != nil {
		return err
	}
	if err = e.backlogHold(ctx, iid, controlledTool(release)); err != nil {
		return err
	}
	var accepted []AdmissionAttempt
	proof := BacklogProof{Lookups: map[string]bool{}}
	defer func() {
		proof.Accepted = accepted
		err = errors.Join(err, r.Evidence.JSON("raw/backlog-acceptance.json", accepted), r.Evidence.JSON("raw/backlog-proof.json", proof))
	}()
	caps := store.BusySendCapabilitiesFor("claude")
	if caps.HeldCount != 1000 {
		return errors.New("candidate held count differs from the qualification's required 1000-message exercise")
	}
	for i := 0; i < 1000; i++ {
		if er := e.backlogHold(ctx, iid, controlledTool(release)); er != nil {
			return er
		}
		id := fmt.Sprintf("backlog-%04d", i+1)
		text, er := Corpus(id, 1900)
		if er != nil {
			return er
		}
		c, b, er := r.AD(ctx, id, "send-keys", "--instance-id", iid, "--text", text, "--send-mode", "wait_turn", "--confirmed-delivery", "--caller-uuid", r.Manifest.RunID, "--delivery-request-id", id)
		var res api.DeliveryResult
		decodeErr := json.Unmarshal(b, &res)
		// send-keys embeds DeliveryResult's fields in its public result.
		accepted = append(accepted, AdmissionAttempt{id, len(text), c.Started, c.Ended, c.Stdout, c.Stderr, res})
		if er != nil || decodeErr != nil {
			return errors.Join(er, decodeErr)
		}
		if res.Class != "held" || res.Final {
			return errors.New("large backlog did not stay Held; stop without draining")
		}
		if er := e.backlogHold(ctx, iid, controlledTool(release)); er != nil {
			return er
		}
		if i%25 == 24 {
			s, er := e.observe(ctx, iid, id+"-held")
			if er != nil {
				return er
			}
			if len(s.Tables["prompts"]) != 0 {
				return errors.New("backlog unexpectedly issued before controlled boundary")
			}
		}
	}
	before, err := e.observe(ctx, iid, "backlog-1000-before-refusals")
	if err != nil {
		return err
	}
	proof.Before = before
	limit, _ := caps.Mode(store.BusySendWaitTurn)
	// Deliberate semantic refusals may return a nonzero CLI status; retain both
	// streams and let CheckBacklog inspect the actual machine-readable outcome.
	over := strings.Repeat("x", int(limit.MessageBytes)+1)
	if err = e.backlogHold(ctx, iid, controlledTool(release)); err != nil {
		return err
	}
	if err = e.refusalProbe(ctx, iid, "backlog-over-text", over); err != nil {
		return err
	}
	extra, err := Corpus("backlog-1001", 1900)
	if err != nil {
		return err
	}
	if err = e.backlogHold(ctx, iid, controlledTool(release)); err != nil {
		return err
	}
	if err = e.refusalProbe(ctx, iid, "backlog-over-count", extra); err != nil {
		return err
	}
	if err = e.backlogHold(ctx, iid, controlledTool(release)); err != nil {
		return err
	}
	after, err := e.observe(ctx, iid, "backlog-1000-after-refusals")
	if err != nil {
		return err
	}
	proof.After = after
	for _, id := range []string{"backlog-over-text", "backlog-over-count"} {
		b, er := runner.Read(r.Evidence.Root, "raw/"+id+"-refusal.json")
		if er != nil {
			return er
		}
		var refusal RefusalObservation
		if er = json.Unmarshal(b, &refusal); er != nil {
			return er
		}
		proof.Refusals = append(proof.Refusals, refusal)
	}
	if len(before.Tables["messages"]) != len(after.Tables["messages"]) || len(after.Tables["prompts"]) != 0 {
		return errors.New("refusal claimed a Message or backlog emitted a prompt")
	}
	if err = e.stop(ctx, iid, "backlog-end-before-Stop", false); err != nil {
		return err
	}
	// Fresh reuse finalizes Held by the documented lifecycle. No startup input
	// is passed, so this launch has no authorized parent turn.
	proof.Exited = true
	if err = e.resume(ctx, iid, "backlog-fresh-reuse", true); err != nil {
		return err
	}
	if _, err = e.wait(ctx, iid, ready); err != nil {
		return err
	}
	proof.Final, err = e.observe(ctx, iid, "backlog-reuse-finalized")
	if err != nil {
		return err
	}
	for _, id := range []string{"backlog-0001", "backlog-0500", "backlog-1000"} {
		got, er := e.lookup(ctx, id, id+"-final-lookup")
		if er != nil {
			return er
		}
		if !got.Final || got.Class != "not_started" {
			return errors.New("fresh reuse did not finalize the confirmed Held pair")
		}
		proof.Lookups[id] = true
	}
	return nil
}

func (e *Exercise) refusalProbe(ctx context.Context, iid, id, text string) error {
	c, b, commandErr := e.Run.AD(ctx, id, "send-keys", "--instance-id", iid, "--text", text, "--send-mode", "wait_turn", "--confirmed-delivery", "--caller-uuid", e.Run.Manifest.RunID, "--delivery-request-id", id)
	record := struct {
		Command runner.Command  `json:"command"`
		Stdout  json.RawMessage `json:"stdout,omitempty"`
		Error   string          `json:"error,omitempty"`
	}{Command: c}
	if json.Valid(b) {
		record.Stdout = b
	}
	if commandErr != nil {
		record.Error = commandErr.Error()
	}
	if err := e.Run.Evidence.JSON("raw/"+id+".json", record); err != nil {
		return err
	}
	if commandErr == nil {
		return errors.New("over-limit send unexpectedly succeeded")
	}
	// A transport failure is not a semantic refusal; the finalizer also checks
	// exact err_details and absence of pair custody from both raw streams.
	stderr, err := runner.Read(e.Run.Evidence.Root, c.Stderr)
	if err != nil {
		return err
	}
	var refusal struct {
		ErrName string              `json:"err_name"`
		Details api.CantSendDetails `json:"err_details"`
	}
	decoded := false
	for _, stream := range [][]byte{b, stderr} {
		if json.Unmarshal(stream, &refusal) == nil && refusal.Details.Class == "cant_send" {
			decoded = true
			break
		}
	}
	if !decoded || refusal.Details.Limit == nil {
		return errors.New("over-limit command lacks a machine-readable limit refusal")
	}
	_, lookupData, lookupErr := e.Run.AD(ctx, id+"-unclaimed-lookup", "get-delivery", "--caller-uuid", e.Run.Manifest.RunID, "--delivery-request-id", id)
	if lookupErr != nil {
		return lookupErr
	}
	var lookup api.GetDeliveryResult
	if err = json.Unmarshal(lookupData, &lookup); err != nil {
		return err
	}
	if lookup.Lookup != "unknown" || lookup.Result != nil {
		return errors.New("refused Message claimed a confirmed pair")
	}
	d := refusal.Details
	want := "held_count"
	if id == "backlog-over-text" {
		want = "text_bytes"
	}
	if d.Reason != want {
		return fmt.Errorf("over-limit refusal reason %q, expected %q", d.Reason, want)
	}
	return e.Run.Evidence.JSON("raw/"+id+"-refusal.json", RefusalObservation{Case: id, Reason: d.Reason, Actual: d.Limit.Actual, Allowed: d.Limit.Allowed, ResendSafe: d.ResendSafe, RetryWith: d.RetryWith, Command: c.Stderr})
}
