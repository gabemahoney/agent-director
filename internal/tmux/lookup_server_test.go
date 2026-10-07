package tmux_test

import (
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
)

// The clock-free server check of SR-3.3 with the u8 review amendments: match,
// restarted, different server and no identity, over every reply shape; an
// empty listing names its server too (b.47f).

// replyShape is what answers the row's socket in a server-check case.
type replyShape struct {
	name  string
	setup func(f *lookupFixture)
	// answered: lookupOther answered, with or without session lines (its
	// identity line names it either way; b.47f).
	answered bool
	// gone is the outcome when the recorded server process reads gone.
	gone lookupWant
}

// answeredBy has lookupOther take the socket, holding one session per kind.
func answeredBy(kinds ...lbl) func(*lookupFixture) {
	return func(f *lookupFixture) {
		f.Rec.RebindServer(testSocket, lookupOther)
		f.seed(kinds...)
	}
}

// noReply has the socket give fl, tmux.FailNoServer or tmux.FailNoSocket.
func noReply(fl tmux.Failure) func(*lookupFixture) {
	return func(f *lookupFixture) { f.noServer(fl) }
}

// replyShapes are the SR-3.3 reply shapes where the recorded identity is not
// matched: a listing, empty or not, with another identity logs server_restarted
// once the recorded process is gone (b.47f); a no-server or no-socket Gone
// logs no reason.
var replyShapes = []replyShape{
	{name: "listing with the current label", setup: answeredBy(lblCurrent), answered: true,
		gone: lookupWant{Verdict: tmux.Ours, Token: "ours", Server: "restarted", Ours: at{0}, Disagree: []string{"server_restarted"}}},
	{name: "listing with a foreign label", setup: answeredBy(lblForeign), answered: true,
		gone: lookupWant{Verdict: tmux.Gone, Token: "gone", Server: "restarted", Disagree: []string{"server_restarted"}}},
	{name: "zero-session listing", setup: answeredBy(), answered: true,
		gone: lookupWant{Verdict: tmux.Gone, Token: "gone", Server: "restarted", Disagree: []string{"server_restarted"}}},
	{name: "no-server reply", setup: noReply(tmux.FailNoServer),
		gone: lookupWant{Verdict: tmux.Gone, Token: "gone", Server: "restarted"}},
	{name: "no-socket reply", setup: noReply(tmux.FailNoSocket),
		gone: lookupWant{Verdict: tmux.Gone, Token: "gone", Server: "restarted"}},
}

// differentServer is the outcome of every different-server case.
var differentServer = lookupWant{Verdict: tmux.CantTell, CantTell: tmux.CantTellDifferentServer,
	Token: "different_server", Server: "differs", Disagree: []string{"server_mismatch"}}

// recordedProcs are the recorded server process's states, and whether each
// reads gone with and without a recorded start time (u8 amendment).
var recordedProcs = []struct {
	name                       string
	proc                       procfix.Process
	goneWithStart, goneNoStart bool
}{
	{"absent", procfix.Gone(), true, true},
	{"zombie", procfix.Zombie(), true, true},
	// LFR C1: a server agent-director started carries no AGENT_DIRECTOR_*.
	{"alive with its recorded start and no AGENT_DIRECTOR_ environment",
		procfix.Alive(lookupRecorded.ProcStart).WithEnv(map[string]string{"PATH": "/usr/bin"}), false, false},
	{"alive with another start", procfix.Alive(lookupOther.ProcStart), true, false},
	{"unreadable", procfix.Unreadable(), false, false},
}

// runSteady runs f's lookup, then again after stepping the Recorder's clock
// 2 s past and 2 s before the recorded server start; every run must agree.
func runSteady(t *testing.T, f *lookupFixture) tmux.Result {
	t.Helper()
	want, asked := f.run(""), f.Asked
	start := time.Unix(lookupRecorded.Start, 0)
	clock := tmuxfix.NewClock(start)
	f.Rec.WithVirtualTime(clock, testTimeouts)
	for _, step := range []time.Duration{2 * time.Second, -2 * time.Second} {
		clock.Advance(start.Add(step).Sub(clock.Now()))
		if got := f.run(""); !reflect.DeepEqual(got, want) || !slices.Equal(f.Asked, asked) {
			t.Errorf("clock at start%+v: %+v asked %v, want %+v asked %v", step, got, f.Asked, want, asked)
		}
	}
	if n := f.PC.EnvReads(); n != 0 {
		t.Errorf("%d environment reads, want 0", n)
	}
	f.Asked = asked
	return want
}

