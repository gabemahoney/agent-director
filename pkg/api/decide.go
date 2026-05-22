package api

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/gabemahoney/agent-director/internal/store"
)

// ErrRelayModeOff is returned by decide() when the target Spawn's
// `relay_mode` column is anything other than "on". Per SRD §6.2 the
// decide verb only makes sense for Spawns the operator has opted
// into relay mode for.
var ErrRelayModeOff = errors.New("ErrRelayModeOff")

// ErrInvalidDecision is returned by decide() when --decision is
// neither "allow" nor "deny". SRD §6.3 pins the two-valued surface.
var ErrInvalidDecision = errors.New("ErrInvalidDecision")

// DecideStore is the narrow store surface Decide needs.
type DecideStore interface {
	GetSpawn(instanceID string) (Spawn, error)
	DecidePermissionRequest(instanceID, decision, reason string) (bool, error)
	GetPermissionRequest(instanceID string) (PermissionRow, error)
}

// DecideParams is the typed parameter shape for the decide verb.
// Decision is either "allow" or "deny"; Reason is the optional
// free-text message the orchestrator wants surfaced to Claude. On a
// deny with an empty Reason the envelope defaults to
// "Denied by orchestrator" — see hook.EncodeDecision.
type DecideParams struct {
	// ClaudeInstanceID identifies the Spawn whose open permission request is
	// being decided.
	ClaudeInstanceID string `json:"claude_instance_id"`
	// Decision is the orchestrator's verdict: "allow" or "deny".
	Decision string `json:"decision"`
	// Reason is the optional free-text message surfaced to Claude on deny.
	// When empty on a deny, the hook envelope defaults to "Denied by orchestrator".
	Reason string `json:"reason"`
}

// DecideResult is the typed return shape. Empty today; reserved so a
// future field (e.g. echoing the recorded reason) can be added without
// breaking the wire shape.
type DecideResult struct{}

// Decide writes the orchestrator's allow/deny verdict to the open
// permission_requests row (SRD §6.2). Behavior:
//
//   - Unknown id → ErrSpawnNotFound from the store.
//   - Spawn's relay_mode != "on" → ErrRelayModeOff. decide is a
//     no-op outside relay mode; the verb refuses rather than write
//     a row Claude will never look at.
//   - Invalid decision string → ErrInvalidDecision.
//   - Single-statement UPDATE writes (decision, reason, updated_at)
//     guarded by `decision IS NULL`. RowsAffected==0 disambiguates
//     "no row" (ErrNoOpenPermissionRequest) from "already decided"
//     (ErrAlreadyDecided) via a follow-up SELECT.
func Decide(s DecideStore, params DecideParams) (DecideResult, error) {
	if params.Decision != "allow" && params.Decision != "deny" {
		return DecideResult{}, fmt.Errorf("%w: %q", ErrInvalidDecision, params.Decision)
	}

	row, err := s.GetSpawn(params.ClaudeInstanceID)
	if err != nil {
		return DecideResult{}, err
	}
	if row.RelayMode != "on" {
		return DecideResult{}, fmt.Errorf("%w: spawn %s relay_mode=%q",
			ErrRelayModeOff, params.ClaudeInstanceID, row.RelayMode)
	}

	updated, err := s.DecidePermissionRequest(params.ClaudeInstanceID, params.Decision, params.Reason)
	if err != nil {
		return DecideResult{}, err
	}
	if updated {
		return DecideResult{}, nil
	}

	// RowsAffected==0 — either the row is absent or the decision was
	// already written. Disambiguate via a follow-up SELECT. The race
	// window here is benign: if a concurrent caller has written a
	// decision since our UPDATE, we'll report ErrAlreadyDecided; if
	// the row was preempted by a fresh DELETE-INSERT we'll see
	// "no row" again and report ErrNoOpenPermissionRequest.
	pr, err := s.GetPermissionRequest(params.ClaudeInstanceID)
	if errors.Is(err, sql.ErrNoRows) {
		return DecideResult{}, fmt.Errorf("%w: %s", store.ErrNoOpenPermissionRequest, params.ClaudeInstanceID)
	}
	if err != nil {
		return DecideResult{}, err
	}
	if pr.Decision != "" {
		return DecideResult{}, fmt.Errorf("%w: %s already decided as %q",
			store.ErrAlreadyDecided, params.ClaudeInstanceID, pr.Decision)
	}
	// Unreachable in practice — the row exists, decision is NULL, yet
	// UPDATE didn't affect it. The only way to land here is a SQL
	// driver oddity; surface as the more conservative
	// ErrNoOpenPermissionRequest.
	return DecideResult{}, fmt.Errorf("%w: %s (UPDATE no-op against open row)",
		store.ErrNoOpenPermissionRequest, params.ClaudeInstanceID)
}

// Decide writes the orchestrator's allow or deny verdict on the open
// PermissionRequest for the identified Spawn. Only callable on Spawns with
// relay_mode=on. The underlying UPDATE is race-free (first-call-wins guarded
// by `decision IS NULL`); a concurrent call by a second orchestrator returns
// [ErrAlreadyDecided].
//
// CLI: agent-director decide
//
// Errors:
//   - [ErrSpawnNotFound]: no row exists for the instance id.
//   - [ErrRelayModeOff]: the Spawn's relay_mode is not "on".
//   - [ErrNoOpenPermissionRequest]: no undecided permission request exists.
//   - [ErrAlreadyDecided]: a concurrent caller already wrote a verdict.
//   - [ErrInvalidDecision]: Decision is not "allow" or "deny".
//
// Nondeterminism: none.
func (c *Client) Decide(params DecideParams) (DecideResult, error) {
	if err := c.checkClosed(); err != nil {
		return DecideResult{}, err
	}
	return Decide(c.st, params)
}
