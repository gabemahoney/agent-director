package hook

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/google/uuid"
)

// AGENT_DIRECTOR_RELAY_MODE env-var values per SRD §6.5. The hook
// reads this from the process env (NOT the DB) so a DB-unreachable
// failure still surfaces the right boundary.
const (
	EnvRelayMode = "AGENT_DIRECTOR_RELAY_MODE"
	RelayModeOn  = "on"
	RelayModeOff = "off"
)

// permissionPayload is the leniently-typed shape the hook payload
// takes for PermissionRequest events. tool_name + tool_input are
// preserved verbatim into the row so a future MCP audit can see what
// was being asked.
type permissionPayload struct {
	ToolName  string          `json:"tool_name"`
	ToolInput json.RawMessage `json:"tool_input"`
}

// mintRequestToken generates a cryptographically-random UUIDv4 string in
// standard 8-4-4-4-12 hex form (RFC 4122 §4.4) using crypto/rand. The token
// is minted once per runRelay invocation and closed over for the full relay
// lifecycle: Upsert → Poll → timeout-path Decide. Distinct concurrent
// invocations of runRelay each receive their own unique token, ensuring that
// per-request isolation is enforced at the DB row level.
//
// On failure (crypto/rand unavailable) the caller must write a fail-closed
// deny envelope and return without touching the store.
func mintRequestToken() (string, error) {
	id, err := uuid.NewRandom()
	if err != nil {
		return "", err
	}
	return id.String(), nil
}

// RelayStore is the narrow surface the relay flow needs: the INSERT to write a
// fresh open request, the polling-loop read, and the two writes needed on
// timeout so CSCB's poller can observe the abandoned relay and expire the
// Slack message. *store.Store satisfies it.
type RelayStore interface {
	PollStore
	UpsertOpenPermissionRequest(instanceID, requestToken, toolName, toolInputJSON string, cap int) error
	DecidePermissionRequest(instanceID, requestToken, decision, reason string) (bool, error)
	ApplyHookTransition(instanceID, newState string, softRefresh bool) error
}

// runRelay is the relay-mode branch invoked from Handle when the
// event is PermissionRequest AND AGENT_DIRECTOR_RELAY_MODE=on. It
// owns the full happy + failure flow per SRD §6.2 + §6.4 and ALWAYS
// writes an envelope to stdout before returning — every failure path
// becomes a deny envelope so Claude Code never hangs.
//
// A UUIDv4 request_token is minted via crypto/rand immediately after the
// payload is parsed. The token is closed over for the full runRelay body:
// it flows into UpsertOpenPermissionRequest (which keys the DB row), into
// Poll (which reads only that specific row), and into the timeout-path
// DecidePermissionRequest call (which writes to only that row). Concurrent
// runRelay invocations each mint their own distinct token so per-request
// isolation is enforced at the DB level.
func runRelay(
	ctx context.Context,
	stdout io.Writer,
	st RelayStore,
	cfg config.Relay,
	clock PollClock,
	logger *log.Logger,
	instanceID string,
	raw json.RawMessage,
) {
	var pp permissionPayload
	if err := json.Unmarshal(raw, &pp); err != nil {
		// We still continue (fail-closed-deny is emitted at the bottom of
		// the runRelay path if UpsertOpenPermissionRequest can't proceed),
		// but log so a post-mortem can see why an audit row is blank.
		logf(logger, "relay: unmarshal payload (instance=%s): %v", instanceID, err)
	}
	toolInput := string(pp.ToolInput)
	if toolInput == "" {
		toolInput = "null"
	}

	// Mint a per-request UUIDv4 token via crypto/rand. Fail closed on any
	// error — a missing token means we can't key the DB row correctly, so
	// we deny immediately without touching the store.
	requestToken, err := mintRequestToken()
	if err != nil {
		logf(logger, "relay: mint token (instance=%s): %v", instanceID, err)
		_, _ = fmt.Fprintln(stdout, EncodeDecision(EventNamePermissionRequest, "deny", ""))
		return
	}

	cap := cfg.PermissionRequestCap
	if cap < 0 {
		// Mirror the TimeoutSeconds <= 0 guard in internal/hook/polling.go:94-99
		// (introduced for b.p48): a negative cap silently falls back to the
		// default (1000) rather than surfacing a config error at runtime.
		// Cap == 0 is intentional (operator opt-in to unbounded growth) and
		// must NOT be collapsed to 1000 here.
		cap = 1000
	}

	if err := st.UpsertOpenPermissionRequest(instanceID, requestToken, pp.ToolName, toolInput, cap); err != nil {
		logf(logger, "relay: upsert (instance=%s, token=%s): %v", instanceID, requestToken, err)
		_, _ = fmt.Fprintln(stdout, EncodeDecision(EventNamePermissionRequest, "deny", ""))
		return
	}

	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	res := Poll(ctx, st, clock, cfg, instanceID, requestToken, rng)
	switch res.Decision {
	case "allow":
		logf(logger, "relay: allow for %s token=%s (%s)", instanceID, requestToken, res.Reason)
		_, _ = fmt.Fprintln(stdout, EncodeDecision(EventNamePermissionRequest, "allow", res.Reason))
	case "deny":
		logf(logger, "relay: deny for %s token=%s (%s)", instanceID, requestToken, res.Reason)
		_, _ = fmt.Fprintln(stdout, EncodeDecision(EventNamePermissionRequest, "deny", res.Reason))
	default:
		// Timeout, ctx cancel, preemption, or read-retry exhaustion —
		// SRD §6.4 fail-closed.
		logf(logger, "relay: fail-closed for %s token=%s (%s)", instanceID, requestToken, res.Why)

		// Write to DB BEFORE writing to stdout so a successful envelope
		// is never observed without the matching state update. Both writes
		// are best-effort — fail-open per SRD §3.2 for state tracking; the
		// stdout envelope still lands regardless (SRD §6.4 fail-closed).
		if _, err := st.DecidePermissionRequest(instanceID, requestToken, "deny", store.DecisionReasonTimeout); err != nil {
			logf(logger, "relay: timeout decision write failed (instance=%s, token=%s): %v", instanceID, requestToken, err)
		}
		if err := st.ApplyHookTransition(instanceID, store.StateWorking, false); err != nil {
			logf(logger, "relay: timeout state transition failed (instance=%s): %v", instanceID, err)
		}

		_, _ = fmt.Fprintln(stdout, EncodeDecision(EventNamePermissionRequest, "deny", ""))
	}
}

// Compile-time assertion that *store.Store satisfies RelayStore. Mirrors
// the HookStore assertion in handler.go so interface drift is caught at
// compile time on either surface.
var _ RelayStore = (*store.Store)(nil)
