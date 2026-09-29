package tmuxfix

import (
	"strconv"
	"strings"

	"github.com/gabemahoney/agent-director/internal/tmux"
)

// Answer entries of the replay catalogue (SR-20.4): what tmux prints on
// standard output, exit 0 unless stated, recorded on tmux 3.2a; identical
// on 3.3a. The F1, F2 and F9 transcripts were taken with other -F formats
// and placeholder tokens (TOK1, TOK2): the lookup answers keep their ids,
// names, server pid and start time and label structure, in SR-2.1's format
// with 16-hex tokens and the ad1 label form. Session creation times not in
// a transcript are the server's start time.

// Token and OtherToken are launch tokens: 16 lowercase hex characters
// (SR-3.5), a row's current token and an earlier launch's.
const (
	Token      = "3f9c1e7a2b6d4058"
	OtherToken = "a1b2c3d4e5f60718"
)

// Recorded server identities: the pid F1, F2 and F9 share, F1/F2's start
// time (srv=294/1790549182) and F9's.
const (
	serverPID     = 294
	f1ServerStart = 1790549182
	f9ServerStart = 1790549353
)

// SessionLine builds one lookup session line, without its newline:
// id, created, server pid, server start, name and label value, tab separated
// (SR-2.1, SR-3.4). An empty label is an unset one.
func SessionLine(id string, created int64, serverPID int, serverStart int64, name, label string) string {
	return strings.Join([]string{id, strconv.FormatInt(created, 10), strconv.Itoa(serverPID),
		strconv.FormatInt(serverStart, 10), name, label}, "\t")
}

// LabelValue builds the @ad_owner value as stored and listed:
// "ad1 <token> <session id> <instance id>" (SR-3.4), also SetLabel's value.
func LabelValue(token, sessionID, instanceID string) string {
	return "ad1 " + token + " " + sessionID + " " + instanceID
}

// ChainLabelValue builds the create chain's set-option -F value:
// "ad1 <token> #{session_id} <instance id with every # doubled>" (SR-3.5, F7).
func ChainLabelValue(token, instanceID string) string {
	return "ad1 " + token + " #{session_id} " + strings.ReplaceAll(instanceID, "#", "##")
}

// PaneLine builds one list-panes -a line, without its newline: session id,
// window index, pane index, pane id and pane pid, tab separated (SR-2.1).
func PaneLine(sessionID string, window, index int, paneID string, pid int) string {
	return strings.Join([]string{sessionID, strconv.Itoa(window), strconv.Itoa(index), paneID, strconv.Itoa(pid)}, "\t")
}

// CreateReplyLine builds the create's -P -F reply with its newline:
// "<$N> <server pid> <server start> <%N> <pane pid>" (SR-2.1).
func CreateReplyLine(r tmux.CreateReply) string {
	return strings.Join([]string{r.SessionID, strconv.Itoa(r.ServerPID), strconv.FormatInt(r.ServerStart, 10),
		r.PaneID, strconv.Itoa(r.PanePID)}, " ") + "\n"
}

// Listed is one session of a lookup answer: its line's fields and the label
// class the client must give it.
type Listed struct {
	ID      string
	Created int64
	Name    string
	// Label is the raw value on the line ("" when unset).
	Label string
	// Want is the classified label the client must return.
	Want tmux.Label
}

// Valid is the tmux.Label a valid label with token and instance id classifies as.
func Valid(token, instanceID string) tmux.Label {
	return tmux.Label{Kind: tmux.LabelValid, Token: token, InstanceID: instanceID}
}

// Answer builds a successful lookup entry: sessions on one server, then the
// scope-section lines (each printed with its newline).
func Answer(name, source string, serverPID int, serverStart int64, sessions []Listed, scope ...string) Entry {
	var out strings.Builder
	ans := tmux.LookupAnswer{}
	var labels []string
	for _, s := range sessions {
		out.WriteString(SessionLine(s.ID, s.Created, serverPID, serverStart, s.Name, s.Label) + "\n")
		ans.Sessions = append(ans.Sessions, tmux.Session{ID: s.ID, Created: s.Created, Name: s.Name, Label: s.Want})
		ans.ServerPID, ans.ServerStart = serverPID, serverStart
		if s.Label != "" {
			labels = append(labels, s.Label)
		}
	}
	for _, v := range scope {
		out.WriteString(v + "\n")
		if v != "" {
			ans.ScopeValue = true
		}
	}
	return Entry{Name: name, Source: source, Stdout: out.String(),
		Want: only(0, tmux.CallLookup), Lookup: ans, LabelValues: labels}
}

// malformed builds a lookup answer the client must reject as FailUnrecognized
// with a fixed description that quotes no line (SR-3.4, LFR H6).
func malformed(name, source string, labels []string, lines ...string) Entry {
	return Entry{Name: name, Source: source, Stdout: strings.Join(lines, "\n") + "\n",
		Want: only(tmux.FailUnrecognized, tmux.CallLookup), LabelValues: labels}
}

