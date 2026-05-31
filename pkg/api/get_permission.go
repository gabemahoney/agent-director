package api

import (
	"time"
)

// GetPermissionStore is the narrow store surface GetPermission needs. Defined
// as an interface so the verb is testable without a real *store.Store and so
// the existing Client wiring can pass its embedded handle (c.st) directly.
//
// Uses the api.PermissionRow alias (re-exported from internal/store via
// aliases.go) so the interface signature stays clear of any internal/* path
// — TestPublicSurface enforces this invariant across pkg/api.
type GetPermissionStore interface {
	GetPermissionRequestByToken(requestToken string) (PermissionRow, error)
}

// GetPermissionParams is the typed parameter shape for the get-permission
// verb. RequestToken is the UUIDv4 minted by runRelay; it uniquely identifies
// the row across all Spawns (SR-3.5), so no claude_instance_id is needed.
type GetPermissionParams struct {
	// RequestToken is the UUIDv4 token identifying the permission_requests
	// row to fetch. Required; empty string is rejected at the CLI layer.
	RequestToken string `json:"request_token"`
}

// GetPermissionResult is the SR-7.4 wire projection of a permission_requests
// row. Nullable columns (decision, decision_reason, decided_at) surface as
// pointer fields — a nil pointer marshals to JSON null. Open (not-yet-decided)
// rows carry nil for all three. Allow rows carry a non-nil Decision pointing
// to "allow" and a non-nil DecidedAt, with DecisionReason still nil (SR-1.3:
// allow does not carry a reason). Deny rows carry all three non-nil.
//
// ToolInput is the raw JSON string from the DB column — callers parse it
// themselves. The verb MUST NOT re-encode, validate, or normalize whitespace
// on it; the byte-identical pass-through is contract per SR-7.4.
type GetPermissionResult struct {
	// RequestToken is the UUIDv4 token this row is keyed under.
	RequestToken string `json:"request_token"`
	// RequestID is the autoincrement primary key of the permission_requests row.
	RequestID int64 `json:"request_id"`
	// ToolName is the Claude Code tool that triggered the permission request
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
	// Decision is "allow" or "deny" once decided; nil while the row is open
	// (decision IS NULL in the DB). A nil pointer marshals to JSON null.
	Decision *string `json:"decision"`
	// DecisionReason is the canonical store.DecisionReason* string when the
	// row is a deny; nil for open rows AND for allow rows (SR-1.3 closed-allow
	// rows carry no reason annotation). A nil pointer marshals to JSON null.
	DecisionReason *string `json:"decision_reason"`
	// DecidedAt is the RFC3339 timestamp when the verdict was written; nil
	// while the row is open (decided_at IS NULL in the DB). A nil pointer
	// marshals to JSON null.
	DecidedAt *time.Time `json:"decided_at"`
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
//
// ToolInput passes through byte-identical — no re-encode, no validation, no
// whitespace normalization.
func GetPermission(s GetPermissionStore, params GetPermissionParams) (GetPermissionResult, error) {
	row, err := s.GetPermissionRequestByToken(params.RequestToken)
	if err != nil {
		return GetPermissionResult{}, err
	}

	out := GetPermissionResult{
		RequestToken: row.RequestToken,
		RequestID:    row.RequestID,
		ToolName:     row.ToolName,
		ToolInput:    row.ToolInput,
		RequestedAt:  row.CreatedAt,
	}
	if row.Decision != "" {
		d := row.Decision
		out.Decision = &d
	}
	if row.DecisionReason != "" {
		r := row.DecisionReason
		out.DecisionReason = &r
	}
	if !row.DecidedAt.IsZero() {
		t := row.DecidedAt
		out.DecidedAt = &t
	}
	return out, nil
}

// GetPermission returns the current state of a permission_requests row
// identified by request_token. The lookup is token-only (no
// claude_instance_id needed) per SR-3.5; the UUIDv4 token is globally
// selective.
//
// CLI: agent-director get-permission
//
// Errors:
//   - [ErrPermissionRequestNotFound]: no row exists for the supplied token.
//
// Nondeterminism: none.
func (c *Client) GetPermission(params GetPermissionParams) (GetPermissionResult, error) {
	if err := c.checkClosed(); err != nil {
		return GetPermissionResult{}, err
	}
	return GetPermission(c.st, params)
}
