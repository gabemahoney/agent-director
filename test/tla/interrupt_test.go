package tla_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// live is a runner started in its own process group.
type live struct {
	out, err bytes.Buffer
	pid      int
	done     chan error
}

func (r *rig) start(t *testing.T) *live {
	t.Helper()
	cmd := r.command(context.Background())
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	l := &live{done: make(chan error, 1)}
	cmd.Stdout, cmd.Stderr = &l.out, &l.err
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	l.pid = cmd.Process.Pid
	t.Cleanup(func() { _ = syscall.Kill(-l.pid, syscall.SIGKILL) })
	go func() { l.done <- cmd.Wait() }()
	return l
}

// waitFor waits for the fake to create path.
func waitFor(t *testing.T, path string) {
	t.Helper()
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if _, err := os.Stat(path); err == nil {
			return
		}
	}
	t.Fatalf("%s never appeared", filepath.Base(path))
}

// waitInPoll waits until the runner sits in its poll wait: its sleep child
// exists, plus a moment for bash to enter the wait builtin.
func (l *live) waitInPoll(t *testing.T) {
	t.Helper()
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		stats, _ := filepath.Glob("/proc/[0-9]*/stat")
		for _, p := range stats {
			b, err := os.ReadFile(p)
			s := string(b)
			if i := strings.LastIndex(s, ")"); err == nil && i > 0 && strings.HasSuffix(s[:i], "(sleep") {
				if f := strings.Fields(s[i+1:]); len(f) > 1 && f[1] == strconv.Itoa(l.pid) {
					time.Sleep(200 * time.Millisecond)
					return
				}
			}
		}
	}
	t.Fatal("the runner never reached its poll wait")
}

// stop sends sig (to the whole group when group is set) and waits briefly for
// the runner to end, stdout pipe included.
func (l *live) stop(t *testing.T, sig syscall.Signal, group bool) result {
	t.Helper()
	target := l.pid
	if group {
		target = -l.pid
	}
	if err := syscall.Kill(target, sig); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-l.done:
		return result{code: exitCode(t, err), stdout: l.out.String(), stderr: l.err.String()}
	case <-time.After(10 * time.Second):
		t.Fatalf("the runner was still running 10 s after %v", sig)
	}
	return result{}
}

