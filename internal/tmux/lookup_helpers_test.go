package tmux_test

import (
	"cmp"
	"reflect"
	"slices"
	"strconv"
	"testing"

	"github.com/gabemahoney/agent-director/internal/probe"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/procstarttimefix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
)

// Shared fixture of the lookup tests (lookup_test.go, lookup_server_test.go,
// lookup_holder_test.go): a row, a Recorder table labelled for that row, the
// process-checker fake, and a runner that holds Lookup and Classify equal.

// The start-time readers and lookup clients satisfy the lookup's interfaces
// structurally (Appendix F.2); internal/probe never imports internal/tmux.
var (
	_ tmux.ProcChecker  = probe.NewProcChecker()
	_ tmux.ProcChecker  = (*procfix.Checker)(nil)
	_ tmux.LookupClient = (*tmuxfix.Recorder)(nil)
	_ tmux.LookupClient = (*tmux.Client)(nil)
)

// lookupRecorded is the row's recorded server; lookupOther is another server
// for the same socket, whose process start time differs.
var (
	lookupRecorded = tmuxfix.Server{PID: tmuxfix.RecordedCreate.ServerPID, Start: tmuxfix.RecordedCreate.ServerStart,
		ProcStart: procstarttimefix.LinuxProcStarttime}
	lookupOther = tmuxfix.Server{PID: lookupRecorded.PID + 1, Start: lookupRecorded.Start + 1,
		ProcStart: procstarttimefix.DarwinProcStarttime}
)

// lookupRowOpt overrides one field group of the default row.
type lookupRowOpt func(*tmux.Launch)

// newLookupRow is a row on testSocket with this store's id, a token and the
// recorded identity of lookupRecorded; opts override it.
func newLookupRow(opts ...lookupRowOpt) tmux.Launch {
	row := tmux.Launch{InstanceID: "agent-x", Token: tmuxfix.Token, StoreID: tmuxfix.StoreID, Socket: testSocket,
		ServerPID: lookupRecorded.PID, ServerStart: lookupRecorded.Start, ServerStarttime: lookupRecorded.ProcStart}
	for _, o := range opts {
		o(&row)
	}
	return row
}

// The row options: no launch token, no store id, no recorded server identity,
// a pid recorded with no process start time, and another instance id.
func rowNoToken(l *tmux.Launch)            { l.Token = "" }
func rowNoStoreID(l *tmux.Launch)          { l.StoreID = "" }
func rowNoServer(l *tmux.Launch)           { l.ServerPID, l.ServerStart, l.ServerStarttime = 0, 0, "" }
func rowNoStarttime(l *tmux.Launch)        { l.ServerStarttime = "" }
func rowInstanceID(id string) lookupRowOpt { return func(l *tmux.Launch) { l.InstanceID = id } }

// lbl is a seeded session's label relative to the row. A row with no token or
// no store id labels with tmuxfix.Token or tmuxfix.StoreID in their place.
type lbl int

const (
	lblCurrent           lbl = iota + 1 // this store, the row's id and token
	lblOld                              // this store, the row's id, another token
	lblForeign                          // this store, another id, the row's token
	lblOtherStore                       // another store, the row's id and token
	lblOtherStoreOld                    // another store, the row's id, another token
	lblOtherStoreForeign                // another store, another id, the row's token
	lblNone                             // a set @ad_owner value that is no valid label
	lblUnset                            // no @ad_owner value
)

// label is k's typed label for row.
func (k lbl) label(row tmux.Launch) tmux.Label {
	id, token := row.InstanceID, cmp.Or(row.Token, tmuxfix.Token)
	store, other := cmp.Or(row.StoreID, tmuxfix.StoreID), tmuxfix.OtherStoreID
	switch k {
	case lblCurrent:
		return tmuxfix.Valid(token, id, store)
	case lblOld:
		return tmuxfix.Valid(tmuxfix.OtherToken, id, store)
	case lblForeign:
		return tmuxfix.Valid(token, "agent-y", store)
	case lblOtherStore:
		return tmuxfix.Valid(token, id, other)
	case lblOtherStoreOld:
		return tmuxfix.Valid(tmuxfix.OtherToken, id, other)
	case lblOtherStoreForeign:
		return tmuxfix.Valid(token, "agent-y", other)
	}
	return tmux.Label{}
}

