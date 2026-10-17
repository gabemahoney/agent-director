package api

import (
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
)

// PermissionRequestInfo is the projection of one permission request with its
// delivery facts: an open one (still awaiting an answer, b.146 rule 9) in the
// permission_requests of a `get` or `list` row in state `check_permission`;
// one the agent has not proven gone (b.146 step 2c), open or closed in
// agent-director's own records, in `get`'s unproven_requests (a row in any
// state but ended and missing); and the request an err_details names (the
// base of RelayFallenBackDetails and DialogMaybeOpenDetails). ToolInput is
// the raw JSON string from the DB column — callers parse it themselves; the
// verb MUST NOT re-encode it as a nested object. The embedded RequestDelivery
// carries the request's delivery facts (b.146 rule 15) and its proof (b.146
// step 2c).
type PermissionRequestInfo struct {
	// RequestID is the autoincrement primary key of the permission_requests row.
	RequestID int64 `json:"request_id"`
	// RequestToken is the UUIDv4 token minted by runRelay for this request.
	// Callers pass it back to the decide verb to target a specific row.
	RequestToken string `json:"request_token"`
	// ToolName is the agent's tool that triggered the permission request
	// (e.g. "Bash", "Write").
	ToolName string `json:"tool_name"`
	// ToolInput is the raw JSON string of the tool's input as stored in the DB.
	// It is NOT a nested JSON object — callers unmarshal it themselves. Cursor
	// preserves the whole permission/plan params envelope, including raw options
	// and absent/null fields, without adding internal connection context.
	ToolInput string `json:"tool_input"`
	// RequestedAt is the RFC3339 timestamp when the permission request row was
	// created (created_at column).
	RequestedAt time.Time `json:"requested_at"`
	// Decision is the verdict recorded on the request, "allow" or "deny";
	// null while none is. A recorded verdict is not the outcome: Delivery
	// says whether the relay durably acknowledged it; acknowledgement does not
	// establish receipt, execution or dialog closure.
	Decision *string `json:"decision"`
	// DecisionReason is the recorded verdict's decision_reason; null when
	// none is recorded.
	DecisionReason *string `json:"decision_reason"`
	RequestDelivery
}

