package tmux

import (
	"strconv"
	"time"
)

// Call names one kind of socket-taking tmux call (SRD SR-2.1, Appendix F.1).
// Its string is the wording used in descriptions.
type Call string

// The call kinds of SR-2.1. Their strings are the wordings of Appendix F.1.
const (
	// CallLookup is the one-invocation lookup: the listing with labels plus
	// the three scope reads (SR-2.1, SR-3.4).
	CallLookup Call = "lookup"
	// CallListPanes is the pane listing, list-panes -a (SR-3.7).
	CallListPanes Call = "pane listing"
	// CallKillPane is the pane kill by pane id (SR-6.1).
	CallKillPane Call = "pane kill"
	// CallKillSession is the session kill by session id (SR-6.1, SR-3.5).
	CallKillSession Call = "session kill"
	// CallSendText is the literal text send by pane id (SR-2.1).
	CallSendText Call = "text send"
	// CallSendEnter is the Enter send by pane id (SR-2.1).
	CallSendEnter Call = "Enter send"
	// CallSendKey is a key send by pane id: one key, named as tmux names
	// keys and never typed literally, such as pause's C-u before /exit
	// (b.9o4).
	CallSendKey Call = "key send"
	// CallCapture is the capture by pane id (SR-2.1).
	CallCapture Call = "capture"
	// CallCreate is the create invocation with its chained label (SR-3.5).
	CallCreate Call = "session creation"
	// CallSetLabel is the label by id, the session label by session id and
	// the pane label by pane id in one invocation: a name containing $ or \,
	// or a relabel after a failed chained label (SR-3.5).
	CallSetLabel Call = "label by id"
)

// Failure classifies a failed call (SR-2.3, SR-2.5). Only the production
// client derives it from tmux's output: the exit status and the first line of
// standard error. The zero value is not a valid failure.
type Failure int

// The failure kinds of Appendix F.1.
const (
	// FailTimeout: no answer within the call's timeout (SR-2.4).
	FailTimeout Failure = iota + 1
	// FailUnavailable: the tmux binary could not be run (SR-2.6).
	FailUnavailable
	// FailSocketDenied: "error connecting to <socket> (Permission denied)",
	// recognised on every call kind; tmux unavailable (SR-1.8, SR-2.5).
	FailSocketDenied
	// FailNoServer: "no server running on <socket>" (SR-2.5).
	FailNoServer
	// FailNoSocket: "error connecting to <socket> (No such file or
	// directory)" (SR-2.5).
	FailNoSocket
	// FailDuplicate: the "duplicate session: " prefix, on the create only
	// (SR-2.5).
	FailDuplicate
	// FailLabel: create only; a reply was printed but a chained label step
	// (the session label's or the pane label's) failed (SR-3.5).
	FailLabel
	// FailUnrecognized: anything else, unparseable output and the data-call
	// pipe-close-wait case included (SR-2.4, SR-2.5).
	FailUnrecognized
)

// String returns a short wording of the failure kind for descriptions and
// test output.
func (f Failure) String() string {
	switch f {
	case FailTimeout:
		return "timeout"
	case FailUnavailable:
		return "tmux unavailable"
	case FailSocketDenied:
		return "socket permission denied"
	case FailNoServer:
		return "no server running"
	case FailNoSocket:
		return "no socket"
	case FailDuplicate:
		return "duplicate session"
	case FailLabel:
		return "label step failed"
	case FailUnrecognized:
		return "unrecognized reply"
	}
	return "unknown failure " + strconv.Itoa(int(f))
}

// CallError is the only error the socket-taking methods return (Appendix
// F.1; the name-based HasSession keeps its own contract). It
// wraps no catalogued sentinel (SR-1.5): the verb layer maps it to verb
// errors. Its message never carries a label's value.
type CallError struct {
	// Call is the call that failed.
	Call Call
	// Failure classifies the failure.
	Failure Failure
	// Timeout is the timeout that expired (FailTimeout only).
	Timeout time.Duration
	// Socket is the socket path taken from the reply (FailSocketDenied,
	// FailNoServer and FailNoSocket only).
	Socket string
	// FirstLine is the first line of the reply or of the unparseable output,
	// trimmed, at most 200 bytes (FailUnrecognized only). It is never a
	// label's value: an unparseable lookup answer is described by a fixed
	// text instead of its lines.
	FirstLine string
	// ExitStatus is the tmux client's exit status when it exited on its own,
	// and -1 when it did not (a timeout, or the binary could not be run). With
	// HadStdout it lets the create's callers tell an exit-0 reply that did not
	// parse from a non-zero exit with or without a reply (SR-3.5, SR-9.4).
	ExitStatus int
	// HadStdout reports that the call wrote something to standard output.
	HadStdout bool
}

// Error names the call by its wording and the failure. A timeout states the
// effective timeout in seconds, for example "0.3 s".
func (e *CallError) Error() string {
	msg := "tmux " + string(e.Call) + " failed: "
	switch e.Failure {
	case FailTimeout:
		return msg + "no answer within " + formatSeconds(e.Timeout)
	case FailUnavailable:
		return msg + "the tmux binary could not be run"
	case FailSocketDenied:
		return msg + "permission denied on socket " + e.Socket
	case FailNoServer:
		return msg + "no server running on " + e.Socket
	case FailNoSocket:
		return msg + "no socket at " + e.Socket
	case FailDuplicate:
		return msg + "duplicate session"
	case FailLabel:
		return msg + "the session was created but its chained label step failed"
	case FailUnrecognized:
		if e.FirstLine != "" {
			return msg + "unrecognized reply: " + e.FirstLine
		}
		return msg + "unrecognized reply"
	}
	return msg + e.Failure.String()
}

