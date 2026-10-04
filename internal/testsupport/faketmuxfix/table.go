// Package faketmuxfix is the shared, non-test half of test/fake-tmux (SRD
// SR-20.3): the schema of the fake's per-socket session tables, the locked
// reader and writer both the fake and tests use, its injections, the names
// of its control variables, and the one builder that compiles the fake once
// per test binary. Tests never hand-write a table file and never spell a
// control variable's name: they go through this package.
//
// Where a table lives. Each call the production client makes names its
// socket (-S <socket>), and the fake keeps one table per socket. Without
// FAKE_TMUX_TABLES in the fake's environment the table is the file
// "<socket>.fake-tmux.json" beside the socket path, so in-process tests on
// different sockets never share one and need no process-wide variable. With
// FAKE_TMUX_TABLES set to a directory, every socket's table is a file in that
// directory (see Tables.Path), which gives a subprocess test (CLI, MCP,
// envelope-diff, bun) its own tables even on the shared fixed test socket.
//
// Concurrency. Every read-modify-write holds an exclusive flock on
// "<table>.lock" and replaces the table by an atomic rename, so concurrent
// fake invocations of one test never lose an update.
//
// This package imports internal/tmux (for tmux.Call) and
// internal/testsupport/tmuxfix (the replay catalogue); the Makefile's
// fake-tmux rule lists their sources as prerequisites.
package faketmuxfix

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"

	"github.com/gabemahoney/agent-director/internal/tmux"
)

// Table is one socket's state as test/fake-tmux answers from it (SR-20.3):
// the server, the scope values of @ad_owner, the sessions with their panes
// and pane labels, and the injections that replace or delay the fake's
// answers.
type Table struct {
	// Socket is the socket path the table answers for (written by the
	// helpers; informational).
	Socket string `json:"socket"`
	// Server is the running server; nil means none. A create on a socket
	// with no server starts one (PID: the fake's parent, the calling client;
	// Start: the current time). It stays when its last session goes, as a
	// tmux server with exit-empty off does, and the lookup's identity line
	// names it with or without sessions; with none, the fake fails the
	// lookup with the catalogue's no-socket reply, as tmux does on a socket
	// no server listens on (LFR H5; b.47f).
	Server *Server `json:"server,omitempty"`
	// Scope holds the @ad_owner values the lookup's three scope reads print
	// and sessions inherit (SR-3.4).
	Scope Scope `json:"scope"`
	// Sessions in creation order; the fake lists them sorted by name, as
	// tmux does.
	Sessions []Session `json:"sessions,omitempty"`
	// NextSession and NextPane are the numbers of the next $N and %N a
	// create assigns; each is raised past the highest id in the table first,
	// so a table written with ids and no counters stays consistent.
	NextSession int `json:"next_session,omitempty"`
	NextPane    int `json:"next_pane,omitempty"`
	// NewPanePID is the pane pid a create gives its pane; 0 means the fake's
	// parent (the calling client) pid.
	NewPanePID int `json:"new_pane_pid,omitempty"`
	// Injections replace or delay the fake's answer per call kind; the
	// first one matching a call applies (see Injection).
	Injections []Injection `json:"injections,omitempty"`
}

// Server is a tmux server's identity as #{pid} and #{start_time} show it.
type Server struct {
	PID   int   `json:"pid"`
	Start int64 `json:"start"`
}

// Scope holds the @ad_owner option values at the three scopes the lookup
// reads: global session (-g), server (-s) and global window (-gw). An empty
// value is unset. In a session line's #{@ad_owner} a server or global-window
// value takes precedence over the session's own label and a global value
// fills in only for a session with none, as tmux resolves a user option in a
// format (provenance-fresh F2, F9b).
type Scope struct {
	Global       string `json:"global,omitempty"`
	Server       string `json:"server,omitempty"`
	GlobalWindow string `json:"global_window,omitempty"`
}

// Session is one tmux session.
type Session struct {
	// ID is the session id, "$N".
	ID string `json:"id"`
	// Created is #{session_created}, Unix seconds.
	Created int64 `json:"created"`
	// Name is the stored name, as the listing shows it (SR-3.2, SR-3.10).
	Name string `json:"name"`
	// Label is the session's own @ad_owner value; "" is unset.
	Label string `json:"label,omitempty"`
	// Panes are the session's panes; killing the last removes the session.
	Panes []Pane `json:"panes,omitempty"`
}

// Pane is one pane of a session.
type Pane struct {
	Window int `json:"window"`
	Index  int `json:"index"`
	// ID is the pane id, "%N".
	ID  string `json:"id"`
	PID int    `json:"pid"`
	// Capture is what capture-pane prints for the pane; "" falls back to
	// FAKE_TMUX_PANE_OUTPUT, then to the fake's fixed stub text.
	Capture string `json:"capture,omitempty"`
	// AdPane is the pane's own @ad_pane value, raw, as the pane listing's
	// #{@ad_pane} prints it; "" is unset (a split pane). The create chain
	// and the label by id set it (WD 2026-09-29c). A value borrowed from
	// another scope is written here as the text the listing would show,
	// such as a value naming another pane (tmuxfix.PaneLabelShapes).
	AdPane string `json:"ad_pane,omitempty"`
}

