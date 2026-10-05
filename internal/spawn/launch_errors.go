package spawn

import (
	"fmt"

	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api/manifest"
)

// RowStaysPending is the row sentence after a launch failure that leaves the
// row as the launch's write left it: a plain spawn's after every create
// failure other than "duplicate session" (SR-9.4), and resume's after a
// launch timeout (SR-8.5).
const RowStaysPending = "the row stays pending"

// LaunchRetryRule is the launch-timeout rule (SR-1.4, SR-18.1): the session
// may have been created, so the caller does not retry until get shows the
// row ended or missing. Every launch verb's timeout description ends with it
// (LaunchTimeoutError), and plain spawn's retry sentences build on it.
const LaunchRetryRule = "do not retry until get shows the row ended or missing"

// ReuseOptIn names the reuse opt-in in its one spelling for a description
// every surface shows, manifest.ReuseOptInSpelling (b.c4u): the param name
// MCP and the TypeScript client take, then the CLI flag, which the manifest's
// spawn, kill and delete Descriptions use too. It is the one spelling the
// runtime error texts build on (b.1qq, b.c4u).
const ReuseOptIn = "the reuse opt-in " + manifest.ReuseOptInSpelling

// ReuseRetry names the retry that works for a plain spawn whose row for this
// id is, or will be, finished (b.1qq): a plain spawn of the id collides with
// that row at the collision pre-check (ErrInstanceIdCollision), so the retry
// uses the reuse opt-in (ReuseOptIn). Its reason follows it as
// PlainSpawnCollides.
// pkg/api's held-name retry sentences build on both.
const ReuseRetry = "a retry with this id uses " + ReuseOptIn

// PlainSpawnCollides is ReuseRetry's reason: a plain spawn of the id
// collides with its row.
const PlainSpawnCollides = "since a plain spawn of the id now collides"

// explicitIDTimeoutRetry is plain spawn's launch-timeout retry sentence for a
// caller-supplied id (b.1qq): the launch-timeout rule, then the opted-in
// retry, since once get shows the row ended or missing a plain spawn of the
// id collides with it. A minted id keeps LaunchRetryRule alone: its retry
// mints a new id.
const explicitIDTimeoutRetry = LaunchRetryRule + "; then " + ReuseRetry + ", " + PlainSpawnCollides

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
// lives (SR-1.8). It returns nil for CreateLabelled and CreateLostReply,
// which are successes. "duplicate session" (CreateDuplicate) is not a verb
// error here: it becomes a *HeldNameError carrying req's instance id, name,
// socket and token and launchStartMillis, the insert's launch start, for the
// caller's held-name path. Every other error wraps exactly one catalogued
// sentinel and ends with the row sentence; the launch timeout then ends with
// its retry sentence, which minted picks: LaunchRetryRule for a minted id,
// whose retry mints a new one, and for a caller-supplied id the same rule
// followed by the opted-in retry (explicitIDTimeoutRetry, b.1qq). No
// description carries a label value, a token, another row's id or a
// session-environment value.
func plainSpawnCreateError(o CreateOutcome, req CreateRequest, launchStartMillis int64, minted bool) error {
	switch o.Kind {
	case CreateLabelled, CreateLostReply:
		return nil
	case CreateDuplicate:
		return &HeldNameError{
			InstanceID:            req.InstanceID,
			Name:                  req.Name,
			Socket:                req.Socket,
			Token:                 req.Token,
			LaunchStartedAtMillis: launchStartMillis,
		}
	case CreateUnresponsive:
		retry := explicitIDTimeoutRetry
		if minted {
			retry = LaunchRetryRule
		}
		return launchTimeoutError(o.Cause, "spawn", req.InstanceID, RowStaysPending, retry)
	case CreateUnavailable:
		return TmuxUnavailableError(o.Cause, req.Socket, RowStaysPending)
	case CreateUnlabelledEnded, CreateUnlabelledRunning:
		return UnlabelledSessionError(o, req.Name, RowStaysPending)
	}
	// CreateFailed: every other launch failure (SR-2.5).
	return CreateFailedError(o.Cause, req.Name, RowStaysPending)
}

