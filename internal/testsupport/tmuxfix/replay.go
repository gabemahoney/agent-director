package tmuxfix

import (
	"strings"

	"github.com/gabemahoney/agent-director/internal/tmux"
)

// Replay catalogue (SRD SR-20.4): the tmux evidence of SRD Appendix E as
// exported data, recorded on tmux 3.2a; identical on 3.3a. Each entry holds
// the bytes tmux writes to standard output and standard error, the exit
// status, and the typed outcome the production client (internal/tmux) must
// return for each call kind. The catalogue never recognises or parses reply
// text; tests feed an entry to the client's runner seam with Entry.Result.
// Tests never spell a reply text, stored name or lookup line inline
// (SR-20.2): they take it from here or build it with the builders in
// replay_answers.go.

// Entry is one replay catalogue entry, recorded on tmux 3.2a; identical on
// 3.3a (or composed from recorded parts, as its Source says): the bytes on
// each stream, the exit status, and the typed outcome per call kind.
type Entry struct {
	// Name identifies the entry, e.g. "reply/no-server" or "lookup/F9b".
	Name string
	// Source cites the Appendix E section or provenance transcript.
	Source string
	// Stdout and Stderr are the exact bytes on each stream.
	Stdout, Stderr string
	// Exit is the tmux client's exit status.
	Exit int
	// Want maps each call kind the entry applies to onto the Failure the
	// client must return for it; 0 means the call succeeds.
	Want map[tmux.Call]tmux.Failure
	// Socket is the socket path CallError.Socket must carry (the no-server,
	// no-socket and permission replies only).
	Socket string
	// FirstLine is CallError.FirstLine wherever Want is FailUnrecognized.
	FirstLine string
	// ChainOnly marks a create entry whose Want holds only for a chained
	// create; for a name labelled by id ($ or \) a parsed reply is success.
	ChainOnly bool
	// LabelValues lists the raw label values in Stdout, which must never
	// appear in a CallError (SR-2.3).
	LabelValues []string
	// Lookup, Panes and Create are the parsed answers a successful lookup,
	// pane listing or create must return; a capture returns Stdout.
	Lookup tmux.LookupAnswer
	Panes  []tmux.Pane
	Create tmux.CreateReply
}

// Result is the entry as the runner seam's answer: a client that exited on
// its own with the entry's streams and exit status.
func (e Entry) Result() tmux.RunResult {
	return tmux.RunResult{Status: tmux.RunExited, ExitStatus: e.Exit, Stdout: []byte(e.Stdout), Stderr: []byte(e.Stderr)}
}

// Calls lists the call kinds in Want, in AllCalls order.
func (e Entry) Calls() []tmux.Call {
	var out []tmux.Call
	for _, c := range AllCalls() {
		if _, ok := e.Want[c]; ok {
			out = append(out, c)
		}
	}
	return out
}

// AllCalls returns the nine call kinds of SR-2.1.
func AllCalls() []tmux.Call {
	return []tmux.Call{tmux.CallLookup, tmux.CallListPanes, tmux.CallKillPane, tmux.CallKillSession,
		tmux.CallSendText, tmux.CallSendEnter, tmux.CallCapture, tmux.CallCreate, tmux.CallSetLabel}
}

// Find returns the entry named name; it panics when the catalogue has none.
func Find(entries []Entry, name string) Entry {
	for _, e := range entries {
		if e.Name == name {
			return e
		}
	}
	panic("tmuxfix: no replay entry named " + name)
}

// everyCall maps every call kind to f.
func everyCall(f tmux.Failure) map[tmux.Call]tmux.Failure {
	m := make(map[tmux.Call]tmux.Failure, 9)
	for _, c := range AllCalls() {
		m[c] = f
	}
	return m
}

// with returns a copy of m with the given call kinds set to f.
func with(m map[tmux.Call]tmux.Failure, f tmux.Failure, calls ...tmux.Call) map[tmux.Call]tmux.Failure {
	out := make(map[tmux.Call]tmux.Failure, len(m)+len(calls))
	for c, v := range m {
		out[c] = v
	}
	for _, c := range calls {
		out[c] = f
	}
	return out
}

