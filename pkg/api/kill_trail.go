package api

import (
	"context"

	"github.com/gabemahoney/agent-director/internal/trail"
)

// The ad.provenance.disagree action values kill writes (SR-14): whether the
// call sent a kill.
const (
	killActionKillSent    = "kill_sent"
	killActionNothingSent = "nothing_sent"
)

// emit writes one Kill call's trail events, fail-open (SR-6.4, SR-14): the
// call's ad.provenance.disagree records, one per distinct reason it collected
// (none in the normal case), then its one ad.kill.called. who is the caller
// identity collected at Kill's entry; err is Kill's returned error. No field
// carries a label's value, a session-environment value or another row's id
// (SR-15). A trail-write failure is discarded and never changes Kill's
// result.
func (k *killRun) emit(who caller, err error) {
	action := killActionNothingSent
	if k.killSent {
		action = killActionKillSent
	}
	emitProvenanceDisagree(provenanceDisagree{
		Verb:               "kill",
		Source:             "ad_kill",
		InstanceID:         k.id,
		Socket:             k.socket,
		SessionName:        k.row.TmuxSessionName,
		SessionID:          k.sessionID,
		CurrentSessionName: k.currentName,
		Server:             k.server,
		Verdict:            k.lookup,
		Action:             action,
		Caller:             who,
	}, k.reasons...)

	outcome := "ok"
	if err != nil {
		outcome = errorName(err)
	}
	var agentPID any
	if k.agentPID > 0 {
		agentPID = k.agentPID
	}
	survivors := k.survivorPIDs
	if survivors == nil {
		survivors = []int{}
	}
	_ = trail.Emit(context.Background(), "ad.kill.called", map[string]any{
		"claude_instance_id": k.id,
		"tmux_session_name":  k.row.TmuxSessionName,
		"outcome":            outcome,
		"lookup_outcome":     k.lookup,
		"followup_outcome":   k.followup,
		"kill_sent":          k.killSent,
		"pane_killed":        k.paneKilled,
		"process_check":      k.processCheck,
		"agent_pid":          agentPID,
		"survivor_pids":      survivors,
		"include_finished":   false,
		"caller_process":     who.process,
		"caller_pid":         who.pid,
		"caller_hostname":    who.hostname,
		"caller_user":        who.user,
		"source":             "ad_kill",
	})
}
