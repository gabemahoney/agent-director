package store

import "time"

// RowSnapshot is a row's change-detection key (SR-5.3): the values exactly as
// stored, never parsed and re-formatted, so two snapshots compare with ==.
// Conditional writes apply only when the row still holds the snapshot the
// caller examined; row_version makes every pair of lives distinct.
type RowSnapshot struct {
	RowVersion      int64
	StartedAt       string // started_at as stored text
	ClaudeSessionID string // "" = NULL
	PID             int    // 0 = NULL
	ProcStarttime   string // "" = NULL
	TmuxSessionName string
}

// snapshotMatchSQL is the one row-snapshot condition (SR-5.3) every write
// guarded on a full snapshot uses (resume's move, reuse's reset, expire's
// delete, find-missing's marks): a WHERE fragment, joined with AND, whose
// placeholders take snapshotMatchArgs in order. Each column is compared
// through the same expression the read that filled the snapshot uses
// (lifeColumns), so the comparison sees exactly the stored value the snapshot
// holds and never parses or re-formats it: started_at through CAST(... AS
// TEXT), because the TIMESTAMP column has NUMERIC affinity and its stored text
// must not be coerced; the nullable columns through COALESCE to the snapshot's
// zero value, so a NULL column matches that zero value and no other value.
const snapshotMatchSQL = `COALESCE(row_version, 0) = ?
    AND CAST(started_at AS TEXT) = ?
    AND COALESCE(claude_session_id, '') = ?
    AND COALESCE(pid, 0) = ?
    AND COALESCE(proc_starttime, '') = ?
    AND tmux_session_name = ?`

// snapshotMatchArgs returns the bound arguments for snapshotMatchSQL, in its
// placeholder order.
func snapshotMatchArgs(s RowSnapshot) []any {
	return []any{s.RowVersion, s.StartedAt, s.ClaudeSessionID, s.PID, s.ProcStarttime, s.TmuxSessionName}
}

// CondResult reports the outcome of a conditional write (SR-5.3): a write
// that applies only while the row still holds the state, version or snapshot
// the caller examined.
type CondResult int

const (
	// CondApplied: the row met the condition and the write was applied
	// (SR-5.3).
	CondApplied CondResult = iota + 1
	// CondChanged: the row exists but no longer meets the condition; nothing
	// was written (SR-5.3).
	CondChanged
	// CondAbsent: the row no longer exists; nothing was written (SR-5.3).
	CondAbsent
)

// LaunchIdentity is a launch's identity: the eight launch-identity columns
// (SR-3.3 to SR-3.6, SR-5.1). Zero values mean NULL. It holds handles and
// liveness evidence only; the session label is the proof of ownership and
// launch (PO 2026-09-27 LABEL).
type LaunchIdentity struct {
	Token           string // launch_token: 16 lowercase hex characters
	Socket          string // tmux_socket
	ServerPID       int    // tmux_server_pid
	ServerStart     int64  // tmux_server_started, epoch seconds
	ServerStarttime string // tmux_server_starttime, same form as proc_starttime
	PaneID          string // pane_id, "%N"
	PanePID         int    // pane_pid
	PaneStarttime   string // pane_starttime, same form as proc_starttime
}

// lifeColumns is the one column fragment a read selects to fill a row's
// RowSnapshot and LaunchIdentity, in lifeScan.dest's order: the snapshot's six
// columns, each through the expression snapshotMatchSQL compares, then the
// eight launch-identity columns. launch_token is selected bare and decoded by
// decodeLaunchToken; the other nullable columns read NULL as their zero value
// through COALESCE. The reads returning a Spawn (spawnColumns) and
// ListLiveSpawnIdentities select it, so a snapshot and identity read by one
// equal those read by another for the same row, and every snapshot holds the
// stored text the snapshot-match condition compares (SR-5.3, SR-5.5).
const lifeColumns = `
        COALESCE(row_version, 0), CAST(started_at AS TEXT),
        COALESCE(claude_session_id, ''), COALESCE(pid, 0),
        COALESCE(proc_starttime, ''), tmux_session_name,
        launch_token, COALESCE(tmux_socket, ''), COALESCE(tmux_server_pid, 0),
        COALESCE(tmux_server_started, 0), COALESCE(tmux_server_starttime, ''),
        COALESCE(pane_id, ''), COALESCE(pane_pid, 0),
        COALESCE(pane_starttime, '')`

// lifeScan receives one row's lifeColumns: pass dest's pointers to Scan where
// the query selects lifeColumns, then take the values from result.
type lifeScan struct {
	snap        RowSnapshot
	id          LaunchIdentity
	launchToken any
}

