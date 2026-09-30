package tmux

import (
	"slices"
	"time"
)

// Sweep serves one find-missing or expire run (SRD SR-3.15, SR-13.3, SR-13.5,
// Appendix F.2; LFR M7): one lookup per socket, taken at the first row of that
// socket that reaches the Sweep, the stop rule per socket, and the run's tmux
// time budget, measured with the injected clock. find-missing also takes its
// adoption pane listing through it (ListPanes), under the same stop rule and
// budget. A Sweep serves one run and is not for concurrent use.
//
// Sockets are keyed by Launch.Socket exactly as passed; the Sweep never
// resolves or creates a socket. It reads time only through now, reads no
// environment and never imports internal/config: callers pass the effective
// budget.
//
// The stop rule (SR-3.15; LFR M7). A socket stops when a row's result on it
// is Can't tell of the unreadable variant or tmux unavailable, whether from
// the lookup or from the pane listing: that row gets its result, and every
// later row of that socket is Skipped with no call, even one the held answer
// could decide. Other sockets keep their calls. A different server and a
// provenance_conflict stop nothing, and neither does a no-server or no-socket
// reply.
//
// The budget (SR-13.5, SR-13.3). The budget is spent once the total time spent
// in tmux calls is at least budget; a non-positive budget is spent from the
// start, so no call is ever made (fails closed). Before every call, if the
// budget is spent, no call is made and the row is Skipped. Each call's time is
// now() at its return minus now() at its start, floored at zero, so a clock
// stepped back refunds nothing; it is added to the total. If the total
// reaches the budget when a call returns, that call's answer or failure is
// discarded, nothing is held and its row is Skipped. From then on every row
// on every socket is Skipped, rows whose socket's answer is held included. A
// run therefore spends at most budget plus one call on tmux.
type Sweep struct {
	c       LookupClient
	pc      ProcChecker
	now     func() time.Time
	budget  time.Duration
	spent   time.Duration
	sockets map[string]*sweepSocket
}

// sweepSocket is what a Sweep holds for one socket.
type sweepSocket struct {
	// stopped: the socket's stop rule fired; no more calls or held answers.
	stopped bool
	// looked: the lookup call returned within the budget; ans and lookErr
	// hold its outcome.
	looked  bool
	ans     LookupAnswer
	lookErr error
	// listed: the pane listing returned within the budget; panes and listErr
	// hold its outcome.
	listed  bool
	panes   []Pane
	listErr error
}

// NewSweep returns a Sweep for one run over the lookup client c, the
// start-time reader pc, the clock now (non-nil; read only around calls) and
// the run's tmux time budget (Appendix F.2). It makes no call and does not
// read the clock.
func NewSweep(c LookupClient, pc ProcChecker, now func() time.Time, budget time.Duration) *Sweep {
	return &Sweep{c: c, pc: pc, now: now, budget: budget, sockets: map[string]*sweepSocket{}}
}

// Lookup returns row's result for this run (SR-3.15). On the first row of a
// socket it makes that socket's one LookupClient.Lookup call and holds its
// outcome (the answer or the call error); every row of the socket, that one
// included, is then classified from the held outcome through the lookup's one
// outcome mapping (Classify for an answer), with the row's own Launch and
// holderName, so per-row facts such as the recorded server process are judged
// per row. The result is Skipped, with no call, when the budget is spent or
// the socket has stopped, and when the row's own call spent the budget.
func (s *Sweep) Lookup(row Launch, holderName string) Result {
	st := s.socket(row.Socket)
	if !s.open(st) {
		return skipped()
	}
	if !st.looked {
		var ans LookupAnswer
		var err error
		if !s.timed(func() { ans, err = s.c.Lookup(row.Socket) }) {
			return skipped()
		}
		st.looked, st.ans, st.lookErr = true, ans, err
	}
	return st.stopOn(resultForCall(st.ans, st.lookErr, s.pc, row, holderName))
}

