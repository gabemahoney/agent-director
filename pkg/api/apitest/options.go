package apitest

import (
	"encoding/json"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/launchfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/procstarttimefix"
)

// Canonical per-OS proc_starttime fixture values, re-exported from the leaf
// procstarttimefix package so apitest callers have a single import surface.
// These are fixture VALUES in each OS's canonical proc_starttime form; the
// production formatting code that must agree with them lands in Epic t1.93m.is.
// See internal/testsupport/procstarttimefix for the authoritative definitions.
const (
	LinuxProcStarttime  = procstarttimefix.LinuxProcStarttime
	DarwinProcStarttime = procstarttimefix.DarwinProcStarttime
)

// TestSocket is the tmux socket SeedSpawn records on every row unless
// WithLaunchIdentity overrides it; re-exported from the leaf launchfix
// package, where the value is defined once (SR-20.3).
const TestSocket = launchfix.TestSocket

// TestPaneID and TestPanePID are the pane identity SeedSpawn records on a
// live row unless WithLaunchIdentity or WithNoLaunchToken overrides it;
// re-exported from the leaf launchfix package, where the values are defined
// once (SR-20.3). The pid is above Linux's PID_MAX_LIMIT, so every
// start-time reader reads the pane process as gone.
const (
	TestPaneID  = launchfix.TestPaneID
	TestPanePID = launchfix.TestPanePID
)

// storeTimestampLayout is the store's CURRENT_TIMESTAMP layout (UTC, whole
// seconds), in which WithStartedAt and WithEndedAt write a time.Time.
const storeTimestampLayout = "2006-01-02 15:04:05"

// spawnOpts accumulates what SeedSpawn's variadic SpawnOption arguments
// request. cols maps a spawns column name to the value stored, where a nil
// value stores NULL. Only columns an option names are written from here
// (SeedSpawn adds its SR-20.3 defaults for the v5 columns no option names);
// a later option for the same column wins. history holds the
// session_history entries WithSessionHistory seeds, in seeding order; they
// are not spawns columns and never enter cols.
type spawnOpts struct {
	cols    map[string]any
	history []SessionHistorySeed
}

// set records value for column; nil stores NULL.
func (o *spawnOpts) set(column string, value any) {
	if o.cols == nil {
		o.cols = map[string]any{}
	}
	o.cols[column] = value
}

// has reports whether an option named column.
func (o *spawnOpts) has(column string) bool {
	_, ok := o.cols[column]
	return ok
}

// SpawnOption configures optional columns on a SeedSpawn row. Options are
// variadic and trailing, so every pre-existing 7-arg SeedSpawn call site
// compiles unchanged; supplying no options keeps the default row.
type SpawnOption func(*spawnOpts)

// WithPID seeds the pid column. A real pid is ≥1; pass a positive value.
func WithPID(pid int) SpawnOption {
	return func(o *spawnOpts) { o.set("pid", pid) }
}

// WithProcStarttime seeds the proc_starttime column. Use the canonical per-OS
// fixture constants (LinuxProcStarttime / DarwinProcStarttime) rather than
// inline literals.
func WithProcStarttime(procStarttime string) SpawnOption {
	return func(o *spawnOpts) { o.set("proc_starttime", procStarttime) }
}

// WithJsonlPath seeds the jsonl_path column.
func WithJsonlPath(jsonlPath string) SpawnOption {
	return func(o *spawnOpts) { o.set("jsonl_path", jsonlPath) }
}

// WithExtraEnv seeds the extra_env column (stored as a JSON object). A nil or
// empty map is written as the canonical empty object "{}".
func WithExtraEnv(extraEnv map[string]string) SpawnOption {
	if extraEnv == nil {
		extraEnv = map[string]string{}
	}
	encoded, _ := json.Marshal(extraEnv) // a map[string]string always encodes
	return func(o *spawnOpts) { o.set("extra_env", string(encoded)) }
}

// WithLivenessUnverifiedSince seeds the liveness_unverified_since column.
func WithLivenessUnverifiedSince(ts string) SpawnOption {
	return func(o *spawnOpts) { o.set("liveness_unverified_since", ts) }
}

// WithLivenessNote seeds the liveness_note column.
func WithLivenessNote(note string) SpawnOption {
	return func(o *spawnOpts) { o.set("liveness_note", note) }
}

// WithTmuxSessionName stores name as the row's tmux_session_name, exactly as
// given (any text, including names tmux would reject or rewrite).
func WithTmuxSessionName(name string) SpawnOption {
	return func(o *spawnOpts) { o.set("tmux_session_name", name) }
}

// WithStartedAt seeds started_at: a time.Time in the store's timestamp layout
// (UTC, whole seconds), or a string stored byte for byte (for unparseable text,
// SR-4.2, SR-11.2). A pending row's default launch start follows it.
func WithStartedAt[T time.Time | string](at T) SpawnOption {
	v := storedTimestamp(at)
	return func(o *spawnOpts) { o.set("started_at", v) }
}

// WithEndedAt seeds ended_at: a time.Time in the store's timestamp layout
// (UTC, whole seconds), or a string stored byte for byte (SR-4.2, SR-11.2).
func WithEndedAt[T time.Time | string](at T) SpawnOption {
	v := storedTimestamp(at)
	return func(o *spawnOpts) { o.set("ended_at", v) }
}

// storedTimestamp is the text WithStartedAt and WithEndedAt store.
func storedTimestamp[T time.Time | string](at T) string {
	switch v := any(at).(type) {
	case time.Time:
		return v.UTC().Format(storeTimestampLayout)
	default:
		return v.(string)
	}
}

