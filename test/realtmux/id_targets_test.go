package realtmux_test

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
)

// Id targeting on real tmux (SRD SR-2.1, SR-20.7; Appendix E.9 T2a and I1).
// Every action call of the client targets a session id ($N) or a pane id
// (%N). These tests prove such a target reaches only its own session or pane:
// never another one, never a session whose name looks like the id (T2a), and
// never a session that replaced an ended one. Ids are never reused within one
// server and restart at $0/%0 only after the server exits (I1).
//
// A failure's typed kind is asserted only where the replay catalogue records
// one for the call; the reply text is never spelled.

// idtCapLines is the history the client's captures ask for.
const idtCapLines = 50

// idtPane is one pane as the raw runner sees it, with its session's name and
// label and the pane's visible text without its trailing empty rows (a pane
// that grows when a neighbour ends gains only empty rows). Label is compared,
// never printed.
type idtPane struct {
	SessionID, SessionName, PaneID, PanePID, Label, Capture string
}

// String prints the pane; of the label it prints only whether one is set.
func (p idtPane) String() string {
	return redact(fmt.Sprintf("{%s %q %s pid %s labelled %v capture %q}",
		p.SessionID, p.SessionName, p.PaneID, p.PanePID, p.Label != "", p.Capture))
}

// idtWorld reads every pane of the bound server, keyed by pane id.
func idtWorld(t testing.TB, rt *realTmux) map[string]idtPane {
	t.Helper()
	out := rt.must(t, "list-panes", "-a", "-F", "#{session_id}\t#{session_name}\t#{pane_id}\t#{pane_pid}")
	world := map[string]idtPane{}
	labels := map[string]string{}
	for _, line := range strings.Split(strings.TrimSuffix(out, "\n"), "\n") {
		f := strings.Split(line, "\t")
		if len(f) != 4 {
			t.Fatalf("list-panes line has %d fields, want 4: %q", len(f), line)
		}
		label, ok := labels[f[0]]
		if !ok {
			label = rt.label(t, f[0])
			labels[f[0]] = label
		}
		world[f[2]] = idtPane{SessionID: f[0], SessionName: f[1], PaneID: f[2], PanePID: f[3], Label: label,
			Capture: strings.TrimRight(rt.must(t, "capture-pane", "-p", "-t", f[2]), "\n")}
	}
	return world
}

// idtDump prints a world in pane-id order, labels hidden.
func idtDump(world map[string]idtPane) string {
	var b strings.Builder
	for _, id := range idtPaneIDs(world) {
		b.WriteString("\n    " + world[id].String())
	}
	return b.String()
}

// idtSame fails unless every listed pane is in after exactly as in before.
func idtSame(t testing.TB, what string, before, after map[string]idtPane, paneIDs ...string) {
	t.Helper()
	for _, id := range paneIDs {
		b, ok := before[id]
		if !ok {
			t.Fatalf("%s: pane %s was not in the world before the calls:%s", what, id, idtDump(before))
		}
		if a, ok := after[id]; !ok || a != b {
			t.Errorf("%s: pane %s changed\n  before: %s\n  after:  %s (present %v)\n  world after:%s",
				what, id, b, a, ok, idtDump(after))
		}
	}
}

