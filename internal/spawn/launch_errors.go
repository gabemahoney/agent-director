package spawn

import (
	"fmt"

	"github.com/gabemahoney/agent-director/internal/tmux"
)

// RowStaysPending is the row sentence after a launch failure that leaves the
// row as the launch's write left it: a plain spawn's after every create
// failure (SR-9.4), and resume's after a launch timeout (SR-8.5).
const RowStaysPending = "the row stays pending"

// TmuxUnavailableError is the one mapping of a "tmux unavailable" call
// failure, the binary not run or the socket "Permission denied" reply, to
// tmux.ErrTmuxNotAvailable (SR-1.8, SR-2.6), shared by the create and the
// label scan's lookup. A permission reply gives a *SocketDeniedError, which
// names the socket and says it is not accessible to this user (SR-1.4) and
// never reads as a missing binary; socket is used when the reply names none.
// A binary that cannot be run keeps the sentinel's own text. consequence
// says what the caller's state is afterwards.
func TmuxUnavailableError(ce *tmux.CallError, socket, consequence string) error {
	if ce.Failure == tmux.FailSocketDenied {
		if ce.Socket != "" {
			socket = ce.Socket
		}
		return &SocketDeniedError{Socket: socket, Consequence: consequence}
	}
	return fmt.Errorf("%w: %s; %s", tmux.ErrTmuxNotAvailable, ce.Error(), consequence)
}

// SocketDeniedError is a tmux call refused with the socket "Permission
// denied" reply (SR-1.8; Appendix E.9 S6), as TmuxUnavailableError returns
// it. Under errors.Is it matches tmux.ErrTmuxNotAvailable and no other
// sentinel (SR-1.1 class ENVIRONMENT; SR-1.5). Like tmux.SocketDirError it
// has its own text and no Unwrap, so its description never carries the
// sentinel's "tmux: not available" wording (SR-1.4 row "socket
// permission").
type SocketDeniedError struct {
	// Socket is the socket path the reply named, or the one the call used.
	Socket string
	// Consequence is the caller's state afterwards: for a plain spawn's
	// create, that the row stays pending (SR-9.4); for resume's create, the
	// restore's result (SR-8.5); for the label scan, that nothing was written
	// (SR-9.3).
	Consequence string
}

// Error names the socket, says it is not accessible to this user, and ends
// with the consequence sentence (SR-1.4). It never names a session-ending
// command.
func (e *SocketDeniedError) Error() string {
	return "tmux socket " + e.Socket + " is not accessible to this user (permission denied); " + e.Consequence
}

// Is reports whether target is tmux.ErrTmuxNotAvailable, the only sentinel
// a socket-permission refusal matches (SR-1.4, SR-1.5).
func (e *SocketDeniedError) Is(target error) bool {
	return target == tmux.ErrTmuxNotAvailable
}

// plainSpawnCreateError maps a failed create-and-label outcome to plain
// spawn's verb error (SR-1.2, SR-1.4, SR-9.4): the one place this mapping
// lives (SR-1.8). Every error wraps exactly one catalogued sentinel and ends
// with the row sentence. It returns nil for CreateLabelled and
// CreateLostReply, which are successes. No description carries a label
// value, a token, another row's id or a session-environment value.
func plainSpawnCreateError(o CreateOutcome, req CreateRequest) error {
	switch o.Kind {
	case CreateLabelled, CreateLostReply:
		return nil
	case CreateUnresponsive:
		return LaunchTimeoutError(o.Cause, "spawn", req.InstanceID, RowStaysPending)
	case CreateUnavailable:
		return TmuxUnavailableError(o.Cause, req.Socket, RowStaysPending)
	case CreateUnlabelledEnded, CreateUnlabelledRunning:
		return UnlabelledSessionError(o, req.Name, RowStaysPending)
	}
	// CreateDuplicate keeps today's mapping until the classified "duplicate
	// session"; CreateFailed is every other launch failure (SR-2.5).
	return CreateFailedError(o.Cause, req.Name, RowStaysPending)
}

// UnlabelledSessionError is the description of a created session that could
// not be labelled (SR-1.4, SR-3.5), shared by every launch verb (SR-1.8): the
// quoted name and the session's tmux id; that the session was created but
// could not be labelled, naming the failed call; that it was ended by its
// tmux id (CreateUnlabelledEnded) or that ending it failed too, so an
// unlabelled session may still run (CreateUnlabelledRunning); then
// consequence, the verb's row sentence (a plain spawn's "the row stays
// pending", or resume's restore result). It wraps tmux.ErrTmuxSessionCreate
// only. o must be one of the two CreateUnlabelled kinds.
func UnlabelledSessionError(o CreateOutcome, name, consequence string) error {
	if o.Kind == CreateUnlabelledRunning {
		return fmt.Errorf("%w: tmux session %q (%s): the session was created but could not be labelled (the %s call failed: %s), and ending it by its tmux id %s failed too, so an unlabelled session may still run; %s",
			tmux.ErrTmuxSessionCreate, name, o.Reply.SessionID, o.Cause.Call, o.Cause.Failure, o.Reply.SessionID, consequence)
	}
	return fmt.Errorf("%w: tmux session %q (%s): the session was created but could not be labelled (the %s call failed: %s), so it was ended by its tmux id %s; %s",
		tmux.ErrTmuxSessionCreate, name, o.Reply.SessionID, o.Cause.Call, o.Cause.Failure, o.Reply.SessionID, consequence)
}

// CreateFailedError is the description of a create that created no session
// (SR-1.4, SR-2.5), shared by every launch verb (SR-1.8): "duplicate
// session" (until its classified re-lookup) and every other launch failure
// (the no-server and no-socket replies, a non-zero exit with no reply). It
// carries the quoted name and the create call's failure, then consequence,
// the verb's row sentence, and wraps tmux.ErrTmuxSessionCreate only.
func CreateFailedError(ce *tmux.CallError, name, consequence string) error {
	return fmt.Errorf("%w: tmux session %q: %s; %s", tmux.ErrTmuxSessionCreate, name, ce.Error(), consequence)
}

// LaunchTimeoutError is the launch-timeout description of SR-1.4, shared by
// every launch verb (SR-1.8): the verb and the instance id; which call did
// not answer and its effective timeout, or that tmux gave a reply
// agent-director does not recognise; that the session may have been created;
// consequence, the verb's row sentence (for a plain spawn and resume, that the
// row stays pending); and the rule not to retry until get shows the row ended
// or missing (SR-18.1). It wraps tmux.ErrTmuxUnresponsive only and never says
// that nothing was done or to retry later.
func LaunchTimeoutError(ce *tmux.CallError, verb, instanceID, consequence string) error {
	what := ce.Error()
	if ce.Failure != tmux.FailTimeout {
		what += "; tmux gave a reply agent-director does not recognise"
	}
	return fmt.Errorf("%w: %s of instance %s: %s; the session may have been created; %s; do not retry until get shows the row ended or missing",
		tmux.ErrTmuxUnresponsive, verb, instanceID, what, consequence)
}