// LookupAnswers returns the one-call lookup's answers: F9a, F9b, F1, the F2
// scope cases, I2's zero-session server, and the synthesised parse-rule
// cases of LFR H6 (scope blank lines, a session line after a scope value,
// session lines disagreeing on the server, unparseable numeric fields).
func LookupAnswers() []Entry {
	own0 := LabelValue(Token, "$0", "agent-x") // F9's and F2's value, embedding $0
	own1 := LabelValue(Token, "$1", "agent-x")
	own2 := LabelValue(OtherToken, "$2", "agent-y")
	x, y := Valid(Token, "agent-x"), Valid(OtherToken, "agent-y")
	none := tmux.Label{}
	f2 := func(name, bare, keep, made, made2 string, keepWant, madeWant, made2Want tmux.Label, scope ...string) Entry {
		return Answer(name, "provenance-fresh F2 (one-call lookup form)", serverPID, f1ServerStart, []Listed{
			{ID: "$3", Created: f1ServerStart, Name: "bare", Label: bare, Want: none},
			{ID: "$0", Created: f1ServerStart, Name: "keep", Label: keep, Want: keepWant},
			{ID: "$1", Created: f1ServerStart, Name: "made", Label: made, Want: madeWant},
			{ID: "$2", Created: f1ServerStart, Name: "made2", Label: made2, Want: made2Want},
		}, scope...)
	}
	line := func(id, pid, start, label string) string {
		return strings.Join([]string{id, strconv.Itoa(f9ServerStart), pid, start, "a", label}, "\t")
	}
	pid, start := strconv.Itoa(serverPID), strconv.Itoa(f9ServerStart)
	return []Entry{
		Answer("lookup/F9a", "provenance-fresh F9a", serverPID, f9ServerStart, []Listed{
			{ID: "$0", Created: f9ServerStart, Name: "a", Label: own0, Want: x},
			{ID: "$1", Created: f9ServerStart, Name: "b", Want: none},
		}),
		Answer("lookup/F9b", "provenance-fresh F9b: a server-scope value, borrowed by every line", serverPID, f9ServerStart, []Listed{
			{ID: "$0", Created: f9ServerStart, Name: "a", Label: own0, Want: x},
			{ID: "$1", Created: f9ServerStart, Name: "b", Label: own0, Want: none},
		}, own0),
		Answer("lookup/F1", "provenance-fresh F1: made from outside, made2 from keep's pane", serverPID, f1ServerStart, []Listed{
			{ID: "$0", Created: f1ServerStart, Name: "keep", Want: none},
			{ID: "$1", Created: f1ServerStart, Name: "made", Label: own1, Want: x},
			{ID: "$2", Created: f1ServerStart, Name: "made2", Label: own2, Want: y},
		}),
		f2("lookup/F2-global", own0, own0, own1, own2, x, x, y, own0),
		f2("lookup/F2-server-or-global-window", own0, own0, own0, own0, x, none, none, own0),
		f2("lookup/F2-window-or-pane", own0, "", own1, own2, none, x, y),
		Answer("lookup/I2-zero-sessions", "E.3 I2; E.10 O4: exit-empty off, no sessions", 0, 0, nil),
		Answer("lookup/scope-blank-lines", "LFR H6: empty scope lines set no scope value", serverPID, f9ServerStart, []Listed{
			{ID: "$0", Created: f9ServerStart, Name: "a", Label: own0, Want: x},
		}, "", ""),
		malformed("lookup/session-after-scope", "LFR H6", []string{own0},
			line("$0", pid, start, own0), own0, line("$1", pid, start, "")),
		malformed("lookup/server-pid-disagrees", "LFR H6", []string{own0},
			line("$0", pid, start, own0), line("$1", "295", start, "")),
		malformed("lookup/server-start-disagrees", "LFR H6", []string{own0},
			line("$0", pid, start, own0), line("$1", pid, "1790549354", "")),
		malformed("lookup/created-unparseable", "SR-3.9", []string{own0},
			strings.Join([]string{"$0", "", pid, start, "a", own0}, "\t")),
		malformed("lookup/pid-unparseable", "SR-3.4", []string{own0},
			line("$0", "-294", start, own0)),
		malformed("lookup/start-unparseable", "SR-3.4", []string{own0},
			line("$0", pid, "17905e9", own0)),
		malformed("lookup/missing-label-field", "SR-3.4: five fields", nil,
			strings.Join([]string{"$0", start, pid, start, "a"}, "\t")),
	}
}

