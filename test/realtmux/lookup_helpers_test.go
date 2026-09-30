package realtmux_test

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/probe"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
)

// The shared lookup fixture (SRD SR-3.3, SR-3.4, SR-3.10, SR-20.4, SR-20.7):
// agents made by the production create, rows (tmux.Launch) whose recorded
// server identity comes from the create reply and the production start-time
// reader (as SR-3.6's identity write takes it), one tmux.Lookup through the
// production client, a socket re-bind, a server end, and a Result assertion.

// lookupFix is one test's private tmux world plus the production client and
// the production start-time reader (probe.NewProcChecker) as the lookup's
// ProcChecker.
type lookupFix struct {
	*realTmux
	Client *tmux.Client
	PC     tmux.ProcChecker
}

// newLookupFix makes a fresh private world (newRealTmux) bound to its default
// socket, with newClient and probe.NewProcChecker.
func newLookupFix(t testing.TB) *lookupFix {
	t.Helper()
	return &lookupFix{realTmux: newRealTmux(t), Client: newClient(), PC: probe.NewProcChecker()}
}

// serverIdentity is a recorded tmux server identity: #{pid}, #{start_time}
// and the server process's start time from the production reader.
type serverIdentity struct {
	PID       int
	Start     int64
	Starttime string
}

// agent is a labelled session made by the production create: the filled-in
// spec and reply, the socket it was made on, its pane process's start time
// and its server's identity, all read from real tmux and the reader.
type agent struct {
	created
	Socket    string
	PaneStart string
	Server    serverIdentity
}

// agent creates an agent on the fixture's socket; zero spec fields take
// createSpec's defaults (tmuxfix.OtherStoreID in StoreID for another store).
func (f *lookupFix) agent(t testing.TB, spec createSpec) agent {
	t.Helper()
	return f.agentOn(t, f.realTmux, spec)
}

// agentOn is agent on another socket of the same world (rt.fresh, rt.at).
func (f *lookupFix) agentOn(t testing.TB, rt *realTmux, spec createSpec) agent {
	t.Helper()
	if spec.Client == nil {
		spec.Client = f.Client
	}
	c := rt.mustCreate(t, spec)
	return agent{
		created:   c,
		Socket:    rt.Socket,
		PaneStart: f.startOf(t, c.Reply.PanePID),
		Server:    serverIdentity{PID: c.Reply.ServerPID, Start: c.Reply.ServerStart, Starttime: f.startOf(t, c.Reply.ServerPID)},
	}
}

// serverOf reads the identity of the server holding sessionID on rt (for a
// server started by the raw starters): #{pid} and #{start_time}, plus the
// reader's start time.
func (f *lookupFix) serverOf(t testing.TB, rt *realTmux, sessionID string) serverIdentity {
	t.Helper()
	pid := rt.formatInt(t, sessionID, "#{pid}")
	start := rt.formatInt(t, sessionID, "#{start_time}")
	return serverIdentity{PID: pid, Start: int64(start), Starttime: f.startOf(t, pid)}
}

// startOf reads pid's start time through the production reader and fails
// the test unless the process is alive.
func (f *lookupFix) startOf(t testing.TB, pid int) string {
	t.Helper()
	start, alive, known := f.PC.StartTime(pid)
	if !known || !alive {
		t.Fatalf("start-time reader for pid %d: alive %v, known %v; want a running process", pid, alive, known)
	}
	return start
}

// rowOption overrides one part of a row built by agent.row or rowFor.
type rowOption func(*tmux.Launch)

// withoutIdentity records no server identity (the row adopts, SR-3.3).
func withoutIdentity() rowOption {
	return func(l *tmux.Launch) { l.ServerPID, l.ServerStart, l.ServerStarttime = 0, 0, "" }
}

// withServer records srv as the row's server identity.
func withServer(srv serverIdentity) rowOption {
	return func(l *tmux.Launch) {
		l.ServerPID, l.ServerStart, l.ServerStarttime = srv.PID, srv.Start, srv.Starttime
	}
}

// withInstanceID, withToken ("" is no launch token) and withStoreID replace
// the row's instance id, launch token and this store's id.
func withInstanceID(id string) rowOption { return func(l *tmux.Launch) { l.InstanceID = id } }
func withToken(token string) rowOption   { return func(l *tmux.Launch) { l.Token = token } }
func withStoreID(id string) rowOption    { return func(l *tmux.Launch) { l.StoreID = id } }

// withSocket points the row at another socket path (e.g. a re-bound one).
func withSocket(socket string) rowOption { return func(l *tmux.Launch) { l.Socket = socket } }

// rowFor builds a row on socket with srv recorded and this store's id
// tmuxfix.StoreID, then applies opts in order.
func rowFor(instanceID, token, socket string, srv serverIdentity, opts ...rowOption) tmux.Launch {
	l := tmux.Launch{InstanceID: instanceID, Token: token, StoreID: tmuxfix.StoreID, Socket: socket}
	withServer(srv)(&l)
	for _, o := range opts {
		o(&l)
	}
	return l
}

