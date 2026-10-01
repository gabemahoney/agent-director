package api

import (
	"context"
	"strings"

	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/internal/trail"
)

// The ad.launch.name_held source values (SR-14). Plain spawn and reuse write
// nameHeldSourceSpawn; find-missing's sweep writes nameHeldSourceFindMissing
// after a mark attempt whose tick reason is tmux_name_held (SR-11.3).
// nameHeldSourceFindMissing is also the source of every other record the
// sweep writes (ad.find_missing.tick, ad.provenance.disagree), and
// nameHeldSourceResume the source of every record resume writes
// (ad.resume.*, ad.provenance.disagree): pkg/api declares each value only
// here.
const (
	nameHeldSourceSpawn       = "ad_spawn"
	nameHeldSourceFindMissing = "ad_find_missing"
	nameHeldSourceResume      = "ad_resume"
)

// The ad.launch.name_held launch values (SR-14): which launch's create
// reported "duplicate session". Reuse (Epic 17) and resume (Epic 16) add
// theirs here; the sweep writes none (null).
const (
	nameHeldLaunchSpawn = "spawn"
)

// The ad.launch.name_held row_result values (SR-14). Plain spawn's
// conditional end write after "duplicate session" gives one of the first
// three (SR-5.8, SR-9.4); the label scan's refusal, which runs before the
// insert, gives nameHeldRowNotInserted (SR-9.3). find-missing's guarded mark
// with tick reason tmux_name_held gives nameHeldRowMarkedMissing when it
// applied, nameHeldRowLeftChanged when the same-life guard did not apply, and
// nameHeldRowStillPending (with store_error) on a store error (SR-11.3,
// SR-11.6, SR-5.8). nameHeldRowMarkedMissing is also find-missing's
// ad.provenance.disagree action for a marked row (findMissingActionMarked):
// pkg/api declares the value only here.
const (
	nameHeldRowEnded         = "ended"
	nameHeldRowLeftChanged   = "left_changed"
	nameHeldRowStillPending  = "still_pending"
	nameHeldRowNotInserted   = "not_inserted"
	nameHeldRowMarkedMissing = "marked_missing"
)

// nameHeld is one ad.launch.name_held record's content (SR-14): everything
// the record carries besides what emitNameHeld derives (carries_this_id,
// current_launch, the by-hand commands). It never holds a label's value, the
// id a label names, another store's id, another row's id or any
// session-environment content (SR-15, SR-3.12).
type nameHeld struct {
	// Source is the trail source (nameHeldSourceSpawn,
	// nameHeldSourceFindMissing, ...).
	Source string
	// Launch is the launch kind (nameHeldLaunchSpawn, ...); "" writes null
	// (find-missing's sweep, which launches nothing).
	Launch string
	// InstanceID is the launch's claude_instance_id.
	InstanceID string
	// Name is the requested tmux session name, written as a plain JSON
	// string.
	Name string
	// Socket is the socket the launch's calls used.
	Socket string
	// StoreID is this store's store_meta.store_id as the Client read it,
	// never a label's.
	StoreID string
	// Holder is the holder facts (heldHolderFacts) of the lookup that saw the
	// name held: plain spawn's re-lookup, or find-missing's per-row lookup
	// with the row's recorded name as the holder name. When no single
	// holder was identified the session id, creation time and both commands
	// are null; a zero Class makes carries_this_id and current_launch null.
	Holder heldNameHolder
	// LookupOutcome is the lookup Result's token unchanged (tmux.Result.Token).
	LookupOutcome string
	// Err is the error the verb returned, named through errorName; nil
	// writes null (find-missing's sweep, which returns no per-row error).
	Err error
	// RowResult is the row's result (nameHeldRowEnded, ...); "" writes null.
	RowResult string
	// StoreError is the failed store write's error; nil writes null.
	StoreError error
	// LeftoverCount is the label scan's leftover count (SR-9.3), written as
	// leftover_count only when positive: no other source writes the field.
	LeftoverCount int
	// Caller is the invoking process's identity, collected inside
	// agent-director (callerIdentity).
	Caller caller
}

