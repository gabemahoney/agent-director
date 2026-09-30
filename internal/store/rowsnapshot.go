package store

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

// launchTokenLen is the length of a well-formed launch token: 64 bits as
// lowercase hexadecimal (SR-3.5).
const launchTokenLen = 16

// The three SR-5.5 narrow-read rules. Each takes the column's raw driver
// value (scan the column into an `any`: nil for NULL, int64, float64, string
// or []byte by storage class) and never fails, whatever a hand edit stored.
// Every read of these columns uses them rather than re-deriving the rules.

// decodeLaunchStartedAt maps launch_started_at to milliseconds since the
// epoch. Only an integer is a launch start; NULL and any other stored value
// (text, real, blob) yield 0, meaning absent (SR-5.5, SR-11.2).
func decodeLaunchStartedAt(v any) int64 {
	if ms, ok := v.(int64); ok {
		return ms
	}
	return 0
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
