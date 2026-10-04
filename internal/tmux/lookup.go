package tmux

import (
	"errors"
	"slices"
)

// This file holds the shared lookup (SRD SR-3.3, SR-3.4, SR-3.10, SR-3.13,
// SR-3.16, Appendix F.2; PO 2026-09-27 LABEL; LFR C1, H5, H6): given a row's
// launch identity and this store's id, one lookup call on the row's socket
// gives exactly one verdict, Ours, Leftover, Gone or Can't tell, plus the
// holder of a given name. It consumes only the client's typed results
// (LookupAnswer, *CallError) and a start-time reader; it reads no clock, no
// environment and no store, never resolves or creates a socket, and never
// imports internal/probe or internal/config. No verdict depends on a
// session's name; the name matters only for the holder.

// Launch is what a lookup needs of a row (PO 2026-09-27 LABEL; Appendix
// F.2): its instance id and launch token, this store's id, its socket and its
// recorded server identity. Zero values mean "none"; a zero ServerPID means
// no server identity is recorded (SR-3.3).
type Launch struct {
	// InstanceID is the row's instance id, compared byte for byte.
	InstanceID string
	// Token is the row's launch_token, 16 lowercase hex characters; "" when
	// the row records none, and then the row is never Ours (SR-3.4).
	Token string
	// StoreID is this store's store_meta.store_id (WD 2026-09-29 STORE). The
	// lookup never reads the store: callers pass it. "" matches no label.
	StoreID string
	// Socket is the socket path the lookup call uses: the row's recorded
	// tmux_socket, or the caller's resolved one for a row that records none
	// (SR-3.3).
	Socket string
	// ServerPID is the recorded tmux_server_pid.
	ServerPID int
	// ServerStart is the recorded tmux_server_started, the server's
	// #{start_time}.
	ServerStart int64
	// ServerStarttime is the recorded tmux_server_starttime: the server
	// process's start time as the ProcChecker reports it (SR-3.8).
	ServerStarttime string
}

// ProcChecker is the start-time reader (SR-3.8; LFR C1), the only process
// judgement of this release: the server check, the identity write,
// adoption, SessionStart and pane liveness, kill's wait, expire's
// process_alive and resume's and reuse's process check. On Linux it reads
// field 22 of /proc/<pid>/stat, on darwin the KERN_PROC_PID entry's
// p_starttime. It never reads a process's environment. A zombie (state
// Z) counts as gone. Implemented in internal/probe (probe.NewProcChecker),
// which satisfies this interface structurally; this package does not import
// it (Appendix F.2).
type ProcChecker interface {
	// StartTime reports the pid's start time. known false: unreadable (EACCES, no /proc).
	// alive false with known true: no such process, or a zombie.
	StartTime(pid int) (start string, alive bool, known bool)
}

// LookupClient is what a lookup needs (Appendix F.2). *Client and
// tmuxfix.Recorder satisfy it.
type LookupClient interface {
	Lookup(socket string) (LookupAnswer, error)
}

var _ LookupClient = (*Client)(nil)

// Verdict is the lookup's verdict for a row (SR-3.4). The zero value is not a
// valid verdict.
type Verdict int

// The four verdicts of SR-3.4; tmux unavailable is a Can't tell kind (LFR
// S15).
const (
	// Ours: exactly one session carries the row's current label, whatever
	// its name.
	Ours Verdict = iota + 1
	// Leftover: no current label, and one or more old labels of this row.
	Leftover
	// Gone: neither, on the recorded server (an empty listing of it
	// included; LFR H5; b.47f), a restarted one, or (with no identity
	// recorded) the server at the socket; also a no-server or no-socket
	// reply, or an empty listing of another server, whose recorded server
	// process is gone; and, with no server identity recorded, an empty
	// listing or a no-server reply (SR-3.3).
	Gone
	// CantTell: see CantTellKind.
	CantTell
)

// CantTellKind is the variant of a Can't tell verdict (SR-3.4). The zero
// value is not a valid kind.
type CantTellKind int

// The four Can't tell variants of SR-3.4.
const (
	// CantTellUnreadable: a timeout, an unrecognised reply or a malformed
	// answer.
	CantTellUnreadable CantTellKind = iota + 1
	// CantTellDifferentServer: the socket's server is not the one the agent
	// was launched on, or that cannot be told (SR-3.3).
	CantTellDifferentServer
	// CantTellProvenanceConflict: a scope value, or two sessions carrying the
	// current label.
	CantTellProvenanceConflict
	// CantTellUnavailable: the binary cannot be run, or the socket-permission
	// reply.
	CantTellUnavailable
)