// only maps just the given call kinds to f.
func only(f tmux.Failure, calls ...tmux.Call) map[tmux.Call]tmux.Failure {
	return with(nil, f, calls...)
}

// The recorded reply wordings (SR-2.5; E.10 O1-O4, where every reply
// arrives on standard error with exit status 1).
const (
	wordNoServer  = "no server running on "
	wordConnect   = "error connecting to "
	wordNoSocket  = " (No such file or directory)"
	wordDenied    = " (Permission denied)"
	wordDuplicate = "duplicate session: "
	wordNoPane    = "can't find pane: %0"        // E.10 O4: send, Enter or capture on a vanished pane
	wordExited    = "server exited unexpectedly" // E.3 I1: a create racing an exiting server
)

// firstLineCap is CallError.FirstLine's bound in bytes (Appendix F.1).
const firstLineCap = 200

// NoServer is "no server running on <socket>" (E.10 O1, E.3 R2/I1,
// provenance-fresh F9c/F9d): FailNoServer on every call kind.
func NoServer(socket string) Entry {
	return Entry{Name: "reply/no-server", Source: "E.10 O1; E.3 R2, I1; provenance-fresh F9c, F9d",
		Stderr: wordNoServer + socket + "\n", Exit: 1,
		Want: everyCall(tmux.FailNoServer), Socket: socket}
}

// NoSocket is "error connecting to <socket> (No such file or directory)"
// (E.10 O1, E.3 R1, F9e): FailNoSocket on every call kind.
func NoSocket(socket string) Entry {
	return Entry{Name: "reply/no-socket", Source: "E.10 O1; E.3 R1; provenance-fresh F9e",
		Stderr: wordConnect + socket + wordNoSocket + "\n", Exit: 1,
		Want: everyCall(tmux.FailNoSocket), Socket: socket}
}

// SocketDenied is "error connecting to <socket> (Permission denied)",
// recorded for every call kind (E.9 S6, E.10 O2): FailSocketDenied on all
// nine (AC-CLS-02).
func SocketDenied(socket string) Entry {
	return Entry{Name: "reply/permission-denied", Source: "E.9 S6; E.10 O2; E.3 R3",
		Stderr: wordConnect + socket + wordDenied + "\n", Exit: 1,
		Want: everyCall(tmux.FailSocketDenied), Socket: socket}
}

// Duplicate is "duplicate session: <stored name>" (E.3 D2, E.10 O4, N6):
// FailDuplicate on the create only, FailUnrecognized on every other call.
func Duplicate(stored string) Entry {
	line := wordDuplicate + stored
	return Entry{Name: "reply/duplicate:" + stored, Source: "E.3 D2; E.10 O4, N6; rn4 R2a, H2",
		Stderr: line + "\n", Exit: 1,
		Want:      with(everyCall(tmux.FailUnrecognized), tmux.FailDuplicate, tmux.CallCreate),
		FirstLine: line}
}

// unrecognized is a stderr reply no call recognises, for the given calls.
func unrecognized(name, source, line string, calls ...tmux.Call) Entry {
	return Entry{Name: name, Source: source, Stderr: line + "\n", Exit: 1,
		Want: only(tmux.FailUnrecognized, calls...), FirstLine: line}
}

// longReply returns an unrecognised reply longer than the 200-byte cap whose
// cut falls inside a two-byte character, and the FirstLine it must give.
func longReply() (line, capped string) {
	const prefix = "can't find session: " // E.3 D1 wording
	capped = prefix + strings.Repeat("a", firstLineCap-len(prefix)-1)
	return capped + "\u00fc-tail", capped
}

