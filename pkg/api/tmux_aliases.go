package api

import "github.com/gabemahoney/agent-director/internal/tmux"

// ── tmux typed API re-exports (SRD Appendix F.3) ─────────────────────────────
//
// These aliases and constants re-export internal/tmux's typed call API so an
// external implementer of TmuxClient can name every type and constant in its
// method signatures without importing internal/tmux. Each is identical to its
// original: api.TmuxFailTimeout == tmux.FailTimeout, and errors.As into
// *api.TmuxCallError matches a *tmux.CallError.

// TmuxLookupAnswer is re-exported from internal/tmux for the same reason as
// the store aliases. It is the result of TmuxClient's Lookup: the sessions
// with their classified labels, the server identity and ScopeValue (SR-3.4).
type TmuxLookupAnswer = tmux.LookupAnswer

// TmuxSession is re-exported from internal/tmux for the same reason. It is
// one session of a TmuxLookupAnswer: id, creation time, stored name and label.
type TmuxSession = tmux.Session

// TmuxPane is re-exported from internal/tmux for the same reason. It is one
// line of TmuxClient's ListPanes: session id, window, pane index, pane id,
// pane pid (SR-3.7) and AdPane, the launch token of the pane's own @ad_pane
// label, "" when it has none or the value is borrowed (WD 2026-09-29c).
type TmuxPane = tmux.Pane

// TmuxLabel is re-exported from internal/tmux for the same reason. It is a
// session's classified @ad_owner label in a TmuxSession, "ad1 <token>
// <session id> <instance id> <store id>" when valid; its Kind is one of
// TmuxLabelNone and TmuxLabelValid, and its StoreID names the writing store
// (SR-3.4; WD 2026-09-29 STORE).
type TmuxLabel = tmux.Label

// TmuxCreateReply is re-exported from internal/tmux for the same reason. It
// is the result of TmuxClient's NewSession: the new session's id, the server
// identity and the first pane (SR-2.1).
type TmuxCreateReply = tmux.CreateReply

// TmuxCall is re-exported from internal/tmux for the same reason. It names
// the call kind a *TmuxCallError reports (the TmuxCall* constants).
type TmuxCall = tmux.Call

// TmuxFailure is re-exported from internal/tmux for the same reason. It
// classifies a *TmuxCallError (the TmuxFail* constants; SR-2.5).
type TmuxFailure = tmux.Failure

// TmuxCallError is re-exported from internal/tmux for the same reason. It is
// the error every socket-taking TmuxClient method reports a failure as
// (Appendix F.1).
type TmuxCallError = tmux.CallError

// The call kinds a *TmuxCallError names (SR-2.1), re-declared from
// internal/tmux with the prefix Tmux; each is identical to its original.
const (
	// TmuxCallLookup is tmux.CallLookup: the one-invocation lookup.
	TmuxCallLookup = tmux.CallLookup
	// TmuxCallListPanes is tmux.CallListPanes: the pane listing.
	TmuxCallListPanes = tmux.CallListPanes
	// TmuxCallKillPane is tmux.CallKillPane: the pane kill by pane id.
	TmuxCallKillPane = tmux.CallKillPane
	// TmuxCallKillSession is tmux.CallKillSession: the session kill by id.
	TmuxCallKillSession = tmux.CallKillSession
	// TmuxCallSendText is tmux.CallSendText: the literal text send.
	TmuxCallSendText = tmux.CallSendText
	// TmuxCallSendEnter is tmux.CallSendEnter: the Enter send.
	TmuxCallSendEnter = tmux.CallSendEnter
	// TmuxCallCapture is tmux.CallCapture: the capture by pane id.
	TmuxCallCapture = tmux.CallCapture
	// TmuxCallCreate is tmux.CallCreate: the create with its chained labels.
	TmuxCallCreate = tmux.CallCreate
	// TmuxCallSetLabel is tmux.CallSetLabel: sets the session label by
	// session id and the pane label by pane id, in one invocation.
	TmuxCallSetLabel = tmux.CallSetLabel
)

// The failure kinds a *TmuxCallError carries (SR-2.3, SR-2.5), re-declared
// from internal/tmux with the prefix Tmux; each is identical to its original.
const (
	// TmuxFailTimeout is tmux.FailTimeout: no answer within the call's timeout.
	TmuxFailTimeout = tmux.FailTimeout
	// TmuxFailUnavailable is tmux.FailUnavailable: the tmux binary could not
	// be run.
	TmuxFailUnavailable = tmux.FailUnavailable
	// TmuxFailSocketDenied is tmux.FailSocketDenied: permission denied on the
	// socket.
	TmuxFailSocketDenied = tmux.FailSocketDenied
	// TmuxFailNoServer is tmux.FailNoServer: no server running on the socket.
	TmuxFailNoServer = tmux.FailNoServer
	// TmuxFailNoSocket is tmux.FailNoSocket: no socket at the path.
	TmuxFailNoSocket = tmux.FailNoSocket
	// TmuxFailDuplicate is tmux.FailDuplicate: a duplicate session name, on
	// the create only.
	TmuxFailDuplicate = tmux.FailDuplicate
	// TmuxFailLabel is tmux.FailLabel: the create printed a reply but its
	// chained label step failed.
	TmuxFailLabel = tmux.FailLabel
	// TmuxFailUnrecognized is tmux.FailUnrecognized: any other reply or
	// unparseable output. An error of any other type from an injected
	// TmuxClient counts as this kind.
	TmuxFailUnrecognized = tmux.FailUnrecognized
)

// The label classes of a TmuxLabel's Kind (SR-3.4), re-declared from
// internal/tmux with the prefix Tmux; each is identical to its original.
const (
	// TmuxLabelNone is tmux.LabelNone: no label, or one that is malformed or
	// borrowed.
	TmuxLabelNone = tmux.LabelNone
	// TmuxLabelValid is tmux.LabelValid: a well-formed label embedding its
	// own session's id.
	TmuxLabelValid = tmux.LabelValid
)