// idtPaneIDs returns the world's pane ids in order.
func idtPaneIDs(world map[string]idtPane) []string {
	ids := make([]string, 0, len(world))
	for id := range world {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

// idtHasSession reports whether any pane of the world belongs to sessionID.
func idtHasSession(world map[string]idtPane, sessionID string) bool {
	for _, p := range world {
		if p.SessionID == sessionID {
			return true
		}
	}
	return false
}

// idtIDShapedName returns the catalogue's stored name spelled as a session
// id (sigil '$') or a pane id (sigil '%'): the sigil and decimal digits only.
func idtIDShapedName(t testing.TB, sigil byte) tmuxfix.StoredName {
	t.Helper()
	for _, n := range tmuxfix.StoredNames() {
		if len(n.Raw) > 1 && n.Raw[0] == sigil && strings.Trim(n.Raw[1:], "0123456789") == "" {
			return n
		}
	}
	t.Fatalf("the replay catalogue has no stored name spelled as a %c id", sigil)
	return tmuxfix.StoredName{}
}

// idtNum returns the number of a "$N" or "%N" id.
func idtNum(t testing.TB, id string, sigil byte) int {
	t.Helper()
	if len(id) < 2 || id[0] != sigil {
		t.Fatalf("id %q does not start with %c", id, sigil)
	}
	n, err := strconv.Atoi(id[1:])
	if err != nil || n < 0 {
		t.Fatalf("id %q has no number: %v", id, err)
	}
	return n
}

// idtFails checks err is a *tmux.CallError for call. When entry names a
// catalogue reply it also checks the Failure the catalogue records for that
// call; "" asserts only the error's type and call (no entry exists).
func idtFails(t testing.TB, rt *realTmux, err error, call tmux.Call, entry string) {
	t.Helper()
	var ce *tmux.CallError
	if !errors.As(err, &ce) {
		t.Errorf("%s call: error %s, want a *tmux.CallError", call, describe(err))
		return
	}
	if ce.Call != call {
		t.Errorf("%s call: CallError.Call = %q", call, ce.Call)
	}
	if entry == "" {
		return
	}
	e := tmuxfix.Find(tmuxfix.Replies(rt.Socket), entry)
	want, ok := e.Want[call]
	if !ok || want == 0 {
		t.Fatalf("catalogue entry %s records no failure for the %s call", entry, call)
	}
	if ce.Failure != want {
		t.Errorf("%s call: Failure = %s, want %s (catalogue %s)", call, ce.Failure, want, entry)
	}
}

// idtAllFail makes every id-targeted call with a session id and a pane id
// that exist on no session or pane of the server, and checks each fails:
// session kill and label by sessionID; pane kill, text (with Enter) and
// capture by paneID.
func idtAllFail(t testing.TB, rt *realTmux, cl *tmux.Client, sessionID, paneID string) {
	t.Helper()
	idtFails(t, rt, cl.KillSessionID(rt.Socket, sessionID), tmux.CallKillSession, "reply/cant-find-session")
	idtFails(t, rt, cl.SetLabel(rt.Socket, sessionID, paneID, newToken(t), newInstanceID("agent"), tmuxfix.StoreID), tmux.CallSetLabel, "reply/no-such-session")
	idtFails(t, rt, cl.KillPane(rt.Socket, paneID), tmux.CallKillPane, "") // no catalogue entry
	idtFails(t, rt, cl.SendKeysPane(rt.Socket, paneID, "idt-never-typed", true), tmux.CallSendText, "reply/cant-find-pane")
	got, err := cl.CapturePaneID(rt.Socket, paneID, idtCapLines, false)
	idtFails(t, rt, err, tmux.CallCapture, "reply/cant-find-pane")
	if got != "" {
		t.Errorf("failed CapturePaneID returned text %q", redact(got))
	}
}

// idtSendAndSee types a fresh marker into paneID by id and waits until the
// client's capture of that pane shows it (the pane's tty echoes it). It is
// both a positive check and a barrier: the server has handled every earlier
// call on the socket.
func idtSendAndSee(t testing.TB, rt *realTmux, cl *tmux.Client, paneID string) string {
	t.Helper()
	marker := "idt-" + newToken(t)[:8]
	if err := cl.SendKeysPane(rt.Socket, paneID, marker, false); err != nil {
		t.Fatalf("SendKeysPane to %s: %s", paneID, describe(err))
	}
	var last string
	waitFor(t, "text sent to "+paneID+" in its capture", func() bool {
		out, err := cl.CapturePaneID(rt.Socket, paneID, idtCapLines, false)
		last = out + describe(err)
		return err == nil && strings.Contains(out, marker)
	}, func() string { return fmt.Sprintf("capture %q", redact(last)) })
	return marker
}

// idtSplit adds a second pane (a teammate) running cmd to sessionID with the
// raw runner and tracks it; it returns the new pane's id and pid.
func idtSplit(t testing.TB, rt *realTmux, sessionID string, cmd []string) (string, int) {
	t.Helper()
	args := append([]string{"split-window", "-d", "-t", sessionID, "-P", "-F", "#{pane_id}\t#{pane_pid}", "--"}, cmd...)
	out := rt.must(t, args...)
	f := strings.Split(strings.TrimSuffix(out, "\n"), "\t")
	if len(f) != 2 {
		t.Fatalf("split-window reply: %q", out)
	}
	pid, err := strconv.Atoi(f[1])
	if err != nil {
		t.Fatalf("split-window pane pid: %q", out)
	}
	rt.trackPane(pid)
	return f[0], pid
}

// TestIdTargetReachesOnlyItsTarget: with two production-created sessions, each
// call by id (text, capture, label, pane and session kill) reaches only its target.
func TestIdTargetReachesOnlyItsTarget(t *testing.T) {
	rt := newRealTmux(t)
	a := rt.mustCreate(t, createSpec{})
	b := rt.mustCreate(t, createSpec{})
	aSecond, aSecondPID := idtSplit(t, rt, a.Reply.SessionID, stubCommand())
	cl := newClient()

	bCapture, err := cl.CapturePaneID(rt.Socket, b.Reply.PaneID, idtCapLines, false)
	if err != nil {
		t.Fatalf("CapturePaneID of B's pane: %s", describe(err))
	}
	before := idtWorld(t, rt)

	marker := idtSendAndSee(t, rt, cl, a.Reply.PaneID)
	after := idtWorld(t, rt)
	idtSame(t, "text sent to A's first pane", before, after, b.Reply.PaneID, aSecond)
	if got, err := cl.CapturePaneID(rt.Socket, b.Reply.PaneID, idtCapLines, false); err != nil || got != bCapture || strings.Contains(got, marker) {
		t.Errorf("CapturePaneID of B's pane after the send = %q, %s; want unchanged %q", got, describe(err), bCapture)
	}

	token, id := newToken(t), newInstanceID("relabel")
	if err := cl.SetLabel(rt.Socket, a.Reply.SessionID, a.Reply.PaneID, token, id, tmuxfix.StoreID); err != nil {
		t.Fatalf("SetLabel on A: %s", describe(err))
	}
	if rt.label(t, a.Reply.SessionID) != tmuxfix.LabelValue(token, a.Reply.SessionID, id, tmuxfix.StoreID) {
		t.Errorf("A's label is not the value SetLabel wrote (value not printed)")
	}
	if rt.label(t, b.Reply.SessionID) != before[b.Reply.PaneID].Label {
		t.Errorf("B's label changed when A was labelled by id (value not printed)")
	}
	before = idtWorld(t, rt)

	if err := cl.KillPane(rt.Socket, a.Reply.PaneID); err != nil {
		t.Fatalf("KillPane of A's first pane: %s", describe(err))
	}
	waitPidGone(t, a.Reply.PanePID)
	after = idtWorld(t, rt)
	if _, ok := after[a.Reply.PaneID]; ok {
		t.Errorf("pane %s is still listed after KillPane:%s", a.Reply.PaneID, idtDump(after))
	}
	if len(after) != 2 {
		t.Errorf("KillPane ended more than its pane; world:%s", idtDump(after))
	}
	idtSame(t, "KillPane of A's first pane", before, after, aSecond, b.Reply.PaneID)
	if pidGone(aSecondPID) || pidGone(b.Reply.PanePID) {
		t.Errorf("a pane process other than the target ended: A's second %s, B's %s",
			procState(aSecondPID), procState(b.Reply.PanePID))
	}
	before = after

	if err := cl.KillSessionID(rt.Socket, b.Reply.SessionID); err != nil {
		t.Fatalf("KillSessionID of B: %s", describe(err))
	}
	waitPidGone(t, b.Reply.PanePID)
	after = idtWorld(t, rt)
	if idtHasSession(after, b.Reply.SessionID) {
		t.Errorf("session %s is still listed after KillSessionID:%s", b.Reply.SessionID, idtDump(after))
	}
	if !slices.Equal(idtPaneIDs(after), []string{aSecond}) {
		t.Errorf("after KillSessionID of B the world is not just A's remaining pane:%s", idtDump(after))
	}
	idtSame(t, "KillSessionID of B", before, after, aSecond)
	if pidGone(aSecondPID) {
		t.Errorf("A's remaining pane process ended with B: %s", procState(aSecondPID))
	}
}

// TestIdTargetNoNameFallback (E.9 T2a): calls by an absent $/% id fail and never
// reach a session named like that id.
func TestIdTargetNoNameFallback(t *testing.T) {
	dollar := idtIDShapedName(t, '$')
	percent := idtIDShapedName(t, '%')
	rt := newRealTmux(t)
	sd := rt.startSession(t, dollar.Raw)
	sp := rt.startSession(t, percent.Raw)
	barrier := rt.startSession(t, "")
	before := idtWorld(t, rt)

	for id, p := range before {
		if p.SessionID == dollar.Raw || id == percent.Raw {
			t.Fatalf("precondition: a session or pane already has the id a name spells:%s", idtDump(before))
		}
	}
	if before[sd.PaneID].SessionName != dollar.Stored || before[sp.PaneID].SessionName != percent.Stored {
		t.Fatalf("precondition: sessions are not listed under the catalogue's stored names:%s", idtDump(before))
	}

	cl := newClient()
	idtAllFail(t, rt, cl, dollar.Raw, percent.Raw)

	idtSendAndSee(t, rt, cl, barrier.PaneID)
	after := idtWorld(t, rt)
	idtSame(t, "calls by the ids the names spell", before, after, sd.PaneID, sp.PaneID)
	if len(after) != 3 {
		t.Errorf("a pane was added or ended; world:%s", idtDump(after))
	}
	if pidGone(sd.PanePID) || pidGone(sp.PanePID) {
		t.Errorf("a named session's pane process ended: %s, %s", procState(sd.PanePID), procState(sp.PanePID))
	}
	if after[sd.PaneID].Label != "" {
		t.Errorf("the session named like a session id got a label (value not printed)")
	}
}

// TestIdTargetReplacedSession: calls by an ended session's ids fail and leave
// the session that took its name untouched.
func TestIdTargetReplacedSession(t *testing.T) {
	rt := newRealTmux(t)
	keep := rt.startSession(t, "") // keeps the server running between the two
	old := rt.mustCreate(t, createSpec{})
	rt.must(t, "kill-session", "-t", old.Reply.SessionID)
	waitPidGone(t, old.Reply.PanePID)
	repl := rt.mustCreate(t, createSpec{Name: old.Name})
	if repl.Reply.SessionID == old.Reply.SessionID || repl.Reply.PaneID == old.Reply.PaneID {
		t.Fatalf("precondition: the replacement reuses an ended id: old %s %s, new %s %s",
			old.Reply.SessionID, old.Reply.PaneID, repl.Reply.SessionID, repl.Reply.PaneID)
	}
	before := idtWorld(t, rt)
	if before[repl.Reply.PaneID].SessionName != old.Name {
		t.Fatalf("precondition: the replacement is not listed under the ended session's name:%s", idtDump(before))
	}

	cl := newClient()
	idtAllFail(t, rt, cl, old.Reply.SessionID, old.Reply.PaneID)

	idtSendAndSee(t, rt, cl, keep.PaneID)
	after := idtWorld(t, rt)
	idtSame(t, "calls by the ended session's ids", before, after, repl.Reply.PaneID)
	if len(after) != 2 {
		t.Errorf("a pane was added or ended; world:%s", idtDump(after))
	}
	if pidGone(repl.Reply.PanePID) {
		t.Errorf("the replacement's pane process ended: %s", procState(repl.Reply.PanePID))
	}
}

// TestIdTargetMonotonic (I1): ids rise within a server, killed ones included;
// a new server on the same socket starts again at $0 and %0.
func TestIdTargetMonotonic(t *testing.T) {
	rt := newRealTmux(t)
	first := rt.mustCreate(t, createSpec{}) // keeps the server running
	maxSession := idtNum(t, first.Reply.SessionID, '$')
	maxPane := idtNum(t, first.Reply.PaneID, '%')
	for round := range 3 {
		c := rt.mustCreate(t, createSpec{})
		s, p := idtNum(t, c.Reply.SessionID, '$'), idtNum(t, c.Reply.PaneID, '%')
		if s <= maxSession || p <= maxPane {
			t.Errorf("round %d: new session %s pane %s, want ids above $%d and %%%d",
				round, c.Reply.SessionID, c.Reply.PaneID, maxSession, maxPane)
		}
		maxSession, maxPane = max(maxSession, s), max(maxPane, p)
		if c.Reply.ServerPID != first.Reply.ServerPID {
			t.Fatalf("round %d: create reached another server (pid %d, want %d)", round, c.Reply.ServerPID, first.Reply.ServerPID)
		}
		if err := newClient().KillSessionID(rt.Socket, c.Reply.SessionID); err != nil {
			t.Fatalf("round %d: KillSessionID: %s", round, describe(err))
		}
		waitPidGone(t, c.Reply.PanePID)
	}

	rt.must(t, "kill-server")
	waitPidGone(t, first.Reply.ServerPID)
	waitPidGone(t, first.Reply.PanePID)
	again := rt.mustCreate(t, createSpec{})
	if again.Reply.SessionID != "$0" || again.Reply.PaneID != "%0" {
		t.Errorf("after the server exited, the new server's first session %s pane %s, want $0 and %%0",
			again.Reply.SessionID, again.Reply.PaneID)
	}
}
