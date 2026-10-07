package api_test

// sendkeys_test.go: send-keys' guards and their precedence (the unusable-name
// refusal after the state and relay guards, SR-3.2), its text
// handling, and which pane it types into (SR-7.1, SR-7.2, SR-3.3, SR-3.7,
// SR-4.4): always the agent's pane by its pane id on the row's recorded
// socket, text then Enter, never a session name, a neighbour's session or a
// session holding the name. On the pane-verb fixture
// (pane_verb_fixture_test.go) and tmuxfix.Recorder.

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// errSentinel is a stand-in error for injected failures (find_missing_core_test.go uses it).
var errSentinel = errors.New("test sentinel")

// skDeliver runs send-keys on r with text and fails unless it typed text
// into r's agent pane by id, then pressed Enter there (assertDelivered).
func skDeliver(t *testing.T, e *killEnv, r killRow, text string) {
	t.Helper()
	if _, err := e.sendKeys(api.SendKeysParams{ClaudeInstanceID: r.ID, Text: text}); err != nil {
		t.Fatalf("SendKeys: %v", err)
	}
	e.assertDelivered(t, r.Socket, r.Spawn.Identity.PaneID, text)
}

// TestSendKeysText: CR bytes are stripped, LF is kept, and one Enter
// submits. Empty text (Enter only) is TestSendKeysEmptyTextPressesEnterOnly's.
func TestSendKeysText(t *testing.T) {
	t.Parallel()
	cases := []struct{ name, text, want string }{
		{"single line", "hello", "hello"},
		{"multi-line keeps LF", "line one\nline two", "line one\nline two"},
		{"CR stripped", "ab\rcd\r\nef", "abcd\nef"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			r := e.seedRow(t, killRowSpec{})
			if _, err := e.sendKeys(api.SendKeysParams{ClaudeInstanceID: r.ID, Text: tc.text}); err != nil {
				t.Fatalf("SendKeys: %v", err)
			}
			e.assertDelivered(t, r.Socket, r.Spawn.Identity.PaneID, tc.want)
		})
	}
}

// TestSendKeysSpawnNotFound: an unknown id is ErrSpawnNotFound with no tmux call.
func TestSendKeysSpawnNotFound(t *testing.T) {
	t.Parallel()
	e := newKillEnv(t)
	e.seedRow(t, killRowSpec{})
	_, err := e.sendKeys(api.SendKeysParams{ClaudeInstanceID: "absent", Text: "hi"})
	if !errors.Is(err, store.ErrSpawnNotFound) {
		t.Fatalf("err = %v; want ErrSpawnNotFound", err)
	}
	e.assertNoTmuxCall(t)
}

// skRelay is a guard case's relay_mode and permission request.
type skRelay int

const (
	skRelayOff      skRelay = iota // relay_mode off
	skRelayNoGuard                 // relay_mode on, the state is not check_permission
	skRelayHeld                    // relay_mode on, one request still in its window
	skRelayReleased                // relay_mode on, the one request past its window
)

// TestSendKeysGuards: the state and relay guards refuse before any tmux call
// though the row's own session is up; a row they pass gets the keys by pane id.
func TestSendKeysGuards(t *testing.T) {
	t.Parallel()
	type guardCase struct {
		name  string
		state string
		allow bool
		relay skRelay
		want  error // nil: delivered
	}
	var cases []guardCase
	for _, state := range []string{store.StateEnded, store.StateMissing} {
		for _, allow := range []bool{false, true} {
			cases = append(cases, guardCase{fmt.Sprintf("%s allow_pending=%v", state, allow), state, allow,
				skRelayOff, api.ErrSpawnNotInteractive})
		}
	}
	cases = append(cases, guardCase{"pending without allow_pending", store.StatePending, false, skRelayOff,
		api.ErrSpawnNotInteractive})
	for _, state := range []string{store.StateWaiting, store.StateWorking, store.StateAskUser, store.StateCheckPermission} {
		for _, allow := range []bool{false, true} {
			cases = append(cases, guardCase{fmt.Sprintf("%s allow_pending=%v", state, allow), state, allow,
				skRelayOff, nil})
		}
	}
	cases = append(cases,
		guardCase{"relay on, waiting", store.StateWaiting, false, skRelayNoGuard, nil},
		guardCase{"relay held on check_permission", store.StateCheckPermission, false, skRelayHeld,
			api.ErrSendKeysWhileRelayed},
		guardCase{"relay held on check_permission, allow_pending", store.StateCheckPermission, true, skRelayHeld,
			api.ErrSendKeysWhileRelayed},
		guardCase{"relay released on check_permission", store.StateCheckPermission, false, skRelayReleased, nil},
	)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newKillEnv(t)
			r := e.seedRow(t, killRowSpec{State: tc.state, RelayOn: tc.relay != skRelayOff})
			if tc.relay == skRelayHeld || tc.relay == skRelayReleased {
				storefix.SeedOpenPermissionRequests(t, e.st, r.ID, []string{storefix.TestRequestTokenA})
			}
			if tc.relay == skRelayReleased {
				storefix.SeedUndeliverablePermissionRequest(t, e.st, e.dbPath, r.ID, storefix.TestRequestTokenA,
					2*sendKeysWindow())
			}
			// The requests are stored at wall-clock time, so the guard is judged against it.
			_, err := e.sendKeysAt(sendKeysWindow(), time.Now(), api.SendKeysParams{ClaudeInstanceID: r.ID,
				Text: "1", AllowPending: tc.allow})
			if tc.want == nil {
				if err != nil {
					t.Fatalf("SendKeys: %v", err)
				}
				e.assertDelivered(t, r.Socket, r.Spawn.Identity.PaneID, "1")
				return
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v; want %v", err, tc.want)
			}
			e.assertNoTmuxCall(t)
		})
	}
}

