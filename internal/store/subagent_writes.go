package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"unicode/utf8"
)

// The gated entry and claimable-launch writes of a hook event, and the
// transitions of SR-7 (b.xjn). Everything here is agent-neutral: the events are
// a subagent's start and stop, a launch's details, a launch request, a launch
// failure and the main agent's turn end. The hook package maps an agent's
// payloads onto them.
//
// Each function runs on the caller's connection inside the caller's BEGIN
// IMMEDIATE transaction (subagent_hook_writes.go), so the write lock is held
// before the first read. A read of an entry decides the transition; it runs
// after the lock is taken, and every statement that creates, changes or deletes
// an entry or a claimable launch carries the hook gate and the four row
// conditions of SR-3.1 in its own WHERE (subagentGateExists), the current
// launch decided by the row's launch_token read in that statement. A statement
// that finds the gate no longer holding writes nothing.
//
// The store reads no clock: each event carries its receipt time in integer
// milliseconds. The store keeps no trail record and logs no content; errors are
// wrapped with context.

// SubagentGraceMs is the grace of SR-7.5, a fixed constant: a claimable launch
// closes at a main-agent Stop only when its opened_at is strictly more than this
// many milliseconds (2 s) before the Stop's receipt time. A snapshot's close of
// background entries uses the same constant: an entry opened exactly this long
// before the Stop is not closed.
const SubagentGraceMs int64 = 2000

// SubagentStringMaxBytes is the bound of SR-5.3: every string a hook takes from
// a payload is cut to at most this many bytes (1 KiB) before it is compared,
// matched or stored.
const SubagentStringMaxBytes = 1024

