package spawn

// The launch owner (b.kdf, b.146 rules 11 and 14): CurrentLaunchOwner's
// reading of this process, LaunchOwnerAlive's verdict, plain spawn's record
// of its owner at the insert and the end of its hold, and ReleaseLaunchOwner's
// bounded retry. The launchEnv fixture is in launch_test.go.

import (
	"bytes"
	"database/sql"
	"errors"
	"log"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/probe"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/procstarttimefix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/writefailfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
)

// ownStart is the start time the fake reads for this process.
const ownStart = procstarttimefix.LinuxProcStarttime

// selfNS is this process's pid namespace, skipping t where it cannot be read.
func selfNS(t *testing.T) string {
	t.Helper()
	ns, known := probe.SelfPIDNamespace()
	if !known {
		t.Skip("this process's pid namespace cannot be read here")
	}
	return ns
}

// TestCurrentLaunchOwner: this process is the owner, with its start time and
// pid namespace, when its start time reads; otherwise no owner. It reads one
// start time, this process's.
func TestCurrentLaunchOwner(t *testing.T) {
	ns := selfNS(t)
	cases := []struct {
		name string
		self procfix.Process
		want store.LaunchOwner
	}{
		{"start time read", procfix.Alive(ownStart), store.LaunchOwner{PID: os.Getpid(), Starttime: ownStart, PIDNamespace: ns}},
		{"start time unreadable", procfix.Unreadable(), store.LaunchOwner{}},
		{"read as gone", procfix.Gone(), store.LaunchOwner{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pc := procfix.New()
			pc.Set(os.Getpid(), tc.self)
			if got := CurrentLaunchOwner(pc); got != tc.want {
				t.Errorf("CurrentLaunchOwner = %+v; want %+v", got, tc.want)
			}
			if calls := pc.StartTimeCalls(); !slices.Equal(calls, []int{os.Getpid()}) {
				t.Errorf("start-time reads = %v; want one of pid %d", calls, os.Getpid())
			}
		})
	}
}

// TestLaunchOwnerAlive: an owner is alive only when recorded, in this
// process's pid namespace, and read alive with its recorded start time; no
// owner or another namespace reads no start time.
func TestLaunchOwnerAlive(t *testing.T) {
	ns := selfNS(t)
	const pid = 4500
	owner := store.LaunchOwner{PID: pid, Starttime: ownStart, PIDNamespace: ns}
	cases := []struct {
		name  string
		owner store.LaunchOwner
		proc  procfix.Process
		want  bool
		reads []int
	}{
		{"alive with its start time", owner, procfix.Alive(ownStart), true, []int{pid}},
		{"pid reused, another start time", owner, procfix.Alive(procstarttimefix.DarwinProcStarttime), false, []int{pid}},
		{"gone", owner, procfix.Gone(), false, []int{pid}},
		{"zombie", owner, procfix.Zombie(), false, []int{pid}},
		{"unreadable", owner, procfix.Unreadable(), false, []int{pid}},
		{"another pid namespace", store.LaunchOwner{PID: pid, Starttime: ownStart, PIDNamespace: ns + "-other"},
			procfix.Alive(ownStart), false, nil},
		{"no owner recorded", store.LaunchOwner{}, procfix.Alive(ownStart), false, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pc := procfix.New()
			pc.Set(pid, tc.proc)
			if got := LaunchOwnerAlive(pc, tc.owner); got != tc.want {
				t.Errorf("LaunchOwnerAlive(%+v) = %v; want %v", tc.owner, got, tc.want)
			}
			if calls := pc.StartTimeCalls(); !slices.Equal(calls, tc.reads) {
				t.Errorf("start-time reads = %v; want %v", calls, tc.reads)
			}
		})
	}
}

// failIdentityWrite makes the identity write of e's row fail in the store
// (writefailfix.LaunchIdentityWrite), which no other write of the launch
// matches.
func failIdentityWrite(e *launchEnv) {
	raw, err := sql.Open("sqlite", e.dbPath+"?_pragma=busy_timeout(10000)")
	if err != nil {
		e.t.Fatalf("open raw store: %v", err)
	}
	defer raw.Close()
	if _, err := writefailfix.Install(raw, writefailfix.LaunchIdentityWrite, e.r.ClaudeInstanceID); err != nil {
		e.t.Errorf("install the identity write failure: %v", err)
	}
}

