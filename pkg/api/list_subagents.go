package api

import (
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
)

// SubagentRecord is one subagent of an agent, as list-subagents reports it.
// Every field is always present in the JSON: a field with no known value
// encodes as null, never omitted and never an empty string or zero standing
// in for unknown. Each time is an instant in UTC from the store's
// milliseconds, encoded exactly as a pending row's launch_started_at is:
// RFC3339 with the millisecond fraction, trailing zeros dropped, no fraction on
// a whole second, ending Z.
type SubagentRecord struct {
	// SubagentID is the subagent's id, unique within its agent.
	SubagentID string `json:"subagent_id"`
	// InstanceID is the instance_id of the agent the subagent belongs to.
	InstanceID string `json:"instance_id"`
	// SessionID is the session of the event that created the record. It
	// never changes after. Null while unknown.
	SessionID *string `json:"session_id"`
	// SubagentType is the subagent's type; null while unknown.
	SubagentType *string `json:"subagent_type"`
	// Description is the short description the launching call gave the
	// subagent. It is never the prompt text. Null while unknown.
	Description *string `json:"description"`
	// Model is the model the subagent runs; null while unknown.
	Model *string `json:"model"`
	// ParentType says what launched the subagent: "agent" (the agent itself,
	// so ParentID is its instance_id) or "subagent" (another subagent, so
	// ParentID is that subagent's id). Null while the parent is unknown.
	ParentType *string `json:"parent_type"`
	// ParentID is the instance_id or the parent subagent's id, as ParentType
	// says; null while the parent is unknown.
	ParentID *string `json:"parent_id"`
	// ToolUseID is the id of the tool call that launched the subagent; null
	// while unknown.
	ToolUseID *string `json:"tool_use_id"`
	// RunMode is "background" or "foreground"; null while unknown.
	RunMode *string `json:"run_mode"`
	// FirstStartedAt is when the subagent first started; null while unknown.
	FirstStartedAt *time.Time `json:"first_started_at"`
	// StartedAt is when the subagent's latest run started; null while
	// unknown.
	StartedAt *time.Time `json:"started_at"`
	// StoppedAt is when the subagent last stopped. It is kept when the
	// subagent runs again. Null if it never has stopped, or the time is
	// unknown.
	StoppedAt *time.Time `json:"stopped_at"`
	// LostAt is when the subagent was judged lost; null unless it was.
	LostAt *time.Time `json:"lost_at"`
	// State is a plain string, one of "running" (started, not stopped and not
	// judged lost), "stopped" (its agent reported it finished) or "lost"
	// (judged gone without a stop report).
	State string `json:"state"`
	// CloseReason is why the subagent is no longer running: "stop_event" (its
	// agent reported it finished), "absent_from_snapshot" (a snapshot of the
	// agent's running background tasks did not list it) or "agent_gone" (its
	// agent ended, went missing or began a new launch). Null exactly while
	// State is "running".
	CloseReason *string `json:"close_reason"`
}

// ListSubagentsResult is the result of list-subagents.
type ListSubagentsResult struct {
	// Tracked is whether the agent's launch registered subagent tracking. It
	// is false for a row launched before subagent tracking existed, which has
	// no records, and for a row whose launch registers no subagent events; any
	// other row is true.
	Tracked bool `json:"tracked"`
	// Subagents is every subagent record of the agent, every launch and life
	// of it, ordered by first_started_at ascending (records with no
	// first_started_at last), then by subagent_id. Never nil: [] when the
	// agent has none.
	Subagents []SubagentRecord `json:"subagents"`
}

// ListSubagentsStore is the narrow store surface ListSubagents needs: the one
// read of an agent's tracking flag and subagent records. *store.Store
// implements it.
type ListSubagentsStore interface {
	// ListSubagentRecords returns the row's tracking flag and every record of
	// it (confirmed entries only), in one consistent read, ordered by
	// first_started_at ascending with unknown last, then subagent_id. A row
	// that does not exist returns an error matching ErrSpawnNotFound.
	ListSubagentRecords(instanceID string) (tracked bool, records []SubagentRow, err error)
}

// Compile-time assertion that *store.Store implements ListSubagentsStore.
var _ ListSubagentsStore = (*store.Store)(nil)

// optMillisToUTC maps an optional stored millisecond time to the nullable
// instant list-subagents reports (millisToUTC, the conversion launch_started_at
// uses).
func optMillisToUTC(millis *int64) *time.Time {
	if millis == nil {
		return nil
	}
	t := millisToUTC(*millis)
	return &t
}

// subagentRecordOf projects one stored record onto its JSON shape.
func subagentRecordOf(r SubagentRow) SubagentRecord {
	return SubagentRecord{
		SubagentID:     r.SubagentID,
		InstanceID:     r.InstanceID,
		SessionID:      r.SessionID,
		SubagentType:   r.SubagentType,
		Description:    r.Description,
		Model:          r.Model,
		ParentType:     r.ParentType,
		ParentID:       r.ParentID,
		ToolUseID:      r.ToolUseID,
		RunMode:        r.RunMode,
		FirstStartedAt: optMillisToUTC(r.FirstStartedAt),
		StartedAt:      optMillisToUTC(r.StartedAt),
		StoppedAt:      optMillisToUTC(r.StoppedAt),
		LostAt:         optMillisToUTC(r.LostAt),
		State:          r.State,
		CloseReason:    r.CloseReason,
	}
}

// emptyListSubagentsResult is the result every failure of ListSubagents and
// (c *Client).ListSubagents returns: tracked false and a fresh non-nil empty
// slice, so the result encodes subagents as [] rather than null.
func emptyListSubagentsResult() ListSubagentsResult {
	return ListSubagentsResult{Subagents: []SubagentRecord{}}
}

// ListSubagents returns whether the agent row of instanceID tracks subagents
// and every subagent record of it (every launch and life of the row), read
// once from s. It changes nothing.
// Missing rows surface store.ErrSpawnNotFound for the CLI to translate.
//
// Every failure returns tracked false and a non-nil empty slice.
func ListSubagents(s ListSubagentsStore, instanceID string) (ListSubagentsResult, error) {
	tracked, rows, err := s.ListSubagentRecords(instanceID)
	if err != nil {
		return emptyListSubagentsResult(), err
	}
	out := ListSubagentsResult{Tracked: tracked, Subagents: make([]SubagentRecord, 0, len(rows))}
	for _, r := range rows {
		out.Subagents = append(out.Subagents, subagentRecordOf(r))
	}
	return out, nil
}

// ListSubagents returns the subagent records of one agent: whether the agent
// tracks subagents (tracked; false for a row launched before subagent tracking
// existed, which has none, and for a row whose launch registers no subagent
// events) and every subagent of it, every launch and life of
// the row, ordered by first_started_at (unknown last), then subagent_id. Each
// record carries subagent_id, instance_id, session_id, subagent_type,
// description, model, parent_type, parent_id, tool_use_id, run_mode,
// first_started_at, started_at, stopped_at, lost_at, state and close_reason;
// a field with no known value is null. The call reads once and changes
// nothing.
//
// CLI: agent-director list-subagents
//
// Errors:
//   - [ErrSpawnNotFound]: no row exists for instanceID.
func (c *Client) ListSubagents(instanceID string) (ListSubagentsResult, error) {
	if err := c.checkClosed(); err != nil {
		return emptyListSubagentsResult(), err
	}
	return ListSubagents(c.st, instanceID)
}
