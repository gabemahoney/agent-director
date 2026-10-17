package cursorhost

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/store"
)

type reporter struct {
	store               *store.Store
	cfg                 config.Config
	binding             store.CursorRuntime
	exe                 string
	path                string
	loaded              bool
	rotationContext     context.Context
	rotationObserved    func(store.CursorRuntime)
	rotationReconciled  func()
	afterRuntimeRetired func()
	terminationUntil    time.Time
}

// A locally retired authorization is not evidence that the durable dialog is
// gone. Every new turn checks all outstanding requests and the current binding.
func (r *reporter) promptEligible() (bool, error) {
	check := func(row store.Spawn) error {
		if err := VerifyRuntime(r.binding, false); err != nil {
			return err
		}
		return VerifyPane(r.store, row, r.cfg)
	}
	until := time.Now().Add(transportWriteWait)
	if r.loaded {
		return r.store.CheckLoadedCursorPrompt(r.binding, r.path, until, check)
	}
	return r.store.CheckCursorPrompt(r.binding, until, check)
}

func (r *reporter) report(event string) error {
	return r.reportWaiting(event, nil)
}

// reportWaiting lets Enter independently confirm the committed waiting report
// while its hook is finishing. It never grants admission before that commit.
func (r *reporter) reportWaiting(event string, waiting func(func() bool)) error {
	if event == "SessionEnd" {
		// Retirement can outlive the controller's await. Keep its local proof
		// separate from the binding used by concurrent fatal cleanup.
		snapshot := *r
		return snapshot.reportIdentity(event, waiting, "")
	}
	return r.reportIdentity(event, waiting, "")
}