// SpawnRow is the JSON-friendly projection of store.Spawn. Field names
// match the SRD §4.2 column names so callers reading the verb output can
// cross-reference against the schema doc without translation.
type SpawnRow struct {
	// InstanceID is the stable id of the Spawn (UUID4 or caller-supplied).
	InstanceID string `json:"instance_id"`
	// ParentID is the instance_id of the Spawn that launched this one
	// (read from AGENT_DIRECTOR_INSTANCE_ID env at spawn time). Empty when
	// launched from a plain shell.
	ParentID string `json:"parent_id"`
	// State is the current lifecycle state. One of: pending, waiting, working,
	// ask_user, check_permission, ended, missing. pending means a launch
	// (spawn, reuse or resume) is in progress and the agent has not reported
	// in yet (its SessionStart hook); it may be loading or waiting at a
	// startup prompt. A resumed pending row keeps its session id and history;
	// a non-empty SessionID tells it from a fresh one.
	State string `json:"state"`
	// CWD is the canonicalized working directory the Spawn's agent was started in.
	CWD string `json:"cwd"`
	// Agent is the agent the row runs, "claude", "codex" or "cursor": set by the spawn
	// that most recently launched the row, followed by
	// resume, never changed by anything else (SR-1.5).
	Agent string `json:"agent"`
	// TmuxSessionName is the tmux session under which the Spawn is running.
	TmuxSessionName string `json:"tmux_session_name"`
	// TmuxSocket is the absolute path of the tmux socket the row's latest
	// launch recorded, shown for a row in any state (a finished row keeps
	// its latest launch's socket). Empty (and omitted from JSON) for a row
	// with no recorded socket. Display only: no verb
	// reads it back (SR-3.3, SR-16.1).
	TmuxSocket string `json:"tmux_socket,omitempty"`
	// AgentArgs is the verbatim argv passed through to the agent program after the
	// supervisor's own flags.
	// Always a non-nil slice (possibly empty) for JSON-stability.
	AgentArgs []string `json:"agent_args"`
	// RelayMode is "on" or "off" — whether this Spawn participates in the
	// permission-relay flow.
	RelayMode string `json:"relay_mode"`
	// JSONLPath is the current session's transcript path, persisted by its
	// hooks; legacy rows may be empty. Resume tries it, then for Claude
	// recomputes only the current session's path from cwd + session_id under
	// a usable absolute CLAUDE_CONFIG_DIR (otherwise ~/.claude). Codex uses
	// only the recorded path. Older sessions are never resume candidates.
	JSONLPath string `json:"jsonl_path"`
	// SessionID is the agent's session id, recorded from its hooks: a Claude
	// Code session's is the basename of the hook's transcript_path, a Codex
	// session's the session_id its hooks report. Empty until the first
	// SessionStart fires.
	SessionID string `json:"session_id"`
	// Labels are the caller-supplied key-value tags set at spawn time.
	// Always a non-nil map (possibly empty) for JSON-stability.
	Labels map[string]string `json:"labels"`
	// StartedAt is the time the row was inserted (spawn time).
	StartedAt time.Time `json:"started_at"`
	// LastSeenAt is the time of the most recent hook UPSERT for this Spawn.
	LastSeenAt time.Time `json:"last_seen_at"`
	// EndedAt is set when state moves to ended. Omitted from JSON (omitempty)
	// while the Spawn is live.
	EndedAt *time.Time `json:"ended_at,omitempty"`
	// LaunchStartedAt is the start of the launch in progress, RFC3339 UTC
	// with millisecond precision. Present only on a pending row; nil (and
	// omitted from JSON) otherwise (SR-22.2).
	LaunchStartedAt *time.Time `json:"launch_started_at,omitempty"`
	// LivenessUnverifiedSince is the RFC3339 timestamp of the first sweep that
	// noted this live row (left it unverified, or noted it unreported,
	// b.kdf); it is kept while later sweeps change LivenessNote, unreported
	// included. Nil (and omitted from JSON) when NULL in the store — i.e. no
	// note: a sweep that finds the agent process alive clears it together
	// with LivenessNote, except on a pending row it notes unreported and a
	// pending row noted provenance_conflict, which keeps it. The store
	// carries it as a COALESCE-scanned string ("" == NULL); Get maps "" to nil
	// per the ended_at nullable precedent.
	LivenessUnverifiedSince *string `json:"liveness_unverified_since,omitempty"`
	// LivenessNote is the reason token of the latest sweep that left this
	// live row unverified (its agent process could not be checked and tmux
	// did not settle it), for example process_not_seen_session_present,
	// process_not_seen_tmux_unchecked, probe_eacces, tmux_server_changed or
	// provenance_conflict; a later sweep overwrites it when the reason
	// changes, and a sweep that finds the agent process alive clears it
	// together with LivenessUnverifiedSince, except on a pending row it notes
	// unreported and a pending row noted provenance_conflict, which keeps it.
	// Or it is unreported, on a pending row past the pending grace period
	// (b.kdf): its agent is alive, but no hook has reported since its launch,
	// so it may sit at a Claude Code startup screen or idle at its prompt.
	// The row stays pending; the caller reads its pane and, only having
	// looked, sends keys with allow_pending. A later sweep that finds the
	// agent alive keeps the note, and the agent's next hook clears it.
	// unreported never replaces provenance_conflict. Nil (omitted) when NULL
	// in the store. Same "" == NULL mapping.
	LivenessNote *string `json:"liveness_note,omitempty"`
	// PermissionRequests is the slice of open permission requests: those that
	// still await an answer (not acked by their relay hook and not answered
	// at the pane, decided or not; one recorded before schema v7 while
	// undecided), each with its delivery facts. Populated only when the row's
	// live (raw) state is check_permission, which with [state] settle_ms set
	// State may not show yet (requests are never debounced, b.3ie); always a
	// non-nil slice (encodes as [] when empty, never null, never omitted).
	// Callers use the request_token of each element to target a specific row
	// with the decide verb.
	PermissionRequests []PermissionRequestInfo `json:"permission_requests"`
	// UnprovenRequests is every permission request of the row that the agent
	// has not proven gone (b.146 step 2c: proven_gone_at null), oldest
	// first, each with its delivery facts, in any state but ended and missing
	// ([] for those states). It lists requests that still await an
	// answer and, unlike PermissionRequests, those agent-director's own
	// records read closed: acked (delivery delivered), answered at the pane or
	// recorded answered outside it; such a request carries unproven_since.
	// While it is not empty, plain send-keys without a pane hash, and pause
	// (b.zdz), are refused (ErrDialogMaybeOpen or an earlier relay refusal).
	// Always a non-nil slice (encodes as [] when empty).
	UnprovenRequests []PermissionRequestInfo `json:"unproven_requests"`
	// TranscriptStatus is a derived, operator-facing summary of the current
	// session's transcript state (b.v2c AC8). Session history belongs to a
	// life; "never_written" and "rotated" are decided on the visible history
	// — the current life's history minus the entry for the row's current
	// session id — the same entries PriorSessions lists. One of:
	//   - "present":      jsonl_path is recorded (a verified transcript).
	//   - "never_written": a session id exists but jsonl_path is NULL and the
	//                      visible history is empty — nothing was ever written
	//                      in the current life (the case of a freshly restarted
	//                      agent that has not been messaged).
	//   - "rotated":       jsonl_path is NULL and the visible history is
	//                      non-empty — the current life has history under a
	//                      different session id (see prior_sessions). This is
	//                      the case a bare jsonl_path could not distinguish
	//                      from "never_written".
	//   - "no_session":    no session_id yet (pre-first-SessionStart).
	// Same-agent reuse retains history; a switch or switch back starts a new
	// life with no history; a failed reuse's restore returns the row to its
	// pre-reuse life, whose history counts again.
	TranscriptStatus string `json:"transcript_status"`
	// PriorSessions is the row's visible history, newest first: the archived
	// sessions of the current life (session history belongs to a life),
	// minus the entry for the row's current session id. It is the queryable
	// link from this row back to earlier sessions orphaned by a rotation
	// (b.v2c AC6/AC8). Same-agent reuse retains these sessions; a switch or
	// switch back starts a new life and hides earlier lives. Older sessions
	// are listed for inspection only, never resumed; a failed reuse's restore
	// returns the row to its pre-reuse life, whose sessions are listed again. Always
	// a non-nil slice (encodes as []).
	PriorSessions []PriorSession `json:"prior_sessions"`
}

