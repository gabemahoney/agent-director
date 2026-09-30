package main

import (
	"context"
	"os"
	"time"

	"golang.org/x/sys/unix"

	"github.com/gabemahoney/agent-director/internal/hook"
)

// noVerbStdinDeadline bounds a no-verb run's read of standard input (SR-22.9,
// "A Claude Code that does not run exec-form hooks"): input that has not
// ended by then is not a hook payload, and the run prints help.
const noVerbStdinDeadline = time.Second

// noVerbHookIgnored is a no-verb run's check for a hook payload (SR-22.9). A
// Claude Code that does not run exec-form hooks runs the bare binary through
// /bin/sh, so each of its hooks reaches run() with no verb and the payload on
// standard input. When standard input is not a terminal, it reads at most
// hook.MaxPayloadBytes, bounded by noVerbStdinDeadline, and hands the bytes to
// hook.HandleNoExecForm, which writes the one ad.hook.ignored no_exec_form
// when they are a hook payload. It reports whether they were: the caller then
// prints nothing and exits 0. Otherwise (a terminal, a read error, empty
// input, more than 1 MiB, input still open at the deadline, or input that is
// not a hook payload) it reports false and the caller prints help as before.
//
// It runs after the global flags and --home are applied, so the trail record
// lands under that home. It opens no store, loads no config and writes
// nothing on stdout or stderr.
func noVerbHookIgnored() bool {
	raw, ok := readNoVerbStdin(os.Stdin, noVerbStdinDeadline)
	if !ok {
		return false
	}
	return hook.HandleNoExecForm(context.Background(), raw, hook.HandleConfig{
		Env:        hook.OSGetenv,
		ParentPID:  os.Getppid,
		ParentProc: hookParentProc(),
	})
}

// readNoVerbStdin reads f whole, capped at hook.MaxPayloadBytes, with a real
// deadline. A terminal is not read at all. The read runs in its own goroutine
// and the deadline is a timer, not f.SetReadDeadline: an inherited blocking
// stdin (the usual case) is not pollable, so SetReadDeadline returns
// os.ErrNoDeadline there and a plain read of a silent open pipe would block
// forever. At the deadline the goroutine is left blocked in its read; the
// process prints help and exits, which ends it. ok is false for a terminal, a
// read error, empty input, input over the cap, or a read still running at the
// deadline.
func readNoVerbStdin(f *os.File, deadline time.Duration) (raw []byte, ok bool) {
	if isTerminal(f) {
		return nil, false
	}
	type result struct {
		raw []byte
		err error
	}
	done := make(chan result, 1)
	go func() {
		b, err := hook.ReadPayload(f)
		done <- result{raw: b, err: err}
	}()
	timer := time.NewTimer(deadline)
	defer timer.Stop()
	select {
	case r := <-done:
		if r.err != nil || len(r.raw) == 0 {
			return nil, false
		}
		return r.raw, true
	case <-timer.C:
		return nil, false
	}
}

// isTerminal reports whether f is a terminal: the per-OS termios read
// (ioctlReadTermios) succeeds only on one. /dev/null, a pipe and a regular
// file are not terminals.
func isTerminal(f *os.File) bool {
	_, err := unix.IoctlGetTermios(int(f.Fd()), ioctlReadTermios)
	return err == nil
}