// PaneListings returns list-panes -a answers: P1's one pane, P2's
// base-index 1 indices, F4's grouped session listing a pane twice, and a
// line that does not parse (FailUnrecognized carrying it).
func PaneListings() []Entry {
	listing := func(name, source string, panes ...tmux.Pane) Entry {
		var out strings.Builder
		for _, p := range panes {
			out.WriteString(PaneLine(p.SessionID, p.Window, p.Index, p.ID, p.PID) + "\n")
		}
		return Entry{Name: name, Source: source, Stdout: out.String(), Want: only(0, tmux.CallListPanes), Panes: panes}
	}
	bad := strings.Join([]string{"$0", "0", "0", "%0"}, "\t")
	return []Entry{
		listing("panes/P1", "E.3 P1", tmux.Pane{SessionID: "$0", Window: 0, Index: 0, ID: "%0", PID: 295}),
		listing("panes/P2-base-index-1", "E.3 P2",
			tmux.Pane{SessionID: "$0", Window: 0, Index: 1, ID: "%0", PID: 295},
			tmux.Pane{SessionID: "$1", Window: 1, Index: 1, ID: "%1", PID: 298}),
		listing("panes/F4-grouped", "provenance-fresh F4; SR-3.7: one pane per session showing it",
			tmux.Pane{SessionID: "$0", Window: 0, Index: 0, ID: "%0", PID: 1641},
			tmux.Pane{SessionID: "$1", Window: 0, Index: 0, ID: "%0", PID: 1641}),
		{Name: "panes/four-fields", Source: "SR-2.1: five fields", Stdout: bad + "\n",
			Want: only(tmux.FailUnrecognized, tmux.CallListPanes), FirstLine: bad},
	}
}

// RecordedCreate is rn4-tmux-check F1's reply ($1, server 295 started
// 1790545195, pane pid 307); its pane id, not in F1's format, is %1.
var RecordedCreate = tmux.CreateReply{SessionID: "$1", ServerPID: 295, ServerStart: 1790545195, PaneID: "%1", PanePID: 307}

// CreateReplies returns the create's outcomes: a reply with exit 0 (rn4
// R2a), a reply with exit 1 and the chain's error on stderr (F1, G7), the
// duplicate (R2a, H2), and replies that do not parse.
func CreateReplies() []Entry {
	reply := CreateReplyLine(RecordedCreate)
	cut := strings.TrimSuffix(reply, "\n")
	short := "$1 295\n"
	return []Entry{
		{Name: "create/reply", Source: "rn4-tmux-check R2a; F1's reply values", Stdout: reply,
			Want: only(0, tmux.CallCreate), Create: RecordedCreate},
		{Name: "create/reply-then-label-failed", Source: "rn4-tmux-check F1, G7 (SR-2.1)", Stdout: reply,
			Stderr: "no such session: =n1\n", Exit: 1, ChainOnly: true,
			Want: only(tmux.FailLabel, tmux.CallCreate), Create: RecordedCreate},
		{Name: "create/duplicate", Source: "rn4-tmux-check R2a, H2; E.3 D2", Stderr: wordDuplicate + "n1\n", Exit: 1,
			Want: only(tmux.FailDuplicate, tmux.CallCreate)},
		{Name: "create/reply-cut-short", Source: "SR-2.4: a reply with no newline does not parse", Stdout: cut,
			Want: only(tmux.FailUnrecognized, tmux.CallCreate), FirstLine: cut},
		{Name: "create/reply-unparseable", Source: "SR-2.1: a reply that does not parse, exit 0", Stdout: short,
			Want: only(tmux.FailUnrecognized, tmux.CallCreate), FirstLine: strings.TrimSuffix(short, "\n")},
		{Name: "create/reply-extra-line", Source: "SR-2.1: exactly one line", Stdout: reply + reply,
			Want: only(tmux.FailUnrecognized, tmux.CallCreate), FirstLine: cut},
		{Name: "create/unparseable-nonzero", Source: "E.3 I1 wording after a partial reply", Stdout: short,
			Stderr: wordExited + "\n", Exit: 1,
			Want: only(tmux.FailUnrecognized, tmux.CallCreate), FirstLine: wordExited},
	}
}

// Captures returns capture-pane -p answers, returned unchanged: E.10 O4's
// two lines and 22 empty rows, E.3 P1's text, and an -e capture's SGR bytes.
func Captures() []Entry {
	capture := func(name, source, text string) Entry {
		return Entry{Name: name, Source: source, Stdout: text, Want: only(0, tmux.CallCapture)}
	}
	return []Entry{
		capture("capture/O4", "E.10 O4", "streams check\nstreams check\n"+strings.Repeat("\n", 22)),
		capture("capture/P1", "E.3 P1", "hello from id target\nhello from id target\n"),
		capture("capture/ansi", "synthesised: capture-pane -e keeps SGR sequences", "\x1b[1mstreams check\x1b[0m\n"),
	}
}