// lookupFixture is one lookup case: the Recorder with lookupRecorded bound to
// testSocket, the fake with that server alive, and the row.
type lookupFixture struct {
	t   *testing.T
	Rec *tmuxfix.Recorder
	PC  *procfix.Checker
	Row tmux.Launch
	// Asked holds the pids whose start time the last run's Lookup asked.
	Asked []int
	ids   []string           // seeded session ids, in seeding order
	ans   *tmux.LookupAnswer // the last run's answer; nil after a call failure
}

// newLookupFixture builds a case for newLookupRow(opts...).
func newLookupFixture(t *testing.T, opts ...lookupRowOpt) *lookupFixture {
	f := &lookupFixture{t: t, Rec: tmuxfix.NewRecorder().StartServer(testSocket, lookupRecorded),
		PC: procfix.New(), Row: newLookupRow(opts...)}
	f.PC.Set(lookupRecorded.PID, procfix.Alive(lookupRecorded.ProcStart))
	return f
}

// seed adds one session per label kind to the socket's server, unnamed.
func (f *lookupFixture) seed(kinds ...lbl) *lookupFixture {
	for _, k := range kinds {
		f.seedNamed("", k)
	}
	return f
}

// seedNamed adds one session with stored name and label kind k; take $ and \
// names from tmuxfix.StoredNames.
func (f *lookupFixture) seedNamed(name string, k lbl) *lookupFixture {
	id := "$" + strconv.Itoa(len(f.ids))
	f.ids = append(f.ids, id)
	f.Rec.SeedSessions(testSocket, tmuxfix.SeedSession{ID: id, Name: name, Created: lookupRecorded.Start,
		Label: k.label(f.Row), LabelSet: k == lblNone})
	return f
}

// ID is the session id of the i-th seeded session (for scope values).
func (f *lookupFixture) ID(i int) string { return f.ids[i] }

// Label is k's typed label for the fixture's row (for scope values).
func (f *lookupFixture) Label(k lbl) tmux.Label { return k.label(f.Row) }

// fail scripts every lookup call on the socket to fail with fl.
func (f *lookupFixture) fail(fl tmux.Failure) *lookupFixture {
	f.Rec.Script(testSocket, tmuxfix.Script{Failure: fl}, tmux.CallLookup)
	return f
}

// noServer stops the socket's server so the lookup gets fl (tmux.FailNoServer
// or tmux.FailNoSocket).
func (f *lookupFixture) noServer(fl tmux.Failure) *lookupFixture {
	f.Rec.StopServer(testSocket).SetNoServerFailure(testSocket, fl)
	return f
}

// syncProcs sets the fake from the Recorder's servers: a running one alive
// with its process start time, a stopped one gone.
func (f *lookupFixture) syncProcs() *lookupFixture {
	for _, s := range f.Rec.Servers() {
		if s.Running {
			f.PC.Set(s.PID, procfix.Alive(s.ProcStart))
		} else {
			f.PC.Set(s.PID, procfix.Gone())
		}
	}
	return f
}

// lookupCapture passes Lookup calls through, counting them and keeping the
// last answer for Classify.
type lookupCapture struct {
	c   tmux.LookupClient
	n   int
	ans tmux.LookupAnswer
	err error
}

func (c *lookupCapture) Lookup(socket string) (tmux.LookupAnswer, error) {
	c.n++
	c.ans, c.err = c.c.Lookup(socket)
	return c.ans, c.err
}

// run is runOn(f.Rec, holder).
func (f *lookupFixture) run(holder string) tmux.Result { return f.runOn(f.Rec, holder) }

// runOn runs Lookup on c (one call) and, when it answered, Classify on the
// same answer; both Results and their start-time questions must be equal.
func (f *lookupFixture) runOn(c tmux.LookupClient, holder string) tmux.Result {
	f.t.Helper()
	capt := &lookupCapture{c: c}
	before := len(f.PC.StartTimeCalls())
	got := tmux.Lookup(capt, f.PC, f.Row, holder)
	calls := f.PC.StartTimeCalls()
	f.Asked, f.ans = calls[before:], nil
	if capt.n != 1 {
		f.t.Fatalf("Lookup made %d lookup calls, want 1", capt.n)
	}
	if capt.err != nil {
		return got
	}
	f.ans = &capt.ans
	again := tmux.Classify(capt.ans, f.PC, f.Row, holder)
	if asked := f.PC.StartTimeCalls()[len(calls):]; !slices.Equal(asked, f.Asked) {
		f.t.Errorf("Classify asked start times of %v, Lookup of %v", asked, f.Asked)
	}
	if !reflect.DeepEqual(got, again) {
		f.t.Fatalf("Lookup and Classify disagree:\nLookup   %+v\nClassify %+v", got, again)
	}
	return got
}

