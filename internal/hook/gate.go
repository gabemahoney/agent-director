package hook

import (
	"context"
	"errors"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/trail"
)

// ParentProc reads the hook's parent process: its start time, which the gate
// compares with the row's recorded pane_starttime (SR-22.9); its command
// name, which only ad.hook.ignored's parent_command (SR-14) and
// ad.hook.launcher_detected report; and its parent pid, which only the
// launcher warning reads (launcherPID, b.9n6), never the gate. The method set
// mirrors probe.ProcChecker's StartTime, probe.CommandNameReader's
// CommandName and probe.ParentPIDReader's PPID exactly; declaring it here
// keeps internal/hook free of a probe import. cmd/agent-director wires the
// per-OS readers (probe.NewProcChecker, probe.NewCommandNameReader and
// probe.NewParentPIDReader); tests inject a double.
//
// StartTime's contract is probe.ProcChecker's: start is non-empty only when
// alive. CommandName answers ("", false) when it cannot read the name, and
// PPID (0, false) when it cannot read the parent pid.
type ParentProc interface {
	StartTime(pid int) (start string, alive bool, known bool)
	CommandName(pid int) (name string, ok bool)
	PPID(pid int) (ppid int, ok bool)
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
	instanceID string // "" = none (only a no-verb run can lack one)
	event      string
	sessionID  string // the payload's; "" = none
	parent     parentIdentity
	// reason is store.HookReasonPIDMismatch or store.HookReasonNoPaneRecorded
	// (from the gated write), store.HookReasonSubagentEvent (from the payload,
	// before any write) or store.HookReasonNoExecForm (a no-verb run given a
	// hook payload, emitNoExecForm; no store access).
	reason string
	// silentWithoutRow writes nothing when no row has the id. A reason the
	// store reports implies the row existed; subagent_event is decided with
	// no store read, so its one row read carries the no-row rule (decision
	// A2).
	silentWithoutRow bool
}

// ignoredFields builds ad.hook.ignored's SR-14 field set for h with the row
// fields null: claude_instance_id (null when h has none), hook_event, reason,
// parent_pid, parent_command (read now, through pp; null when unreadable or pp
// is nil), hook_session_id (null when the payload gives none), row_session_id,
// row_pane_pid and launcher_pid (null here; emitIgnored fills them from its
// row read) and source ad_hook. It is the one builder both emitIgnored and
// emitNoExecForm use, so the two records cannot drift. It reads no store.
func ignoredFields(pp ParentProc, h ignoredHook) map[string]any {
	fields := map[string]any{
		"claude_instance_id": nil,
		"hook_event":         h.event,
		"reason":             h.reason,
		"parent_pid":         h.parent.pid,
		"parent_command":     nil,
		"hook_session_id":    nil,
		"row_session_id":     nil,
		"row_pane_pid":       nil,
		"launcher_pid":       nil,
		"source":             "ad_hook",
	}
	if h.instanceID != "" {
		fields["claude_instance_id"] = h.instanceID
	}
	if pp != nil {
		if name, ok := pp.CommandName(h.parent.pid); ok && name != "" {
			fields["parent_command"] = name
		}
	}
	if h.sessionID != "" {
		fields["hook_session_id"] = h.sessionID
	}
	return fields
}

// emitIgnored writes the one ad.hook.ignored record of a hook SR-22.9 did not
// apply (SR-14), fail-open: a trail-write failure changes nothing. Its fields
// come from ignoredFields, plus row_session_id and row_pane_pid from one read
// of the row (null when the row records none or the read fails; row_pane_pid
// is build-lead decision A4), and launcher_pid from the same read (launcherPID;
// null unless h is a pid_mismatch hook whose parent's parent is the row's
// pane process, whatever the row's state). With h.silentWithoutRow, a read
// that finds no row writes nothing. It never names another row or any
// session-environment content (SR-15).
//
// When launcher_pid is set, h is a SessionStart and that same read found the
// row still pending, it then writes ad.hook.launcher_detected
// (emitLauncherDetected, b.9n6). Neither record changes the gate's decision.
func emitIgnored(ctx context.Context, rr ignoredRowReader, pp ParentProc, h ignoredHook) {
	sp, err := rr.GetSpawn(h.instanceID)
	if h.silentWithoutRow && errors.Is(err, store.ErrSpawnNotFound) {
		return
	}
	fields := ignoredFields(pp, h)
	launcher := 0
	if err == nil {
		if sp.ClaudeSessionID != "" {
			fields["row_session_id"] = sp.ClaudeSessionID
		}
		if sp.Identity.PanePID > 0 {
			fields["row_pane_pid"] = sp.Identity.PanePID
		}
		launcher = launcherPID(pp, h, sp.Identity.PanePID)
	}
	if launcher > 0 {
		fields["launcher_pid"] = launcher
	}
	_ = trail.Emit(ctx, "ad.hook.ignored", fields)
	if launcher > 0 && h.event == "SessionStart" && sp.State == store.StatePending {
		emitLauncherDetected(ctx, pp, h, launcher, fields["parent_command"])
	}
}