// CutSubagentString returns s unchanged when it is at most
// SubagentStringMaxBytes bytes, and otherwise its longest prefix of whole UTF-8
// characters within SubagentStringMaxBytes bytes, so no stored string ends in
// part of a character and a long value always matches what was stored from it
// (SR-5.3). The store applies it to every payload-derived string of an event
// (ids, session id, types, description, model, tool_use_id, launcher id) on
// entry; a caller needs no cut of its own. The instance id comes from the row
// and is never cut.
func CutSubagentString(s string) string {
	if len(s) <= SubagentStringMaxBytes {
		return s
	}
	n := SubagentStringMaxBytes
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// SubagentLifecycleKind names the two lifecycle events of a subagent.
type SubagentLifecycleKind string

const (
	// SubagentLifecycleStart: the agent reported a subagent started (SR-7.1).
	SubagentLifecycleStart SubagentLifecycleKind = "start"
	// SubagentLifecycleStop: the agent reported a subagent finished (SR-7.2).
	SubagentLifecycleStop SubagentLifecycleKind = "stop"
)

// SubagentLifecycleEvent is a subagent's start or stop, as ApplySubagentLifecycle
// takes it. An empty SubagentID makes the event only the row's soft refresh: no
// entry write, no claim (SR-3.2). Strings are cut to SubagentStringMaxBytes on
// entry (CutSubagentString); an empty string is "not supplied".
type SubagentLifecycleEvent struct {
	Kind SubagentLifecycleKind
	// SubagentID is the subagent's id (never cut short of its 1 KiB bound).
	SubagentID string
	// SessionID (start only) is the session id of the event; an entry the start
	// creates records it and never changes it (SR-6.3).
	SessionID string
	// Type (start only) is the subagent's type. A start that creates an entry
	// and supplies a type claims a claimable launch of that type (SR-4.5); one
	// without a type never claims and creates an untyped observation.
	Type string
	// SupersedesBlockedStop (stop only) says the stop carries the flag that an
	// earlier stop of this subagent was blocked by another stop hook, so this
	// stop's receipt time replaces any stopped_at of a stopped entry (SR-7.2).
	SupersedesBlockedStop bool
	// Listing (stop only; nil or empty is no listing) is the stop's list of
	// running background subagents (SR-7.6). After the stop, whatever the stop
	// did (it may have changed nothing, SR-4.4), each listed id with an entry of
	// any launch of the current life is confirmed and moved to the current
	// launch, and a null run_mode becomes background (source snapshot). A listing
	// never creates, closes, reopens or retypes an entry, never changes a state or a time, and
	// is never applied for an empty SubagentID. Only each id is used; a listed
	// Type is ignored. No description is ever an input.
	Listing []SubagentListed
	// ReceivedMs is the event's receipt time in milliseconds since the epoch.
	ReceivedMs int64
}

// SubagentLaunchOutcome is what a launch's result says happened to the
// subagent it launched.
type SubagentLaunchOutcome string

const (
	// SubagentLaunchOther (the zero value): any other result. It changes no state
	// or time and creates no entry.
	SubagentLaunchOther SubagentLaunchOutcome = ""
	// SubagentLaunchBackgroundStarted: the subagent was launched and runs on in
	// the background: a start (SR-7.3), run_mode background.
	SubagentLaunchBackgroundStarted SubagentLaunchOutcome = "background_started"
	// SubagentLaunchForegroundFinished: the subagent ran to its end within the
	// launching call: a stop (SR-7.3), run_mode foreground.
	SubagentLaunchForegroundFinished SubagentLaunchOutcome = "foreground_finished"
)

// SubagentLaunchDetails is a launch's result (SR-7.3): the details of the
// subagent a launching call started. Strings are cut on entry; an empty string
// is "not supplied".
type SubagentLaunchDetails struct {
	// ToolUseID is the launching call's id. The open claimable launch with it
	// closes, whatever else the event does.
	ToolUseID string
	// SubagentID is the launched subagent's id. Empty (a teammate, say): the
	// event only closes the claimable launch.
	SubagentID string
	// LauncherID is the id of the subagent that made the call; empty when the
	// agent itself did. parent_type is subagent with this id when it is set, and
	// agent with the instance id otherwise (SR-6.3).
	LauncherID  string
	SessionID   string // recorded only by the event that creates the entry
	Type        string
	Description string
	Model       string
	Outcome     SubagentLaunchOutcome
	ReceivedMs  int64
}

// SubagentLaunchRequest is a launching call about to run (SR-4.5, SR-7.8). An
// empty ToolUseID or Type opens no claimable launch.
type SubagentLaunchRequest struct {
	ToolUseID  string
	Type       string
	ReceivedMs int64
}

// SubagentLaunchFailure is a launching call that failed: it closes the
// claimable launch with ToolUseID and does nothing else.
type SubagentLaunchFailure struct {
	ToolUseID string
}

// SubagentListed is one subagent a snapshot or a listing names: its id and the
// type the agent gave it, "" when it gave none. Both are cut to
// SubagentStringMaxBytes on entry. The store is given only these two values: no
// description of a listed subagent is ever an input (SR-6.3, N13).
type SubagentListed struct {
	ID   string
	Type string
}

// SubagentSnapshot is a main-agent Stop's list of running background subagents
// (SR-7.4): the Stop's `background_tasks` array, present. Present with no listed
// subagent is still a snapshot and can close entries (SR-7.5).
type SubagentSnapshot struct {
	// SessionID is the Stop's session id, recorded by the entries the snapshot
	// creates and never changed afterwards; "" records none.
	SessionID string
	// Listed are the listed subagents in array order. An empty id is dropped; ids
	// repeated after the cut are applied once, the first in array order, whose Type
	// types an entry the snapshot creates.
	Listed []SubagentListed
}

// SubagentTurnEnd is the main agent's turn end (a Stop without a subagent id): it
// closes the current launch's claimable launches opened more than SubagentGraceMs
// before ReceivedMs (SR-4.5) and, when Snapshot is set, applies it (SR-7.4,
// SR-7.5) in the same transaction.
type SubagentTurnEnd struct {
	ReceivedMs int64
	// Snapshot is the Stop's background_tasks, nil when the Stop carries none (the
	// field is absent or is not an array): then no entry is created, confirmed,
	// filled or closed, and only the claimable launches close.
	Snapshot *SubagentSnapshot
}

// errSubagentWriteRefused rolls a transaction back when a gated insert that
// followed an eviction or a claim inserted nothing: an eviction or a claim never
// stands without its insert.
var errSubagentWriteRefused = errors.New("store: subagent entry write refused after its eviction or claim")

// subagentEntry is one subagents row as the transitions read and write it.
type subagentEntry struct {
	createdSeq      int64
	launchToken     string
	confirmed       bool
	sessionID       sql.NullString
	subagentType    sql.NullString
	description     sql.NullString
	model           sql.NullString
	parentType      sql.NullString
	parentID        sql.NullString
	toolUseID       sql.NullString
	runMode         sql.NullString
	firstStartedAt  sql.NullInt64
	startedAt       sql.NullInt64
	stoppedAt       sql.NullInt64
	lostAt          sql.NullInt64
	state           string
	closeReason     sql.NullString
	openedAt        int64
	activityAt      int64
	typeSource      sql.NullString
	runModeSource   sql.NullString
	stoppedAtSource sql.NullString
}

const subagentEntryColumns = `created_seq, launch_token, confirmed, session_id, subagent_type,
       description, model, parent_type, parent_id, tool_use_id, run_mode,
       first_started_at, started_at, stopped_at, lost_at, state, close_reason,
       opened_at, activity_at, subagent_type_source, run_mode_source, stopped_at_source`

// sourceRank orders the sources of SR-6.3 for the fields that have more than
// one: type start > details > snapshot, run_mode details > snapshot, stopped_at
// stop > details. A start and a stop never compete (different fields), so one
// order serves all. An unknown or NULL source ranks lowest. A snapshot or a
// listing is the snapshot source: it types only an entry it creates and fills
// only a null run_mode (applyListed).
func sourceRank(src string) int {
	switch src {
	case SubagentSourceStart, SubagentSourceStop:
		return 3
	case SubagentSourceDetails:
		return 2
	case SubagentSourceSnapshot:
		return 1
	}
	return 0
}

// applyRanked applies v from src to a field with more than one source (SR-6.3):
// it fills an unknown value, and replaces one only from a higher-precedence
// source; a source never replaces a value it set itself. An unsupplied v ("")
// changes nothing.
func applyRanked(val, valSource *sql.NullString, v, src string) {
	if v == "" {
		return
	}
	if !val.Valid || sourceRank(src) > sourceRank(valSource.String) {
		*val = sql.NullString{String: v, Valid: true}
		*valSource = sql.NullString{String: src, Valid: true}
	}
}

// applyFirst applies v to a field with one source (SR-6.3): it keeps its first
// value.
func applyFirst(val *sql.NullString, v string) {
	if v != "" && !val.Valid {
		*val = sql.NullString{String: v, Valid: true}
	}
}

// earlier sets *t to at when it is null or later: first_started_at only moves
// earlier (SR-6.5).
func earlier(t *sql.NullInt64, at int64) {
	if !t.Valid || t.Int64 > at {
		*t = sql.NullInt64{Int64: at, Valid: true}
	}
}

func nullStr(s string) sql.NullString { return sql.NullString{String: s, Valid: s != ""} }
func millis(v int64) sql.NullInt64    { return sql.NullInt64{Int64: v, Valid: true} }

// subagentTx runs the transitions for one hook event of one row on the
// caller's connection, inside the caller's BEGIN IMMEDIATE transaction.
type subagentTx struct {
	ctx        context.Context
	conn       *sql.Conn
	q          querier
	instanceID string
	gate       HookGate
	// token is the row's launch_token read at the start of the event, after the
	// write lock; "" with ok false when the row has none (the statements then
	// write nothing, since the gate's launch_token condition fails). It only
	// decides which transition applies: every write reads the token again in its
	// own statement.
	token   string
	tokenOK bool
}

// newSubagentTx begins the event's reads on conn.
func newSubagentTx(ctx context.Context, conn *sql.Conn, instanceID string, gate HookGate) (*subagentTx, error) {
	t := &subagentTx{ctx: ctx, conn: conn, q: connQuerier{ctx: ctx, conn: conn}, instanceID: instanceID, gate: gate}
	var tok sql.NullString
	err := conn.QueryRowContext(ctx, `SELECT launch_token FROM spawns WHERE instance_id = ?`, instanceID).Scan(&tok)
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return nil, fmt.Errorf("store: subagent launch token: %w", err)
	default:
		t.token, t.tokenOK = tok.String, tok.Valid && tok.String != ""
	}
	return t, nil
}

