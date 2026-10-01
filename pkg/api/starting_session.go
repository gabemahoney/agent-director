package api

import (
	"fmt"
	"strconv"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/tmux"
)

// This file holds the shared starting-session refusal (SR-4.2, SR-1.2,
// SR-1.4): it runs tmux.StartingSession on a row the verb examined as
// finished and builds the refusal for its outcome. resume's pre-launch check
// (decidePreLaunch) and its re-lookup after "duplicate session"
// (heldNameOutcome, with the restore's sentence as the row's Consequence) use
// it. Its later users are reuse (the old row's lookup and its re-lookup) and
// kill's finished-row opt-in, which takes steps 1 and 2 (unavailableError)
// and replaces step 3 with its reported-in rule (SR-6.5, SR-6.7). It makes no
// tmux call, no store read or write, no trail write and no log line: a
// refusal is reported by its error alone (SR-4.3). No description names a session-ending command, another
// row's id or a label value, and the stopping texts never say "dead" or
// "gone".

// startingSessionLimits holds the effective starting-session bound and
// stopping window the rule uses (SR-4.1).
type startingSessionLimits struct {
	// Bound is the starting-session bound (starting_session_seconds).
	Bound time.Duration
	// Window is the stopping window (stopping_window_seconds).
	Window time.Duration
}

// startingSessionLimitsOf reads the bound and the window through config.Tmux's
// accessors, the only source of their defaults (SR-4.1), for a caller holding
// a config.Config (resume, reuse). The exported Kill passes its parameters
// straight through instead.
func startingSessionLimitsOf(t config.Tmux) startingSessionLimits {
	return startingSessionLimits{Bound: t.EffectiveStartingSession(), Window: t.EffectiveStoppingWindow()}
}

// startingSessionRow holds the facts of the row as the verb examined it.
type startingSessionRow struct {
	// InstanceID is the row's instance id.
	InstanceID string
	// Name is the session name the caller looked up or asked tmux to
	// create, quoted in every description: resume's recorded name, reuse's
	// requested name at its re-lookup. Never a listing's stored form.
	Name string
	// EndedAt is the ended_at the verb examined before its move or reset,
	// never a re-read; nil when NULL (or unparseable in reuse's read).
	EndedAt *time.Time
	// RecordsPID reports that the row records a pid (positive).
	RecordsPID bool
	// RecordsSessionID reports that the row records a session id.
	RecordsSessionID bool
	// Consequence is the sentence saying what the caller's state is, in the
	// cantTellRefusal.Consequence style: "" means nothingWasDone (a refusal
	// before any write). A refusal that follows writes states what was done
	// instead: resume after "duplicate session" passes its restore's row
	// sentence (resumeRestoreResultOf).
	Consequence string
}

// consequence returns r's consequence sentence, nothingWasDone by default.
func (r startingSessionRow) consequence() string {
	if r.Consequence == "" {
		return nothingWasDone
	}
	return r.Consequence
}

// startingSessionCheck is a classified row: the facts, and the rule's
// classification of them.
type startingSessionCheck struct {
	row   startingSessionRow
	class tmux.StartingSessionClass
}

// checkStartingSession classifies row with the starting-session rule (SR-4.2).
// session is the lookup's Ours session, or nil for Gone while the row's agent
// process still runs; now is the verb's injected instant, read once after the
// lookup returned.
func checkStartingSession(lim startingSessionLimits, now time.Time, row startingSessionRow, session *tmux.Session) startingSessionCheck {
	return startingSessionCheck{row: row, class: tmux.StartingSession(tmux.StartingSessionInput{
		EndedAt:          row.EndedAt,
		RecordsPID:       row.RecordsPID,
		RecordsSessionID: row.RecordsSessionID,
		Session:          session,
		Now:              now,
		Bound:            lim.Bound,
		Window:           lim.Window,
	})}
}

// refusal returns the refusal for every outcome: unavailableError for still
// stopping or still starting, ownIDConflictError past both (resume, reuse).
func (c startingSessionCheck) refusal() error {
	if err := c.unavailableError(); err != nil {
		return err
	}
	return c.ownIDConflictError()
}

// unavailableError is steps 1 and 2's tmux.ErrTmuxUnresponsive (SR-1.4), nil
// past both, so kill's opt-in never builds the own-id conflict:
//
//   - still stopping, session present: the quoted name; "appears to still be
//     stopping"; that the row ended less than the stopping window ago (its
//     effective value in seconds) and its agent has not yet exited;
//     "retry later";
//   - still stopping, no session: the instance id; "appears to still be
//     stopping"; that the agent process still runs although no session of
//     its launch was found; "retry later";
//   - still starting: the quoted name; "appears to still be starting".
func (c startingSessionCheck) unavailableError() error {
	r := c.row
	switch {
	case c.class.Outcome == tmux.StillStopping && c.class.SessionPresent:
		return fmt.Errorf("%w: instance %s: its session %s appears to still be stopping: the row ended less than the stopping window of %s ago and its agent has not yet exited; %s; %s",
			tmux.ErrTmuxUnresponsive, r.InstanceID, strconv.Quote(r.Name), inSeconds(c.class.Window), r.consequence(), retryLater)
	case c.class.Outcome == tmux.StillStopping:
		return fmt.Errorf("%w: instance %s appears to still be stopping: the row ended less than the stopping window of %s ago and its agent process still runs although no session of its launch was found; %s; %s",
			tmux.ErrTmuxUnresponsive, r.InstanceID, inSeconds(c.class.Window), r.consequence(), retryLater)
	case c.class.Outcome == tmux.StillStarting:
		return fmt.Errorf("%w: instance %s: its session %s appears to still be starting: it has run for less than the starting-session bound of %s; %s; %s",
			tmux.ErrTmuxUnresponsive, r.InstanceID, strconv.Quote(r.Name), inSeconds(c.class.Bound), r.consequence(), retryLater)
	}
	return nil
}

// ownIDConflictError is step 3's tmux.ErrTmuxSessionConflict (SR-1.4 row
// "own old session"; GAP 2): the quoted name; "this row's own id"; that the
// session has run for at least the starting-session bound, or, with no
// session, that no session of its launch was found while the row's agent
// process still runs; only when the window was checked, that the row ended at
// least the stopping window ago (no statement of when the row ended
// otherwise, SRD-RR2 T13); that the agent may be hung or running on a row
// wrongly marked finished, no automated action on it is safe and a human must
// look, with the pointer to "Operator actions"; when the row records a session
// id, that the conversation stays resumable; and "list --tmux-session-name".
// Built on its own only past both.
func (c startingSessionCheck) ownIDConflictError() error {
	r := c.row
	why := "it has run for at least the starting-session bound of " + inSeconds(c.class.Bound)
	if !c.class.SessionPresent {
		why = "no session of its launch was found while the row's agent process still runs"
	}
	if c.class.WindowChecked {
		why += ", and the row ended at least the stopping window of " + inSeconds(c.class.Window) + " ago"
	}
	resumable := ""
	if r.RecordsSessionID {
		resumable = "; the conversation stays resumable"
	}
	return fmt.Errorf("%w: instance %s: session name %s: this row's own id: %s; the agent may be hung or running on a row wrongly marked finished, so no automated action on it is safe and a human must look, %s; %s%s; %s",
		tmux.ErrTmuxSessionConflict, r.InstanceID, strconv.Quote(r.Name), why, operatorActionsPointer, r.consequence(),
		resumable, listSessionNameHint)
}