// The Result.Server values (SR-3.3; SR-14's server field).
const (
	// ServerMatch: the answering server's pid and start equal the recorded
	// ones, on a listing with or without sessions (LFR H5; b.47f).
	ServerMatch = "match"
	// ServerRestarted: the recorded server process is gone (a listing, empty
	// or not, with another identity, or a no-server reply).
	ServerRestarted = "restarted"
	// ServerDiffers: every different-server outcome.
	ServerDiffers = "differs"
	// ServerUnknown: no server identity is recorded; whichever server
	// answers counts.
	ServerUnknown = "unknown"
)

// The six ad.provenance.disagree reasons (SR-3.16, SR-14). The lookup
// produces the first four; the verbs decide the last two, adopted (SR-3.6)
// and name_changed (SR-3.4), and never write a literal for any of them.
// pid_mismatch is retired as a disagree reason (SR-3.8; WD 2026-09-29c).
const (
	// ReasonServerRestarted: a listing, with or without session lines,
	// carried another server identity and the recorded server process is
	// gone (AC-LKP-19; LFR H5; b.47f). A no-server or no-socket reply logs
	// none: nothing answered.
	ReasonServerRestarted = "server_restarted"
	// ReasonServerMismatch: every different_server outcome.
	ReasonServerMismatch = "server_mismatch"
	// ReasonDuplicateLabel: two or more sessions carry the current label.
	ReasonDuplicateLabel = "duplicate_label"
	// ReasonScopeValue: an @ad_owner value exists at the global, server or
	// global-window scope.
	ReasonScopeValue = "scope_value"
	// ReasonAdopted: a verb's adoption write, recording a lost create reply's
	// server and pane identity, applied (SR-3.6). Decided by the verb, never
	// by the lookup.
	ReasonAdopted = "adopted"
	// ReasonNameChanged: the lookup found Ours under a name other than the
	// row's recorded one (SR-3.4). Decided by the verb, never by the lookup.
	ReasonNameChanged = "name_changed"
)

// The outcome tokens of SR-3.4 (trail fields), one per verdict or variant.
const (
	tokenOurs               = "ours"
	tokenLeftover           = "leftover"
	tokenGone               = "gone"
	tokenCantTell           = "cant_tell"
	tokenDifferentServer    = "different_server"
	tokenProvenanceConflict = "provenance_conflict"
	tokenTmuxUnavailable    = "tmux_unavailable"
)

// TokenNotRun is the not_run token (SR-3.15, SR-6.4, SR-13.5): the token of
// a Skipped result, where no call was made for the row because the sweep had
// stopped calling tmux or the row's own call spent the budget and its outcome
// was discarded; and the value a verb's trail field takes when no lookup, no
// follow-up or no process check ran (kill's lookup_outcome,
// followup_outcome and process_check). It is none of the seven outcome
// tokens; callers check Skipped first.
const TokenNotRun = "not_run"

