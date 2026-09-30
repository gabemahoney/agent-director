package hook

import (
	"context"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/trail"
)

// ParentProc reads the hook's parent process: its start time, which the gate
// compares with the row's recorded pane_starttime (SR-22.9), and its command
// name, which only ad.hook.ignored's parent_command reports (SR-14). The
// method set mirrors probe.ProcChecker's StartTime and
// probe.CommandNameReader's CommandName exactly; declaring it here keeps
// internal/hook free of a probe import. cmd/agent-director wires the per-OS
// readers (probe.NewProcChecker and probe.NewCommandNameReader); tests inject
// a double.
//
// StartTime's contract is probe.ProcChecker's: start is non-empty only when
// alive. CommandName answers ("", false) when it cannot read the name.
type ParentProc interface {
	StartTime(pid int) (start string, alive bool, known bool)
	CommandName(pid int) (name string, ok bool)
}

// parentIdentity is the hook's parent process as captured once at the start
// of Handle, before any store call: its pid (getppid(); with an exec-form hook
// the agent process, SR-22.9) and that pid's start time, "" when unreadable
// or when no ParentProc is wired (such a hook never matches a row).
type parentIdentity struct {
	pid   int
	start string
}

// captureParent reads the hook's parent pid through hc.ParentPID and its start
// time through hc.ParentProc. A nil ParentPID is a caller error in production
// (cmd/agent-director always wires os.Getppid) and reads as pid 0, which no
// row's pane_pid equals.
func captureParent(hc HandleConfig) parentIdentity {
	var p parentIdentity
	if hc.ParentPID != nil {
		p.pid = hc.ParentPID()
	}
	if hc.ParentProc != nil && p.pid > 0 {
		if start, alive, _ := hc.ParentProc.StartTime(p.pid); alive {
			p.start = start
		}
	}
	return p
}

// ignoredRowReader is the read ad.hook.ignored's row fields come from.
// HookStore satisfies it.
type ignoredRowReader interface {
	GetSpawn(instanceID string) (store.Spawn, error)
}

// ignoredHook is what one hook that SR-22.9 did not apply reports.
type ignoredHook struct {
	instanceID string
	event      string
	sessionID  string // the payload's; "" = none
	parent     parentIdentity
	reason     string // store.HookReasonPIDMismatch or store.HookReasonNoPaneRecorded
}

// emitIgnored writes the one ad.hook.ignored record of a hook the gate did
// not apply (SR-14; SR-22.9), fail-open: a trail-write failure changes
// nothing. Its fields are claude_instance_id, hook_event, reason, parent_pid,
// parent_command (read only now, through hc.ParentProc; null when
// unreadable), hook_session_id (null when the payload gives none),
// row_session_id and row_pane_pid (from one read of the row; null when the
// row records none or the read fails; row_pane_pid is build-lead decision A4)
// and source ad_hook. It never names another row or any session-environment
// content (SR-15).
func emitIgnored(ctx context.Context, rr ignoredRowReader, pp ParentProc, h ignoredHook) {
	fields := map[string]any{
		"claude_instance_id": h.instanceID,
		"hook_event":         h.event,
		"reason":             h.reason,
		"parent_pid":         h.parent.pid,
		"parent_command":     nil,
		"hook_session_id":    nil,
		"row_session_id":     nil,
		"row_pane_pid":       nil,
		"source":             "ad_hook",
	}
	if pp != nil {
		if name, ok := pp.CommandName(h.parent.pid); ok && name != "" {
			fields["parent_command"] = name
		}
	}
	if h.sessionID != "" {
		fields["hook_session_id"] = h.sessionID
	}
	if sp, err := rr.GetSpawn(h.instanceID); err == nil {
		if sp.ClaudeSessionID != "" {
			fields["row_session_id"] = sp.ClaudeSessionID
		}
		if sp.Identity.PanePID > 0 {
			fields["row_pane_pid"] = sp.Identity.PanePID
		}
	}
	_ = trail.Emit(ctx, "ad.hook.ignored", fields)
}