// TestRunnerInterrupt: an interrupt exits 130 at once; a queued job's context
// is removed (a cancel), a context the job may still need is kept.
func TestRunnerInterrupt(t *testing.T) {
	queued := state("queued", false)
	cases := []struct {
		name    string
		hold    string         // "" interrupts the poll wait; else the call ("list", "submit", or "cp" for the context build) it blocks in
		sig     syscall.Signal // sent to the runner alone in the poll wait, else to its group
		status  []string       // the parse job's status answers: the poll's, then the handler's
		ctx     string         // the parse context afterwards: "kept", "gone" or "" (no run dir)
		say     []string
		notSay  []string
		submits int
	}{
		{name: "queued is cancelled", sig: syscall.SIGTERM, status: []string{queued, queued, queued}, ctx: "gone", submits: 1,
			say:    []string{"# interrupted: job parse (j1-parse) was still queued; its context was removed so it will not run"},
			notSay: []string{"# note:"}},
		{name: "running is kept", sig: syscall.SIGINT, status: []string{queued, state("running", false)}, ctx: "kept", submits: 1,
			say: []string{"# interrupted: job parse (j1-parse) is already running; it will finish on its own"}},
		{name: "building is kept", sig: syscall.SIGTERM, status: []string{queued, state("building", false)}, ctx: "kept", submits: 1,
			say: []string{"is already building; it will finish on its own"}},
		{name: "unknown: status exit 3", sig: syscall.SIGTERM, status: []string{queued, `3 {"error":"no such job"}`}, ctx: "kept", submits: 1,
			say: []string{"state unknown (status exit 3,", "its context was kept in"}},
		{name: "unknown: not JSON", sig: syscall.SIGTERM, status: []string{queued, "0 garbage"}, ctx: "kept", submits: 1,
			say: []string{"state unknown (status exit 0, state 'unreadable'"}},
		{name: "unknown: state paused", sig: syscall.SIGTERM, status: []string{queued, state("paused", false)}, ctx: "kept", submits: 1,
			say: []string{"state unknown (status exit 0, state 'paused'"}},
		{name: "unknown: queued marked ended", sig: syscall.SIGTERM, status: []string{queued, state("queued", true)}, ctx: "kept", submits: 1,
			say: []string{"state unknown (status exit 0, state 'queued', terminal 'true')"}},
		{name: "unknown: queued without terminal", sig: syscall.SIGTERM, status: []string{queued, `0 {"state":"queued"}`}, ctx: "kept", submits: 1,
			say: []string{"state unknown (status exit 0, state 'queued', terminal 'null')"}},
		{name: "left the queue as it was removed", sig: syscall.SIGTERM, status: []string{queued, queued, state("building", false)}, ctx: "gone", submits: 1,
			say: []string{"was still queued; its context was removed", "# note: job parse (j1-parse) reports 'building' after its context was removed"}},
		{name: "already ended is removed", sig: syscall.SIGTERM, status: []string{queued, state("succeeded", true)}, ctx: "gone", submits: 1,
			say: []string{"# interrupted: job parse (j1-parse) had already ended (succeeded); its context was removed"}},
		{name: "mid-submit is kept", hold: "submit", sig: syscall.SIGTERM, ctx: "kept", submits: 1,
			say: []string{"# interrupted: job parse was being submitted, so whether the scheduler queued it is unknown; its context was kept in"}},
		{name: "not submitted yet is removed", hold: "cp", sig: syscall.SIGTERM, ctx: "gone", submits: 0,
			say: []string{"# interrupted: job parse was not submitted; its partial context was removed"}},
		{name: "before any job", hold: "list", sig: syscall.SIGINT, submits: 0,
			say: []string{"# interrupted"}, notSay: []string{"# interrupted: job"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			r.env["TLA_POLL_S"] = "30"
			r.env["TLA_SUITE"] = writeSuite(t, []row{byCfg(t, shipped(t), "ci_C1")})
			if tc.hold != "" {
				r.hold(t, tc.hold)
			} else {
				r.plan(t, "status.parse", tc.status...)
			}
			l := r.start(t)
			if tc.hold != "" {
				waitFor(t, filepath.Join(r.fake, "held."+tc.hold))
			} else {
				l.waitInPoll(t)
			}

			res := l.stop(t, tc.sig, tc.hold != "")

			if res.code != 130 || res.last() != "CI-VERDICT FAIL (interrupted, tier fast)" {
				t.Fatalf("want exit 130 and CI-VERDICT FAIL (interrupted, tier fast)\n%s", res)
			}
			for _, s := range tc.say {
				if !strings.Contains(res.stderr, s) {
					t.Errorf("stderr lacks %q\n%s", s, res)
				}
			}
			for _, s := range tc.notSay {
				if strings.Contains(res.stderr, s) {
					t.Errorf("stderr has %q\n%s", s, res)
				}
			}
			if got := len(r.submits(t)); got != tc.submits {
				t.Errorf("%d submits, want %d", got, tc.submits)
			}
			if tc.ctx == "" {
				if runDirRe.MatchString(res.stderr) {
					t.Errorf("a run dir was made\n%s", res)
				}
				return
			}
			dir := runDir(t, res)
			if !strings.Contains(res.stderr, "# run dir: "+dir+" (logs kept)") || !exists(filepath.Join(dir, "logs")) {
				t.Errorf("run dir %s and its logs not kept\n%s", dir, res)
			}
			ctx := filepath.Join(dir, "ctx", "parse")
			if tc.ctx == "kept" {
				for _, f := range []string{"Dockerfile", "runs", "tla2tools.jar", "ci_C1.cfg"} {
					if !exists(filepath.Join(ctx, f)) {
						t.Errorf("kept context lacks %s", f)
					}
				}
			} else if exists(ctx) {
				t.Errorf("context %s not removed", ctx)
			}
			if left, _ := filepath.Glob(ctx + ".removed.*"); len(left) > 0 {
				t.Errorf("removed context left behind: %v", left)
			}
		})
	}
}