// TestSendKeysGuardStoreReadError: a failed relay-guard read on a relay-on
// check_permission row fails the send with that error and no tmux call.
func TestSendKeysGuardStoreReadError(t *testing.T) {
	t.Parallel()
	e := newKillEnv(t)
	r := e.seedRow(t, killRowSpec{State: store.StateCheckPermission, RelayOn: true})
	storeErr := errors.New("permission_requests read exploded")
	e.store.failPermissionRequests(storeErr)
	_, err := e.sendKeys(api.SendKeysParams{ClaudeInstanceID: r.ID, Text: "1"})
	if !errors.Is(err, storeErr) {
		t.Fatalf("err = %v; want %v", err, storeErr)
	}
	e.assertNoTmuxCall(t)
	eval, refuse, gerr := api.EvaluateRelayGuardForTest(e.store, sendKeysWindow(), e.clock.Now(), r.Spawn, r.ID)
	if !errors.Is(gerr, storeErr) || eval != api.GuardErrorEval || refuse {
		t.Errorf("guard = %q, refuse %v, err %v; want %q, false, %v", eval, refuse, gerr, api.GuardErrorEval, storeErr)
	}
}

// TestSendKeysRenamedSession: the renamed session's agent pane gets the keys
// by id; a session now holding the recorded name never does.
func TestSendKeysRenamedSession(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		holder func(r killRow) *tmuxfix.SeedSession
	}{
		{"renamed", func(killRow) *tmuxfix.SeedSession { return nil }},
		{"renamed, unlabelled session holds the name", func(r killRow) *tmuxfix.SeedSession {
			return &tmuxfix.SeedSession{Name: r.Name}
		}},
		{"renamed, another store's session holds the name", func(r killRow) *tmuxfix.SeedSession {
			return &tmuxfix.SeedSession{Name: r.Name, Label: r.otherStore(newToken()),
				Panes: []tmuxfix.SeedPane{{AdPane: newToken()}}}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			r := e.seedRow(t, killRowSpec{NoSession: true})
			e.seedSession(t, &r, tmuxfix.WithRowSessionName("renamed-"+r.ID))
			if h := tc.holder(r); h != nil {
				e.seedOther(t, r.Socket, *h)
			}
			skDeliver(t, e, r, "hi")
		})
	}
}

// TestSendKeysNeighbours (AC-LKP-01/02/03): a row with no session gets the gone
// error and never sends to a prefix-, name- or 8-character-id-sharing neighbour.
func TestSendKeysNeighbours(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name         string
		xID, yID     string
		xName, yName string
	}{
		{"name prefix", "", "", "proj-abc", "proj-abc123"},
		{"same name, held by the other row's session", "", "", "proj-same", "proj-same"},
		{"ids share first 8 characters, same-named folders", "abcd1234-x-row", "abcd1234-y-row", "work", "work"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			x := e.seedRow(t, killRowSpec{ID: tc.xID, NoSession: true,
				Opts: []apitest.SpawnOption{apitest.WithTmuxSessionName(tc.xName)}})
			y := e.seedRow(t, killRowSpec{ID: tc.yID, Opts: []apitest.SpawnOption{apitest.WithTmuxSessionName(tc.yName)}})
			_, err := e.sendKeys(api.SendKeysParams{ClaudeInstanceID: x.ID, Text: "hi"})
			if !errors.Is(err, tmux.ErrTmuxSendKeys) {
				t.Fatalf("err = %v; want ErrTmuxSendKeys", err)
			}
			apitest.AssertDescription(t, err.Error(), apitest.DescPaneGone(apitest.PaneGone{
				Verb: apitest.PaneSendKeys, InstanceID: x.ID, Name: x.Name}), y.ID, y.Token)
			e.assertPaneCalls(t, tmux.CallLookup)
		})
	}
}

