package api

import (
	"fmt"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
)

// Compatibility delivery preserves the existing pending-Codex hook check. It
// makes no exact native-proof claim, and never retries either text or Enter.
func (r *sendKeysRun) confirmLegacySubmission(sent Spawn) error {
	const timeout = 2 * time.Second
	state := sent.State
	deadline := r.j.now().Add(timeout)
	for attempt := 0; attempt <= int(timeout/submissionPollInterval); attempt++ {
		remaining := deadline.Sub(r.j.now())
		if remaining <= 0 {
			break
		}
		row, err := r.s.GetSpawnWithin(sent.InstanceID, remaining)
		if err != nil {
			return legacySubmissionUnconfirmed(sent.InstanceID, state, fmt.Sprintf("could not read submission evidence: %v", err))
		}
		state = row.State
		if row.LifeNumber != sent.LifeNumber || row.Identity.Token != sent.Identity.Token {
			return legacySubmissionUnconfirmed(sent.InstanceID, state, "the launch changed after the keys were sent")
		}
		switch state {
		case store.StateWorking, store.StateAskUser, store.StateCheckPermission:
			if r.j.now().Before(deadline) {
				return nil
			}
		case store.StateEnded, store.StateMissing:
			return legacySubmissionUnconfirmed(sent.InstanceID, state, "the row finished before submission was observed")
		}
		remaining = deadline.Sub(r.j.now())
		if remaining <= 0 {
			break
		}
		r.sleepInput(min(submissionPollInterval, remaining))
	}
	return legacySubmissionUnconfirmed(sent.InstanceID, state, "no turn activity was observed within 2 seconds")
}

func legacySubmissionUnconfirmed(id, state, reason string) error {
	return &DetailedError{
		Err: fmt.Errorf("%w: send-keys: %s: tmux accepted the keys and Enter, but Codex submission could not be confirmed (%s); do not repeat the text or send another Enter automatically; check hook-derived get or status; if submission remains unknown, escalate to the caller or a human",
			ErrSubmissionUnconfirmed, id, reason),
		Details: SubmissionUnconfirmedDetails{KeysSent: true, SubmissionConfirmed: false, RowState: state},
	}
}