// PriorSession is one archived (session_id, jsonl_path) pair the Spawn
// pointed at before a session rotation (b.v2c). JSONLPath is empty when the
// archived session had no recorded transcript path.
type PriorSession struct {
	// SessionID is the archived session's id.
	SessionID string `json:"session_id"`
	// JSONLPath is the archived session's recorded transcript path (may be empty).
	JSONLPath string `json:"jsonl_path"`
	// RecordedAt is when the archive row was written (the rotation moment).
	RecordedAt string `json:"recorded_at"`
}

// nullableString maps a COALESCE-scanned store string ("" == NULL) to the
// pointer-with-omitempty JSON shape used for nullable, omit-when-NULL fields.
// Returns nil for "" (encodes as omitted), a pointer to the value otherwise.
// Mirrors the ended_at *time.Time nullable precedent for the liveness columns,
// which the store carries as plain strings rather than sql.Null types.
func nullableString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// sqliteTimestampLayout is the text layout SQLite's CURRENT_TIMESTAMP writes
// ("2026-09-20 01:38:41"). It carries no timezone; SQLite emits UTC, matching
// how ended_at/started_at (scanned into time.Time via modernc.org/sqlite) land
// in UTC. liveness_unverified_since is COALESCE-scanned as raw text rather than
// time.Time, so it never gets that free RFC3339 normalization — nullableTimestamp
// supplies it.
const sqliteTimestampLayout = "2006-01-02 15:04:05"

// nullableTimestamp maps a COALESCE-scanned store timestamp string ("" == NULL)
// to the pointer-with-omitempty JSON shape, normalizing the value to RFC3339 so
// the wire contract ("RFC3339 timestamp") holds for liveness_unverified_since.
//
// The store column carries SQLite CURRENT_TIMESTAMP text ("2026-09-20 01:38:41",
// UTC). This parses that layout in UTC and formats RFC3339. If that fails it
// tries RFC3339 directly (test seeders may write RFC3339 via apitest options) and
// re-formats it normalized. If both fail it passes the raw string through
// verbatim — fail-open, never dropping data nor erroring the verb.
func nullableTimestamp(s string) *string {
	if s == "" {
		return nil
	}
	if t, err := time.Parse(sqliteTimestampLayout, s); err == nil {
		out := t.UTC().Format(time.RFC3339)
		return &out
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		out := t.UTC().Format(time.RFC3339)
		return &out
	}
	return &s
}

