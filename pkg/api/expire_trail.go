package api

import (
	"context"

	"github.com/gabemahoney/agent-director/internal/trail"
)

// expire's trail event, its one source value and the ad.provenance.disagree
// action values it writes besides a kept row's reason (SR-12.5, SR-14).
const (
	expireKeptEvent = "ad.expire.kept"
	// expireSource is the source of every record expire writes: its
	// ad.expire.kept and its ad.provenance.disagree records.
	expireSource = "ad_expire"
	// expireActionDeleted is the action of a row the conditional delete
	// removed.
	expireActionDeleted = "deleted"
	// expireActionChanged is the action of a row another caller removed
	// before the conditional delete: in neither result list.
	expireActionChanged = findMissingActionChanged
)

// action is the ad.provenance.disagree action of a judged row (SR-14):
// deleted, the kept reason of a kept row, or left_changed for a row another
// caller removed first.
func (r expireRow) action() string {
	switch {
	case r.deleted:
		return expireActionDeleted
	case r.reason != "":
		return r.reason
	}
	return expireActionChanged
}

// emitRow writes a judged row's trail records once its outcome is final, after
// its delete attempt (SR-12.5, SR-14, SR-3.16):
//
//   - a kept row, changed_since_examined and store_error included, gets
//     exactly one ad.expire.kept with claude_instance_id, reason, the
//     recorded tmux_session_name and source ad_expire, and no other field; a
//     deleted row and a row another caller removed get none;
//   - one ad.provenance.disagree per distinct reason the row's lookup
//     collected (expireRow.disagree), through the shared emitter, with verb
//     expire, source ad_expire, the socket the lookup used, the recorded
//     name, the Ours session's tmux id and stored name, the lookup's server
//     and outcome token, the row's action and the run's caller identity;
//     nothing for a row with no reason, so a normal row, a process_alive row
//     and a Skipped one write none. expire never adopts, so never adopted.
//
// No record carries a label's value, a session-environment value or another
// row's id (SR-15). Fail-open: a trail-write failure is discarded and never
// changes the run's result.
func (r *expireRun) emitRow(cand ExpireCandidate, row expireRow) {
	if row.reason != "" {
		_ = trail.Emit(context.Background(), expireKeptEvent, map[string]any{
			"claude_instance_id": cand.ClaudeInstanceID,
			"reason":             row.reason,
			"tmux_session_name":  cand.TmuxSessionName,
			"source":             expireSource,
		})
	}
	if len(row.disagree) == 0 {
		return
	}
	emitProvenanceDisagree(provenanceDisagree{
		Verb:               "expire",
		Source:             expireSource,
		InstanceID:         cand.ClaudeInstanceID,
		Socket:             row.socket,
		SessionName:        cand.TmuxSessionName,
		SessionID:          row.res.Session.ID,
		CurrentSessionName: row.res.Session.Name,
		Server:             row.res.Server,
		Verdict:            row.verdict(),
		Action:             row.action(),
		Caller:             r.caller.get(),
	}, row.disagree...)
}