// Replies returns every reply entry, the socket replies naming socket: the
// four recognised replies (duplicate with plain and escaped stored names),
// the unrecognised replies tmux prints (the label by id's session and pane
// steps included), a non-zero exit with empty stderr,
// multi-line stderr, reply wording on stdout, and silent successes.
func Replies(socket string) []Entry {
	long, capped := longReply()
	return []Entry{
		NoServer(socket),
		NoSocket(socket),
		SocketDenied(socket),
		Duplicate("owned"),       // E.10 O4
		Duplicate("proj-abc123"), // E.3 D2
		Duplicate("dot_x"),       // E.10 N6: the stored form of dot.x
		Duplicate(`a\$b`),        // E.3 N1: the stored form of a$b after the D2 prefix
		unrecognized("reply/cant-find-pane", "E.10 O4; E.3 D1", wordNoPane,
			tmux.CallSendText, tmux.CallSendEnter, tmux.CallCapture),
		unrecognized("reply/cant-find-session", "E.10 O4; E.3 D1, D3", "can't find session: $0", tmux.CallKillSession),
		unrecognized("reply/no-such-session", "E.10 O3; E.13 R1 (option reads of a vanished id)",
			"no such session: $99", tmux.CallSetLabel),
		unrecognized("reply/no-such-pane", "label by id's pane step, set-option -p -t on a vanished pane id; "+
			"recorded on tmux 3.3a only (WD 2026-09-29c)", "no such pane: %99", tmux.CallSetLabel),
		unrecognized("reply/server-exited", "E.3 I1", wordExited,
			tmux.CallCreate, tmux.CallLookup, tmux.CallListPanes),
		{Name: "reply/over-200-bytes", Source: "composed: E.3 D1 wording past the F.1 cap",
			Stderr: long + "\n", Exit: 1, Want: everyCall(tmux.FailUnrecognized), FirstLine: capped},
		{Name: "reply/nonzero-empty-stderr", Source: "SR-2.5: a non-zero exit with no text",
			Exit: 1, Want: everyCall(tmux.FailUnrecognized)},
		{Name: "reply/multiline-unrecognized-first", Source: "composed: E.10 O4 then O1 wording",
			Stderr: wordNoPane + "\n" + wordNoServer + socket + "\n", Exit: 1,
			Want: everyCall(tmux.FailUnrecognized), FirstLine: wordNoPane},
		{Name: "reply/multiline-recognized-first", Source: "composed: E.10 O1 then E.3 I1 wording",
			Stderr: wordNoServer + socket + "\n" + wordExited + "\n", Exit: 1,
			Want: everyCall(tmux.FailNoServer), Socket: socket},
		{Name: "reply/surrounding-whitespace", Source: "composed: E.10 O1 wording, padded (SR-2.3 trim)",
			Stderr: " \t" + wordNoServer + socket + " \r\n", Exit: 1,
			Want: everyCall(tmux.FailNoServer), Socket: socket},
		{Name: "reply/wording-on-stdout-exit0", Source: "SR-2.5: wording on stdout is data (E.10)",
			Stdout:    wordNoServer + socket + "\n",
			Want:      with(everyCall(0), tmux.FailUnrecognized, tmux.CallListPanes, tmux.CallCreate),
			FirstLine: wordNoServer + socket, Lookup: tmux.LookupAnswer{ScopeValue: true}},
		{Name: "reply/wording-on-stdout-exit1", Source: "SR-2.3: replies only from stderr (E.10)",
			Stdout: wordNoServer + socket + "\n", Exit: 1,
			Want: everyCall(tmux.FailUnrecognized)},
		Silent(),
	}
}

// Silent is a call that prints nothing and exits 0 (E.10 O4; E.3 I2): the
// kills, text, Enter, label by id and the lookup of a server with no
// sessions succeed; a create with no reply is FailUnrecognized (SR-2.1).
func Silent() Entry {
	return Entry{Name: "reply/silent-success", Source: "E.10 O4; E.3 I2, D3, P1",
		Want: with(everyCall(0), tmux.FailUnrecognized, tmux.CallCreate)}
}