// PaneLister is the pane-listing capability a sweep's adoption listing needs
// (SR-3.6, SR-3.7): list-panes -a on a socket. *Client and tmuxfix.Recorder
// satisfy it. Only find-missing supplies one; expire never lists panes.
type PaneLister interface {
	ListPanes(socket string) ([]Pane, error)
}

var _ PaneLister = (*Client)(nil)

// PaneListing is a sweep's adoption pane listing for one row's socket.
type PaneListing struct {
	// Listed reports that the listing answered; Panes then holds it, each
	// Pane with its AdPane (pick the row's pane with PaneByToken).
	Listed bool
	// Panes is the listing, in listing order; nil unless Listed. It is the
	// caller's copy.
	Panes []Pane
	// Result is the zero Result when Listed. Otherwise it is Skipped (no call
	// was made, or the row's own call spent the budget), or what the
	// listing's failure means for the row, mapped exactly as a lookup call's
	// failure: Can't tell unreadable (a timeout, an unrecognised or malformed
	// reply, any other error) or tmux unavailable, with Cause set for a
	// *CallError; for a no-server or no-socket reply, Gone or a different
	// server by the row's server check.
	Result Result
}

// ListPanes returns the adoption pane listing of row's socket for this run
// (SR-3.6, SR-3.7, SR-3.15, SR-13.3, SR-13.5), through pl. It makes at most
// one pl.ListPanes call per socket per run, gated like a lookup call: none
// when the budget is spent or the socket has stopped, and then neither a held
// listing nor a held failure is handed out (the result is Skipped). The call
// is timed with now and charged to the same budget as the lookups; a call
// that spends the budget is discarded and its row Skipped. A later request
// for the same socket gets the held listing, or the held failure mapped for
// its own row, never a second call. A failure that is Can't tell unreadable
// or tmux unavailable stops the socket exactly like a lookup's (SR-13.3: at
// most one call timeout per socket), so later rows of that socket are
// Skipped even when its lookup answer is held; a no-server or no-socket reply
// does not.
func (s *Sweep) ListPanes(pl PaneLister, row Launch) PaneListing {
	st := s.socket(row.Socket)
	if !s.open(st) {
		return PaneListing{Result: skipped()}
	}
	if !st.listed {
		var panes []Pane
		var err error
		if !s.timed(func() { panes, err = pl.ListPanes(row.Socket) }) {
			return PaneListing{Result: skipped()}
		}
		st.listed, st.panes, st.listErr = true, panes, err
	}
	if st.listErr == nil {
		return PaneListing{Listed: true, Panes: slices.Clone(st.panes)}
	}
	return PaneListing{Result: st.stopOn(resultForFailure(st.listErr, s.pc, row))}
}

// socket returns what the Sweep holds for socket, creating an empty entry on
// first use.
func (s *Sweep) socket(socket string) *sweepSocket {
	st, ok := s.sockets[socket]
	if !ok {
		st = &sweepSocket{}
		s.sockets[socket] = st
	}
	return st
}

// open reports whether the run may still use st: the budget is not spent and
// the socket has not stopped.
func (s *Sweep) open(st *sweepSocket) bool {
	return s.spent < s.budget && !st.stopped
}

// timed runs one tmux call, charges its time to the budget (floored at zero)
// and reports whether the budget is still not spent after it; false means the
// call's outcome must be discarded.
func (s *Sweep) timed(call func()) bool {
	start := s.now()
	call()
	if d := s.now().Sub(start); d > 0 {
		s.spent += d
	}
	return s.spent < s.budget
}

// stopOn applies the stop rule to r, a result for a row of st's socket, and
// returns r: Can't tell unreadable or tmux unavailable stops the socket.
func (st *sweepSocket) stopOn(r Result) Result {
	if r.Verdict == CantTell && (r.CantTell == CantTellUnreadable || r.CantTell == CantTellUnavailable) {
		st.stopped = true
	}
	return r
}

// skipped is the result of a row the sweep did not call tmux for.
func skipped() Result { return Result{Skipped: true} }
