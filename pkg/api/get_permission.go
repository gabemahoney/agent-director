package api

import (
	"time"
)

// GetPermissionStore is the narrow store surface GetPermission needs. Defined
// as an interface so the verb is testable without a real *store.Store and so
// the existing Client wiring can pass its embedded handle (c.st) directly. A
// GetPermissionStore that also records hook_gone_at (*store.Store does) gets
// that write from GetPermission; one that does not reports the stored value
// only.
//
// Uses the api.PermissionRow alias (re-exported from internal/store via
// aliases.go) so the interface signature stays clear of any internal/* path
// — TestPublicSurface enforces this invariant across pkg/api.
type GetPermissionStore interface {
	GetPermissionRequestByToken(requestToken string) (PermissionRow, error)
}

// GetPermissionParams is the typed parameter shape for the get-permission
// verb. RequestToken is the UUIDv4 minted by runRelay; it uniquely identifies
// the row across all Spawns (SR-3.5), so no instance_id is needed.
type GetPermissionParams struct {
	// RequestToken is the UUIDv4 token identifying the permission_requests
	// row to fetch. Required; empty string is rejected at the CLI layer.
	RequestToken string `json:"request_token"`
}

// GetPermissionResult is one permission request with its delivery facts.
// Undecided requests have nil Decision and DecidedAt; a decided request may
// still be open until its relay hook acknowledges it. DecisionReason is nil
// while undecided and for an allow from Decide; pane answers can carry a
// reason on either verdict. A RecordPaneAnswer claiming unknown leaves
// Decision nil. Nil fields marshal to JSON null.
//
// ToolInput is the raw JSON string, passed through byte for byte for callers
// to parse. A recorded decision is not the outcome: read Delivery.
type GetPermissionResult struct {
	// RequestToken is the UUIDv4 token this row is keyed under.
	RequestToken string `json:"request_token"`
	// RequestID is the autoincrement primary key of the permission_requests row.
	RequestID int64 `json:"request_id"`
	// ToolName is the agent's tool that triggered the permission request
	// (e.g. "Bash", "Write").
	ToolName string `json:"tool_name"`
	// ToolInput is the raw JSON string of the tool's input as stored in the DB.
	// It is NOT a nested JSON object — callers unmarshal it themselves. The
	// verb passes it through byte-identical from the DB column.
	ToolInput string `json:"tool_input"`
	// RequestedAt is the RFC3339 timestamp when the permission request row was
	// created. Maps from the created_at DB column (SR-7.4 renames it on the
	// wire).
	RequestedAt time.Time `json:"requested_at"`
	// Decision is "allow" or "deny" once a verdict is recorded; nil while the
	// request is undecided or RecordPaneAnswer claims unknown. A nil pointer
	// marshals to JSON null. A recorded verdict has reached the agent only when Delivery is
	// delivered.
	Decision *string `json:"decision"`
	// DecisionReason is the recorded reason; nil while undecided and for an
	// allow from Decide. Pane answers may record a reason for either verdict.
	// A nil pointer marshals to JSON null.
	DecisionReason *string `json:"decision_reason"`
	// DecidedAt is the RFC3339 timestamp when the verdict was written; nil
	// while the request is undecided. A nil pointer marshals to JSON null.
	DecidedAt *time.Time `json:"decided_at"`
	RequestDelivery
}

// GetPermission reads the permission_requests row identified by request_token
// alone (SR-3.5: token UUIDv4 is globally selective). Behavior:
//
//   - Unknown token → ErrPermissionRequestNotFound (the store sentinel,
//     re-exported via api.ErrPermissionRequestNotFound). errors.Is matches
//     across both names.
//   - Found row → fields copied 1:1; empty-string Decision/DecisionReason
//     and zero-value DecidedAt are normalized to nil pointers so the JSON
//     wire shape renders null for NULL DB columns.
//   - The delivery facts (b.146 rule 15) are judged through v by the
//     check-before-read rule (b.146 rule 5): the row is read, its relay hook
//     process judged, and the row read again. get-permission is a reading
//     verb and never waits for the store's write lock (b.146 problem 4): it
//     writes hook_gone_at on a row it found fallen back only if the lock is
//     free at that moment.
//
// ToolInput passes through byte-identical — no re-encode, no validation, no
// whitespace normalization.
func GetPermission(s GetPermissionStore, v RelayView, params GetPermissionParams) (GetPermissionResult, error) {
	j := newRelayJudge(v)
	row, verdict, err := readOneJudged(func() (PermissionRow, error) {
		return s.GetPermissionRequestByToken(params.RequestToken)
	}, j)
	if err != nil {
		return GetPermissionResult{}, err
	}
	now := j.now()
	out := GetPermissionResult{
		RequestToken:    row.RequestToken,
		RequestID:       row.RequestID,
		ToolName:        row.ToolName,
		ToolInput:       row.ToolInput,
		RequestedAt:     row.CreatedAt,
		Decision:        nullableString(row.Decision),
		DecisionReason:  nullableString(row.DecisionReason),
		DecidedAt:       nullableTime(row.DecidedAt),
		RequestDelivery: j.delivery(row, verdict, now),
	}
	recordGone(s, now, readerLockWait, addGone(nil, row.RequestID, &out.RequestDelivery))
	return out, nil
}

// GetPermission returns the current state of a permission_requests row
// identified by request_token, with its delivery facts: delivery
// (delivered, not_confirmed or fallen_back), confirm_by, hook_alive,
// hook_gone_at, attempted_decision, attempted_at and tool_use_id. The lookup
// is token-only (no instance_id needed) per SR-3.5; the UUIDv4 token
// is globally selective. GetPermission never waits for the store's write
// lock.
//
// CLI: agent-director get-permission
//
// Errors:
//   - [ErrPermissionRequestNotFound]: no row exists for the supplied token.
//
// Nondeterminism: the delivery facts depend on the clock and on whether the
// request's relay hook process runs.
func (c *Client) GetPermission(params GetPermissionParams) (GetPermissionResult, error) {
	if err := c.checkClosed(); err != nil {
		return GetPermissionResult{}, err
	}
	return GetPermission(c.st, c.relayView(), params)
}
