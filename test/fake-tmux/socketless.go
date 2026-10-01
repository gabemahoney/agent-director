package main

import (
	"errors"
	"os"

	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
)

// socketlessMain answers argv without the leading -u and returns the exit
// status. The bare invocation exits 0 and has-session exits 1, as for an
// absent session, both logging nothing. Every other invocation is logged and
// refused with the catalogue's no-server reply for the default socket, as
// tmux answers when no server runs there (SR-2.1: no call names a session
// without the socket). No table is read or written.
func socketlessMain() int {
	if len(os.Args) < 2 {
		// `tmux` with no subcommand; nothing to record. Exit 0 so the
		// caller's `command -v tmux` probes still succeed.
		return 0
	}
	if os.Args[1] == "has-session" {
		// No verb calls the name-based HasSession; *tmux.Client keeps it
		// (SR-16.2 item 2).
		return 1
	}
	logArgv()
	e := tmuxfix.NoServer(defaultSocket())
	return write(output{stdout: e.Stdout, stderr: e.Stderr, exit: e.Exit})
}

// defaultSocket is the socket tmux would use in the fake's environment
// (tmux.ResolveSocket, creating nothing), or the would-be socket of a
// refused per-user directory.
func defaultSocket() string {
	socket, err := tmux.ResolveSocket(false)
	var dirErr *tmux.SocketDirError
	if errors.As(err, &dirErr) {
		return dirErr.Socket
	}
	return socket
}