// emitNameHeld writes one ad.launch.name_held record (SR-14, SR-15, SR-3.10,
// SR-3.12), fail-open: a trail-write failure is discarded and never changes
// the verb's result, error or description (the ad.kill.called pattern). It is
// the one emitter of the event: plain spawn's label scan (pkg/api
// spawn_scan.go) and its "duplicate session" path call it, and find-missing
// calls it once per row after a mark attempt whose tick reason is
// tmux_name_held (SR-11.3) with source nameHeldSourceFindMissing, no launch
// kind and no error (launch and outcome null), row_result
// nameHeldRowMarkedMissing, nameHeldRowLeftChanged or nameHeldRowStillPending,
// and the Client's store id; resume (Epic 16) and reuse (Epic 17) add their
// sources through it.
//
// carries_this_id and current_launch come from the holder's label class
// only, never the environment (SR-3.12): an old or current label of this
// store carries this id, a foreign, another store's or no valid label does
// not (another store's label counts as not carrying it even when it names
// the same id), and a class not trusted (zero) gives null for both;
// current_launch is true for current, false for old, null otherwise. The
// by-hand attach_command and end_command, for humans reading the trail and
// never in an error description (SR-1.4), are present exactly when a holder
// was identified, whatever its class, with the socket and the holder's
// session id each shell-quoted (shellQuote), and null otherwise.
func emitNameHeld(r nameHeld) {
	var launch, outcome, rowResult, storeError any
	if r.Launch != "" {
		launch = r.Launch
	}
	if r.Err != nil {
		outcome = errorName(r.Err)
	}
	if r.RowResult != "" {
		rowResult = r.RowResult
	}
	if r.StoreError != nil {
		storeError = r.StoreError.Error()
	}
	var sessionID, created, attach, end any
	if r.Holder.Identified {
		sessionID = r.Holder.SessionID
		created = r.Holder.Created
		attach = "tmux -u -S " + shellQuote(r.Socket) + " attach-session -r -t " + shellQuote(r.Holder.SessionID)
		end = "tmux -u -S " + shellQuote(r.Socket) + " kill-session -t " + shellQuote(r.Holder.SessionID)
	}
	var carries, current any
	if r.Holder.Identified {
		switch r.Holder.Class {
		case tmux.ClassCurrent:
			carries, current = true, true
		case tmux.ClassOld:
			carries, current = true, false
		case tmux.ClassForeign, tmux.ClassOtherStore, tmux.ClassNone:
			carries = false
		}
	}
	fields := map[string]any{
		"source":             r.Source,
		"claude_instance_id": r.InstanceID,
		"launch":             launch,
		"tmux_session_name":  r.Name,
		"tmux_socket":        r.Socket,
		"tmux_session_id":    sessionID,
		"session_created":    created,
		"store_id":           r.StoreID,
		"carries_this_id":    carries,
		"current_launch":     current,
		"lookup_outcome":     r.LookupOutcome,
		"outcome":            outcome,
		"row_result":         rowResult,
		"store_error":        storeError,
		"attach_command":     attach,
		"end_command":        end,
		"caller_process":     r.Caller.process,
		"caller_pid":         r.Caller.pid,
		"caller_hostname":    r.Caller.hostname,
		"caller_user":        r.Caller.user,
	}
	if r.LeftoverCount > 0 {
		fields["leftover_count"] = r.LeftoverCount
	}
	_ = trail.Emit(context.Background(), "ad.launch.name_held", fields)
}

// shellQuote quotes s for a POSIX shell: it wraps s in single quotes and
// writes each single quote inside s as a closing quote, a backslash-escaped
// quote and an opening quote, so a by-hand command is copied exactly (SR-14).
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