// launchStartedAt maps a row's state and its launch start in milliseconds
// (0 = absent) to the launch_started_at field shared by status, get and
// list: nil unless the row is pending with a launch start, otherwise the
// instant in UTC at millisecond precision (SR-22.2).
func launchStartedAt(state string, millis int64) *time.Time {
	if state != store.StatePending || millis == 0 {
		return nil
	}
	t := millisToUTC(millis)
	return &t
}

// millisToUTC maps integer milliseconds since the epoch, as the store holds a
// time, to the instant in UTC. It is the one conversion behind
// launch_started_at and every time field of a subagent record, so they encode
// alike: RFC3339Nano with the millisecond fraction, trailing zeros dropped, no
// fraction on a whole second, ending Z.
func millisToUTC(millis int64) time.Time {
	return time.UnixMilli(millis).UTC()
}

// GetStore is the narrow store surface Get needs. Matches the existing
// methods on *store.Store; defined as an interface so api.Get's
// permission-fetch branch is testable without raw SQL fixtures. A GetStore
// that also records hook_gone_at (*store.Store does) gets that write from
// Get; one that does not reports the stored value only.
type GetStore interface {
	GetSpawn(instanceID string) (Spawn, error)
	// PermissionRequestsForSpawn reads every permission request of the
	// Spawn: Get reports, from that one read, those that still await an
	// answer (b.146 rule 9) and those not proven gone (b.146 step 2c).
	PermissionRequestsForSpawn(instanceID string) ([]PermissionRow, error)
	// ListSessionHistory returns the instance's archived sessions of one
	// life only — life is the LifeNumber of the row Get read — newest
	// first. Session history belongs to a life: each entry is a session that
	// ran while that life's id was current. The read does not apply the
	// current-session rule; Get does, keeping the visible history (that
	// life's entries minus the entry for the row's current session id) to
	// populate PriorSessions and derive TranscriptStatus (b.v2c AC6/AC8).
	// Same-agent reuse retains the history; a switch or switch back starts a
	// new life, so no earlier life's entry is read.
	ListSessionHistory(instanceID string, life int64) ([]SessionHistoryEntry, error)
}

// deriveTranscriptStatus computes the operator-facing transcript-status summary
// (b.v2c AC8) from the row's session id, jsonl_path, and the length of its
// visible history (visibleHistory).
func deriveTranscriptStatus(sessionID, jsonlPath string, historyLen int) string {
	switch {
	case sessionID == "":
		return "no_session"
	case jsonlPath != "":
		return "present"
	case historyLen > 0:
		return "rotated"
	default:
		return "never_written"
	}
}

// emptySpawnRow is the SpawnRow every failure of Get and (c *Client).Get
// returns: every field is zero except AgentArgs, Labels, PermissionRequests,
// UnprovenRequests and PriorSessions, which are non-nil and empty, as SpawnRow
// documents, so the result encodes them as [] and {} rather than null (b.4nt).
// It builds a fresh value on each call, so no two results share a slice or
// map.
func emptySpawnRow() SpawnRow {
	return SpawnRow{
		AgentArgs:          []string{},
		Labels:             map[string]string{},
		PermissionRequests: []PermissionRequestInfo{},
		UnprovenRequests:   []PermissionRequestInfo{},
		PriorSessions:      []PriorSession{},
	}
}