// reportIdentity publishes a rotation through the same direct-parent hook.
// The binding remains outgoing until the hook transaction has committed.
func (r *reporter) reportIdentity(event string, waiting func(func() bool), next string, rotationUntil ...time.Time) error {
	if err := VerifyTranscriptPath(r.path); err != nil {
		return err
	}
	terminated := event == "SessionEnd"
	if err := VerifyRuntime(r.binding, terminated); err != nil {
		return err
	}
	row, err := r.store.GetSpawn(r.binding.InstanceID)
	if err != nil {
		return fmt.Errorf("Cursor row unavailable")
	}
	if row.Identity.Token != r.binding.Token || row.LifeNumber != r.binding.Life || row.Identity.PanePID != r.binding.GatewayPID || row.Identity.PaneStarttime != r.binding.GatewayStart {
		return fmt.Errorf("Cursor launch changed")
	}
	beforeVersion := row.Snapshot.RowVersion
	if err = VerifyPane(r.store, row, r.cfg); err != nil {
		return err
	}
	if terminated {
		ok, e := r.store.MarkCursorRuntimeGone(r.binding)
		if e != nil || !ok {
			return fmt.Errorf("Cursor retirement binding changed")
		}
		if r.afterRuntimeRetired != nil {
			r.afterRuntimeRetired()
		}
		r.binding.Terminated = true
	}
	payload := map[string]any{"hook_event_name": event, "session_id": r.binding.SessionID, "transcript_path": r.path, "cursor_connection": r.binding.Connection, "origin": "main"}
	if next != "" {
		if len(rotationUntil) != 1 || !time.Now().Before(rotationUntil[0]) {
			return fmt.Errorf("Cursor rotation publication deadline passed")
		}
		payload["cursor_previous_session"] = r.binding.SessionID
		payload["session_id"] = next
		payload["cursor_rotation_deadline"] = rotationUntil[0].UTC().Format(time.RFC3339Nano)
	}
	if terminated {
		payload["reason"] = "prompt_input_exit"
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	reportUntil := time.Now().Add(5 * time.Second)
	if terminated && !r.terminationUntil.IsZero() && r.terminationUntil.Before(reportUntil) {
		reportUntil = r.terminationUntil
	}
	if !time.Now().Before(reportUntil) {
		return fmt.Errorf("Cursor end report deadline passed")
	}
	if next != "" && rotationUntil[0].Before(reportUntil) {
		reportUntil = rotationUntil[0]
	}
	parent := r.rotationContext
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithDeadline(parent, reportUntil)
	defer cancel()
	// The gateway directly execs the hook. No shell, wrapper, controller or
	// child runtime can become its immediate parent.
	cmd := exec.CommandContext(ctx, r.exe, "hook", "--agent", "cursor")
	cmd.Stdin = bytes.NewReader(raw)
	if waiting != nil {
		snapshot := *r
		waiting(func() bool { return snapshot.verifyReport(event, beforeVersion, true) == nil })
	}
	var commandErr error
	if next == "" {
		commandErr = cmd.Run()
	} else {
		expected := r.binding
		expected.SessionID = next
		observationContext, stopObservation := context.WithDeadline(context.Background(), reportUntil)
		defer stopObservation()
		observe := func(final bool) bool {
			committed, unchanged, e := r.store.ReconcileCursorRotation(observationContext, r.binding, row, next, r.path)
			if final && e == nil && (committed || unchanged) && r.rotationReconciled != nil {
				r.rotationReconciled()
			}
			if e == nil && committed {
				if r.rotationObserved != nil {
					r.rotationObserved(expected)
				}
				return true
			}
			return false
		}
		// Observe the transaction while the direct child hook is still running.
		// Hook exit/output is not the publication evidence: row, full binding,
		// examined launch and required history must agree in the database.
		if commandErr = cmd.Start(); commandErr == nil {
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			ticker := time.NewTicker(10 * time.Millisecond)
			defer ticker.Stop()
			committed := false
		waitHook:
			for {
				select {
				case commandErr = <-done:
					committed = observe(true) || committed
					break waitHook
				case <-ticker.C:
					if !committed {
						committed = observe(false)
					}
				case <-ctx.Done():
					// Stop the hook before the final bounded snapshot, so it cannot
					// commit after a negative observation during intentional exit.
					select {
					case commandErr = <-done:
						committed = observe(true) || committed
						break waitHook
					case <-observationContext.Done():
						return fmt.Errorf("Cursor rotation report deadline; publication may already be committed")
					}
				}
			}
			if committed {
				r.binding = expected
			}
		} else if observe(true) {
			// No child exists after Start fails, so this final bounded snapshot
			// cannot be overtaken by a later commit from this hook.
			r.binding = expected
		}
	}
	if commandErr != nil {
		return fmt.Errorf("Cursor report failed")
	}
	return r.verifyReport(event, beforeVersion, false)
}

func (r *reporter) verifyReport(event string, beforeVersion int64, waitingOnly bool) error {
	terminated := event == "SessionEnd"
	row, err := r.store.GetSpawn(r.binding.InstanceID)
	if err != nil || row.Identity.Token != r.binding.Token || row.LifeNumber != r.binding.Life || row.SessionID != r.binding.SessionID || row.Snapshot.RowVersion <= beforeVersion {
		return fmt.Errorf("Cursor report was not applied")
	}
	expected := store.StateWaiting
	if event == "UserPromptSubmit" {
		expected = store.StateWorking
	}
	if terminated {
		expected = store.StateEnded
	}
	if row.JSONLPath != r.path {
		return fmt.Errorf("Cursor report was not applied")
	}
	if waitingOnly {
		if row.State != store.StateWaiting || terminated || event == "UserPromptSubmit" {
			return fmt.Errorf("Cursor waiting report was not applied")
		}
		requests, e := r.store.PermissionRequestsForSpawn(row.InstanceID)
		if e != nil {
			return fmt.Errorf("Cursor waiting requests unavailable")
		}
		for _, request := range requests {
			if request.AwaitsAnswer() && !request.ProvenGone() {
				return fmt.Errorf("Cursor waiting report has held requests")
			}
		}
	}
	if row.State != expected {
		if terminated || row.State != store.StateCheckPermission {
			return fmt.Errorf("Cursor report was not applied")
		}
		requests, e := r.store.PermissionRequestsForSpawn(row.InstanceID)
		if e != nil {
			return fmt.Errorf("Cursor held report could not be verified")
		}
		held := false
		for _, request := range requests {
			held = held || (request.AwaitsAnswer() && !request.ProvenGone())
		}
		if !held {
			return fmt.Errorf("Cursor report was not applied")
		}
	}
	// Report acceptance retains the same runtime/connection/pane authority;
	// another launch or an unrelated version increment cannot confirm it.
	bound, err := r.store.ReadCursorRuntime(row.InstanceID)
	if err != nil || bound != r.binding || row.Identity.PanePID != r.binding.GatewayPID || row.Identity.PaneStarttime != r.binding.GatewayStart {
		return fmt.Errorf("Cursor report binding changed")
	}
	if err = VerifyRuntime(r.binding, terminated); err != nil {
		return err
	}
	return VerifyPane(r.store, row, r.cfg)
}
