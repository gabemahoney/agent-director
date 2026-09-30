package store

import (
	"database/sql"
	"errors"
	"fmt"
)

// HookGate is what a hook write is conditioned on (SR-22.9; Appendix F.4).
// Every event, SessionStart included: ParentPID and ParentStart must equal the
// row's pane_pid and pane_starttime, or pane_starttime is NULL and the write
// records ParentStart. The store makes the gate part of each write's own
// statement, so no write can land between a check and the write.
type HookGate struct {
	Event        string      // the Claude Code hook event name
	ParentPID    int         // os.Getppid() of the exec-form hook: the agent process
	ParentStart  string      // its start time from ProcChecker.StartTime; "" when unreadable (never matches then)
	SessionID    string      // recorded, not a gate: SessionStart's new id; an ordinary hook's when the row has none
	SessionStart bool        // true for SessionStart: Examined must still match (SR-5.3)
	Examined     RowSnapshot // SessionStart only
}

// HookApplied says whether a gated hook write applied (SR-22.9; Appendix
// F.4). When it did not, the hook handler writes ad.hook.ignored with Reason
// (pid_mismatch, no_pane_recorded or subagent_event), ParentPID and the
// parent's command; no_exec_form is written by the no-verb path, never here
// (WD 2026-09-30b). A store write reports only HookReasonPIDMismatch or
// HookReasonNoPaneRecorded: subagent_event is decided by the handler from the
// payload, before any write. Reason is "" when no row has the id (nothing to
// report) or, for SessionStart, when the row changed after the caller examined
// it.
type HookApplied struct {
	Applied bool
	Reason  string
}

// The four reasons of SR-14's ad.hook.ignored: a hook that SR-22.9 did not
// apply. The first two come from a gated store write; the last two are
// decided before any store write (WD 2026-09-30b).
const (
	// HookReasonPIDMismatch: the hook's parent process, with its start time,
	// is not the row's recorded pane process (or its start time could not be
	// read).
	HookReasonPIDMismatch = "pid_mismatch"
	// HookReasonNoPaneRecorded: the row records no pane yet (a lost create
	// reply not yet adopted, SR-3.6), so no hook matches it.
	HookReasonNoPaneRecorded = "no_pane_recorded"
	// HookReasonSubagentEvent: a SessionStart or SessionEnd whose payload
	// carries a non-empty agent_id (a subagent or an in-process teammate). It
	// changes nothing, even from the pane process.
	HookReasonSubagentEvent = "subagent_event"
	// HookReasonNoExecForm: a no-verb run that received a hook payload on
	// standard input, from a Claude Code that does not run exec-form hooks.
	// Written with no store access.
	HookReasonNoExecForm = "no_exec_form"
)

// hookGateSQL is the parent-process gate (SR-22.9): a WHERE fragment, joined
// with AND, whose placeholders take hookGateArgs in order. It holds when the
// row's pane_pid is the hook's parent pid and its pane_starttime is the
// parent's start time or NULL (then the pid alone decides and the write
// records the start time through hookPaneStartSet). An unreadable parent start
// time ("") never matches, whatever the row holds; a row with no pane_pid
// matches nothing.
const hookGateSQL = `pane_pid = ? AND ? <> '' AND (pane_starttime = ? OR pane_starttime IS NULL)`

// hookGateArgs returns the bound arguments for hookGateSQL, in its
// placeholder order. A non-positive parent pid is bound as NULL, which no
// pane_pid equals.
func hookGateArgs(g HookGate) []any {
	return []any{positiveIntArg(g.ParentPID), g.ParentStart, g.ParentStart}
}

// hookPaneStartSet is the SET fragment every gated hook write carries: when
// the row's pane start time was unreadable at the create (NULL), the first
// applied hook records its parent's (SR-22.9). Its placeholder takes
// HookGate.ParentStart, which is never "" when the gate holds.
const hookPaneStartSet = `pane_starttime = COALESCE(pane_starttime, ?)`

// hookGateRow is the one read that decides why a gated hook write did not
// apply (SR-22.9): whether the row exists, whether it records a pane, and
// whether the gate holds for it now.
type hookGateRow struct {
	found       bool
	state       string
	paneNull    bool
	gateMatches bool
}

// readHookGateRow reads instanceID's state and the gate's standing against
// the row, in one statement. It only reads. errPrefix names the write in a
// driver error.
func (s *Store) readHookGateRow(instanceID string, g HookGate, errPrefix string) (hookGateRow, error) {
	args := append(hookGateArgs(g), instanceID)
	var r hookGateRow
	err := s.db.QueryRow(`SELECT state, pane_pid IS NULL,
	                             CASE WHEN `+hookGateSQL+` THEN 1 ELSE 0 END
	                        FROM spawns WHERE claude_instance_id = ?`, args...,
	).Scan(&r.state, &r.paneNull, &r.gateMatches)
	if errors.Is(err, sql.ErrNoRows) {
		return hookGateRow{}, nil
	}
	if err != nil {
		return hookGateRow{}, fmt.Errorf("%s: gate read: %w", errPrefix, err)
	}
	r.found = true
	return r, nil
}

// notAppliedReason is the reason a gated hook write that matched no row
// reports, from the read taken after its statement (decision A1): no row
// yields no reason; a row that records no pane yields
// HookReasonNoPaneRecorded, even when the parent's start time was unreadable;
// any other row yields HookReasonPIDMismatch.
func notAppliedReason(r hookGateRow) HookApplied {
	switch {
	case !r.found:
		return HookApplied{}
	case r.paneNull:
		return HookApplied{Reason: HookReasonNoPaneRecorded}
	default:
		return HookApplied{Reason: HookReasonPIDMismatch}
	}
}

// hookNotApplied reads the row after an ordinary gated hook write matched no
// row and returns the reason (notAppliedReason). It writes nothing.
func (s *Store) hookNotApplied(instanceID string, g HookGate, errPrefix string) (HookApplied, error) {
	r, err := s.readHookGateRow(instanceID, g, errPrefix)
	if err != nil {
		return HookApplied{}, err
	}
	return notAppliedReason(r), nil
}

// hookSessionRecordSet returns the SET fragment, with its arguments, that
// records an ordinary hook's Claude session id and transcript path on a row
// that records no session id yet (SR-22.9: a lost reply adopted after its
// SessionStart was ignored catches up at the agent's next hook). A row that
// already records a session id keeps it and its path. The path follows the
// SessionStart presence rule: set when reported and present, NULL when
// reported but not yet on disk, kept when not reported. A hook with no
// session id records nothing and the fragment is empty. The fragment, when
// not empty, ends with a comma.
func hookSessionRecordSet(g HookGate, jsonlPath string, jsonlPresent bool) (string, []any) {
	if g.SessionID == "" {
		return "", nil
	}
	const noSession = `COALESCE(claude_session_id, '') = ''`
	frag := `claude_session_id = CASE WHEN ` + noSession + ` THEN ? ELSE claude_session_id END, `
	args := []any{g.SessionID}
	switch {
	case jsonlPath != "" && jsonlPresent:
		frag += `jsonl_path = CASE WHEN ` + noSession + ` THEN ? ELSE jsonl_path END, `
		args = append(args, jsonlPath)
	case jsonlPath != "":
		frag += `jsonl_path = CASE WHEN ` + noSession + ` THEN NULL ELSE jsonl_path END, `
	}
	return frag, args
}
