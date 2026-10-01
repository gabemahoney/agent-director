package faketmuxfix

import (
	"fmt"
	"strings"
	"time"

	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
)

// The fake's control variables, read from its environment. None starts with
// AGENT_DIRECTOR_, which the production client strips (SR-3.12).
const (
	// EnvLog names a file every invocation appends its argv to: one argv
	// element per line, then a "---" line.
	EnvLog = "FAKE_TMUX_LOG"
	// EnvPaneOutput is the capture text for a pane with no Capture.
	EnvPaneOutput = "FAKE_TMUX_PANE_OUTPUT"
	// EnvFailNewSessionName makes a create whose -s name equals its value
	// print the catalogue's "duplicate session" reply for the name's stored
	// form and exit 1, creating nothing.
	EnvFailNewSessionName = "FAKE_TMUX_FAIL_NEWSESSION_NAME"
	// EnvTables names a directory holding every socket's table; unset, each
	// table lives beside its socket path (see TablePath).
	EnvTables = "FAKE_TMUX_TABLES"
)

// Bounds on the fake's own waiting, so no fake process outlives a test.
const (
	// DefaultHangBound is how long an ActHang call waits before exiting on
	// its own when Injection.ForMs is 0.
	DefaultHangBound = 30 * time.Second
	// MaxBound caps every hang and pipe hold.
	MaxBound = 2 * time.Minute
)

// TB is the part of testing.TB the helpers use (this package does not
// import testing, because the fake binary links it).
type TB interface {
	Helper()
	Fatalf(format string, args ...any)
}

// Tables locates the fake's tables for a test: Dir is the FAKE_TMUX_TABLES
// directory the fake is given, or "" for tables beside each socket path (the
// in-process default, which needs no environment).
type Tables struct {
	Dir string
}

// Path returns socket's table file.
func (s Tables) Path(socket string) string { return TablePath(socket, s.Dir) }

// Env returns the environment entries a fake subprocess needs to use these
// tables: FAKE_TMUX_TABLES=<Dir>, or none when Dir is "".
func (s Tables) Env() []string {
	if s.Dir == "" {
		return nil
	}
	return []string{EnvTables + "=" + s.Dir}
}

// Write replaces socket's table with tb (its Socket field set to socket),
// creating directories as needed. Every Reply injection's entry must
// resolve (ResolveEntry).
func (s Tables) Write(t TB, socket string, tb Table) {
	t.Helper()
	s.Update(t, socket, func(cur *Table) { *cur = tb })
}

// Read returns socket's table; a socket with no table gives the zero Table.
func (s Tables) Read(t TB, socket string) Table {
	t.Helper()
	tb, _, err := ReadTable(s.Path(socket))
	if err != nil {
		t.Fatalf("faketmuxfix: read table for %s: %v", socket, err)
	}
	return tb
}

// Update applies f to socket's table (the zero Table when there is none)
// under the table lock and writes the result.
func (s Tables) Update(t TB, socket string, f func(*Table)) {
	t.Helper()
	err := UpdateTable(s.Path(socket), true, func(tb *Table, _ bool) (bool, error) {
		f(tb)
		tb.Socket = socket
		for _, inj := range tb.Injections {
			if inj.Entry == "" {
				continue
			}
			if _, ok := ResolveEntry(inj.Entry, socket); !ok {
				return false, fmt.Errorf("injection on %q names no catalogue entry %q", inj.Call, inj.Entry)
			}
		}
		return true, nil
	})
	if err != nil {
		t.Fatalf("faketmuxfix: update table for %s: %v", socket, err)
	}
}

// Inject appends injections to socket's table, creating it when needed.
func (s Tables) Inject(t TB, socket string, injs ...Injection) {
	t.Helper()
	s.Update(t, socket, func(tb *Table) { tb.Injections = append(tb.Injections, injs...) })
}

// Reply injects catalogue entry e on call: the fake writes e's Stdout and
// Stderr and exits with e.Exit. The fake re-resolves e by name for the
// call's socket, so the socket replies name the socket called.
func Reply(call tmux.Call, e tmuxfix.Entry) Injection {
	return Injection{Call: call, Action: ActReply, Entry: e.Name}
}

// ExitCode injects a bare exit status with no output on call.
func ExitCode(call tmux.Call, code int) Injection {
	return Injection{Call: call, Action: ActExit, Exit: code}
}

// Hang makes call never answer (bounded by DefaultHangBound; see Bound).
func Hang(call tmux.Call) Injection {
	return Injection{Call: call, Action: ActHang}
}

// HoldPipes makes call answer normally and exit 0 while a child keeps its
// output pipes open for d (capped at MaxBound).
func HoldPipes(call tmux.Call, d time.Duration) Injection {
	return Injection{Call: call, Action: ActHoldPipes, ForMs: int(d / time.Millisecond)}
}

// ChainFails makes the create's chained label step fail after the session is
// created and its reply printed.
func ChainFails() Injection {
	return Injection{Call: tmux.CallCreate, Action: ActChainFails}
}

// WithEffect applies the call's normal effect to the table before the
// injected answer (for example: create and label, then hang).
func (i Injection) WithEffect() Injection { i.Effect = true; return i }

// FirstN limits the injection to the next n matching calls.
func (i Injection) FirstN(n int) Injection { i.Times = n; return i }

// Bound sets a hang's own bound or a hold's duration.
func (i Injection) Bound(d time.Duration) Injection { i.ForMs = int(d / time.Millisecond); return i }

// WithReply makes a HoldPipes injection answer with catalogue entry e
// instead of the normal answer.
func (i Injection) WithReply(e tmuxfix.Entry) Injection { i.Entry = e.Name; return i }

// duplicatePrefix is the name prefix of the catalogue's Duplicate entries.
const duplicatePrefix = "reply/duplicate:"

// ResolveEntry finds the replay catalogue entry named name, the socket
// replies built for socket: every Replies, CreateReplies, LookupAnswers,
// PaneListings and Captures entry, each LabelShape's, PaneLabelShape's and
// StoredName's entry, and "reply/duplicate:<stored>" for any stored name.
func ResolveEntry(name, socket string) (tmuxfix.Entry, bool) {
	if stored, ok := strings.CutPrefix(name, duplicatePrefix); ok {
		return tmuxfix.Duplicate(stored), true
	}
	lists := [][]tmuxfix.Entry{tmuxfix.Replies(socket), tmuxfix.CreateReplies(), tmuxfix.LookupAnswers(),
		tmuxfix.PaneListings(), tmuxfix.Captures()}
	for _, l := range lists {
		for _, e := range l {
			if e.Name == name {
				return e, true
			}
		}
	}
	for _, s := range tmuxfix.LabelShapes() {
		if e := s.Entry(); e.Name == name {
			return e, true
		}
	}
	for _, s := range tmuxfix.PaneLabelShapes() {
		if e := s.Entry(); e.Name == name {
			return e, true
		}
	}
	for _, n := range tmuxfix.StoredNames() {
		if e := n.Entry(); e.Name == name {
			return e, true
		}
	}
	return tmuxfix.Entry{}, false
}

// StoredForm returns the name tmux stores and lists for a session created
// with -s raw: the recorded stored form when the catalogue has one
// (tmuxfix.StoredNames), else raw with '.' and ':' replaced by '_' (E.10 N6).
func StoredForm(raw string) string {
	for _, n := range tmuxfix.StoredNames() {
		if n.Raw == raw {
			return n.Stored
		}
	}
	return strings.NewReplacer(".", "_", ":", "_").Replace(raw)
}
