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
// ad.hook.pane_is_grandparent report; and its parent pid, which only
// paneIsGrandparent reads (b.9n6, b.zde), never the gate. The method set
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
// is nil), hook_session_id (null when the payload gives none), row_session_id
// and row_pane_pid (null here; emitIgnored fills them from its row read) and
// source ad_hook. It is the one builder both emitIgnored and emitNoExecForm
// use, so the two records cannot drift. It reads no store.
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
// is build-lead decision A4). With h.silentWithoutRow, a read that finds no
// row writes nothing. It never names another row or any session-environment
// content (SR-15).
//
// When that same read succeeded and paneIsGrandparent holds for h and the row
// (a pending row's SessionStart refused with pid_mismatch whose parent's
// parent is the row's pane process), it then writes ad.hook.pane_is_grandparent
// (emitPaneIsGrandparent; b.9n6, b.zde). Neither record changes the gate's
// decision.
func emitIgnored(ctx context.Context, rr ignoredRowReader, pp ParentProc, h ignoredHook) {
	sp, err := rr.GetSpawn(h.instanceID)
	if h.silentWithoutRow && errors.Is(err, store.ErrSpawnNotFound) {
		return
	}
	fields := ignoredFields(pp, h)
	if err == nil {
		if sp.ClaudeSessionID != "" {
			fields["row_session_id"] = sp.ClaudeSessionID
		}
		if sp.Identity.PanePID > 0 {
			fields["row_pane_pid"] = sp.Identity.PanePID
		}
	}
	_ = trail.Emit(ctx, "ad.hook.ignored", fields)
	if err == nil && paneIsGrandparent(pp, h, sp) {
		emitPaneIsGrandparent(ctx, pp, h, sp.Identity.PanePID, fields["parent_command"])
	}
}

// paneIsGrandparent reports whether sp's recorded pane process is the parent
// of h's parent (b.9n6, b.zde): the hook's parent is then a child of the pane
// process, not the pane process itself, so SR-22.9 refuses every hook of the
// agent with pid_mismatch. It does not say why; a `claude` launcher that does
// not exec and a hook run through a shell instead of in exec form look exactly
// alike here.
//
// It checks only a SessionStart refused with pid_mismatch on a row whose state
// is pending; for any other hook it answers false with no read. Only then, with
// a non-nil pp, a parent pid and a recorded pane_pid, does it read the parent's
// parent pid, once, through pp.PPID, and compare it with the row's pane_pid. A
// failed read answers false. It never changes the gate's decision.
func paneIsGrandparent(pp ParentProc, h ignoredHook, sp store.Spawn) bool {
	if h.event != "SessionStart" || h.reason != store.HookReasonPIDMismatch || sp.State != store.StatePending {
		return false
	}
	if pp == nil || h.parent.pid <= 0 || sp.Identity.PanePID <= 0 {
		return false
	}
	ppid, ok := pp.PPID(h.parent.pid)
	return ok && ppid == sp.Identity.PanePID
}

// paneIsGrandparentAdvice is ad.hook.pane_is_grandparent's advice: what the
// operator changes so later launches are tracked. It names both causes
// paneIsGrandparent cannot tell apart (a non-exec launcher, or a shell-form
// hook) and asserts neither. Callers branch on the event name; the advice is
// an English extra. It never advises ending a session by hand.
const paneIsGrandparentAdvice = "The parent of this agent's hooks is a child of its pane process, not the pane process itself, " +
	"so agent-director ignores every hook of this agent (pid_mismatch). " +
	"Either the claude on PATH is a launcher that runs Claude Code as a child instead of exec-ing it " +
	"(replace it with the native claude binary or a wrapper that execs it), " +
	"or Claude Code ran the hook through a shell instead of in exec form; parent_command names that parent."

// emitPaneIsGrandparent writes ad.hook.pane_is_grandparent (b.9n6, b.zde) for
// a SessionStart refused with pid_mismatch on a pending row whose pane process,
// panePID, is the hook's grandparent (paneIsGrandparent). The event states only
// that: the hook's parent is not the pane process, and the pane process is
// the parent's parent. It does not tell a non-exec launcher from a shell-form
// hook; the advice names both. It keeps no record of having warned:
// SessionStart fires once per launch and again on /clear, compact and resume,
// and each such refused SessionStart writes one. Its fields:
// claude_instance_id, pane_pid, pane_command (read now, through pp; null when
// unreadable), parent_pid and parent_command (ad.hook.ignored's values),
// advice (paneIsGrandparentAdvice) and source ad_hook. It reads no store.
// Fail-open: a trail-write failure changes nothing; the gate's decision, the
// row and the hook's exit are those of any ignored hook.
func emitPaneIsGrandparent(ctx context.Context, pp ParentProc, h ignoredHook, panePID int, parentCommand any) {
	fields := map[string]any{
		"claude_instance_id": h.instanceID,
		"pane_pid":           panePID,
		"pane_command":       nil,
		"parent_pid":         h.parent.pid,
		"parent_command":     parentCommand,
		"advice":             paneIsGrandparentAdvice,
		"source":             "ad_hook",
	}
	if name, ok := pp.CommandName(panePID); ok && name != "" {
		fields["pane_command"] = name
	}
	_ = trail.Emit(ctx, "ad.hook.pane_is_grandparent", fields)
}