// TestLaunchOwnerHeldOnlyDuringTheLaunch: Launch records this process as the
// row's owner at the insert (seen during the create) and ends the hold when
// the launch ends: the identity write clears it, and a lost reply, an identity
// write that did not apply or failed in the store (its one WARN line) or a
// failed create releases it; "duplicate session" leaves it to the held-name
// path's end write (row_version 0).
func TestLaunchOwnerHeldOnlyDuringTheLaunch(t *testing.T) {
	ns := selfNS(t)
	held := store.LaunchOwner{PID: os.Getpid(), Starttime: ownStart, PIDNamespace: ns}
	cases := []struct {
		name      string
		script    *tmuxfix.Script
		duringFn  func(e *launchEnv) // runs during the create, after the row is read
		wantErr   bool
		wantOwner store.LaunchOwner
		version   int64
		warn      string // the one log line's text ("": no log)
	}{
		{name: "labelled, identity written", version: 1},
		{name: "lost reply, released", script: &tmuxfix.Script{Failure: tmux.FailUnrecognized, ExitStatus: 0, Applied: true}, version: 1},
		{name: "identity write not applied, released", duringFn: func(e *launchEnv) {
			if err := e.s.SetParentID(e.r.ClaudeInstanceID, ""); err != nil {
				e.t.Errorf("SetParentID: %v", err)
			}
		}, version: 2},
		{name: "identity write store error, released", duringFn: failIdentityWrite, version: 1,
			warn: "WARN: recording the launch identity of instance id-launch-1 failed"},
		{name: "create timed out, released", script: &tmuxfix.Script{Failure: tmux.FailTimeout}, wantErr: true, version: 1},
		{name: "duplicate session, kept", script: &tmuxfix.Script{Failure: tmux.FailDuplicate}, wantErr: true, wantOwner: held},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newLaunchEnv(t)
			e.pc.Set(os.Getpid(), procfix.Alive(ownStart))
			if tc.script != nil {
				e.rec.Script(tmuxfix.AnySocket, *tc.script, tmux.CallCreate)
			}
			var during store.Spawn
			e.rec.AfterCall(tmux.CallCreate, func(tmuxfix.SocketCall, error) {
				during = e.row(e.r.ClaudeInstanceID)
				if tc.duringFn != nil {
					tc.duringFn(e)
				}
			})

			_, _, err := e.launch()

			if (err != nil) != tc.wantErr {
				t.Fatalf("Launch err = %v; want an error: %v", err, tc.wantErr)
			}
			if during.LaunchOwner != held {
				t.Errorf("owner during the create = %+v; want this process %+v", during.LaunchOwner, held)
			}
			row := e.row(e.r.ClaudeInstanceID)
			if row.State != store.StatePending || row.LaunchOwner != tc.wantOwner || row.RowVersion != tc.version {
				t.Errorf("row after the launch = state %q, owner %+v, row_version %d; want pending, %+v, %d",
					row.State, row.LaunchOwner, row.RowVersion, tc.wantOwner, tc.version)
			}
			lines := strings.Split(strings.TrimSpace(e.logs.String()), "\n")
			if tc.warn == "" && e.logs.Len() != 0 || tc.warn != "" && (len(lines) != 1 || !strings.HasPrefix(lines[0], tc.warn)) {
				t.Errorf("log = %q; want %q", e.logs.String(), tc.warn)
			}
		})
	}
}

// stubReleaseSleep records releaseSleep's durations instead of sleeping, for t.
func stubReleaseSleep(t *testing.T) *[]time.Duration {
	t.Helper()
	var slept []time.Duration
	old := releaseSleep
	releaseSleep = func(d time.Duration) { slept = append(slept, d) }
	t.Cleanup(func() { releaseSleep = old })
	return &slept
}

// scriptedReleaser answers ReleaseLaunchOwner with errs in order (nil: res), then res.
type scriptedReleaser struct {
	errs  []error
	res   store.CondResult
	calls int
}

func (r *scriptedReleaser) ReleaseLaunchOwner(string, string) (store.CondResult, error) {
	r.calls++
	if r.calls <= len(r.errs) && r.errs[r.calls-1] != nil {
		return 0, r.errs[r.calls-1]
	}
	return r.res, nil
}

// TestReleaseLaunchOwnerRetries: a store error is retried, up to three tries
// with 50 ms then 100 ms between them, and only the last failure logs one WARN
// line naming the instance and the tries (no token); a write that does not
// apply is not retried.
func TestReleaseLaunchOwnerRetries(t *testing.T) {
	// Serial: it stubs the package's releaseSleep.
	errLocked := errors.New("database is locked")
	cases := []struct {
		name  string
		r     *scriptedReleaser
		calls int
		slept []time.Duration
		warn  bool
	}{
		{"applied at once", &scriptedReleaser{res: store.CondApplied}, 1, nil, false},
		{"store error, then applied", &scriptedReleaser{errs: []error{errLocked}, res: store.CondApplied}, 2,
			[]time.Duration{50 * time.Millisecond}, false},
		{"store error on every try", &scriptedReleaser{errs: []error{errLocked, errLocked, errLocked}}, 3,
			[]time.Duration{50 * time.Millisecond, 100 * time.Millisecond}, true},
		{"not applied, not retried", &scriptedReleaser{res: store.CondChanged}, 1, nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			slept := stubReleaseSleep(t)
			var logs bytes.Buffer

			ReleaseLaunchOwner(tc.r, log.New(&logs, "", 0), "id-release", "0123456789abcdef")

			if tc.r.calls != tc.calls || !slices.Equal(*slept, tc.slept) {
				t.Errorf("tries %d, sleeps %v; want %d, %v", tc.r.calls, *slept, tc.calls, tc.slept)
			}
			want := ""
			if tc.warn {
				want = "WARN: ending the launch hold of instance id-release failed after 3 tries: database is locked\n"
			}
			if logs.String() != want {
				t.Errorf("log = %q; want %q", logs.String(), want)
			}
		})
	}
}
