package api

import (
	"context"
	"slices"

	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/internal/trail"
)

// disagreeReasons are the six ad.provenance.disagree reasons (SR-14; WD
// 2026-09-30 DRIFT item 5), all declared in internal/tmux; pid_mismatch is
// retired (SR-3.8; WD 2026-09-29c). The emitter writes no other reason.
var disagreeReasons = []string{
	tmux.ReasonServerRestarted,
	tmux.ReasonServerMismatch,
	tmux.ReasonAdopted,
	tmux.ReasonDuplicateLabel,
	tmux.ReasonScopeValue,
	tmux.ReasonNameChanged,
}

// provenanceDisagree is one verb call's ad.provenance.disagree context
// (SR-14): everything each record carries besides its reason. It never holds
// a label's content, a session-environment value or another row's id
// (SR-15).
type provenanceDisagree struct {
	// Verb is the deciding verb ("kill"); Source its trail source ("ad_kill").
	Verb   string
	Source string
	// InstanceID is the row's claude_instance_id.
	InstanceID string
	// Socket is the socket the verb's calls used (tmux_socket).
	Socket string
	// SessionName is the row's recorded tmux_session_name.
	SessionName string
	// SessionID is the tmux id ($N) of the session concerned; "" writes null.
	SessionID string
	// CurrentSessionName is the Ours session's stored name, written only on
	// the name_changed record (null on every other record).
	CurrentSessionName string
	// Server is the lookup Result's Server value; "" (no server check ran)
	// writes unknown (SR-14).
	Server string
	// Verdict is the call's outcome token (tmux.Result.Token).
	Verdict string
	// Action is what the verb did (for example whether a kill was sent).
	Action string
	// Caller is the invoking process's identity, collected inside
	// agent-director at method entry (callerIdentity).
	Caller caller
}

// emitProvenanceDisagree writes a verb call's ad.provenance.disagree records
// (SR-14, SR-3.16, SR-15): nothing when reasons holds none of the six, so the
// normal case writes nothing; otherwise exactly one record per distinct
// reason, each at most once per verb call, however many times the reasons
// were collected (the first lookup, a follow-up lookup, adoption and
// agent-process selection may each report one). A reason that is not one of
// the six is dropped. Records are written in the order of disagreeReasons.
// A sweep (find-missing, expire) calls it once per row, so each reason is
// written at most once per row per sweep.
//
// Each record carries claude_instance_id, verb, reason, tmux_socket,
// tmux_session_name (the recorded name), tmux_session_id (null when none),
// current_session_name (on name_changed only, null otherwise), server
// (match, restarted, differs or unknown), verdict, action, source and the
// caller_* fields, and never a label's content, a session-environment value
// or another row's id (SR-15).
//
// Fail-open: a trail-write failure is discarded and never changes the verb's
// result. Its users are kill (this release's first), plain spawn's re-lookup
// after "duplicate session" (spawnHeldName: a scope value, SR-3.16),
// send-keys, pause, resume, reuse's spawn, find-missing and expire (Epics 11
// and 14 to 17); the plain-spawn label scan never writes it (SR-9.3, SR-14).
func emitProvenanceDisagree(d provenanceDisagree, reasons ...string) {
	server := d.Server
	if server == "" {
		server = tmux.ServerUnknown
	}
	for _, reason := range disagreeReasons {
		if !slices.Contains(reasons, reason) {
			continue
		}
		var current any
		if reason == tmux.ReasonNameChanged {
			current = d.CurrentSessionName
		}
		_ = trail.Emit(context.Background(), "ad.provenance.disagree", map[string]any{
			"claude_instance_id":   d.InstanceID,
			"verb":                 d.Verb,
			"reason":               reason,
			"tmux_socket":          d.Socket,
			"tmux_session_name":    d.SessionName,
			"tmux_session_id":      nullableString(d.SessionID),
			"current_session_name": current,
			"server":               server,
			"verdict":              d.Verdict,
			"action":               d.Action,
			"caller_process":       d.Caller.process,
			"caller_pid":           d.Caller.pid,
			"caller_hostname":      d.Caller.hostname,
			"caller_user":          d.Caller.user,
			"source":               d.Source,
		})
	}
}

// nameChanged reports the name_changed reason's condition (SR-3.4, SR-14):
// the lookup found Ours and the Ours session's stored name is not a stored
// form of the row's recorded name (tmux.StoredForms), so the agent's session
// was renamed. Every other verdict reports false.
func nameChanged(res tmux.Result, recordedName string) bool {
	return res.Verdict == tmux.Ours && !slices.Contains(tmux.StoredForms(recordedName), res.Session.Name)
}