// row is the agent's row: its id, token, socket and server identity, and
// this store's id tmuxfix.StoreID whatever store the agent was created for.
func (a agent) row(opts ...rowOption) tmux.Launch {
	return rowFor(a.InstanceID, a.Token, a.Socket, a.Server, opts...)
}

// pane is the agent's recorded pane identity, for tmux.JudgeProcess.
func (a agent) pane() tmux.ProcIdentity {
	return tmux.ProcIdentity{PID: a.Reply.PanePID, Starttime: a.PaneStart}
}

// lookup runs the one production lookup for row on row.Socket; holder is the
// name whose holder to report ("" for none).
func (f *lookupFix) lookup(row tmux.Launch, holder string) tmux.Result {
	return tmux.Lookup(f.Client, f.PC, row, holder)
}

// judge judges a recorded process with the production reader.
func (f *lookupFix) judge(id tmux.ProcIdentity) tmux.ProcState {
	return tmux.JudgeProcess(f.PC, id)
}

// rebind moves rt's running server's socket aside (a new path under UserDir,
// still reachable and cleaned up) and starts a new server at rt's path with
// an agent from spec; it returns the world bound to the moved socket and the
// new agent.
func (f *lookupFix) rebind(t testing.TB, rt *realTmux, spec createSpec) (*realTmux, agent) {
	t.Helper()
	aside := rt.fresh(t)
	if err := os.Rename(rt.Socket, aside.Socket); err != nil {
		t.Fatalf("move socket %s aside: %v", rt.Socket, err)
	}
	rt.tr.addSocket(aside.Socket)
	return aside, f.agentOn(t, rt, spec)
}

// endServer sends kill-server on rt and waits until srv's process reads gone
// through the production reader (a zombie or a reused pid counts as gone).
func (f *lookupFix) endServer(t testing.TB, rt *realTmux, srv serverIdentity) {
	t.Helper()
	rt.must(t, "kill-server")
	id := tmux.ProcIdentity{PID: srv.PID, Starttime: srv.Starttime}
	waitFor(t, fmt.Sprintf("tmux server pid %d reads gone", srv.PID),
		func() bool { return f.judge(id) == tmux.ProcGone },
		func() string { return procState(srv.PID) })
}

// wantResult is what assertResult compares. Every field is checked: "" or
// nil means none (no Ours session, no holder, no leftovers, no reasons).
type wantResult struct {
	Verdict     tmux.Verdict
	CantTell    tmux.CantTellKind
	Token       string   // Result.Token(), e.g. "ours", "different_server"
	Session     string   // the Ours session's id
	Leftovers   []string // leftover session ids, in listing order
	Holder      string   // the holder's session id
	HolderClass tmux.LabelClass
	Server      string // tmux.ServerMatch, ServerRestarted, ServerDiffers, ServerUnknown or ""
	Disagree    []string
}

// verdictName names a verdict in failure messages.
func verdictName(v tmux.Verdict) string {
	names := map[tmux.Verdict]string{tmux.Ours: "Ours", tmux.Leftover: "Leftover", tmux.Gone: "Gone", tmux.CantTell: "CantTell"}
	if n, ok := names[v]; ok {
		return n
	}
	return fmt.Sprintf("Verdict(%d)", v)
}

// assertResult reports (t.Errorf) every field of got that differs from want,
// labelled by what; label values are never printed.
func assertResult(t testing.TB, what string, got tmux.Result, want wantResult) {
	t.Helper()
	var diffs []string
	diff := func(field string, g, w any) {
		if fmt.Sprint(g) != fmt.Sprint(w) {
			diffs = append(diffs, fmt.Sprintf("%s = %v, want %v", field, g, w))
		}
	}
	diff("Verdict", verdictName(got.Verdict), verdictName(want.Verdict))
	diff("CantTell", int(got.CantTell), int(want.CantTell))
	diff("Token()", fmt.Sprintf("%q", got.Token()), fmt.Sprintf("%q", want.Token))
	diff("Session.ID", fmt.Sprintf("%q", got.Session.ID), fmt.Sprintf("%q", want.Session))
	diff("Leftovers", sessionIDs(tmux.LookupAnswer{Sessions: got.Leftovers}), want.Leftovers)
	holder := ""
	if got.Holder != nil {
		holder = got.Holder.ID
	}
	diff("Holder", fmt.Sprintf("%q", holder), fmt.Sprintf("%q", want.Holder))
	diff("HolderClass", int(got.HolderClass), int(want.HolderClass))
	diff("Server", fmt.Sprintf("%q", got.Server), fmt.Sprintf("%q", want.Server))
	if !slices.Equal(got.Disagree, want.Disagree) {
		diffs = append(diffs, fmt.Sprintf("Disagree = %v, want %v", got.Disagree, want.Disagree))
	}
	if len(diffs) > 0 {
		if got.Cause != nil {
			diffs = append(diffs, "(call failure: "+describe(got.Cause)+")")
		}
		t.Errorf("%s: lookup result differs:\n  %s", what, strings.Join(diffs, "\n  "))
	}
}
