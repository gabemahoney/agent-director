package hook

import (
	"context"
	"encoding/json"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/trail"
)

// HandleNoExecForm is the hook side of a no-verb run (SR-22.9, "A Claude Code
// that does not run exec-form hooks"). A Claude Code older than the exec-form
// minimum ignores a hook's `args` and runs `command`, the bare binary, through
// /bin/sh, so every hook reaches agent-director with no verb and its payload
// on standard input. cmd/agent-director reads that input (not a terminal, at
// most MaxPayloadBytes, with a deadline) and passes it here as raw.
//
// It reports whether raw is a hook payload: a JSON object with a non-empty
// string `hook_event_name`. The `event_name` alias PeekEventName accepts does
// not count. When it is, HandleNoExecForm writes exactly one ad.hook.ignored
// with reason no_exec_form (SR-14; emitNoExecForm) and runs no hook logic; the
// caller then writes nothing on stdout and exits 0. When it is not, it writes
// nothing and the caller prints help as before.
//
// It opens no store, loads no config, writes no ad.hook.fired and logs
// nothing (so nothing reaches stderr); a trail-write failure changes nothing.
// Only hc.Env, hc.ParentPID and hc.ParentProc (for the command name) are read.
func HandleNoExecForm(ctx context.Context, raw []byte, hc HandleConfig) bool {
	event, transcriptPath, ok := noExecFormPayload(raw)
	if !ok {
		return false
	}
	emitNoExecForm(ctx, hc, event, extractSessionID(transcriptPath))
	return true
}

// noExecFormPayload reads raw as a no-verb run's hook payload: it must be a
// JSON object whose `hook_event_name` is a non-empty string. It returns that
// name and the payload's `transcript_path` ("" when absent or not a string).
// Anything else (invalid JSON, a non-object, null, a missing, null, empty or
// non-string `hook_event_name`) is not a hook payload.
func noExecFormPayload(raw []byte) (event, transcriptPath string, ok bool) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
		return "", "", false
	}
	if err := json.Unmarshal(obj["hook_event_name"], &event); err != nil || event == "" {
		return "", "", false
	}
	if tp, present := obj["transcript_path"]; present {
		if err := json.Unmarshal(tp, &transcriptPath); err != nil {
			transcriptPath = ""
		}
	}
	return event, transcriptPath, true
}

// emitNoExecForm writes the one ad.hook.ignored no_exec_form record (SR-14)
// with no store access, fail-open. Its fields come from ignoredFields, the
// builder emitIgnored uses: claude_instance_id from the environment (null when
// absent or invalid, ResolveInstanceID), hook_event, reason, parent_pid
// (hc.ParentPID; 0 when nil) and parent_command, hook_session_id (the
// transcript_path basename, or null), and null row_session_id and
// row_pane_pid.
func emitNoExecForm(ctx context.Context, hc HandleConfig, event, sessionID string) {
	env := hc.Env
	if env == nil {
		env = func(string) string { return "" }
	}
	instanceID, err := ResolveInstanceID(env)
	if err != nil {
		instanceID = ""
	}
	var parent parentIdentity
	if hc.ParentPID != nil {
		parent.pid = hc.ParentPID()
	}
	_ = trail.Emit(ctx, "ad.hook.ignored", ignoredFields(hc.ParentProc, ignoredHook{
		instanceID: instanceID,
		event:      event,
		sessionID:  sessionID,
		parent:     parent,
		reason:     store.HookReasonNoExecForm,
	}))
}
