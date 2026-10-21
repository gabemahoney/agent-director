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
// WithLaunchIdentity or WithTmuxSocket overrides it; re-exported from the leaf launchfix
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

// WithNoPID stores NULL in pid and proc_starttime: a row whose latest launch
// never reported in (no SessionStart identity write), such as a finished row
// that keeps its pane identity (SR-6.7). It wins over an earlier WithPID or
// WithProcStarttime, as a later option for the same column does.
func WithNoPID() SpawnOption {
	return func(o *spawnOpts) {
		o.set("pid", nil)
		o.set("proc_starttime", nil)
	}
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

// WithLastSeenAt seeds last_seen_at in the store's timestamp layout (UTC,
// whole seconds), in the past so a test sees a hook move it.
func WithLastSeenAt(at time.Time) SpawnOption {
	v := storedTimestamp(at)
	return func(o *spawnOpts) { o.set("last_seen_at", v) }
}

// WithNoEndedAt stores NULL in ended_at: a finished row whose end time is
// unknown (SR-4.2).
func WithNoEndedAt() SpawnOption {
	return func(o *spawnOpts) { o.set("ended_at", nil) }
}

// storedTimestamp is the text WithStartedAt, WithEndedAt and WithLastSeenAt store.
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

// WithRowVersion stores v as row_version after SeedSpawn's store writes, in
// place of the version they left. It gives rows no write sequence reaches (a
// row off pending at version 0, or a pending row at 0 that records a session
// id), so a conditional write's guards can each be tested alone (SR-5.3).
func WithRowVersion(v int64) SpawnOption {
	return func(o *spawnOpts) { o.set("row_version", v) }
}

// SessionHistorySeed is one archived session_history entry WithSessionHistory
// seeds on the row (SR-5.9, SR-8.7).
type SessionHistorySeed struct {
	// SessionID is the entry's session_id. It may equal the row's
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

// WithLaunchOwner stores o in the three schema-v6 launch-owner columns
// (launch_owner_pid, launch_owner_starttime, launch_owner_pidns; b.kdf), each
// zero field as NULL. Without it the row records no owner (all three NULL),
// as SeedSpawn's insert leaves them.
func WithLaunchOwner(o store.LaunchOwner) SpawnOption {
	return func(so *spawnOpts) {
		so.set("launch_owner_pid", nullIfZero(o.PID))
		so.set("launch_owner_starttime", nullIfZero(o.Starttime))
		so.set("launch_owner_pidns", nullIfZero(o.PIDNamespace))
	}
}

// WithIdleSince stores ts as the row's schema-v7 idle_since (b.146 problem
// 3): the time the main agent's idle-prompt Notification last landed, which
// find-missing's check_permission repair reads. Without it the row has none
// (NULL).
func WithIdleSince(ts string) SpawnOption {
	return func(o *spawnOpts) { o.set("idle_since", ts) }
}

// WithSettle stores the row's schema-v8 settle columns (b.3ie): settled as
// settled_state (the last settled state) and stampMs as state_changed_ms (when
// the raw state last changed, in Unix milliseconds). Without it SeedSpawn
// seeds the row settled: settled_state its state and state_changed_ms 0.
func WithSettle(settled string, stampMs int64) SpawnOption {
	return func(o *spawnOpts) {
		o.set("settled_state", settled)
		o.set("state_changed_ms", stampMs)
	}
}

// WithAgent stores agent as the row's schema-v9 agent column in place of
// SeedSpawn's claude, exactly as given (any text, so an unreadable agent is
// expressible).
func WithAgent(agent string) SpawnOption {
	return func(o *spawnOpts) { o.set("agent", agent) }
}

// WithEndReportedAt stores ms (Unix milliseconds) as the row's schema-v9
// end_reported_at, the time its agent's SessionEnd was reported. Without it the
// row has no end report (NULL).
func WithEndReportedAt(ms int64) SpawnOption {
	return func(o *spawnOpts) { o.set("end_reported_at", ms) }
}

// WithRelayMode stores mode as the row's relay_mode in place of SeedSpawn's
// relayMode argument, for seeders that take no relay mode of their own.
func WithRelayMode(mode string) SpawnOption {
	return func(o *spawnOpts) { o.set("relay_mode", mode) }
}

// WithTmuxSocket stores socket as the row's recorded tmux socket in place of
// TestSocket, keeping every other launch-identity default (token, pane). A
// launch on the row (resume) needs the socket's directory to exist.
func WithTmuxSocket(socket string) SpawnOption {
	return func(o *spawnOpts) { o.set("tmux_socket", nullIfZero(socket)) }
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

// WithNoPane seeds a live row whose launch reply was lost (SR-11.3): no pane
// identity (pane_id, pane_pid and pane_starttime NULL), keeping every other
// launch-identity column, the default token and TestSocket included, so the
// row's session can still read as Ours on the tmux path.
func WithNoPane() SpawnOption {
	return func(o *spawnOpts) {
		o.set("pane_id", nil)
		o.set("pane_pid", nil)
		o.set("pane_starttime", nil)
	}
}

// WithRawLabels stores text in the labels column byte for byte (no JSON
// encoding), for malformed-value cases.
func WithRawLabels(text string) SpawnOption {
	return func(o *spawnOpts) { o.set("labels", text) }
}

// WithRawAgentArgs stores text in the agent_args column byte for byte (no
// JSON encoding), for malformed-value cases.
func WithRawAgentArgs(text string) SpawnOption {
	return func(o *spawnOpts) { o.set("agent_args", text) }
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

// WithNoSubagentTracking stores 0 as the row's schema-v9 subagent_tracking: a
// row whose current launch registered no subagent events, one from before the
// upgrade or a Codex launch (InsertPending stores 0 for a codex agent and 1 for
// a claude one). Without it a seeded row is tracked (1) whatever its agent, so
// a codex seed passes it to look as a Codex launch stored it.
func WithNoSubagentTracking() SpawnOption {
	return func(o *spawnOpts) { o.set("subagent_tracking", int64(0)) }
}

// SeededSubagentMs is the instant, in Unix milliseconds, SeedSubagent and
// SeedSubagentLaunch store as opened_at (and SeedSubagent as activity_at) when
// no option names one, so a default seed is deterministic and ties fall to
// created_seq.
const SeededSubagentMs int64 = 1_700_000_000_000

// subagentOpts accumulates what SeedSubagent's SubagentOption arguments
// request: cols maps a subagents column name to the value stored (nil stores
// NULL). launch is the launch token the entry belongs to; empty means the
// row's current launch token, read at seed time.
type subagentOpts struct {
	cols   map[string]any
	launch string
}

// set records value for column; nil stores NULL.
func (o *subagentOpts) set(column string, value any) {
	if o.cols == nil {
		o.cols = map[string]any{}
	}
	o.cols[column] = value
}

// SubagentOption configures one subagents row seeded by SeedSubagent. A column
// no option names takes SeedSubagent's default: a confirmed, running entry of
// the row's current life and launch, opened_at and activity_at SeededSubagentMs, every
// other field NULL. Options write raw column values and enforce no SR-6.5
// invariant, so a test can also seed a row that breaks one; a later option for
// the same column wins.
type SubagentOption func(*subagentOpts)

// WithSubagentLife stores the life the entry belongs to; otherwise the owning row supplies it.
func WithSubagentLife(life int64) SubagentOption {
	return func(o *subagentOpts) { o.set("life_number", life) }
}

// WithSubagentState stores state (running, stopped or lost, or any text) as the
// entry's state.
func WithSubagentState(state string) SubagentOption {
	return func(o *subagentOpts) { o.set("state", state) }
}

// WithSubagentCloseReason stores reason (stop_event, absent_from_snapshot or
// agent_gone, or any text) as the entry's close_reason.
func WithSubagentCloseReason(reason string) SubagentOption {
	return func(o *subagentOpts) { o.set("close_reason", reason) }
}

// WithSubagentLaunchToken makes the entry belong to the launch with this token
// (an earlier launch of the row, say) instead of the row's current one.
func WithSubagentLaunchToken(token string) SubagentOption {
	return func(o *subagentOpts) { o.launch = token }
}

// WithSubagentConfirmed stores confirmed: true (the default) for a record,
// false for an observation.
func WithSubagentConfirmed(confirmed bool) SubagentOption {
	v := int64(0)
	if confirmed {
		v = 1
	}
	return func(o *subagentOpts) { o.set("confirmed", v) }
}

// WithSubagentSessionID stores the entry's session_id.
func WithSubagentSessionID(id string) SubagentOption {
	return func(o *subagentOpts) { o.set("session_id", id) }
}

// WithSubagentType stores the entry's subagent_type.
func WithSubagentType(t string) SubagentOption {
	return func(o *subagentOpts) { o.set("subagent_type", t) }
}

// WithSubagentDescription stores the entry's description.
func WithSubagentDescription(d string) SubagentOption {
	return func(o *subagentOpts) { o.set("description", d) }
}

// WithSubagentModel stores the entry's model.
func WithSubagentModel(m string) SubagentOption {
	return func(o *subagentOpts) { o.set("model", m) }
}

// WithSubagentParent stores the entry's parent_type (agent or subagent) and
// parent_id.
func WithSubagentParent(parentType, parentID string) SubagentOption {
	return func(o *subagentOpts) {
		o.set("parent_type", parentType)
		o.set("parent_id", parentID)
	}
}

// WithSubagentToolUseID stores the entry's tool_use_id.
func WithSubagentToolUseID(id string) SubagentOption {
	return func(o *subagentOpts) { o.set("tool_use_id", id) }
}

// WithSubagentRunMode stores the entry's run_mode (background or foreground).
func WithSubagentRunMode(mode string) SubagentOption {
	return func(o *subagentOpts) { o.set("run_mode", mode) }
}

// WithSubagentFirstStartedAt stores first_started_at, in Unix milliseconds.
func WithSubagentFirstStartedAt(ms int64) SubagentOption {
	return func(o *subagentOpts) { o.set("first_started_at", ms) }
}

// WithSubagentStartedAt stores started_at, in Unix milliseconds.
func WithSubagentStartedAt(ms int64) SubagentOption {
	return func(o *subagentOpts) { o.set("started_at", ms) }
}

// WithSubagentStoppedAt stores stopped_at, in Unix milliseconds.
func WithSubagentStoppedAt(ms int64) SubagentOption {
	return func(o *subagentOpts) { o.set("stopped_at", ms) }
}

// WithSubagentLostAt stores lost_at, in Unix milliseconds.
func WithSubagentLostAt(ms int64) SubagentOption {
	return func(o *subagentOpts) { o.set("lost_at", ms) }
}

// WithSubagentOpenedAt stores opened_at, in Unix milliseconds: the latest time
// the entry became running, or its creation time.
func WithSubagentOpenedAt(ms int64) SubagentOption {
	return func(o *subagentOpts) { o.set("opened_at", ms) }
}

// WithSubagentActivityAt stores activity_at, in Unix milliseconds: the instant
// of the latest write that changed the entry, which the entry cap orders by.
func WithSubagentActivityAt(ms int64) SubagentOption {
	return func(o *subagentOpts) { o.set("activity_at", ms) }
}

// WithSubagentTypeSource stores subagent_type_source (start, details or
// snapshot).
func WithSubagentTypeSource(source string) SubagentOption {
	return func(o *subagentOpts) { o.set("subagent_type_source", source) }
}

// WithSubagentRunModeSource stores run_mode_source (details or snapshot).
func WithSubagentRunModeSource(source string) SubagentOption {
	return func(o *subagentOpts) { o.set("run_mode_source", source) }
}

// WithSubagentStoppedAtSource stores stopped_at_source (stop or details).
func WithSubagentStoppedAtSource(source string) SubagentOption {
	return func(o *subagentOpts) { o.set("stopped_at_source", source) }
}

// subagentLaunchOpts accumulates what SeedSubagentLaunch's options request.
type subagentLaunchOpts struct {
	launch   string // "" = the row's current launch token
	typ      string
	openedAt int64
}

// SubagentLaunchOption configures one subagent_launches row seeded by
// SeedSubagentLaunch. Defaults: the row's current launch, the type
// "general-purpose", opened_at SeededSubagentMs.
type SubagentLaunchOption func(*subagentLaunchOpts)

// WithClaimableLaunchToken makes the claimable launch belong to the launch with
// this token (an earlier launch of the row, say) instead of the current one.
func WithClaimableLaunchToken(token string) SubagentLaunchOption {
	return func(o *subagentLaunchOpts) { o.launch = token }
}

// WithClaimableType stores the claimable launch's subagent_type.
func WithClaimableType(t string) SubagentLaunchOption {
	return func(o *subagentLaunchOpts) { o.typ = t }
}

// WithClaimableOpenedAt stores the claimable launch's opened_at, in Unix
// milliseconds: what the claimable-launch cap orders by.
func WithClaimableOpenedAt(ms int64) SubagentLaunchOption {
	return func(o *subagentLaunchOpts) { o.openedAt = ms }
}

// WithPermissionEpoch seeds the current continuous permission interval.
func WithPermissionEpoch(epoch int64) SpawnOption {
	return func(o *spawnOpts) { o.set("permission_epoch", epoch) }
}

// WithStuckObservation seeds committed report detail or a pending candidate.
func WithStuckObservation(raw string) SpawnOption {
	return func(o *spawnOpts) { o.set("stuck_observation", nullIfZero(raw)) }
}

// WithStuckExit seeds a pending stuck exit record (valid or malformed).
func WithStuckExit(raw string) SpawnOption {
	return func(o *spawnOpts) { o.set("stuck_exit", nullIfZero(raw)) }
}