// Result is the lookup result (Appendix F.2). It stays internal: no exported
// signature of pkg/api uses it.
//
// Callers act on the verdict first. The holder is reported on every verdict
// that read an answer, a different server and a provenance_conflict
// included; under provenance_conflict the holder's class cannot be trusted.
type Result struct {
	// Verdict is the row's verdict.
	Verdict Verdict
	// CantTell is the variant (CantTell only).
	CantTell CantTellKind
	// Session is the session with the current label (Ours only).
	Session Session
	// Leftovers holds the sessions with an old label of this store, in
	// listing order (Leftover, and Ours for messages). Another store's
	// sessions are never listed.
	Leftovers []Session
	// Conflicting holds the sessions carrying the row's current label, in
	// listing order, when two or more do (provenance_conflict with reason
	// duplicate_label), so a description can name them (SR-1.4); nil
	// otherwise, a scope value's provenance_conflict included.
	Conflicting []Session
	// Holder is the one session holding the given name (SR-3.10); nil when
	// no name was given, no answer was read, none holds it, or more than one
	// does.
	Holder *Session
	// HolderClass is the holder's label class (SR-3.10); zero when Holder is
	// nil. Its CaseWords give the description's words.
	HolderClass LabelClass
	// HolderAmbiguous reports more than one listing entry matching the name:
	// Can't tell for the holder check (SR-3.10).
	HolderAmbiguous bool
	// Server is ServerMatch, ServerRestarted, ServerDiffers or ServerUnknown;
	// "" when no server check ran (unreadable, tmux unavailable).
	Server string
	// ServerPID and ServerStart are the answering server's #{pid} and
	// #{start_time} from the lookup's identity line, for adoption (SR-3.6);
	// set for an empty listing too (LFR H5; b.47f), zero when no answer was
	// read.
	ServerPID   int
	ServerStart int64
	// Adopt is true for Ours on a row with no recorded server identity
	// (SR-3.6); only kill, send-keys, pause and find-missing write it (LFR
	// H2).
	Adopt bool
	// Disagree holds the ad.provenance.disagree reasons to log (SR-3.16):
	// the server check's and the scope or duplicate one, each at most once,
	// never a label's value. pid_mismatch is retired (WD 2026-09-29c).
	Disagree []string
	// Cause is the call failure behind CantTellUnreadable or
	// CantTellUnavailable when it is a *CallError; nil otherwise.
	Cause *CallError
	// Skipped reports that the row is "tmux already skipped": the sweep had
	// stopped calling tmux on the row's socket or for the run, or the row's
	// own call spent the run's budget and its outcome was discarded (SR-3.15,
	// SR-13.5). Set only by the sweep; a Skipped result carries nothing else
	// (no verdict, Can't tell kind, Cause, holder or server status), and its
	// Token is not_run. Callers check Skipped first.
	Skipped bool
}

// Token returns the outcome token of SR-3.4: ours, leftover, gone, cant_tell
// (unreadable), different_server, provenance_conflict or tmux_unavailable;
// not_run for a Skipped result. It returns "" for a zero or unknown verdict
// or variant.
func (r Result) Token() string {
	if r.Skipped {
		return TokenNotRun
	}
	switch r.Verdict {
	case Ours:
		return tokenOurs
	case Leftover:
		return tokenLeftover
	case Gone:
		return tokenGone
	case CantTell:
		switch r.CantTell {
		case CantTellUnreadable:
			return tokenCantTell
		case CantTellDifferentServer:
			return tokenDifferentServer
		case CantTellProvenanceConflict:
			return tokenProvenanceConflict
		case CantTellUnavailable:
			return tokenTmuxUnavailable
		}
	}
	return ""
}

// Lookup makes the one lookup call on row.Socket and classifies it for the
// row (single-row verbs, follow-ups, re-lookups; Appendix F.2). holderName,
// when not empty, is the name whose holder to report (SR-3.10). It makes
// exactly one c.Lookup call, never resolves or creates a socket, and reads no
// clock and no environment.
func Lookup(c LookupClient, pc ProcChecker, row Launch, holderName string) Result {
	ans, err := c.Lookup(row.Socket)
	return resultForCall(ans, err, pc, row, holderName)
}

// Classify classifies a row against an answer the caller already holds
// (sweeps; Appendix F.2). It makes no tmux call.
//
// Precedence: the server check (a different server wins); then a scope value
// (provenance_conflict, on any listing, an empty or restarted one included);
// then the labels (ClassOf): exactly one current is Ours, whatever its name;
// two or more are provenance_conflict; else one or more old is Leftover; else
// Gone. Another store's sessions count toward Gone and are never listed.
func Classify(ans LookupAnswer, pc ProcChecker, row Launch, holderName string) Result {
	sc := checkServer(row, pc, replyListing, ans)
	r := Result{Server: sc.server, ServerPID: ans.ServerPID, ServerStart: ans.ServerStart}
	r.Holder, r.HolderAmbiguous = matchHolder(ans.Sessions, holderName)
	if r.Holder != nil {
		r.HolderClass = row.ClassOf(r.Holder.Label)
	}
	r.Disagree = appendReason(r.Disagree, sc.reason)
	switch {
	case sc.differs:
		r.Verdict, r.CantTell = CantTell, CantTellDifferentServer
	case ans.ScopeValue:
		r.Verdict, r.CantTell = CantTell, CantTellProvenanceConflict
		r.Disagree = appendReason(r.Disagree, ReasonScopeValue)
	default:
		classifySessions(&r, ans.Sessions, row)
	}
	return r
}

