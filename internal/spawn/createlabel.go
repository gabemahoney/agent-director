package spawn

import (
	"errors"

	"github.com/gabemahoney/agent-director/internal/tmux"
)

// This file holds the shared create-and-label step (SR-3.5, SR-9.4,
// SR-13.2): the tmux-side sequence around a session-creating call and the
// classification of its outcome. It knows no verb: plain spawn maps the
// outcome to its errors in launch_errors.go, and resume and reuse add their
// own mappings (with their restores) without touching this sequence (SR-1.8).

// LaunchTmux is the tmux surface the create-and-label step needs: the create
// with its chained labels, the one label by id, and the kill of a session
// that could not be labelled (Appendix F.5). *tmux.Client and
// tmuxfix.Recorder satisfy it. Each method reports a failure as
// *tmux.CallError; an error of any other type counts as
// tmux.FailUnrecognized for that call (Appendix F.3).
type LaunchTmux interface {
	NewSession(socket, name, cwd string, envs map[string]string, command []string, token, instanceID, storeID string) (tmux.CreateReply, error)
	SetLabel(socket, sessionID, paneID, token, instanceID, storeID string) error
	KillSessionID(socket, sessionID string) error
}

// CreateRequest is what one create-and-label step launches: the session name,
// cwd, environment and command on the launch's socket, labelled with the
// launch token, the instance id and this store's id (SR-3.5; WD 2026-09-29
// STORE). StoreID is the writing store's (*store.Store).StoreID(); the step
// never invents or defaults one.
type CreateRequest struct {
	Socket     string
	Name       string
	CWD        string
	Env        map[string]string
	Command    []string
	Token      string
	InstanceID string
	StoreID    string
}

// CreateKind classifies the outcome of one create-and-label step. The zero
// value is not a valid kind.
type CreateKind int

// The outcomes of the create-and-label step (SR-3.5, SR-9.4, SR-2.5).
const (
	// CreateLabelled: the reply parsed and the label is in place, chained or
	// set by id. Reply is set.
	CreateLabelled CreateKind = iota + 1
	// CreateLostReply: the create exited 0 but its reply did not parse (a
	// reply cut short by the pipe-close wait included). The session was
	// created; its chained label, if any, is in place; its id is unknown, so
	// no identity can be recorded (SR-2.1, SR-3.5). For a name labelled by id
	// this leaves an unlabelled session (the documented residual).
	CreateLostReply
	// CreateUnresponsive: the create timed out, or exited non-zero with a
	// reply on standard output that did not parse. The session may have been
	// created; it is never relabelled (SR-3.5, SR-9.4).
	CreateUnresponsive
	// CreateUnavailable: the tmux binary could not be run, or the socket
	// "Permission denied" reply (SR-1.8, SR-2.6).
	CreateUnavailable
	// CreateDuplicate: the "duplicate session" reply (SR-2.5).
	CreateDuplicate
	// CreateUnlabelledEnded: the session was created (Reply set) but could not
	// be labelled, and was then ended by its tmux id (SR-3.5).
	CreateUnlabelledEnded
	// CreateUnlabelledRunning: the session was created (Reply set) but could
	// not be labelled, and ending it by its tmux id failed too, so an
	// unlabelled session may still run (SR-3.5).
	CreateUnlabelledRunning
	// CreateFailed: any other launch failure: the no-server and no-socket
	// replies, and a non-zero exit with nothing on standard output and an
	// unrecognised reply (SR-2.5).
	CreateFailed
)

// CreateOutcome is the result of one create-and-label step.
type CreateOutcome struct {
	// Kind classifies the outcome.
	Kind CreateKind
	// Reply is the parsed create reply (CreateLabelled and the two
	// CreateUnlabelled kinds); its SessionID is the new session's id.
	Reply tmux.CreateReply
	// Cause is the failure that decided the outcome: the create's for
	// CreateUnresponsive, CreateUnavailable, CreateDuplicate and CreateFailed
	// (and the lost reply's for CreateLostReply), the label by id's for the
	// two CreateUnlabelled kinds; nil for CreateLabelled.
	Cause *tmux.CallError
}

// CreateAndLabel runs the one create-and-label sequence of SR-3.5 on
// req.Socket:
//
//  1. One create invocation, NewSession, carrying the chained session and
//     pane labels, or no chain for a name containing $ or \.
//  2. At most one label by id, SetLabel with the reply's session and pane
//     ids: the labelling itself for a name containing $ or \ after a parsed
//     reply, or the one relabel after a reply whose chained label step failed
//     (tmux.FailLabel).
//  3. When that label by id fails, one KillSessionID of the reply's session.
//
// A create that timed out or whose reply did not parse is never labelled by
// id, since its session id is unknown. So no path makes more than one create,
// one label by id and one kill: the worst case costs C + 2A (SR-13.2). Every
// create and label by id carries req.StoreID as the label's last field.
func CreateAndLabel(t LaunchTmux, req CreateRequest) CreateOutcome {
	reply, err := t.NewSession(req.Socket, req.Name, req.CWD, req.Env, req.Command, req.Token, req.InstanceID, req.StoreID)
	if err != nil {
		ce := asCallError(tmux.CallCreate, err)
		if ce.Failure == tmux.FailLabel {
			return labelByID(t, req, reply)
		}
		return CreateOutcome{Kind: createFailureKind(ce), Cause: ce}
	}
	if tmux.NeedsLabelByID(req.Name) {
		return labelByID(t, req, reply)
	}
	return CreateOutcome{Kind: CreateLabelled, Reply: reply}
}

// labelByID makes the one label-by-id call for a created session whose reply
// parsed, and kills the session by its id when the call fails (SR-3.5).
func labelByID(t LaunchTmux, req CreateRequest, reply tmux.CreateReply) CreateOutcome {
	err := t.SetLabel(req.Socket, reply.SessionID, reply.PaneID, req.Token, req.InstanceID, req.StoreID)
	if err == nil {
		return CreateOutcome{Kind: CreateLabelled, Reply: reply}
	}
	out := CreateOutcome{Kind: CreateUnlabelledEnded, Reply: reply, Cause: asCallError(tmux.CallSetLabel, err)}
	if t.KillSessionID(req.Socket, reply.SessionID) != nil {
		out.Kind = CreateUnlabelledRunning
	}
	return out
}

// createFailureKind classifies a failed create with no parsed reply (SR-2.5,
// SR-3.5, SR-9.4). A FailUnrecognized create is told apart by its exit
// status and standard output: exit 0 is a lost reply; a non-zero exit with
// output that did not parse is unresponsive; a non-zero exit with no output
// is another launch failure.
func createFailureKind(ce *tmux.CallError) CreateKind {
	switch ce.Failure {
	case tmux.FailTimeout:
		return CreateUnresponsive
	case tmux.FailUnavailable, tmux.FailSocketDenied:
		return CreateUnavailable
	case tmux.FailDuplicate:
		return CreateDuplicate
	case tmux.FailUnrecognized:
		switch {
		case ce.ExitStatus == 0:
			return CreateLostReply
		case ce.HadStdout:
			return CreateUnresponsive
		}
	}
	return CreateFailed
}

// asCallError returns err's *tmux.CallError, or, for an error of any other
// type from an injected client, a FailUnrecognized failure of call with no
// exit status and no output (Appendix F.3), so it never reads as a lost reply.
func asCallError(call tmux.Call, err error) *tmux.CallError {
	var ce *tmux.CallError
	if errors.As(err, &ce) {
		return ce
	}
	return &tmux.CallError{Call: call, Failure: tmux.FailUnrecognized, ExitStatus: -1}
}