// launcherPID is the row's pane process when it is the parent of h's parent,
// else 0 (b.9n6). The hook's parent is then a child of the pane process, not
// the pane process itself, so SR-22.9 refuses every hook of the agent with
// pid_mismatch. Two causes look exactly alike here and are not told apart: a
// `claude` launcher that does not exec (the pane process is the launcher, the
// parent its Claude Code child), or Claude Code running the hook through a
// shell instead of in exec form (the pane process is Claude Code, the parent
// that shell). The result is named launcher_pid either way. Only a
// pid_mismatch hook is checked: it reads the parent's parent pid once,
// through pp.PPID, and compares it with panePID, the row's recorded pane_pid
// (0 = none). A nil pp, no parent pid or a failed read answers 0. It never
// changes the gate's decision.
func launcherPID(pp ParentProc, h ignoredHook, panePID int) int {
	if h.reason != store.HookReasonPIDMismatch || pp == nil || h.parent.pid <= 0 || panePID <= 0 {
		return 0
	}
	if ppid, ok := pp.PPID(h.parent.pid); ok && ppid == panePID {
		return panePID
	}
	return 0
}

// launcherAdvice is ad.hook.launcher_detected's advice: what the operator
// changes so later launches are tracked. It names both causes launcherPID
// cannot tell apart (a non-exec launcher, or a shell-form hook) and asserts
// neither. Callers branch on the event name; the advice is an English extra.
// It never advises ending a session by hand.
const launcherAdvice = "The parent of this agent's hooks is a child of its pane process, not the pane process itself, " +
	"so agent-director ignores every hook of this agent (pid_mismatch). " +
	"Either the claude on PATH is a launcher that runs Claude Code as a child instead of exec-ing it " +
	"(replace it with the native claude binary or a wrapper that execs it), " +
	"or Claude Code ran the hook through a shell instead of in exec form; parent_command names that parent."

// emitLauncherDetected writes ad.hook.launcher_detected (b.9n6) for a
// SessionStart refused with pid_mismatch whose parent's parent is the pending
// row's pane process, launcher (launcherPID). Despite its name the event does
// not assert a launcher: a shell-form hook produces the same structure, and
// the advice names both causes. It keeps no record of having
// warned: SessionStart fires once per launch and again on /clear, compact and
// resume, and each such refused SessionStart writes one. Its fields:
// claude_instance_id, launcher_pid, launcher_command (read now, through pp;
// null when unreadable), parent_pid and parent_command (ad.hook.ignored's
// values), advice (launcherAdvice) and source ad_hook. It reads no store.
// Fail-open: a trail-write failure changes nothing; the gate's decision, the
// row and the hook's exit are those of any ignored hook.
func emitLauncherDetected(ctx context.Context, pp ParentProc, h ignoredHook, launcher int, parentCommand any) {
	fields := map[string]any{
		"claude_instance_id": h.instanceID,
		"launcher_pid":       launcher,
		"launcher_command":   nil,
		"parent_pid":         h.parent.pid,
		"parent_command":     parentCommand,
		"advice":             launcherAdvice,
		"source":             "ad_hook",
	}
	if name, ok := pp.CommandName(launcher); ok && name != "" {
		fields["launcher_command"] = name
	}
	_ = trail.Emit(ctx, "ad.hook.launcher_detected", fields)
}