// exec runs one statement and returns its rows affected.
func (t *subagentTx) exec(what, stmt string, args ...any) (int64, error) {
	res, err := t.conn.ExecContext(t.ctx, stmt, args...)
	if err != nil {
		return 0, fmt.Errorf("store: %s: %w", what, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("store: %s rows affected: %w", what, err)
	}
	return n, nil
}

// readEntry reads the entry (instance id, subagentID) of any launch of the
// row's current life. An entry held only by an earlier life is absent.
func (t *subagentTx) readEntry(subagentID string) (subagentEntry, bool, error) {
	var (
		e         subagentEntry
		confirmed int
	)
	err := t.conn.QueryRowContext(t.ctx, `SELECT `+subagentEntryColumns+`
	    FROM subagents WHERE instance_id = ? AND subagent_id = ?
	      AND life_number = (SELECT sp.life_number FROM spawns sp WHERE sp.instance_id = ?)`,
		t.instanceID, subagentID, t.instanceID,
	).Scan(&e.createdSeq, &e.launchToken, &confirmed, &e.sessionID, &e.subagentType,
		&e.description, &e.model, &e.parentType, &e.parentID, &e.toolUseID, &e.runMode,
		&e.firstStartedAt, &e.startedAt, &e.stoppedAt, &e.lostAt, &e.state, &e.closeReason,
		&e.openedAt, &e.activityAt, &e.typeSource, &e.runModeSource, &e.stoppedAtSource)
	if errors.Is(err, sql.ErrNoRows) {
		return subagentEntry{}, false, nil
	}
	if err != nil {
		return subagentEntry{}, false, fmt.Errorf("store: read subagent entry: %w", err)
	}
	e.confirmed = confirmed != 0
	return e, true, nil
}

// writeEntry writes next over old when any field but activity_at differs, with
// activity_at the receipt time (SR-6.5, SR-7); an event that changes nothing
// writes nothing, activity_at included (SR-7.7). The statement is gated
// (subagentGateExists); move puts the entry into the row's current launch, read
// in the statement, and currentOnly makes the statement act only on an entry
// that already belongs to it. session_id and life_number are never written.
func (t *subagentTx) writeEntry(old, next subagentEntry, at int64, move, currentOnly bool) error {
	next.activityAt = old.activityAt
	if next == old {
		return nil
	}
	next.activityAt = at
	belongs := ""
	if currentOnly {
		belongs = "subagents"
	}
	frag, gateArgs := subagentGateExists(t.instanceID, t.gate, belongs)
	launch := `launch_token`
	var args []any
	if move {
		launch = `(SELECT sp2.launch_token FROM spawns sp2 WHERE sp2.instance_id = ?)`
		args = append(args, t.instanceID)
	}
	args = append(args, boolInt(next.confirmed), next.subagentType, next.description, next.model,
		next.parentType, next.parentID, next.toolUseID, next.runMode,
		next.firstStartedAt, next.startedAt, next.stoppedAt, next.lostAt, next.state, next.closeReason,
		next.openedAt, next.activityAt, next.typeSource, next.runModeSource, next.stoppedAtSource,
		old.createdSeq, t.instanceID)
	args = append(args, gateArgs...)
	_, err := t.exec("update subagent entry", `UPDATE subagents
	   SET launch_token = `+launch+`, confirmed = ?, subagent_type = ?, description = ?, model = ?,
	       parent_type = ?, parent_id = ?, tool_use_id = ?, run_mode = ?,
	       first_started_at = ?, started_at = ?, stopped_at = ?, lost_at = ?, state = ?, close_reason = ?,
	       opened_at = ?, activity_at = ?, subagent_type_source = ?, run_mode_source = ?, stopped_at_source = ?
	 WHERE created_seq = ? AND instance_id = ? AND `+frag, args...)
	return err
}

// insertEntry creates e, an entry of the row's current life and launch, with
// the subagent id: one gated insert-select from the spawns row reads both
// life_number and launch_token. It reports whether a row was inserted.
func (t *subagentTx) insertEntry(subagentID string, e subagentEntry) (bool, error) {
	args := []any{subagentID, boolInt(e.confirmed), e.sessionID, e.subagentType, e.description, e.model,
		e.parentType, e.parentID, e.toolUseID, e.runMode,
		e.firstStartedAt, e.startedAt, e.stoppedAt, e.lostAt, e.state, e.closeReason,
		e.openedAt, e.activityAt, e.typeSource, e.runModeSource, e.stoppedAtSource}
	args = append(args, subagentRowGateArgs(t.instanceID, t.gate)...)
	n, err := t.exec("insert subagent entry", `INSERT INTO subagents
	    (instance_id, subagent_id, launch_token, confirmed, session_id, subagent_type, description, model,
	     parent_type, parent_id, tool_use_id, run_mode, first_started_at, started_at, stopped_at, lost_at,
	     state, close_reason, opened_at, activity_at, subagent_type_source, run_mode_source, stopped_at_source,
	     life_number)
	SELECT sp.instance_id, ?, sp.launch_token, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, sp.life_number
	  FROM spawns sp WHERE `+subagentRowGateSQL, args...)
	return n > 0, err
}

// create makes room for and inserts a new entry of the current life and launch:
// the gated entry-cap eviction first, then (claimType not empty) the gated claim
// of the oldest open claimable launch of the current launch with that type, which
// confirms the entry and fills no field, then the gated insert. When the insert
// inserts nothing after an eviction or a claim happened, it returns
// errSubagentWriteRefused so the caller's transaction rolls back whole.
func (t *subagentTx) create(subagentID string, e subagentEntry, claimType string) error {
	evicted, err := evictSubagentEntryGated(t.q, t.instanceID, t.gate)
	if err != nil {
		return err
	}
	claimed := false
	if claimType != "" {
		if claimed, err = t.claim(claimType); err != nil {
			return err
		}
	}
	e.confirmed = e.confirmed || claimed
	inserted, err := t.insertEntry(subagentID, e)
	if err != nil {
		return err
	}
	if !inserted && (evicted || claimed) {
		return errSubagentWriteRefused
	}
	return nil
}

// claim deletes the oldest open claimable launch of the current launch with
// subagent type typ (opened_at, then created_seq) and reports whether it did
// (SR-4.5). One gated statement; a claimable launch of another launch is never
// claimed.
func (t *subagentTx) claim(typ string) (bool, error) {
	frag, gateArgs := subagentGateExists(t.instanceID, t.gate, "subagent_launches")
	args := append([]any{t.instanceID, typ, t.instanceID}, gateArgs...)
	n, err := t.exec("claim subagent launch", `DELETE FROM subagent_launches
	  WHERE created_seq = (
	        SELECT l.created_seq FROM subagent_launches l
	         WHERE l.instance_id = ? AND l.subagent_type = ?
	           AND l.launch_token = (SELECT sp3.launch_token FROM spawns sp3 WHERE sp3.instance_id = ?)
	         ORDER BY l.opened_at, l.created_seq LIMIT 1)
	    AND `+frag, args...)
	return n > 0, err
}

// closeLaunch deletes the open claimable launch with toolUseID (SR-4.5), gated.
// An empty toolUseID closes nothing.
func (t *subagentTx) closeLaunch(toolUseID string) error {
	if toolUseID == "" {
		return nil
	}
	frag, gateArgs := subagentGateExists(t.instanceID, t.gate, "")
	args := append([]any{t.instanceID, toolUseID}, gateArgs...)
	_, err := t.exec("close subagent launch", `DELETE FROM subagent_launches
	  WHERE instance_id = ? AND tool_use_id = ? AND `+frag, args...)
	return err
}

// start applies a SubagentStart (SR-7.1, SR-4.5, SR-6.3).
func (t *subagentTx) start(ev SubagentLifecycleEvent) error {
	at := ev.ReceivedMs
	e, found, err := t.readEntry(ev.SubagentID)
	if err != nil {
		return err
	}
	if !found {
		n := subagentEntry{
			sessionID: nullStr(ev.SessionID), state: SubagentStateRunning,
			firstStartedAt: millis(at), startedAt: millis(at), openedAt: at, activityAt: at,
		}
		applyRanked(&n.subagentType, &n.typeSource, ev.Type, SubagentSourceStart)
		return t.create(ev.SubagentID, n, ev.Type)
	}
	n := e
	applyRanked(&n.subagentType, &n.typeSource, ev.Type, SubagentSourceStart)
	n.launchToken = t.token
	if n.state != SubagentStateRunning {
		n.state = SubagentStateRunning
		n.startedAt = millis(at)
		n.openedAt = at
		n.closeReason = sql.NullString{}
		n.lostAt = sql.NullInt64{}
	} else if !n.startedAt.Valid {
		n.startedAt = millis(at)
	}
	earlier(&n.firstStartedAt, at)
	return t.writeEntry(e, n, at, true, false)
}

// stop applies a SubagentStop (SR-7.2, SR-7.6): the stop of the current launch's
// entry, then, whatever the stop did (even nothing), the stop's listing: it
// confirms, moves and fills each listed entry of any launch of the current
// life, and never creates, closes or reopens one (applyListed).
func (t *subagentTx) stop(ev SubagentLifecycleEvent) error {
	if err := t.stopEntry(ev); err != nil {
		return err
	}
	return t.applyListed(cutListed(ev.Listing), ev.ReceivedMs, false, "")
}

// stopEntry stops the current launch's entry of ev (SR-7.2); another launch's
// entry or none changes nothing.
func (t *subagentTx) stopEntry(ev SubagentLifecycleEvent) error {
	at := ev.ReceivedMs
	e, found, err := t.readEntry(ev.SubagentID)
	if err != nil || !found || e.launchToken != t.token {
		return err
	}
	n := e
	switch e.state {
	case SubagentStateRunning, SubagentStateLost:
		n.state = SubagentStateStopped
		n.stoppedAt, n.stoppedAtSource = millis(at), nullStr(SubagentSourceStop)
		n.closeReason = nullStr(SubagentCloseStopEvent)
		n.lostAt = sql.NullInt64{}
	case SubagentStateStopped:
		if ev.SupersedesBlockedStop || !n.stoppedAt.Valid || n.stoppedAtSource.String != SubagentSourceStop {
			n.stoppedAt, n.stoppedAtSource = millis(at), nullStr(SubagentSourceStop)
		}
	}
	return t.writeEntry(e, n, at, false, true)
}

// launchDetails applies a launch result (SR-7.3, SR-6.3).
func (t *subagentTx) launchDetails(d SubagentLaunchDetails) error {
	at := d.ReceivedMs
	if err := t.closeLaunch(d.ToolUseID); err != nil {
		return err
	}
	if d.SubagentID == "" {
		return nil
	}
	parentType, parentID := SubagentParentAgent, t.instanceID
	if d.LauncherID != "" {
		parentType, parentID = SubagentParentSubagent, d.LauncherID
	}
	runMode := ""
	switch d.Outcome {
	case SubagentLaunchBackgroundStarted:
		runMode = SubagentRunModeBackground
	case SubagentLaunchForegroundFinished:
		runMode = SubagentRunModeForeground
	}
	fill := func(n *subagentEntry) {
		applyRanked(&n.subagentType, &n.typeSource, d.Type, SubagentSourceDetails)
		applyRanked(&n.runMode, &n.runModeSource, runMode, SubagentSourceDetails)
		applyFirst(&n.description, d.Description)
		applyFirst(&n.model, d.Model)
		applyFirst(&n.toolUseID, d.ToolUseID)
		if !n.parentType.Valid {
			n.parentType, n.parentID = nullStr(parentType), nullStr(parentID)
		}
	}

	e, found, err := t.readEntry(d.SubagentID)
	if err != nil {
		return err
	}
	if !found {
		if d.Outcome != SubagentLaunchBackgroundStarted && d.Outcome != SubagentLaunchForegroundFinished {
			return nil
		}
		n := subagentEntry{confirmed: true, sessionID: nullStr(d.SessionID), openedAt: at, activityAt: at}
		fill(&n)
		if d.Outcome == SubagentLaunchBackgroundStarted {
			n.state = SubagentStateRunning
			n.firstStartedAt, n.startedAt = millis(at), millis(at)
		} else {
			n.state = SubagentStateStopped
			n.stoppedAt, n.stoppedAtSource = millis(at), nullStr(SubagentSourceDetails)
			n.closeReason = nullStr(SubagentCloseStopEvent)
		}
		return t.create(d.SubagentID, n, "")
	}

	n := e
	fill(&n)
	n.confirmed = true
	n.launchToken = t.token
	switch d.Outcome {
	case SubagentLaunchBackgroundStarted:
		if !n.startedAt.Valid {
			n.startedAt = millis(at)
		}
		earlier(&n.firstStartedAt, at)
	case SubagentLaunchForegroundFinished:
		if n.state == SubagentStateRunning || n.state == SubagentStateLost {
			n.state = SubagentStateStopped
			n.stoppedAt, n.stoppedAtSource = millis(at), nullStr(SubagentSourceDetails)
			n.closeReason = nullStr(SubagentCloseStopEvent)
			n.lostAt = sql.NullInt64{}
		}
	}
	return t.writeEntry(e, n, at, true, false)
}

// launchRequest applies a launch request (SR-4.5, SR-7.8): it opens a claimable
// launch of the current launch holding only the tool_use_id, the type and the
// receipt time. An empty tool_use_id or type opens none; a tool_use_id that is
// already open, or already the tool_use_id of an entry of the current launch,
// changes nothing.
func (t *subagentTx) launchRequest(r SubagentLaunchRequest) error {
	if r.ToolUseID == "" || r.Type == "" || !t.tokenOK {
		return nil
	}
	var exists bool
	if err := t.conn.QueryRowContext(t.ctx, `SELECT EXISTS (
	      SELECT 1 FROM subagent_launches WHERE instance_id = ? AND tool_use_id = ?)
	    OR EXISTS (SELECT 1 FROM subagents WHERE instance_id = ? AND tool_use_id = ? AND launch_token = ?)`,
		t.instanceID, r.ToolUseID, t.instanceID, r.ToolUseID, t.token).Scan(&exists); err != nil {
		return fmt.Errorf("store: read subagent launch: %w", err)
	}
	if exists {
		return nil
	}
	evicted, err := evictSubagentLaunchGated(t.q, t.instanceID, t.gate)
	if err != nil {
		return err
	}
	args := append([]any{r.ToolUseID, r.Type, r.ReceivedMs}, subagentRowGateArgs(t.instanceID, t.gate)...)
	args = append(args, r.ToolUseID)
	n, err := t.exec("open subagent launch", `INSERT INTO subagent_launches
	    (instance_id, launch_token, tool_use_id, subagent_type, opened_at)
	SELECT sp.instance_id, sp.launch_token, ?, ?, ?
	  FROM spawns sp WHERE `+subagentRowGateSQL+`
	   AND NOT EXISTS (SELECT 1 FROM subagent_launches l
	                    WHERE l.instance_id = sp.instance_id AND l.tool_use_id = ?)`, args...)
	if err != nil {
		return err
	}
	if n == 0 && evicted {
		return errSubagentWriteRefused
	}
	return nil
}

// launchFailure applies a launch failure: it closes the claimable launch with
// the tool_use_id and nothing else.
func (t *subagentTx) launchFailure(f SubagentLaunchFailure) error {
	return t.closeLaunch(f.ToolUseID)
}

// turnEnd applies the main agent's turn end (SR-4.5, SR-7.4, SR-7.5): it deletes
// the current launch's claimable launches opened strictly more than
// SubagentGraceMs before the receipt time. Only the current launch's: an earlier
// launch's claimable launches are already gone, deleted by the closes of SR-8.1
// (closeSubagentsAgentGone) when the launch ended or the next one began. Then, when the Stop carries a
// snapshot, it creates, confirms and fills the listed subagents and closes the
// unlisted running background entries past the grace.
func (t *subagentTx) turnEnd(te SubagentTurnEnd) error {
	frag, gateArgs := subagentGateExists(t.instanceID, t.gate, "subagent_launches")
	args := append([]any{t.instanceID, te.ReceivedMs - SubagentGraceMs}, gateArgs...)
	if _, err := t.exec("close stale subagent launches", `DELETE FROM subagent_launches
	  WHERE instance_id = ? AND opened_at < ? AND `+frag, args...); err != nil {
		return err
	}
	if te.Snapshot == nil {
		return nil
	}
	listed := cutListed(te.Snapshot.Listed)
	if err := t.applyListed(listed, te.ReceivedMs, true, CutSubagentString(te.Snapshot.SessionID)); err != nil {
		return err
	}
	return t.closeAbsent(listed, te.ReceivedMs)
}

// cutListed cuts every listed id and type to SubagentStringMaxBytes, drops an
// empty id and applies each id once, the first in array order, after the cut
// (SR-5.3, SR-7.4).
func cutListed(in []SubagentListed) []SubagentListed {
	if len(in) == 0 {
		return nil
	}
	out := make([]SubagentListed, 0, len(in))
	seen := make(map[string]struct{}, len(in))
	for _, l := range in {
		id := CutSubagentString(l.ID)
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, SubagentListed{ID: id, Type: CutSubagentString(l.Type)})
	}
	return out
}