// Get returns the full Spawn row for the given instance_id. Missing
// rows surface store.ErrSpawnNotFound for the CLI to translate.
//
// Every failure (the row read's ErrSpawnNotFound or other error, the
// session-history read's, the permission-request read's, and ErrClientClosed
// from (c *Client).Get) returns emptySpawnRow: AgentArgs, Labels,
// PermissionRequests, UnprovenRequests and PriorSessions non-nil and empty,
// every other field zero (b.4nt).
//
// On a row in any state but ended and missing, every permission request of
// the row is read once (PermissionRequestsForSpawn), by the check-before-read
// rule (b.146 rule 5): read, judge each request's relay hook process, read
// again, so a hook that acked and exited between a read and its check is
// never reported fallen back. Both request lists are projected from that one
// read, each request with its delivery facts (b.146 rule 15) judged through
// v, oldest first:
//
//   - PermissionRequests, only when the row's state is `check_permission`:
//     its open requests, those that still await an answer (b.146 rule 9).
//     Closed requests (acked, answered at the pane, or closed with their
//     Spawn by find-missing's mark, the ended transition or resume's move)
//     are never visible in it. In other states it is an empty non-nil slice
//     (encodes as []).
//   - UnprovenRequests: every request Claude Code has not proven gone (b.146
//     step 2c), closed in agent-director's records or not.
//
// On an ended or missing row nothing is read and both are empty. Get is a
// reading verb and never waits for the store's write lock (b.146 problem 4):
// it writes hook_gone_at on the requests of either list it found fallen back,
// in one write, only if the lock is free at that moment, and otherwise
// reports them with hook_gone_at as stored.
//
// PriorSessions and TranscriptStatus cover the visible history only.
// Session history belongs to a life: Get reads the entries of the life of
// the row it read, then drops the entry for the row's current session id
// (a row with no current session id drops nothing).
//
// Get reads with no settle time; the Client applies its [state] settle_ms
// (getSettled).
func Get(s GetStore, v RelayView, instanceID string) (SpawnRow, error) {
	return getSettled(s, v, store.SettleRead{}, instanceID)
}

// settledSpawnReader is the optional read get uses with a settle time
// (b.3ie): the row with its reported state. *store.Store satisfies it; a
// GetStore without it reports the raw state.
type settledSpawnReader interface {
	GetSpawnSettled(instanceID string, r store.SettleRead) (Spawn, error)
}

// readSpawnSettled reads instanceID's row and the state get reports: with a
// settle time, and a store that provides the settled read, the row's
// ReportedState (store.SettleRead's derivation, in the read's own statement);
// otherwise the row GetSpawn returns and its raw state, exactly as before
// b.3ie.
func readSpawnSettled(s GetStore, settle store.SettleRead, instanceID string) (Spawn, string, error) {
	if sr, ok := s.(settledSpawnReader); ok && settle.SettleMs > 0 {
		row, err := sr.GetSpawnSettled(instanceID, settle)
		return row, row.ReportedState, err
	}
	row, err := s.GetSpawn(instanceID)
	return row, row.State, err
}

