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

// RelayStore is the narrow surface the relay flow needs: the UPSERT
// to write a fresh open request, the polling-loop read, and the two
// writes needed on timeout so CSCB's poller can observe the abandoned
// relay and expire the Slack message. *store.Store satisfies it.
type RelayStore interface {
	PollStore
	UpsertOpenPermissionRequest(instanceID, toolName, toolInputJSON string) error
	DecidePermissionRequest(instanceID, decision, reason string) (bool, error)
	ApplyHookTransition(instanceID, newState string, softRefresh bool) error
}

// runRelay is the relay-mode branch invoked from Handle when the
// event is PermissionRequest AND AGENT_DIRECTOR_RELAY_MODE=on. It
// owns the full happy + failure flow per SRD §6.2 + §6.4 and ALWAYS
// writes an envelope to stdout before returning — every failure path
// becomes a deny envelope so Claude Code never hangs.
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

	if err := st.UpsertOpenPermissionRequest(instanceID, pp.ToolName, toolInput); err != nil {
		logf(logger, "relay: upsert: %v", err)
		_, _ = fmt.Fprintln(stdout, EncodeDecision("deny", ""))
		return
	}

	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	res := Poll(ctx, st, clock, cfg, instanceID, rng)
	switch res.Decision {
	case "allow":
		logf(logger, "relay: allow for %s (%s)", instanceID, res.Reason)
		_, _ = fmt.Fprintln(stdout, EncodeDecision("allow", res.Reason))
	case "deny":
		logf(logger, "relay: deny for %s (%s)", instanceID, res.Reason)
		_, _ = fmt.Fprintln(stdout, EncodeDecision("deny", res.Reason))
	default:
		// Timeout, ctx cancel, preemption, or read-retry exhaustion —
		// SRD §6.4 fail-closed.
		logf(logger, "relay: fail-closed for %s (%s)", instanceID, res.Why)

		// Write to DB BEFORE writing to stdout so a successful envelope
		// is never observed without the matching state update. Both writes
		// are best-effort — fail-open per SRD §3.2 for state tracking; the
		// stdout envelope still lands regardless (SRD §6.4 fail-closed).
		if _, err := st.DecidePermissionRequest(instanceID, "deny", "timeout"); err != nil {
			logf(logger, "relay: timeout decision write failed (instance=%s): %v", instanceID, err)
		}
		if err := st.ApplyHookTransition(instanceID, store.StateWorking, false); err != nil {
			logf(logger, "relay: timeout state transition failed (instance=%s): %v", instanceID, err)
		}

		_, _ = fmt.Fprintln(stdout, EncodeDecision("deny", ""))
	}
}

// Compile-time assertion that *store.Store satisfies RelayStore. Mirrors
// the HookStore assertion in handler.go so interface drift is caught at
// compile time on either surface.
var _ RelayStore = (*store.Store)(nil)