// applyListed applies a snapshot's or a SubagentStop's listing to each listed
// subagent in order (SR-7.4, SR-7.6). An id with an entry of any launch of the
// current life: the entry is confirmed, moves to the current launch and a null
// run_mode becomes background (source snapshot); its type, description, state and times never
// change, so a stopped or lost entry is not reopened. When it changes nothing it
// writes nothing. An id with no current-life entry: with create, a confirmed
// running entry of the current life and launch with run_mode background,
// subagent_type the listing's type (source snapshot; null when none),
// session_id sessionID (null when empty),
// opened_at and activity_at at and every other field null (the gated entry-cap
// eviction runs first); without create, nothing. listed is already cut
// (cutListed).
func (t *subagentTx) applyListed(listed []SubagentListed, at int64, create bool, sessionID string) error {
	for _, l := range listed {
		e, found, err := t.readEntry(l.ID)
		if err != nil {
			return err
		}
		if !found {
			if !create {
				continue
			}
			n := subagentEntry{
				confirmed: true, sessionID: nullStr(sessionID), state: SubagentStateRunning,
				runMode: nullStr(SubagentRunModeBackground), runModeSource: nullStr(SubagentSourceSnapshot),
				openedAt: at, activityAt: at,
			}
			applyRanked(&n.subagentType, &n.typeSource, l.Type, SubagentSourceSnapshot)
			if err := t.create(l.ID, n, ""); err != nil {
				return err
			}
			continue
		}
		n := e
		n.confirmed = true
		n.launchToken = t.token
		if !n.runMode.Valid {
			n.runMode = nullStr(SubagentRunModeBackground)
			n.runModeSource = nullStr(SubagentSourceSnapshot)
		}
		if err := t.writeEntry(e, n, at, true, false); err != nil {
			return err
		}
	}
	return nil
}

