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
	store   *store.Store
	cfg     config.Config
	binding store.CursorRuntime
	exe     string
	path    string
}

func (r *reporter) report(event string) error {
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
		r.binding.Terminated = true
	}
	payload := map[string]any{"hook_event_name": event, "session_id": r.binding.SessionID, "transcript_path": r.path, "cursor_connection": r.binding.Connection, "origin": "main"}
	if terminated {
		payload["reason"] = "prompt_input_exit"
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// The gateway directly execs the hook. No shell, wrapper, controller or
	// child runtime can become its immediate parent.
	cmd := exec.CommandContext(ctx, r.exe, "hook", "--agent", "cursor")
	cmd.Stdin = bytes.NewReader(raw)
	if err = cmd.Run(); err != nil {
		return fmt.Errorf("Cursor report failed")
	}
	row, err = r.store.GetSpawn(r.binding.InstanceID)
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
