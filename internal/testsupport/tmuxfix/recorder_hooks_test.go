package tmuxfix_test

import (
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
)

// start is the shared test clock's starting instant.
var start = time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)

// TestRecorder_SessionHooks: a replace or remove hook fires once after its
// call kind on its socket, between that call and the caller's next action.
func TestRecorder_SessionHooks(t *testing.T) {
	cur := tmuxfix.Valid(tmuxfix.Token, agent, tmuxfix.StoreID)
	type left struct {
		id, name, pane string
		label          tmux.Label
	}
	again := left{"$0", "again", "%7", tmux.Label{}}
	cases := []struct {
		name     string
		arrange  func(r *tmuxfix.Recorder)
		wantGone bool // the pane kill after the lookup finds nothing
		wantLeft []left
	}{
		{"replace", func(r *tmuxfix.Recorder) { r.ReplaceSessionAfter(tmux.CallLookup, sockA, "$0", cur) },
			true, []left{again, {"$1", "s0", "%1", cur}}},
		{"replace-any-socket", func(r *tmuxfix.Recorder) {
			r.ReplaceSessionAfter(tmux.CallLookup, tmuxfix.AnySocket, "$0", tmux.Label{})
		},
			true, []left{again, {"$1", "s0", "%1", tmux.Label{}}}},
		{"remove", func(r *tmuxfix.Recorder) { r.RemoveSessionAfter(tmux.CallLookup, sockA, "$0") }, true, []left{again}},
		{"remove-after-failed-lookup", func(r *tmuxfix.Recorder) {
			r.Script(sockA, tmuxfix.Script{Failure: tmux.FailTimeout}, tmux.CallLookup).RemoveSessionAfter(tmux.CallLookup, sockA, "$0")
		}, true, []left{again}},
		{"other-socket", func(r *tmuxfix.Recorder) { r.RemoveSessionAfter(tmux.CallLookup, sockB, "$0") }, false, []left{again}},
		{"other-call-kind", func(r *tmuxfix.Recorder) { r.RemoveSessionAfter(tmux.CallListPanes, sockA, "$0") }, false, []left{again}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := seeded()
			tc.arrange(r)
			if a, err := r.Lookup(sockA); err == nil && a.Sessions[0].ID != "$0" {
				t.Fatalf("the lookup saw %+v; the hook must apply after it returns", a.Sessions)
			}
			err := r.KillPane(sockA, "%0")
			if tc.wantGone != (err != nil) {
				t.Fatalf("pane kill after the lookup: %v, want gone %v", err, tc.wantGone)
			}
			// A new $0 must survive a second lookup: the hook fired once.
			r.SeedSessions(sockA, sess("$0", "again", tmux.Label{}, false, "%7"))
			_, _ = r.Lookup(sockA)
			var got []left
			for _, s := range r.Sessions(sockA) {
				got = append(got, left{s.ID, s.Name, s.Panes[0].ID, s.Label})
			}
			if !reflect.DeepEqual(got, tc.wantLeft) {
				t.Errorf("sessions = %+v, want %+v", got, tc.wantLeft)
			}
		})
	}
}

// TestRecorder_AfterCall: the hook runs once per call of its kind, with the
// call and its error, before the caller continues, and may re-enter the Recorder.
func TestRecorder_AfterCall(t *testing.T) {
	r := seeded().Script(sockA, tmuxfix.Script{Failure: tmux.FailTimeout, Times: 1}, tmux.CallLookup)
	var events []string
	var failures []tmux.Failure
	r.AfterCall(tmux.CallLookup, func(c tmuxfix.SocketCall, err error) {
		events = append(events, "hook "+c.Socket)
		var f tmux.Failure
		if err != nil {
			f = callErr(t, err).Failure
		}
		failures = append(failures, f)
		_ = r.KillSessionID(sockA, "$0") // re-enters; its effect is visible to the caller
	})
	_, _ = r.Lookup(sockA)
	events = append(events, "returned")
	if left := r.Sessions(sockA); len(left) != 0 {
		t.Errorf("the hook's kill was not visible on return: %+v", left)
	}
	_, _ = r.ListPanes(sockA)
	events = append(events, "returned")
	_, _ = r.Lookup(sockA)
	events = append(events, "returned")

	want := []string{"hook " + sockA, "returned", "returned", "hook " + sockA, "returned"}
	if !reflect.DeepEqual(events, want) {
		t.Errorf("events = %q, want %q", events, want)
	}
	if !reflect.DeepEqual(failures, []tmux.Failure{tmux.FailTimeout, 0}) {
		t.Errorf("hook errors = %v, want the timeout then success", failures)
	}
}