// readUnlistedCandidates reads closeAbsent's candidates (every running
// background entry of the current launch opened strictly more than
// SubagentGraceMs before at) and returns the created_seq of those whose id is not
// in named. The rows are closed before it returns, so the caller's writes never
// overlap an open cursor.
func (t *subagentTx) readUnlistedCandidates(named map[string]struct{}, at int64) ([]int64, error) {
	rows, err := t.conn.QueryContext(t.ctx, `SELECT created_seq, subagent_id FROM subagents
	  WHERE instance_id = ? AND launch_token = ? AND state = ? AND run_mode = ? AND opened_at < ?`,
		t.instanceID, t.token, SubagentStateRunning, SubagentRunModeBackground, at-SubagentGraceMs)
	if err != nil {
		return nil, fmt.Errorf("store: read subagent snapshot candidates: %w", err)
	}
	defer rows.Close()
	var stale []int64
	for rows.Next() {
		var seq int64
		var id string
		if err := rows.Scan(&seq, &id); err != nil {
			return nil, fmt.Errorf("store: scan subagent snapshot candidate: %w", err)
		}
		if _, ok := named[id]; !ok {
			stale = append(stale, seq)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: read subagent snapshot candidates: %w", err)
	}
	return stale, nil
}

// closeAbsent is a snapshot's close (SR-7.5): every running entry of the current
// launch with run_mode background that listed does not name and whose opened_at
// is strictly more than SubagentGraceMs before at becomes lost, with close_reason
// absent_from_snapshot and lost_at and activity_at at. No session or nesting
// filter; an entry that is foreground, of unknown mode, stopped, lost or of
// another launch is never touched. The candidates are read first and each is
// closed by its own gated statement that checks every condition again.
func (t *subagentTx) closeAbsent(listed []SubagentListed, at int64) error {
	if !t.tokenOK {
		return nil
	}
	named := make(map[string]struct{}, len(listed))
	for _, l := range listed {
		named[l.ID] = struct{}{}
	}
	stale, err := t.readUnlistedCandidates(named, at)
	if err != nil {
		return err
	}
	frag, gateArgs := subagentGateExists(t.instanceID, t.gate, "subagents")
	for _, seq := range stale {
		args := append([]any{SubagentStateLost, SubagentCloseAbsentFromSnapshot, at, at,
			seq, t.instanceID, SubagentStateRunning, SubagentRunModeBackground, at - SubagentGraceMs}, gateArgs...)
		if _, err := t.exec("close absent subagent entry", `UPDATE subagents
		   SET state = ?, close_reason = ?, lost_at = ?, activity_at = ?
		 WHERE created_seq = ? AND instance_id = ? AND state = ? AND run_mode = ? AND opened_at < ? AND `+frag, args...); err != nil {
			return err
		}
	}
	return nil
}
