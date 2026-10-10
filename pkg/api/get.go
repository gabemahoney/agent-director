package api

import (
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
)

// PermissionRequestInfo is the open permission_requests row projection
// surfaced on `get` when the spawn is in state `check_permission`.
// ToolInput is the raw JSON string from the DB column — callers parse
// it themselves; the verb MUST NOT re-encode it as a nested object.
type PermissionRequestInfo struct {
	// RequestID is the autoincrement primary key of the permission_requests row.
	RequestID int64 `json:"request_id"`
	// RequestToken is the UUIDv4 token minted by runRelay for this request.
	// Callers pass it back to the decide verb to target a specific row.
	RequestToken string `json:"request_token"`
	// ToolName is the Claude Code tool that triggered the permission request
	// (e.g. "Bash", "Write").
	ToolName string `json:"tool_name"`
	// ToolInput is the raw JSON string of the tool's input as stored in the DB.
	// It is NOT a nested JSON object — callers unmarshal it themselves.
	ToolInput string `json:"tool_input"`
	// RequestedAt is the RFC3339 timestamp when the permission request row was
	// created (created_at column).
	RequestedAt time.Time `json:"requested_at"`
}

// SpawnRow is the JSON-friendly projection of store.Spawn. Field names
// match the SRD §4.2 column names so callers reading the verb output can
// cross-reference against the schema doc without translation.
type SpawnRow struct {
	// ClaudeInstanceID is the stable id of the Spawn (UUID4 or caller-supplied).
	ClaudeInstanceID string `json:"claude_instance_id"`
	// ParentID is the claude_instance_id of the Spawn that launched this one
	// (read from AGENT_DIRECTOR_INSTANCE_ID env at spawn time). Empty when
	// launched from a plain shell.
	ParentID string `json:"parent_id"`
	// State is the current lifecycle state. One of: pending, waiting, working,
	// ask_user, check_permission, ended, missing. pending means a launch
	// (spawn, reuse or resume) is in progress and the agent has not reported
	// in yet (Claude Code's SessionStart); it may be loading or waiting at a
	// startup prompt. A resumed pending row keeps its session id and history;
	// a non-empty ClaudeSessionID tells it from a fresh one.
	State string `json:"state"`
	// CWD is the canonicalized working directory the Spawn's Claude was started in.
	CWD string `json:"cwd"`
	// TmuxSessionName is the tmux session under which the Spawn is running.
	TmuxSessionName string `json:"tmux_session_name"`
	// TmuxSocket is the absolute path of the tmux socket the row's latest
	// launch recorded, shown for a row in any state (a finished row keeps
	// its latest launch's socket). Empty (and omitted from JSON) for a row
	// from before this release, which records none. Display only: no verb
	// reads it back (SR-3.3, SR-16.1).
	TmuxSocket string `json:"tmux_socket,omitempty"`
	// ClaudeArgs is the verbatim argv passed through to claude after the
	// supervisor's own flags.
	// Always a non-nil slice (possibly empty) for JSON-stability.
	ClaudeArgs []string `json:"claude_args"`
	// RelayMode is "on" or "off" — whether this Spawn participates in the
	// permission-relay flow.
	RelayMode string `json:"relay_mode"`
	// JSONLPath is the last known JSONL transcript path, persisted by the
	// SessionStart hook; legacy rows may be empty. When empty, resume composes
	// the path on demand from cwd + claude_session_id.
	JSONLPath string `json:"jsonl_path"`
	// ClaudeSessionID is the Claude Code session UUID extracted from the
	// SessionStart hook's transcript_path basename. Empty until the first
	// SessionStart fires.
	ClaudeSessionID string `json:"claude_session_id"`
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
	// PermissionRequests is the slice of open permission requests awaiting
	// orchestrator decisions. Populated only when state is check_permission;
	// always a non-nil slice (encodes as [] when empty, never null, never
	// omitted). Callers use the request_token of each element to target a
	// specific row with the decide verb.
	PermissionRequests []PermissionRequestInfo `json:"permission_requests"`
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
	//   - "no_session":    no claude_session_id yet (pre-first-SessionStart).
	// A reuse starts a new life, so its status is decided on that life's
	// history alone; a failed reuse's restore returns the row to its
	// pre-reuse life, whose history counts again.
	TranscriptStatus string `json:"transcript_status"`
	// PriorSessions is the row's visible history, newest first: the archived
	// sessions of the current life (session history belongs to a life),
	// minus the entry for the row's current session id. It is the queryable
	// link from this row back to earlier sessions orphaned by a rotation
	// (b.v2c AC6/AC8). A reuse starts a new life, so after a reuse it lists
	// none of the earlier lives' sessions; a failed reuse's restore returns
	// the row to its pre-reuse life, whose sessions are listed again. Always
	// a non-nil slice (encodes as []).
	PriorSessions []PriorSession `json:"prior_sessions"`
}

// PriorSession is one archived (claude_session_id, jsonl_path) pair the Spawn
// pointed at before a session rotation (b.v2c). JSONLPath is empty when the
// archived session had no recorded transcript path.
type PriorSession struct {
	// ClaudeSessionID is the archived session's id.
	ClaudeSessionID string `json:"claude_session_id"`
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
	t := time.UnixMilli(millis).UTC()
	return &t
}