// getSettled is Get with the reader's settle time and clock settle (b.3ie):
// the row's state field is the settled state. Everything else keys on the
// raw state, as before: launch_started_at, and whether the permission
// requests are read and shown (permission requests are never debounced). With
// no settle time it is Get exactly as before b.3ie. It never waits for a state
// to settle.
func getSettled(s GetStore, v RelayView, settle store.SettleRead, instanceID string) (SpawnRow, error) {
	row, reported, err := readSpawnSettled(s, settle, instanceID)
	if err != nil {
		return emptySpawnRow(), err
	}
	out := SpawnRow{
		InstanceID:              row.InstanceID,
		ParentID:                row.ParentID,
		State:                   reported,
		CWD:                     row.CWD,
		Agent:                   row.Agent,
		TmuxSessionName:         row.TmuxSessionName,
		TmuxSocket:              row.Identity.Socket,
		AgentArgs:               row.AgentArgs,
		RelayMode:               row.RelayMode,
		JSONLPath:               row.JSONLPath,
		SessionID:               row.SessionID,
		Labels:                  row.Labels,
		StartedAt:               row.StartedAt,
		LastSeenAt:              row.LastSeenAt,
		EndedAt:                 row.EndedAt,
		LaunchStartedAt:         launchStartedAt(row.State, row.LaunchStartedAtMillis),
		LivenessUnverifiedSince: nullableTimestamp(row.LivenessUnverifiedSince),
		LivenessNote:            nullableString(row.LivenessNote),
		PermissionRequests:      []PermissionRequestInfo{},
		UnprovenRequests:        []PermissionRequestInfo{},
		PriorSessions:           []PriorSession{},
	}

	// b.v2c AC6/AC8: surface the visible history (the current life's archived
	// sessions minus the entry for the row's current session id) and derive
	// the operator-facing transcript status from it, so "no history in this
	// life" is distinguishable from "history exists under a different session
	// id".
	lifeHistory, err := s.ListSessionHistory(instanceID, row.LifeNumber)
	if err != nil {
		return emptySpawnRow(), err
	}
	history := visibleHistory(row, lifeHistory)
	for _, h := range history {
		out.PriorSessions = append(out.PriorSessions, PriorSession{
			SessionID:  h.SessionID,
			JSONLPath:  h.JSONLPath,
			RecordedAt: h.RecordedAt,
		})
	}
	out.TranscriptStatus = deriveTranscriptStatus(row.SessionID, row.JSONLPath, len(history))
	// Normalize: callers reading `agent_args:null` cannot distinguish
	// from `[]`; always emit a non-nil slice for the JSON output.
	if out.AgentArgs == nil {
		out.AgentArgs = []string{}
	}
	if out.Labels == nil {
		out.Labels = map[string]string{}
	}

	// The raw state decides the request lists: permission requests are not
	// debounced (b.3ie), so the settled state can be any state, even ended or
	// missing, while the raw check_permission shows its open requests.
	j := newRelayJudge(v)
	if !finishedState(row.State) {
		open, unproven, err := spawnRequestInfos(s.PermissionRequestsForSpawn, j, instanceID)
		if err != nil {
			return emptySpawnRow(), err
		}
		if row.State == store.StateCheckPermission {
			out.PermissionRequests = open
		}
		out.UnprovenRequests = unproven
	}
	slots := goneSlotsOf(nil, out.PermissionRequests)
	recordGone(s, j.now(), readerLockWait, goneSlotsOf(slots, out.UnprovenRequests))

	return out, nil
}

// Get returns the full DB row for a tracked Spawn: state, cwd, tmux session
// name, relay mode, session id, labels, timestamps, and (when applicable) the
// open permission-requests slice, each request with its delivery facts
// (delivery, confirm_by, hook_alive, hook_gone_at, attempted_decision,
// attempted_at, tool_use_id, pane_answer, pane_as, proven_gone_at,
// proven_gone_how, unproven_since). On a row that is not ended or missing it
// carries unproven_requests: every permission request Claude Code has not
// proven gone, closed in agent-director's records or not, each with the same
// facts; while one is listed, a plain send-keys without a pane hash, and
// pause, are refused. On a pending row it also carries launch_started_at, when the
// agent's launch began. When the row records
// one, it carries tmux_socket, the tmux socket the row's latest launch
// uses, in any state. prior_sessions and transcript_status come from the
// current life's history only. Same-agent reuse retains it; an agent switch
// or switch back starts a new life and hides earlier lives. Older sessions
// are listed for inspection only; resume continues only the current session.
// With [state] settle_ms set, state is the settled state: a hook-written
// change shows only once it has held that long, the last settled state until
// then; terminal states and command-driven changes show at once. The
// permission requests and launch_started_at follow the live state.
// Get never waits for the store's write lock, nor for a state to settle.
//
// CLI: agent-director get
//
// Errors:
//   - [ErrSpawnNotFound]: no row exists for instanceID.
//
// Nondeterminism: the delivery facts depend on the clock and on whether each
// request's relay hook process runs; with a settle time, so does the state.
func (c *Client) Get(instanceID string) (SpawnRow, error) {
	if err := c.checkClosed(); err != nil {
		return emptySpawnRow(), err
	}
	proof, err := c.reconcileCursorGateway(instanceID)
	if err != nil {
		return emptySpawnRow(), err
	}
	var proofs []store.CursorMissingProof
	if proof != nil {
		proofs = append(proofs, *proof)
	}
	out, err := getSettled(c.st.WithCursorMissingReads(proofs), c.relayView(), c.settleRead(), instanceID)
	if err != nil {
		return emptySpawnRow(), err
	}
	if err := c.persistCursorMissing(proofs); err != nil {
		return emptySpawnRow(), err
	}
	return out, nil
}

// Compile-time assertion that *store.Store provides the settled read the
// Client's get uses with a settle time.
var _ settledSpawnReader = (*store.Store)(nil)