// tableSuffix and lockSuffix name the files beside a socket path (or in the
// FAKE_TMUX_TABLES directory).
const (
	tableSuffix = ".fake-tmux.json"
	lockSuffix  = ".lock"
)

// TablePath returns where the fake keeps socket's table: in dir when dir is
// non-empty (the value of FAKE_TMUX_TABLES), else beside the socket path.
func TablePath(socket, dir string) string {
	if dir == "" {
		return socket + tableSuffix
	}
	sum := sha256.Sum256([]byte(socket))
	return filepath.Join(dir, hex.EncodeToString(sum[:12])+tableSuffix)
}

// ReadTable reads the table at path without locking; ok is false when there
// is none. The file is only ever replaced by rename, so a read never sees a
// partial table.
func ReadTable(path string) (tb Table, ok bool, err error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Table{}, false, nil
	}
	if err != nil {
		return Table{}, false, err
	}
	if err := json.Unmarshal(data, &tb); err != nil {
		return Table{}, false, fmt.Errorf("faketmuxfix: table %s: %w", path, err)
	}
	return tb, true, nil
}

// UpdateTable runs f on the table at path under the table's exclusive lock
// and, when f reports a change, replaces the file atomically. A missing table
// reaches f as a zero Table with exists false; with create set, the table's
// directory is made (mode 0700) when missing. When create is false and there
// is no table, f runs on the zero Table without locking and nothing is
// written: a call that creates nothing leaves no files.
func UpdateTable(path string, create bool, f func(tb *Table, exists bool) (changed bool, err error)) error {
	if create {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
	} else if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		var tb Table
		_, err := f(&tb, false)
		return err
	}
	unlock, err := lockFile(path + lockSuffix)
	if err != nil {
		return err
	}
	defer unlock()
	tb, exists, err := ReadTable(path)
	if err != nil {
		return err
	}
	changed, err := f(&tb, exists)
	if err != nil || !changed {
		return err
	}
	return writeAtomic(path, tb)
}

// lockFile takes an exclusive flock on path, creating it, and returns the
// release function.
func lockFile(path string) (func(), error) {
	lf, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_EX); err != nil {
		lf.Close()
		return nil, err
	}
	return func() {
		_ = syscall.Flock(int(lf.Fd()), syscall.LOCK_UN)
		lf.Close()
	}, nil
}

// writeAtomic writes tb to a temporary file beside path and renames it over
// path.
func writeAtomic(path string, tb Table) error {
	data, err := json.MarshalIndent(tb, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// Action says what an Injection does to a matching call.
type Action string

// The injection actions (SR-20.3).
const (
	// ActReply writes the named catalogue entry's exact bytes on its
	// recorded streams and exits with its recorded status.
	ActReply Action = "reply"
	// ActExit exits with Injection.Exit and writes nothing.
	ActExit Action = "exit"
	// ActHang never answers: the fake sleeps until the client's timeout
	// kills it, and on its own exits 1 with no output after Injection.ForMs
	// (DefaultHangBound when 0, never more than MaxBound).
	ActHang Action = "hang"
	// ActHoldPipes answers (normally, or with the named entry when Entry is
	// set) and exits while a child process it starts keeps standard output
	// and standard error open for Injection.ForMs (at most MaxBound).
	ActHoldPipes Action = "hold-pipes"
	// ActChainFails is for the create: the session is created and its reply
	// printed, but the chained label step fails as tmux reports it (the
	// catalogue's recorded label-failure line on standard error, exit 1),
	// leaving the session unlabelled.
	ActChainFails Action = "chain-fails"
)

// Injection replaces or delays the fake's answer to one call kind on one
// socket. Build injections with Reply, ExitCode, Hang, HoldPipes and
// ChainFails.
type Injection struct {
	// Call is the call kind the injection matches (tmux.Call wording).
	Call tmux.Call `json:"call"`
	// Action is what the injection does.
	Action Action `json:"action"`
	// Entry names a replay catalogue entry (ActReply; optional for
	// ActHoldPipes). The fake resolves it with ResolveEntry for the call's
	// socket.
	Entry string `json:"entry,omitempty"`
	// Exit is ActExit's exit status.
	Exit int `json:"exit,omitempty"`
	// ForMs is ActHang's bound and ActHoldPipes's hold, in milliseconds.
	ForMs int `json:"for_ms,omitempty"`
	// Effect applies the call's normal effect to the table first (for
	// example the create and its chained label) and then answers as the
	// action says. ActHoldPipes without Entry and ActChainFails always
	// apply it.
	Effect bool `json:"effect,omitempty"`
	// Times limits the injection to the next Times matching calls; 0 means
	// every call. The fake counts down in the table and removes the
	// injection when it is used up.
	Times int `json:"times,omitempty"`
}