// checkAnswering asserts the Result carries server s's identity (zero: none).
func checkAnswering(t *testing.T, got tmux.Result, s tmuxfix.Server) {
	t.Helper()
	if got.ServerPID != s.PID || got.ServerStart != s.Start {
		t.Errorf("answering server %d/%d, want %d/%d", got.ServerPID, got.ServerStart, s.PID, s.Start)
	}
}

// A recorded identity the answer does not match is judged by one start-time
// read of the recorded pid: gone is restarted, anything else a different server.
func TestLookupServer_RecordedIdentityNotMatched(t *testing.T) {
	rows := []struct {
		name string
		opts []lookupRowOpt
	}{{"start time recorded", nil}, {"no start time recorded", []lookupRowOpt{rowNoStarttime}}}
	for _, shape := range replyShapes {
		for _, proc := range recordedProcs {
			for _, row := range rows {
				t.Run(shape.name+"/"+proc.name+"/"+row.name, func(t *testing.T) {
					f := newLookupFixture(t, row.opts...)
					shape.setup(f)
					f.PC.Set(lookupRecorded.PID, proc.proc)
					got := runSteady(t, f)
					gone := proc.goneWithStart
					if row.opts != nil {
						gone = proc.goneNoStart
					}
					want := differentServer
					if gone {
						want = shape.gone
					}
					f.check(got, want)
					if !slices.Equal(f.Asked, []int{lookupRecorded.PID}) {
						t.Errorf("start times asked of %v, want once of the recorded pid %d", f.Asked, lookupRecorded.PID)
					}
					var answering tmuxfix.Server
					if shape.answered {
						answering = lookupOther
					}
					checkAnswering(t, got, answering)
				})
			}
		}
	}
}

// A matched identity (an empty listing of the recorded server included;
// b.47f), or none recorded, is decided without the start-time reader; with
// none recorded whichever server answers counts (Adopt on Ours).
func TestLookupServer_NoReaderCall(t *testing.T) {
	onRecorded := func(k lbl) func(*lookupFixture) { return func(f *lookupFixture) { f.seed(k) } }
	tests := []struct {
		name      string
		opts      []lookupRowOpt
		setup     func(*lookupFixture)
		want      lookupWant
		answering tmuxfix.Server
	}{
		{"match, current label", nil, onRecorded(lblCurrent),
			lookupWant{Verdict: tmux.Ours, Token: "ours", Server: "match", Ours: at{0}}, lookupRecorded},
		{"match, old label", nil, onRecorded(lblOld),
			lookupWant{Verdict: tmux.Leftover, Token: "leftover", Server: "match", Leftovers: at{0}}, lookupRecorded},
		{"match, foreign label", nil, onRecorded(lblForeign),
			lookupWant{Verdict: tmux.Gone, Token: "gone", Server: "match"}, lookupRecorded},
		{"match, zero-session listing", nil, func(*lookupFixture) {},
			lookupWant{Verdict: tmux.Gone, Token: "gone", Server: "match"}, lookupRecorded},
		{"no identity, listing with the current label", []lookupRowOpt{rowNoServer}, answeredBy(lblCurrent),
			lookupWant{Verdict: tmux.Ours, Token: "ours", Server: "unknown", Ours: at{0}, Adopt: true}, lookupOther},
		{"no identity, zero-session listing", []lookupRowOpt{rowNoServer}, answeredBy(),
			lookupWant{Verdict: tmux.Gone, Token: "gone", Server: "unknown"}, lookupOther},
		{"no identity, no-server reply", []lookupRowOpt{rowNoServer}, noReply(tmux.FailNoServer),
			lookupWant{Verdict: tmux.Gone, Token: "gone", Server: "unknown"}, tmuxfix.Server{}},
		{"no identity, no-socket reply", []lookupRowOpt{rowNoServer}, noReply(tmux.FailNoSocket),
			lookupWant{Verdict: tmux.Gone, Token: "gone", Server: "unknown"}, tmuxfix.Server{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newLookupFixture(t, tc.opts...)
			tc.setup(f)
			// Were the reader asked, it would say the recorded server is gone.
			f.PC.Set(lookupRecorded.PID, procfix.Gone())
			got := runSteady(t, f)
			f.check(got, tc.want)
			if calls := f.PC.StartTimeCalls(); len(calls) != 0 {
				t.Errorf("start times asked of %v, want none", calls)
			}
			checkAnswering(t, got, tc.answering)
		})
	}
}