// HeldNameError is plain spawn's "duplicate session" outcome (SR-9.4): the
// create found the requested name already held on the launch socket and
// created nothing. Launch returns it at once, with no store write, file or
// network I/O, further tmux call or log line after the create, so the
// caller's held-name path (in pkg/api: the conditional end write first, then
// the one re-lookup, the classified error and the ad.launch.name_held
// record) acts next. Its fields are the facts that path needs: the new row's
// instance id, the requested name as passed to the create, the launch
// socket, the launch token and the insert's launch start in milliseconds.
//
// It is not a verb error: it wraps and matches no catalogued sentinel, and
// the caller always converts it to the classified error, so it never reaches
// an agent. Its text carries no token.
type HeldNameError struct {
	// InstanceID is the new pending row's instance id.
	InstanceID string
	// Name is the requested session name, as passed to the create.
	Name string
	// Socket is the launch socket the create ran on (the row's tmux_socket).
	Socket string
	// Token is the launch token the insert recorded (the row's launch_token).
	Token string
	// LaunchStartedAtMillis is the launch start the insert recorded (the
	// row's launch_started_at), in milliseconds.
	LaunchStartedAtMillis int64
}

// Error says the create found the name held; it names the requested name and
// the instance id and never the token.
func (e *HeldNameError) Error() string {
	return fmt.Sprintf("spawn of instance %s: tmux session %q: duplicate session (held name not yet classified)", e.InstanceID, e.Name)
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
// (SR-1.4, SR-2.5), shared by every launch verb (SR-1.8): a launch failure
// such as the no-server and no-socket replies or a non-zero exit with no
// reply, and "duplicate session" whose holder vanished before the re-lookup
// (no session held the name any more). It carries the quoted name and the
// create call's failure, then consequence, the verb's row sentence, and wraps
// tmux.ErrTmuxSessionCreate only. Plain spawn's vanished holder uses
// InstanceCreateFailedError instead.
func CreateFailedError(ce *tmux.CallError, name, consequence string) error {
	return createFailedError(ce, "", name, consequence)
}

// InstanceCreateFailedError is CreateFailedError led by "instance <id>: ",
// as an unanswered re-lookup's description is led: plain spawn's after
// "duplicate session" whose holder vanished before the re-lookup, whose
// retry sentence in consequence refers to "this id", so the description
// names the id, a minted one included (b.1qq). Resume and reuse use
// CreateFailedError.
func InstanceCreateFailedError(ce *tmux.CallError, instanceID, name, consequence string) error {
	return createFailedError(ce, "instance "+instanceID+": ", name, consequence)
}

// createFailedError is the one format of CreateFailedError and
// InstanceCreateFailedError: lead, "" or the instance id's, then the quoted
// name.
func createFailedError(ce *tmux.CallError, lead, name, consequence string) error {
	return fmt.Errorf("%w: %stmux session %q: %s; %s", tmux.ErrTmuxSessionCreate, lead, name, ce.Error(), consequence)
}

// LaunchTimeoutError is the launch-timeout description of SR-1.4, shared by
// every launch verb (SR-1.8): the verb and the instance id; which call did
// not answer and its effective timeout, or that tmux gave a reply
// agent-director does not recognise; that the session may have been created;
// consequence, the verb's row sentence (for a plain spawn and resume, that the
// row stays pending); and the rule not to retry until get shows the row ended
// or missing (LaunchRetryRule, SR-18.1). It wraps tmux.ErrTmuxUnresponsive
// only and never says that nothing was done or to retry later. Resume and
// reuse use it as it is; plain spawn's (plainSpawnCreateError) adds the
// opted-in retry for a caller-supplied id.
func LaunchTimeoutError(ce *tmux.CallError, verb, instanceID, consequence string) error {
	return launchTimeoutError(ce, verb, instanceID, consequence, LaunchRetryRule)
}

// launchTimeoutError is LaunchTimeoutError ending with retry, the verb's
// retry sentence, in place of LaunchRetryRule alone.
func launchTimeoutError(ce *tmux.CallError, verb, instanceID, consequence, retry string) error {
	what := ce.Error()
	if ce.Failure != tmux.FailTimeout {
		what += "; tmux gave a reply agent-director does not recognise"
	}
	return fmt.Errorf("%w: %s of instance %s: %s; the session may have been created; %s; %s",
		tmux.ErrTmuxUnresponsive, verb, instanceID, what, consequence, retry)
}
