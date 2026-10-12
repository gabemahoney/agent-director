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
// background entries (Epic 2) uses the same constant.
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

// SubagentTurnEnd is the main agent's turn end (a Stop without a subagent id):
// it closes the current launch's claimable launches opened more than
// SubagentGraceMs before ReceivedMs (SR-4.5).
type SubagentTurnEnd struct {
	ReceivedMs int64
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
// order serves all. An unknown or NULL source ranks lowest. Epic 2's snapshot
// source plugs in here without rework.
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

// readEntry reads the entry (instance id, subagentID) of any launch.
func (t *subagentTx) readEntry(subagentID string) (subagentEntry, bool, error) {
	var (
		e         subagentEntry
		confirmed int
	)
	err := t.conn.QueryRowContext(t.ctx, `SELECT `+subagentEntryColumns+`
	    FROM subagents WHERE instance_id = ? AND subagent_id = ?`, t.instanceID, subagentID,
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
// that already belongs to it. session_id is never written.
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

// insertEntry creates e, an entry of the row's current launch, with the subagent
// id: one gated insert-select from the spawns row, so the launch_token is read
// in the statement. It reports whether a row was inserted.
func (t *subagentTx) insertEntry(subagentID string, e subagentEntry) (bool, error) {
	args := []any{subagentID, boolInt(e.confirmed), e.sessionID, e.subagentType, e.description, e.model,
		e.parentType, e.parentID, e.toolUseID, e.runMode,
		e.firstStartedAt, e.startedAt, e.stoppedAt, e.lostAt, e.state, e.closeReason,
		e.openedAt, e.activityAt, e.typeSource, e.runModeSource, e.stoppedAtSource}
	args = append(args, subagentRowGateArgs(t.instanceID, t.gate)...)
	n, err := t.exec("insert subagent entry", `INSERT INTO subagents
	    (instance_id, subagent_id, launch_token, confirmed, session_id, subagent_type, description, model,
	     parent_type, parent_id, tool_use_id, run_mode, first_started_at, started_at, stopped_at, lost_at,
	     state, close_reason, opened_at, activity_at, subagent_type_source, run_mode_source, stopped_at_source)
	SELECT sp.instance_id, ?, sp.launch_token, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
	  FROM spawns sp WHERE `+subagentRowGateSQL, args...)
	return n > 0, err
}

// create makes room for and inserts a new entry of the current launch: the
// gated entry-cap eviction first, then (claimType not empty) the gated claim of
// the oldest open claimable launch of the current launch with that type, which
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

// stop applies a SubagentStop (SR-7.2): the current launch's entry only.
func (t *subagentTx) stop(ev SubagentLifecycleEvent) error {
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

// turnEnd applies the main agent's turn end (SR-4.5, SR-7.5): it deletes the
// current launch's claimable launches opened strictly more than SubagentGraceMs
// before the receipt time. Only the current launch's: an earlier launch's
// leftovers go only with the cap until the closes of SR-8.1 (a stated deviation
// from SR-12.2's literal "the row"). Epic 2's snapshot joins this transaction.
func (t *subagentTx) turnEnd(te SubagentTurnEnd) error {
	frag, gateArgs := subagentGateExists(t.instanceID, t.gate, "subagent_launches")
	args := append([]any{t.instanceID, te.ReceivedMs - SubagentGraceMs}, gateArgs...)
	_, err := t.exec("close stale subagent launches", `DELETE FROM subagent_launches
	  WHERE instance_id = ? AND opened_at < ? AND `+frag, args...)
	return err
}
