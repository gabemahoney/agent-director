package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/trail"
)

// ErrRelayModeOff is returned by decide() when the target Spawn's
// `relay_mode` column is anything other than "on". Per SRD §6.2 the
// decide verb only makes sense for Spawns the operator has opted
// into relay mode for.
var ErrRelayModeOff = errors.New("ErrRelayModeOff")

// ErrInvalidDecision is returned by decide() when --decision is
// neither "allow" nor "deny". SRD §6.3 pins the two-valued surface.
var ErrInvalidDecision = errors.New("ErrInvalidDecision")

// ErrMissingRequestToken is returned by Decide when RequestToken is empty.
// The token uniquely identifies the permission-request row to decide; without
// it the call is rejected at the API layer regardless of CLI gating.
var ErrMissingRequestToken = errors.New("ErrMissingRequestToken")

// DecideStore is the narrow store surface Decide needs.
type DecideStore interface {
	GetSpawn(instanceID string) (Spawn, error)
	DecidePermissionRequest(instanceID, requestToken, decision, reason string, writerProcess string) (bool, error)
	GetPermissionRequest(instanceID, requestToken string) (PermissionRow, error)
}

// DecideParams is the typed parameter shape for the decide verb.
// Decision is either "allow" or "deny"; Reason is accepted for wire
// compatibility but is currently discarded — the canonical
// DecisionReasonOperator is persisted regardless of its value.
type DecideParams struct {
	// ClaudeInstanceID identifies the Spawn whose open permission request is
	// being decided.
	ClaudeInstanceID string `json:"claude_instance_id"`
	// RequestToken is the UUIDv4 token minted by runRelay for the specific
	// permission request being decided. Required; empty string is rejected at
	// the API layer (ErrMissingRequestToken) as well as at the CLI layer.
	RequestToken string `json:"request_token"`
	// Decision is the orchestrator's verdict: "allow" or "deny".
	Decision string `json:"decision"`
	// Reason is currently discarded on deny; the canonical DecisionReasonOperator
	// is persisted regardless. Reserved for future schema additions.
	Reason string `json:"reason"`
}

// DecideResult is the typed return shape. Empty today; reserved so a
// future field (e.g. echoing the recorded reason) can be added without
// breaking the wire shape.
type DecideResult struct{}

// Decide writes the orchestrator's allow/deny verdict to the open
// permission_requests row identified by the (claude_instance_id, request_token)
// pair (SRD §6.2). request_token is required — it uniquely identifies the
// specific row minted by runRelay for this request. Behavior:
//
//   - Empty request_token → ErrMissingRequestToken (validated at this layer,
//     not just at the CLI).
//   - Unknown id → ErrSpawnNotFound from the store.
//   - Spawn's relay_mode != "on" → ErrRelayModeOff. decide is a
//     no-op outside relay mode; the verb refuses rather than write
//     a row Claude will never look at.
//   - Invalid decision string → ErrInvalidDecision.
//   - Single-statement UPDATE writes (decision, decision_reason, decided_at)
//     guarded by `decision IS NULL AND request_token = ?`.
//     RowsAffected==0 disambiguates "no row" (ErrNoOpenPermissionRequest)
//     from "already decided" (ErrAlreadyDecided) via a follow-up SELECT.
//   - params.Reason is currently discarded; DecisionReasonOperator is always
//     written for deny decisions regardless of its value.
func Decide(s DecideStore, params DecideParams) (DecideResult, error) {
	if params.RequestToken == "" {
		return DecideResult{}, fmt.Errorf("%w: request_token is required", ErrMissingRequestToken)
	}
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

	// Use the canonical operator reason for deny; empty string for allow.
	// decision_reason in the DB is always one of the store.DecisionReason*
	// constants for operator-originated decisions.
	dbReason := ""
	if params.Decision == "deny" {
		dbReason = store.DecisionReasonOperator
	}
	updated, err := s.DecidePermissionRequest(params.ClaudeInstanceID, params.RequestToken, params.Decision, dbReason, store.WriterProcessDecide)
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
	// the row no longer exists we'll report ErrNoOpenPermissionRequest.
	pr, err := s.GetPermissionRequest(params.ClaudeInstanceID, params.RequestToken)
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

// decideOutcome maps a Decide error to its canonical outcome string for
// ad.decide.called. nil → "ok"; known sentinels → their err_name;
// unrecognized errors → "ErrInternal". The function uses errors.Is so
// %w-wrapped errors are matched correctly.
//
// errnames.Classify cannot be used here: pkg/api/errnames imports pkg/api for
// its sentinel variables, so importing errnames from pkg/api would create an
// unresolvable import cycle. This local switch is the canonical pattern for
// decide-path outcome strings.
func decideOutcome(err error) string {
	if err == nil {
		return "ok"
	}
	switch {
	case errors.Is(err, ErrMissingRequestToken):
		return "ErrMissingRequestToken"
	case errors.Is(err, ErrInvalidDecision):
		return "ErrInvalidDecision"
	case errors.Is(err, ErrRelayModeOff):
		return "ErrRelayModeOff"
	case errors.Is(err, store.ErrSpawnNotFound):
		return "ErrSpawnNotFound"
	case errors.Is(err, store.ErrAlreadyDecided):
		return "ErrAlreadyDecided"
	case errors.Is(err, store.ErrNoOpenPermissionRequest):
		return "ErrNoOpenPermissionRequest"
	case errors.Is(err, store.ErrAmbiguousRequest):
		return "ErrAmbiguousRequest"
	default:
		return "ErrInternal"
	}
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
//   - [ErrMissingRequestToken]: RequestToken is empty.
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

	// Collect caller identity once at entry — must come from inside AD, not
	// from caller-asserted params (SR-A-2.4).
	callerProcess := filepath.Base(os.Args[0])
	callerPID := os.Getpid()
	callerHostname, _ := os.Hostname()
	callerUser := ""
	if u, uerr := user.Current(); uerr == nil {
		callerUser = u.Username
	}

	var callErr error
	defer func() {
		// Emit exactly one ad.decide.called per invocation, on every return
		// path including ErrAlreadyDecided no-ops (SR-A-2.4). Fail-open per
		// SR-A-3.2: a trail-emit failure is silently discarded.
		_ = trail.Emit(context.Background(), "ad.decide.called", map[string]any{
			"claude_instance_id":        params.ClaudeInstanceID,
			"request_token":             params.RequestToken,
			"submitted_decision":        params.Decision,
			"submitted_decision_reason": params.Reason,
			"outcome":                   decideOutcome(callErr),
			"caller_process":            callerProcess,
			"caller_pid":                callerPID,
			"caller_hostname":           callerHostname,
			"caller_user":               callerUser,
			"source":                    "ad_decide",
		})
	}()

	var result DecideResult
	result, callErr = Decide(c.st, params)
	return result, callErr
}