// WithLaunchStartedAt seeds launch_started_at as the integer ms (milliseconds
// since the Unix epoch), overriding SeedSpawn's default launch start.
func WithLaunchStartedAt(ms int64) SpawnOption {
	return func(o *spawnOpts) { o.set("launch_started_at", ms) }
}

// WithRawLaunchStartedAt stores raw in launch_started_at as bound (a string,
// float64 or []byte), for SR-5.5's unreadable launch start. The column's
// INTEGER affinity still stores integer-looking text as an integer.
func WithRawLaunchStartedAt(raw any) SpawnOption {
	return func(o *spawnOpts) { o.set("launch_started_at", raw) }
}

// WithNoLaunchStartedAt stores NULL in launch_started_at (no launch start),
// overriding SeedSpawn's default for a pending row.
func WithNoLaunchStartedAt() SpawnOption {
	return func(o *spawnOpts) { o.set("launch_started_at", nil) }
}

// WithLifeNumber seeds life_number, the row's current life (SR-5.9).
func WithLifeNumber(life int64) SpawnOption {
	return func(o *spawnOpts) { o.set("life_number", life) }
}

// SessionHistorySeed is one archived session_history entry WithSessionHistory
// seeds on the row (SR-5.9, SR-8.7).
type SessionHistorySeed struct {
	// SessionID is the entry's claude_session_id. It may equal the row's
	// current session id (the current-session-rule fixtures, SR-8.7).
	SessionID string
	// JSONLPath is the entry's transcript path; empty stores NULL.
	JSONLPath string
	// Life is the life the entry belongs to (life_number); it need not be
	// the row's life.
	Life int64
	// RecordedAt, when non-zero, is stored as recorded_at in the store's
	// timestamp layout (UTC, whole seconds). When zero the entry takes the
	// store's default, the current time.
	RecordedAt time.Time
}

// WithSessionHistory adds one archived session_history entry to the seeded
// row, with its session id, transcript path (empty = NULL) and life, and
// optionally its recorded_at (SR-20.2, SR-20.3). Give it once per entry.
// Entries are inserted after the row's columns are written, in seeding order,
// so with no stated RecordedAt a later-seeded entry reads as newer (equal
// recorded_at is broken by insertion order, newest first); entries with a
// stated RecordedAt order by that time. Two entries with the same session id
// are a SeedSpawn error (one entry per instance and session id), never an
// overwrite. With no WithSessionHistory, SeedSpawn writes no history.
func WithSessionHistory(entry SessionHistorySeed) SpawnOption {
	return func(o *spawnOpts) { o.history = append(o.history, entry) }
}

// WithNoPreTrust records the pre-trust opt-out (no_pre_trust 1, SR-5.1);
// without it SeedSpawn records pre-trust allowed (0).
func WithNoPreTrust() SpawnOption {
	return func(o *spawnOpts) { o.set("no_pre_trust", int64(1)) }
}

// WithRawNoPreTrust stores raw in no_pre_trust as bound, for SR-5.5's unusual
// values (any value other than the integer 0 reads as the opt-out).
func WithRawNoPreTrust(raw any) SpawnOption {
	return func(o *spawnOpts) { o.set("no_pre_trust", raw) }
}

// WithLaunchIdentity stores id in the eight launch-identity columns, replacing
// SeedSpawn's default token and socket. Every zero field is stored as NULL, so
// LaunchIdentity{} gives a row with no token, socket or identity at all; Token
// is stored as given, so a malformed token is expressible.
func WithLaunchIdentity(id store.LaunchIdentity) SpawnOption {
	return func(o *spawnOpts) {
		o.set("launch_token", nullIfZero(id.Token))
		o.set("tmux_socket", nullIfZero(id.Socket))
		o.set("tmux_server_pid", nullIfZero(id.ServerPID))
		o.set("tmux_server_started", nullIfZero(id.ServerStart))
		o.set("tmux_server_starttime", nullIfZero(id.ServerStarttime))
		o.set("pane_id", nullIfZero(id.PaneID))
		o.set("pane_pid", nullIfZero(id.PanePID))
		o.set("pane_starttime", nullIfZero(id.PaneStarttime))
	}
}

// WithNoLaunchToken seeds a row from before the release (SR-20.3): no launch
// token, socket or identity (SR-5.4), so all eight launch-identity columns
// (launch_token, tmux_socket, tmux_server_pid, tmux_server_started,
// tmux_server_starttime, pane_id, pane_pid, pane_starttime) are NULL. It is
// the same as WithLaunchIdentity(store.LaunchIdentity{}). For a row with no
// token but the test socket, use
// WithLaunchIdentity(store.LaunchIdentity{Socket: TestSocket}).
func WithNoLaunchToken() SpawnOption {
	return WithLaunchIdentity(store.LaunchIdentity{})
}

// WithRawLabels stores text in the labels column byte for byte (no JSON
// encoding), for malformed-value cases.
func WithRawLabels(text string) SpawnOption {
	return func(o *spawnOpts) { o.set("labels", text) }
}

// WithRawClaudeArgs stores text in the claude_args column byte for byte (no
// JSON encoding), for malformed-value cases.
func WithRawClaudeArgs(text string) SpawnOption {
	return func(o *spawnOpts) { o.set("claude_args", text) }
}

// WithRawExtraEnv stores text in the extra_env column byte for byte (no JSON
// encoding), for malformed-value cases; it and WithExtraEnv write the same
// column, so the later one wins.
func WithRawExtraEnv(text string) SpawnOption {
	return func(o *spawnOpts) { o.set("extra_env", text) }
}

// nullIfZero returns nil (SQL NULL) for T's zero value, else v.
func nullIfZero[T comparable](v T) any {
	var zero T
	if v == zero {
		return nil
	}
	return v
}