// classifySessions sets r's verdict from the sessions' label classes against
// row (SR-3.4): exactly one current is Ours with Session set and Adopt for a
// row with no recorded server identity; two or more current are
// provenance_conflict (duplicate_label) with Conflicting listing them and no
// Leftovers; no current with one or more old is Leftover; otherwise Gone.
// Leftovers lists the old-labelled sessions.
func classifySessions(r *Result, sessions []Session, row Launch) {
	var current []Session
	for _, s := range sessions {
		switch row.ClassOf(s.Label) {
		case ClassCurrent:
			current = append(current, s)
		case ClassOld:
			r.Leftovers = append(r.Leftovers, s)
		}
	}
	switch {
	case len(current) > 1:
		r.Verdict, r.CantTell = CantTell, CantTellProvenanceConflict
		r.Disagree = appendReason(r.Disagree, ReasonDuplicateLabel)
		r.Conflicting = current
		r.Leftovers = nil
	case len(current) == 1:
		r.Verdict, r.Session = Ours, current[0]
		r.Adopt = row.ServerPID == 0
	case len(r.Leftovers) > 0:
		r.Verdict = Leftover
	default:
		r.Verdict = Gone
	}
}

// resultForCall maps one lookup call's outcome to a result for row: the one
// shared mapping of Lookup and the sweep (SR-3.3, SR-3.4, SR-2.5, SR-2.6).
// No error: Classify; an error: resultForFailure.
func resultForCall(ans LookupAnswer, err error, pc ProcChecker, row Launch, holderName string) Result {
	if err == nil {
		return Classify(ans, pc, row, holderName)
	}
	return resultForFailure(err, pc, row)
}

// ListingFailure classifies a single-row verb's failed pane listing
// (`list-panes -a`) for row (SR-2.5, SR-3.3, SR-3.7): the lookup Result a
// lookup call with the same failure would give, through the lookup's one
// failure mapping and server check, never a copy of them. err is the
// listing's error and must not be nil; an error that is not a *CallError
// counts as FailUnrecognized (Appendix F.3):
//
//   - a timeout or an unrecognised or malformed reply: Can't tell,
//     unreadable, with Cause set for a *CallError;
//   - the binary failing to run, or the socket-permission reply: Can't tell,
//     tmux unavailable, Cause set;
//   - the no-server and no-socket replies: Gone or a different server by the
//     row's server check (SR-3.3), with the same Server value and disagree
//     reasons a lookup gets.
//
// It is for single-row verbs (kill, and later read-pane, send-keys and
// pause); a sweep's listing goes through Sweep.ListPanes, which shares the
// same mapping. It makes no tmux call and reads no clock and no environment.
func ListingFailure(err error, pc ProcChecker, row Launch) Result {
	return resultForFailure(err, pc, row)
}

// resultForFailure maps a failed call to a result for row: the one failure
// mapping of the lookup call, of the sweep's pane listing and of a single-row
// verb's pane listing (ListingFailure) (SR-2.5, SR-2.6, SR-3.3, SR-3.7). err
// must not be nil.
//
//   - FailUnavailable, FailSocketDenied: Can't tell, tmux unavailable, Cause
//     set.
//   - FailNoServer, FailNoSocket: the server check for a no-server reply:
//     Gone, or a different server.
//   - Any other *CallError (a timeout, an unrecognised or malformed answer):
//     Can't tell, unreadable, Cause set; any other error: the same with no
//     Cause.
func resultForFailure(err error, pc ProcChecker, row Launch) Result {
	var ce *CallError
	if !errors.As(err, &ce) {
		return Result{Verdict: CantTell, CantTell: CantTellUnreadable}
	}
	switch ce.Failure {
	case FailUnavailable, FailSocketDenied:
		return Result{Verdict: CantTell, CantTell: CantTellUnavailable, Cause: ce}
	case FailNoServer, FailNoSocket:
		sc := checkServer(row, pc, replyNoServer, LookupAnswer{})
		r := Result{Verdict: Gone, Server: sc.server, Disagree: appendReason(nil, sc.reason)}
		if sc.differs {
			r.Verdict, r.CantTell = CantTell, CantTellDifferentServer
		}
		return r
	}
	return Result{Verdict: CantTell, CantTell: CantTellUnreadable, Cause: ce}
}

// appendReason appends reason to reasons unless it is empty or already there,
// so each reason is logged at most once.
func appendReason(reasons []string, reason string) []string {
	if reason == "" || slices.Contains(reasons, reason) {
		return reasons
	}
	return append(reasons, reason)
}