// TestSendKeysStoredNames (AC-LKP-09): a row whose recorded name holds $ or \
// is found by its label under tmux's stored form and sent to by pane id.
func TestSendKeysStoredNames(t *testing.T) {
	t.Parallel()
	for _, n := range tmuxfix.StoredNames() {
		if !n.LabelByID || strings.ContainsAny(n.Raw, ".:") { // '.' and ':' names are unusable (Epic 19)
			continue
		}
		t.Run(n.Raw, func(t *testing.T) {
			t.Parallel()
			e := newKillEnv(t)
			r := e.seedRow(t, killRowSpec{Opts: []apitest.SpawnOption{apitest.WithTmuxSessionName(n.Raw)}})
			if r.Session.Name != n.Stored {
				t.Fatalf("seeded session name %q; want the stored form %q", r.Session.Name, n.Stored)
			}
			skDeliver(t, e, r, "hi")
		})
	}
}

// TestSendKeysUnusableName (SR-3.2, FR1 C7(k)): after the state and relay
// guards, an unusable recorded name is ErrInternal; their refusals keep their answer.
func TestSendKeysUnusableName(t *testing.T) {
	t.Parallel()
	state := func(s string) func(*killEnv) killRowSpec {
		return func(*killEnv) killRowSpec { return killRowSpec{State: s} }
	}
	pending := func(k pendingKind) func(*killEnv) killRowSpec {
		return func(e *killEnv) killRowSpec { return e.pendingSpec(k, pendingOurs) }
	}
	cases := []struct {
		name    string
		spec    func(*killEnv) killRowSpec
		allow   bool
		relay   bool // relay_mode on, one request still in its window
		fixture string
		want    string
	}{
		{"waiting", state(store.StateWaiting), false, false, "empty", "ErrInternal"},
		{"working", state(store.StateWorking), true, false, "newline", "ErrInternal"},
		{"ask_user", state(store.StateAskUser), false, false, "escape", "ErrInternal"},
		{"check_permission, relay off", state(store.StateCheckPermission), false, false, "colon", "ErrInternal"},
		{"pending fresh spawn, allow_pending", pending(pendingFresh), true, false, "pre-b.gqe default name", "ErrInternal"},
		{"pending resumed row, allow_pending", pending(pendingResumed), true, false, "invalid UTF-8", "ErrInternal"},
		{"pending without allow_pending", pending(pendingFresh), false, false, "pre-b.gqe default name", "ErrSpawnNotInteractive"},
		{"ended", state(store.StateEnded), false, false, "pre-b.gqe default name", "ErrSpawnNotInteractive"},
		{"ended, allow_pending", state(store.StateEnded), true, false, "dot and newline", "ErrSpawnNotInteractive"},
		{"missing", state(store.StateMissing), false, false, "empty", "ErrSpawnNotInteractive"},
		{"missing, allow_pending", state(store.StateMissing), true, false, "colon", "ErrSpawnNotInteractive"},
		{"relay held on check_permission", func(*killEnv) killRowSpec {
			return killRowSpec{State: store.StateCheckPermission, RelayOn: true}
		}, false, true, "pre-b.gqe default name", "ErrSendKeysWhileRelayed"},
	}
	for _, tc := range cases {
		t.Run(tc.name+"/"+tc.fixture, func(t *testing.T) {
			e := newKillEnv(t)
			f := unusableFixture(t, tc.fixture)
			r := e.seedUnusableRow(t, tc.spec(e), f)
			if tc.relay {
				storefix.SeedOpenPermissionRequests(t, e.st, r.ID, []string{storefix.TestRequestTokenA})
			}
			before := e.columns(t, r.ID)

			// The requests are stored at wall-clock time, so the guard is judged against it.
			_, err := e.sendKeysAt(sendKeysWindow(), time.Now(), api.SendKeysParams{ClaudeInstanceID: r.ID,
				Text: "1", AllowPending: tc.allow})

			assertOneName(t, err, tc.want)
			if tc.want == "ErrInternal" && err != nil {
				apitest.AssertDescription(t, err.Error(), f.desc, r.Token)
			}
			e.assertNoTmuxCall(t)
			e.assertRowUnchanged(t, r.ID, before)
		})
	}
}

// TestSendKeysRecordedSocket (AC-LKP-19): with TMUX and TMUX_TMPDIR naming
// another server, every call names the row's recorded socket.
func TestSendKeysRecordedSocket(t *testing.T) {
	// Serial: it sets TMUX, TMUX_TMPDIR with t.Setenv.
	e := newKillEnv(t)
	r := e.seedRow(t, killRowSpec{})
	// No commas: TMUX's socket field ends at one.
	elsewhere := filepath.Join(t.TempDir(), "elsewhere")
	t.Setenv("TMUX", elsewhere+",4242,0")
	t.Setenv("TMUX_TMPDIR", t.TempDir())
	e.seedOther(t, elsewhere, tmuxfix.SeedSession{Name: r.Name, Label: r.current(),
		Panes: []tmuxfix.SeedPane{{AdPane: r.Token}}})
	skDeliver(t, e, r, "hi")
	for _, c := range e.rec.SocketCalls() {
		if c.Socket != r.Socket {
			t.Errorf("%v on socket %q; want the recorded %q", c.Call, c.Socket, r.Socket)
		}
	}
}