// formatSeconds renders d in seconds with no trailing zeros, e.g. "0.3 s",
// "1.5 s", "2 s".
func formatSeconds(d time.Duration) string {
	return strconv.FormatFloat(d.Seconds(), 'f', -1, 64) + " s"
}

// LookupAnswer is the one-call lookup's parsed answer (SR-3.4).
type LookupAnswer struct {
	// Sessions holds one entry per session line, in listing order.
	Sessions []Session
	// ServerPID is the server's #{pid}, the same on every line; 0 with no
	// session lines, which carry no server identity (SR-3.3, LFR H5).
	ServerPID int
	// ServerStart is the server's #{start_time}, epoch seconds; 0 with no
	// session lines.
	ServerStart int64
	// ScopeValue reports a non-empty line in the scope section: a global,
	// server or global-window @ad_owner value exists (SR-3.4 parse rule,
	// LFR H6), so every verdict on that server is provenance_conflict.
	ScopeValue bool
}

// Session is one session line of the lookup: its id, creation time, stored
// name and classified label (SR-3.4, SR-3.9, SR-3.10).
type Session struct {
	// ID is the session id, "$N".
	ID string
	// Created is #{session_created}, epoch seconds.
	Created int64
	// Name is the stored name, bytes exactly as listed: no normalisation and
	// no unescaping (SR-3.10, Appendix E N1, N6).
	Name string
	// Label is the classified @ad_owner value; the raw value never leaves the
	// client.
	Label Label
}

// Label is a classified @ad_owner value (SR-3.4, LFR G1; WD 2026-09-29
// STORE). It is LabelValid only when the value is "ad1 <16 lowercase hex>
// <$N> <instance id> <16 lowercase hex store id>" with $N its own line's
// session id. The store id is the last field, so the instance id is
// everything between the third and the last space: non-empty, with no
// control character, and possibly holding spaces. A four-field value is
// LabelNone, except that a four-field value whose instance id ends in a
// space and 16 lowercase hex reads as a shorter id plus that word as its
// store id. Token, InstanceID and StoreID are empty unless LabelValid.
type Label struct {
	// Kind is the label's class.
	Kind LabelKind
	// Token is the launch token, 16 lowercase hex characters (LabelValid
	// only).
	Token string
	// InstanceID is the instance id, byte for byte (LabelValid only). It is
	// compared, never logged.
	InstanceID string
	// StoreID is the writing store's id, its store_meta.store_id: 16
	// lowercase hex characters (LabelValid only; SR-5.1). A label whose
	// StoreID is not this store's is another store's, never Ours or
	// Leftover; comparing it with this store's is the lookup's job, not the
	// parser's (SR-3.4). It is never put in an error description (SR-15).
	StoreID string
}

// LabelKind is a label's class as the client parses it (SR-3.4).
type LabelKind int

// The label classes. LabelNone is the zero value.
const (
	// LabelNone: empty, unparseable (a four-field value or a store id that
	// is not 16 lowercase hex included), a borrowed value (another session's
	// id) or a control character in the instance id.
	LabelNone LabelKind = iota
	// LabelValid: a well-formed label embedding its own session's id.
	LabelValid
)

// CreateReply is the create call's -P -F line (SR-2.1): '#{session_id}
// #{pid} #{start_time} #{pane_id} #{pane_pid}'.
type CreateReply struct {
	// SessionID is the new session's id, "$N".
	SessionID string
	// ServerPID is the server's #{pid}.
	ServerPID int
	// ServerStart is the server's #{start_time}, epoch seconds.
	ServerStart int64
	// PaneID is the new session's first pane id, "%N".
	PaneID string
	// PanePID is that pane's #{pane_pid}.
	PanePID int
}

// Pane is one list-panes -a line (SR-3.7): session id, window index, pane
// index, pane id, pane pid and the pane label's token.
type Pane struct {
	// SessionID is the id of the session the line lists the pane under.
	SessionID string
	// Window is #{window_index}.
	Window int
	// Index is #{pane_index}.
	Index int
	// ID is the pane id, "%N".
	ID string
	// PID is #{pane_pid}.
	PID int
	// AdPane is the launch token of the line's #{@ad_pane}, the pane label
	// "<16 lowercase hex token> <pane id>" the create sets on its pane (WD
	// 2026-09-29c), when the value embeds this line's own pane id (ID); ""
	// otherwise: unset, malformed, or a window, session, global or server
	// value borrowed through the format, which names another pane or none
	// (the scope guard of SR-3.6). The raw value never leaves the client.
	AdPane string
}

// Timeouts holds the effective per-call bounds (SR-2.4, SR-4.1, SR-13.1):
// pkg/api fills it from the config accessors, and tests pass short ones.
// internal/tmux defines no defaults.
type Timeouts struct {
	// Query bounds the lookup and the pane listing.
	Query time.Duration
	// Action bounds the kills, the text, Enter and key sends, the capture and
	// the label by id.
	Action time.Duration
	// Create bounds the create invocation.
	Create time.Duration
	// WaitDelay is the pipe-close wait (exec.Cmd.WaitDelay), counted from the
	// client process's exit or termination.
	WaitDelay time.Duration
}