// TestRecorder_VirtualTime: every call, failed or not, advances the clock by
// its class timeout from the given Timeouts, config defaults for zero fields;
// a scripted timeout carries the same value. WaitDelay is never charged.
func TestRecorder_VirtualTime(t *testing.T) {
	def := defaultTimeouts()
	given := tmux.Timeouts{Query: 300 * time.Millisecond, Action: 700 * time.Millisecond, Create: 1100 * time.Millisecond, WaitDelay: time.Hour}
	partial := tmux.Timeouts{Action: 700 * time.Millisecond, WaitDelay: time.Hour}
	modes := []struct {
		name      string
		set, want tmux.Timeouts
	}{
		{"given", given, tmux.Timeouts{Query: given.Query, Action: given.Action, Create: given.Create}},
		{"zero-fields-default", tmux.Timeouts{}, def},
		{"partial", partial, tmux.Timeouts{Query: def.Query, Action: partial.Action, Create: def.Create}},
	}
	for _, m := range modes {
		for _, inv := range invokers {
			for _, variant := range []string{"table", "no-server", "scripted-timeout"} {
				t.Run(m.name+"/"+string(inv.call)+"/"+variant, func(t *testing.T) {
					clock := tmuxfix.NewClock(start)
					r := seeded().WithVirtualTime(clock, m.set)
					sock := sockA
					switch variant {
					case "no-server":
						sock = sockB
					case "scripted-timeout":
						r.Script(sockA, tmuxfix.Script{Failure: tmux.FailTimeout}, inv.call)
					}
					err := inv.do(r, sock)
					var want time.Duration
					for _, c := range r.SocketCalls() {
						want += classTimeout(m.want, c.Call)
					}
					if got := clock.Now().Sub(start); got != want || want == 0 {
						t.Errorf("clock advanced %v, want %v", got, want)
					}
					if variant == "scripted-timeout" {
						if d := callErr(t, err).Timeout; d != classTimeout(m.want, inv.call) {
							t.Errorf("CallError.Timeout = %v, want %v", d, classTimeout(m.want, inv.call))
						}
					}
				})
			}
		}
	}
}

// TestRecorder_VirtualTimeOnlyWhenBound: without virtual time the clock stays
// put; with it, name-based calls are not charged and seeds take the clock's second.
func TestRecorder_VirtualTimeOnlyWhenBound(t *testing.T) {
	unbound := tmuxfix.NewClock(start)
	r := seeded()
	for _, inv := range invokers {
		_ = inv.do(r, sockA)
	}
	if got := unbound.Now(); !got.Equal(start) {
		t.Errorf("unbound clock moved to %v", got)
	}

	bound := tmuxfix.NewClock(start)
	r = tmuxfix.NewRecorder().WithVirtualTime(bound, tmux.Timeouts{})
	_, _ = r.HasSession("n")
	_ = r.SendKeys("n", "x", true)
	_, _ = r.CapturePane("n", 1, false)
	if got := bound.Now(); !got.Equal(start) {
		t.Errorf("name-based calls advanced the clock to %v", got)
	}
	bound.Advance(90 * time.Second)
	r.SeedSessions(sockA, tmuxfix.SeedSession{Name: "s"})
	srv, _ := r.Server(sockA)
	if s := r.Sessions(sockA)[0]; s.Created != start.Add(90*time.Second).Unix() || srv.Start != s.Created {
		t.Errorf("seeded created %d, server start %d; want the clock's second", s.Created, srv.Start)
	}
}

// TestRecorder_ConcurrentCallsShareClock: concurrent calls each charge the
// shared clock once while it is read and moved back (run with -race).
func TestRecorder_ConcurrentCallsShareClock(t *testing.T) {
	const workers, calls = 8, 25
	clock := tmuxfix.NewClock(start)
	r := seeded().WithVirtualTime(clock, tmux.Timeouts{})
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := 0; j < calls; j++ {
				_, _ = r.Lookup(sockA)
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < calls; j++ {
				_ = clock.Now()
				clock.Advance(time.Second)
				clock.Advance(-time.Second)
			}
		}()
	}
	wg.Wait()
	want := start.Add(workers * calls * defaultTimeouts().Query)
	if got := clock.Now(); !got.Equal(want) || len(r.SocketCalls()) != workers*calls {
		t.Errorf("clock %v after %d calls, want %v", got, len(r.SocketCalls()), want)
	}
}
