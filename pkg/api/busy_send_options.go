package api

import (
	"fmt"
	"strings"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/store"
)

// checkSendMode refuses a supplied send_mode other than wait_turn or
// steer_turn (SR-1.4) with ErrInvalidFlags; nil is not supplied. An empty
// supplied value is refused, never read as omitted.
func checkSendMode(verb string, mode *string) error {
	if mode == nil || config.ValidSendMode(*mode) {
		return nil
	}
	return fmt.Errorf("%w: %s: send_mode %q must be wait_turn or steer_turn; nothing was sent or written", ErrInvalidFlags, verb, *mode)
}

// canonicalUUID returns s in SR-1.3's canonical form, the hyphenated
// 8-4-4-4-12 hexadecimal UUID in lowercase, accepting either hex case; ok is
// false for any other form (braces, a urn prefix, no hyphens). Caller UUIDs
// and AD-made force ids take this form.
func canonicalUUID(s string) (string, bool) {
	if len(s) != 36 {
		return "", false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case i == 8 || i == 13 || i == 18 || i == 23:
			if c != '-' {
				return "", false
			}
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f', c >= 'A' && c <= 'F':
		default:
			return "", false
		}
	}
	return strings.ToLower(s), true
}

// checkCallerUUID refuses a supplied caller_uuid not in SR-1.3's form with
// ErrInvalidFlags and returns it canonical; nil is not supplied.
func checkCallerUUID(verb string, id *string) (*string, error) {
	if id == nil {
		return nil, nil
	}
	canonical, ok := canonicalUUID(*id)
	if !ok {
		return nil, fmt.Errorf("%w: %s: caller_uuid %q must be a hyphenated hexadecimal UUID (8-4-4-4-12); nothing was sent or written", ErrInvalidFlags, verb, *id)
	}
	return &canonical, nil
}

// busySendGate decides a call that names a busy-send option from coverage,
// the coverage of the Launch the option would apply to (SR-9.1, SR-9.2): the
// current Launch's recorded coverage for send-keys, pause and force-delivery,
// and the coverage the new Launch would record for spawn, reuse, resume and
// make-template. It never returns nil: for an agent busy send does not cover
// it is ErrCantSend with reason agent_not_covered, and for a Covered Agent an
// unnamed error, since this version builds no busy-send delivery; either way
// nothing has been stored, sent, typed or launched, and the call never falls
// back to today's behaviour. target names what was checked; options names
// what the call set.
func busySendGate(target, options string, coverage store.BusySendCoverage) error {
	switch coverage {
	case store.BusySendNotCovered:
		return agentNotCoveredError(target, options)
	case store.BusySendCovered:
		return fmt.Errorf("%s: %s: busy send is not available in this version for a Covered Agent; nothing was stored, sent, typed or launched", target, options)
	}
	return fmt.Errorf("%s: invalid busy-send coverage %q; nothing was stored, sent, typed or launched", target, coverage)
}

// agentNotCoveredError is SR-9.2's refusal: ErrCantSend with reason
// agent_not_covered, resend_safe true and retry_with new_send.
func agentNotCoveredError(target, options string) error {
	return &DetailedError{
		Err: fmt.Errorf("%w: %s: busy send does not cover this agent, so %s cannot apply; nothing was stored, sent, typed or launched; make the same call without them",
			ErrCantSend, target, options),
		Details: CantSendDetails{Class: cantSendClass, Reason: reasonAgentNotCovered, ResendSafe: true, RetryWith: retryWithNewSend},
	}
}

// agentDefaultsNamed lists the Agent Defaults and confirm_from_transcripts a
// launch or template sets, in parameter order, for busySendGate's refusal;
// "" when it sets none.
func agentDefaultsNamed(sendMode *string, confirmed, fromTranscripts *bool) string {
	var named []string
	if sendMode != nil {
		named = append(named, "send_mode")
	}
	if confirmed != nil {
		named = append(named, "confirmed_delivery")
	}
	if fromTranscripts != nil {
		named = append(named, "confirm_from_transcripts")
	}
	return strings.Join(named, ", ")
}
