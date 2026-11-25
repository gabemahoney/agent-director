package api

import (
	"fmt"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
)

const (
	// Codex startup hooks lag accepted input: 953 false-unconfirmed sends on
	// 2026-11-18 through 2026-11-25 had p50 10.7s and p95 16s. Allow 20s,
	// leaving nominal transport headroom under the TS client's 30s default.
	codexLegacySubmissionTimeout = 20 * time.Second
	// Claude's observed post-Enter hook latency was only 50-89ms after settle.
	claudeLegacySubmissionTimeout = 2 * time.Second
)

// Compatibility delivery on a pending Codex row, and on a Claude row read as
// pending, checks hooks for the turn starting within the agent's allowance.
// For Claude this follows
// waitTerminalRaw: the terminal takes raw input, SessionStart has made the same
// launch waiting, and claudeSessionStartSettle has passed since this call first
// saw waiting (b.qcf). It makes no exact native-proof claim, and never retries
// either text or Enter.
func (r *sendKeysRun) confirmLegacySubmission(sent Spawn) error {
	timeout := codexLegacySubmissionTimeout
	agent := "Codex"
	if sent.Agent == store.AgentClaude {
		timeout = claudeLegacySubmissionTimeout
		agent = "Claude Code"
	}
	state := sent.State
	if r.terminal != nil {
		state = r.terminalState
	}
	deadline := r.j.now().Add(timeout)
	callerDeadline := false
	if r.env.Context != nil {
		if end, ok := r.env.Context.Deadline(); ok && end.Before(deadline) {
			deadline, callerDeadline = end, true
		}
	}
	cancelled := func() bool { return r.env.Context != nil && r.env.Context.Err() != nil }
	for attempt := 0; attempt <= int(timeout/submissionPollInterval); attempt++ {
		remaining := deadline.Sub(r.j.now())
		if remaining <= 0 || cancelled() {
			break
		}
		row, err := r.s.GetSpawnWithin(sent.InstanceID, remaining)
		if err != nil {
			return legacySubmissionUnconfirmed(sent.InstanceID, agent, state, fmt.Sprintf("could not read submission evidence: %v", err))
		}
		state = row.State
		if row.LifeNumber != sent.LifeNumber || row.Identity.Token != sent.Identity.Token {
			return legacySubmissionUnconfirmed(sent.InstanceID, agent, state, "the launch changed after the keys were sent")
		}
		if cancelled() {
			break
		}
		switch state {
		case store.StateWorking, store.StateAskUser, store.StateCheckPermission:
			if r.j.now().Before(deadline) {
				return nil
			}
		case store.StateEnded, store.StateMissing:
			return legacySubmissionUnconfirmed(sent.InstanceID, agent, state, "the row finished before submission was observed")
		}
		remaining = deadline.Sub(r.j.now())
		if remaining <= 0 {
			break
		}
		if attempt < int(timeout/submissionPollInterval) {
			r.sleepInput(min(submissionPollInterval, remaining))
		}
	}
	reason := fmt.Sprintf("no turn activity was observed within %d seconds", timeout/time.Second)
	if cancelled() || callerDeadline && !r.j.now().Before(deadline) {
		reason = "the call was cancelled or reached its deadline before submission was observed"
	}
	return legacySubmissionUnconfirmed(sent.InstanceID, agent, state, reason)
}

func legacySubmissionUnconfirmed(id, agent, state, reason string) error {
	return &DetailedError{
		Err: fmt.Errorf("%w: send-keys: %s: tmux accepted the keys and Enter, but %s submission could not be confirmed (%s); do not repeat the text or send another Enter automatically; check hook-derived get or status; if submission remains unknown, escalate to the caller or a human",
			ErrSubmissionUnconfirmed, id, agent, reason),
		Details: SubmissionUnconfirmedDetails{KeysSent: true, SubmissionConfirmed: false, RowState: state},
	}
}