// GetStore is the narrow store surface Get needs. Matches the existing
// methods on *store.Store; defined as an interface so api.Get's
// permission-fetch branch is testable without raw SQL fixtures.
type GetStore interface {
	GetSpawn(instanceID string) (Spawn, error)
	OpenPermissionRequestsForSpawn(instanceID string) ([]PermissionRow, error)
	// ListSessionHistory returns the instance's archived sessions of one
	// life only — life is the LifeNumber of the row Get read — newest
	// first. Session history belongs to a life: each entry is a session that
	// ran while that life's id was current. The read does not apply the
	// current-session rule; Get does, keeping the visible history (that
	// life's entries minus the entry for the row's current session id) to
	// populate PriorSessions and derive TranscriptStatus (b.v2c AC6/AC8).
	// A reuse starts a new life, so after a reuse no earlier life's entry is
	// read.
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
// returns: every field is zero except ClaudeArgs, Labels, PermissionRequests
// and PriorSessions, which are non-nil and empty, as SpawnRow documents, so
// the result encodes them as [] and {} rather than null (b.4nt). It builds a
// fresh value on each call, so no two results share a slice or map.
func emptySpawnRow() SpawnRow {
	return SpawnRow{
		ClaudeArgs:         []string{},
		Labels:             map[string]string{},
		PermissionRequests: []PermissionRequestInfo{},
		PriorSessions:      []PriorSession{},
	}
}

// Get returns the full Spawn row for the given claude_instance_id. Missing
// rows surface store.ErrSpawnNotFound for the CLI to translate.
//
// Every failure (the row read's ErrSpawnNotFound or other error, the
// session-history read's, the permission-request read's, and ErrClientClosed
// from (c *Client).Get) returns emptySpawnRow: ClaudeArgs, Labels,
// PermissionRequests and PriorSessions non-nil and empty, every other field
// zero (b.4nt).
//
// When the spawn's state is `check_permission`, all open (undecided)
// permission_requests rows are fetched and projected into the
// PermissionRequests slice. For all other states the slice is left as
// an empty non-nil slice (encodes as []).
//
// Open-rows-only contract: closed (decided) rows are never visible in
// the PermissionRequests output. OpenPermissionRequestsForSpawn enforces
// this at the SQL layer (decision IS NULL predicate).
//
// PriorSessions and TranscriptStatus cover the visible history only.
// Session history belongs to a life: Get reads the entries of the life of
// the row it read, then drops the entry for the row's current session id
// (a row with no current session id drops nothing).
func Get(s GetStore, instanceID string) (SpawnRow, error) {
	row, err := s.GetSpawn(instanceID)
	if err != nil {
		return emptySpawnRow(), err
	}
	out := SpawnRow{
		ClaudeInstanceID:        row.ClaudeInstanceID,
		ParentID:                row.ParentID,
		State:                   row.State,
		CWD:                     row.CWD,
		TmuxSessionName:         row.TmuxSessionName,
		TmuxSocket:              row.Identity.Socket,
		ClaudeArgs:              row.ClaudeArgs,
		RelayMode:               row.RelayMode,
		JSONLPath:               row.JSONLPath,
		ClaudeSessionID:         row.ClaudeSessionID,
		Labels:                  row.Labels,
		StartedAt:               row.StartedAt,
		LastSeenAt:              row.LastSeenAt,
		EndedAt:                 row.EndedAt,
		LaunchStartedAt:         launchStartedAt(row.State, row.LaunchStartedAtMillis),
		LivenessUnverifiedSince: nullableTimestamp(row.LivenessUnverifiedSince),
		LivenessNote:            nullableString(row.LivenessNote),
		PermissionRequests:      []PermissionRequestInfo{},
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
			ClaudeSessionID: h.ClaudeSessionID,
			JSONLPath:       h.JSONLPath,
			RecordedAt:      h.RecordedAt,
		})
	}
	out.TranscriptStatus = deriveTranscriptStatus(row.ClaudeSessionID, row.JSONLPath, len(history))
	// Normalize: callers reading `claude_args:null` cannot distinguish
	// from `[]`; always emit a non-nil slice for the JSON output.
	if out.ClaudeArgs == nil {
		out.ClaudeArgs = []string{}
	}
	if out.Labels == nil {
		out.Labels = map[string]string{}
	}

	if out.State == "check_permission" {
		prs, err := s.OpenPermissionRequestsForSpawn(instanceID)
		if err != nil {
			return emptySpawnRow(), err
		}
		for _, pr := range prs {
			out.PermissionRequests = append(out.PermissionRequests, PermissionRequestInfo{
				RequestID:    pr.RequestID,
				RequestToken: pr.RequestToken,
				ToolName:     pr.ToolName,
				ToolInput:    pr.ToolInput,
				RequestedAt:  pr.CreatedAt,
			})
		}
	}

	return out, nil
}

// Get returns the full DB row for a tracked Spawn: state, cwd, tmux session
// name, relay mode, session id, labels, timestamps, and (when applicable) the
// open permission-requests slice. On a pending row it also carries
// launch_started_at, when the agent's launch began. When the row records
// one, it carries tmux_socket, the tmux socket the row's latest launch
// uses, in any state. prior_sessions and transcript_status come from the
// current life's history only: after a reuse (spawn with ReuseFinished),
// which starts a new life, Get shows none of the earlier lives' history.
//
// CLI: agent-director get
//
// Errors:
//   - [ErrSpawnNotFound]: no row exists for claudeInstanceID.
//
// Nondeterminism: none.
func (c *Client) Get(claudeInstanceID string) (SpawnRow, error) {
	if err := c.checkClosed(); err != nil {
		return emptySpawnRow(), err
	}
	return Get(c.st, claudeInstanceID)
}