// at names seeded sessions by seeding index (seed, seedNamed).
type at []int

// lookupWant is a Result's expected outcome; every zero field expects zero.
type lookupWant struct {
	Verdict         tmux.Verdict
	CantTell        tmux.CantTellKind
	Token           string
	Server          string
	Ours            at // none or one index
	Leftovers       at // in listing order (by stored name, then seeding order)
	Holder          at // none or one index
	HolderClass     tmux.LabelClass
	HolderAmbiguous bool
	Adopt           bool
	Disagree        []string
	Cause           tmux.Failure // Cause's failure; 0 expects a nil Cause
}

// expect runs the case with holder name holder and checks it against w.
func (f *lookupFixture) expect(holder string, w lookupWant) tmux.Result {
	f.t.Helper()
	got := f.run(holder)
	f.check(got, w)
	return got
}

// check asserts got against w; sessions compare whole with the last answer's.
func (f *lookupFixture) check(got tmux.Result, w lookupWant) {
	f.t.Helper()
	if got.Verdict != w.Verdict || got.CantTell != w.CantTell || got.Token() != w.Token {
		f.t.Errorf("verdict %d/%d %q, want %d/%d %q", got.Verdict, got.CantTell, got.Token(), w.Verdict, w.CantTell, w.Token)
	}
	if got.Server != w.Server || got.Adopt != w.Adopt || !slices.Equal(got.Disagree, w.Disagree) {
		f.t.Errorf("server %q adopt %v disagree %q, want %q %v %q", got.Server, got.Adopt, got.Disagree, w.Server, w.Adopt, w.Disagree)
	}
	var cause tmux.Failure
	if got.Cause != nil {
		cause = got.Cause.Failure
	}
	if cause != w.Cause {
		f.t.Errorf("Cause failure %v, want %v", cause, w.Cause)
	}
	if s := f.one(w.Ours); got.Session != s {
		f.t.Errorf("Session %+v, want %+v", got.Session, s)
	}
	if want := f.sessions(w.Leftovers); !slices.Equal(got.Leftovers, want) {
		f.t.Errorf("Leftovers %+v, want %+v", got.Leftovers, want)
	}
	var holder tmux.Session
	if got.Holder != nil {
		holder = *got.Holder
	}
	if s := f.one(w.Holder); holder != s || (got.Holder == nil) != (len(w.Holder) == 0) {
		f.t.Errorf("Holder %+v, want %+v", got.Holder, s)
	}
	if got.HolderClass != w.HolderClass || got.HolderAmbiguous != w.HolderAmbiguous {
		f.t.Errorf("holder class %d ambiguous %v, want %d %v", got.HolderClass, got.HolderAmbiguous, w.HolderClass, w.HolderAmbiguous)
	}
}

// one is the session named by idx, or the zero Session for none.
func (f *lookupFixture) one(idx at) tmux.Session {
	f.t.Helper()
	s := f.sessions(idx)
	if len(s) > 1 {
		f.t.Fatalf("want names %d sessions where one is allowed", len(s))
	}
	if len(s) == 0 {
		return tmux.Session{}
	}
	return s[0]
}

// sessions are the last answer's sessions seeded at idx.
func (f *lookupFixture) sessions(idx at) []tmux.Session {
	f.t.Helper()
	var out []tmux.Session
	for _, i := range idx {
		if i < 0 || i >= len(f.ids) || f.ans == nil {
			f.t.Fatalf("session %d: not seeded, or no answer was read", i)
		}
		j := slices.IndexFunc(f.ans.Sessions, func(s tmux.Session) bool { return s.ID == f.ids[i] })
		if j < 0 {
			f.t.Fatalf("session %d (%s) is not on the last answer", i, f.ids[i])
		}
		out = append(out, f.ans.Sessions[j])
	}
	return out
}