// dest returns the Scan destinations for lifeColumns, in its order.
func (l *lifeScan) dest() []any {
	return []any{
		&l.snap.RowVersion, &l.snap.StartedAt,
		&l.snap.ClaudeSessionID, &l.snap.PID,
		&l.snap.ProcStarttime, &l.snap.TmuxSessionName,
		&l.launchToken, &l.id.Socket, &l.id.ServerPID,
		&l.id.ServerStart, &l.id.ServerStarttime,
		&l.id.PaneID, &l.id.PanePID,
		&l.id.PaneStarttime,
	}
}

// result returns the scanned snapshot and launch identity. The identity's
// Token is "" unless the stored token is well formed (decodeLaunchToken), so
// it never fails.
func (l *lifeScan) result() (RowSnapshot, LaunchIdentity) {
	id := l.id
	id.Token = decodeLaunchToken(l.launchToken)
	return l.snap, id
}

// launchTokenLen is the length of a well-formed launch token: 64 bits as
// lowercase hexadecimal (SR-3.5).
const launchTokenLen = 16

// The three SR-5.5 narrow-read rules. Each takes the column's raw driver
// value (scan the column into an `any`: nil for NULL, int64, float64, string
// or []byte by storage class) and never fails, whatever a hand edit stored.
// Every read of these columns uses them rather than re-deriving the rules.

// The inclusive range of a launch start, in milliseconds since the Unix
// epoch (SR-5.5): the first millisecond of year 0 and the last millisecond of
// year 9999, UTC. A time outside it has no RFC3339 form, so encoding it as
// JSON would fail.
const (
	// minLaunchStartedAtMillis is 0000-01-01T00:00:00.000Z.
	minLaunchStartedAtMillis int64 = -62167219200000
	// maxLaunchStartedAtMillis is 9999-12-31T23:59:59.999Z.
	maxLaunchStartedAtMillis int64 = 253402300799999
)

// decodeLaunchStartedAt maps launch_started_at to milliseconds since the
// epoch. Only an integer from minLaunchStartedAtMillis to
// maxLaunchStartedAtMillis inclusive (years 0 to 9999, UTC) is a launch
// start. NULL, any other stored value (text, real, blob) and an integer
// outside that range yield 0, meaning absent (SR-5.5, SR-11.2), so a
// hand-edited value never fails a read or the JSON encoding of the row. A
// stored 0 (1970-01-01T00:00:00Z) is in range but also reads as absent,
// because 0 is the absent value; it gets no special case.
func decodeLaunchStartedAt(v any) int64 {
	ms, ok := v.(int64)
	if !ok || ms < minLaunchStartedAtMillis || ms > maxLaunchStartedAtMillis {
		return 0
	}
	return ms
}

// InsidePendingGrace reports whether a row is a pending row inside its
// pending grace period (SR-11.2, SR-22.8): state is StatePending and the row's
// age, now minus its launch start, is below grace. The age is measured from
// the launch start, never started_at, so a reused or resumed row is inside
// however old its started_at. A launch start later than now is inside. An
// absent launch start (0, as decodeLaunchStartedAt reads NULL, a non-integer
// or an out-of-range value) is past, so the row is judged at once. Every
// other state is never inside. grace is used as given, with no default or
// minimum applied here (configuration loading owns those, SR-4.1).
//
// Two callers use it with the same effective pending grace period (SR-13.4):
// find-missing, which leaves a row inside it untouched, and the hook's
// SessionStart, which waits for its launch's identity write only while the
// row is inside it (SR-22.9).
//
// The comparison never subtracts the launch start, so no int64 value, however
// extreme, can overflow into looking young: a huge age is past.
func InsidePendingGrace(state string, launchStartedAtMillis int64, grace time.Duration, now time.Time) bool {
	if state != StatePending || launchStartedAtMillis == 0 {
		return false
	}
	return launchStartedAtMillis > now.UnixMilli()-grace.Milliseconds()
}

// decodeLaunchToken maps launch_token to the token. Only text of exactly 16
// lowercase hexadecimal characters is a token; NULL and anything else yield
// "", so the row has no current label (SR-3.4, SR-5.5).
func decodeLaunchToken(v any) string {
	s, ok := v.(string)
	if !ok || len(s) != launchTokenLen {
		return ""
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return ""
		}
	}
	return s
}

// decodeNoPreTrust maps no_pre_trust to the recorded opt-out. The integer 0
// (the column default) means pre-trust allowed; any other stored value,
// including one only a hand edit can store, means opted out (SR-5.5, SR-22.6).
func decodeNoPreTrust(v any) bool {
	n, ok := v.(int64)
	return !ok || n != 0
}
