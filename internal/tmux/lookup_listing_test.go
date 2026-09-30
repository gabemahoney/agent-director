package tmux_test

import (
	"errors"
	"reflect"
	"slices"
	"testing"

	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
)

// A single-row verb's failed pane listing (tmux.ListingFailure) against the
// lookup's failure mapping for the same typed failure (SR-2.5, SR-3.3, SR-3.7).

// scriptedBoth has the lookup and the pane listing on the socket fail with s.
func scriptedBoth(s tmuxfix.Script) func(*lookupFixture) {
	return func(f *lookupFixture) { f.Rec.Script(testSocket, s, tmux.CallLookup, tmux.CallListPanes) }
}

// stoppedWith stops the socket's server (every call gets fl) and sets the
// recorded server process to proc.
func stoppedWith(fl tmux.Failure, proc procfix.Process) func(*lookupFixture) {
	return func(f *lookupFixture) {
		f.noServer(fl)
		f.PC.Set(lookupRecorded.PID, proc)
	}
}

// TestListingFailureMatchesLookup: ListingFailure's Result equals the lookup's
// for the same failure, the listing's call in Cause, with no tmux call and the
// same start-time reads.
func TestListingFailureMatchesLookup(t *testing.T) {
	restarted := wantGone(tmux.ServerRestarted)
	cases := []struct {
		name  string
		setup func(*lookupFixture)
		plain bool // the failure is an error that is not a *tmux.CallError
		want  lookupWant
	}{
		{name: "timeout", setup: scriptedBoth(tmuxfix.Script{Failure: tmux.FailTimeout}),
			want: wantUnreadable(tmux.FailTimeout)},
		{name: "unrecognised reply",
			setup: scriptedBoth(tmuxfix.Script{Failure: tmux.FailUnrecognized, FirstLine: "unknown command", ExitStatus: 1}),
			want:  wantUnreadable(tmux.FailUnrecognized)},
		{name: "malformed reply",
			setup: scriptedBoth(tmuxfix.Script{Failure: tmux.FailUnrecognized, FirstLine: "%1\tnot-a-pid", HadStdout: true}),
			want:  wantUnreadable(tmux.FailUnrecognized)},
		{name: "error that is not a CallError", setup: func(*lookupFixture) {}, plain: true, want: wantUnreadable(0)},
		{name: "missing binary", setup: scriptedBoth(tmuxfix.Script{Failure: tmux.FailUnavailable}),
			want: wantUnavailable(tmux.FailUnavailable)},
		{name: "socket permission", setup: scriptedBoth(tmuxfix.Script{Failure: tmux.FailSocketDenied}),
			want: wantUnavailable(tmux.FailSocketDenied)},
		{name: "no server, recorded server gone", setup: stoppedWith(tmux.FailNoServer, procfix.Gone()), want: restarted},
		{name: "no server, recorded server alive",
			setup: stoppedWith(tmux.FailNoServer, procfix.Alive(lookupRecorded.ProcStart)), want: differentServer},
		{name: "no server, recorded server unreadable",
			setup: stoppedWith(tmux.FailNoServer, procfix.Unreadable()), want: differentServer},
		{name: "no socket, recorded server gone", setup: stoppedWith(tmux.FailNoSocket, procfix.Gone()), want: restarted},
		{name: "no socket, recorded server alive",
			setup: stoppedWith(tmux.FailNoSocket, procfix.Alive(lookupRecorded.ProcStart)), want: differentServer},
		{name: "no socket, recorded server unreadable",
			setup: stoppedWith(tmux.FailNoSocket, procfix.Unreadable()), want: differentServer},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newLookupFixture(t)
			tc.setup(f)
			var client tmux.LookupClient = f.Rec
			if tc.plain {
				client = errLookup{}
			}
			look := f.runOn(client, "")
			lookAsked := f.Asked
			var err error = errors.New("broken")
			if !tc.plain {
				if _, err = f.Rec.ListPanes(testSocket); err == nil {
					t.Fatal("pane listing succeeded, want it to fail")
				}
			}

			calls, before := len(f.Rec.SocketCalls()), len(f.PC.StartTimeCalls())
			got := tmux.ListingFailure(err, f.PC, f.Row)
			if n := len(f.Rec.SocketCalls()) - calls; n != 0 {
				t.Errorf("ListingFailure made %d tmux calls, want none", n)
			}
			if asked := f.PC.StartTimeCalls()[before:]; !slices.Equal(asked, lookAsked) {
				t.Errorf("ListingFailure asked start times of %v, the lookup of %v", asked, lookAsked)
			}
			if n := f.PC.EnvReads(); n != 0 {
				t.Errorf("%d environment reads, want none", n)
			}

			want := look
			if look.Cause != nil {
				c := *look.Cause
				c.Call = tmux.CallListPanes
				want.Cause = &c
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("listing result\n got %+v\nwant the lookup's %+v", got, want)
			}
			f.check(got, tc.want)
		})
	}
}
