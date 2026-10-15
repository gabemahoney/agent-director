package api

import (
	"fmt"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
)

const (
	submissionConfirmTimeout = 2 * time.Second
	submissionPollInterval   = 100 * time.Millisecond
	submissionPollAttempts   = int(submissionConfirmTimeout/submissionPollInterval) + 1
)

// confirmSubmission checks only hook-driven raw state on the launch that
// received the keys. SessionStart's waiting is not submission evidence. A
// quick completed turn, a blocked hook or a launch change can leave no proof;
// those outcomes are unconfirmed, never a reason to send more keys.
func (r *sendKeysRun) confirmSubmission(sent Spawn) error {
	sleep := r.env.Sleep
	if sleep == nil {
		sleep = time.Sleep
	}
	state := sent.State
	deadline := r.j.now().Add(submissionConfirmTimeout)
	for attempt := 0; attempt < submissionPollAttempts; attempt++ {
		remaining := deadline.Sub(r.j.now())
		if remaining <= 0 {
			break
		}
		row, err := r.s.GetSpawnWithin(sent.InstanceID, remaining)
		if err != nil {
			return submissionUnconfirmed(sent.InstanceID, state, fmt.Sprintf("could not read submission evidence: %v", err))
		}
		state = row.State
		if row.LifeNumber != sent.LifeNumber || row.Identity.Token != sent.Identity.Token {
			return submissionUnconfirmed(sent.InstanceID, state, "the launch changed after the keys were sent")
		}
		switch state {
		case store.StateWorking, store.StateAskUser, store.StateCheckPermission:
			if r.j.now().Before(deadline) {
				return nil
			}
		case store.StateEnded, store.StateMissing:
			return submissionUnconfirmed(sent.InstanceID, state, "the row finished before submission was observed")
		}
		remaining = deadline.Sub(r.j.now())
		if remaining <= 0 {
			break
		}
		if attempt+1 < submissionPollAttempts {
			sleep(min(submissionPollInterval, remaining))
		}
	}
	return submissionUnconfirmed(sent.InstanceID, state, "no turn activity was observed within 2 seconds")
}

func submissionUnconfirmed(id, state, reason string) error {
	return &DetailedError{
		Err: fmt.Errorf("%w: send-keys: %s: tmux accepted the keys and Enter, but Codex submission could not be confirmed (%s); do not repeat the text or send another Enter automatically; check hook-derived get or status; if submission remains unknown, escalate to the caller or a human",
			ErrSubmissionUnconfirmed, id, reason),
		Details: SubmissionUnconfirmedDetails{KeysSent: true, SubmissionConfirmed: false, RowState: state},
	}
}
