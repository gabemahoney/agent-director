package api_test

// sendkeys_test.go: send-keys' text handling (empty text too, b.9o4), its
// state and relay guards (SR-7.1, SR-7.2, SR-4.2, SR-4.4) and the b.kk3
// fallen-back relay. Which pane it types into, with read-pane and pause, is
// readpane_pane_test.go's; its failed keys calls and adoption write
// sendkeys_action_test.go's.

import (
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api"
)

// errSentinel is a stand-in error for injected failures (find_missing_core_test.go uses it).
var errSentinel = errors.New("test sentinel")

// TestSendKeysText: CR bytes are stripped, LF is kept, and one Enter
// submits. Empty text (Enter only) is TestSendKeysEmptyTextPressesEnterOnly's.
func TestSendKeysText(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, text, want string }{
		{"single line", "hello", "hello"},
		{"multi-line keeps LF", "line one\nline two", "line one\nline two"},
		{"CR stripped", "ab\rcd\r\nef", "abcd\nef"},
	} {
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

// TestSendKeysEmptyTextPressesEnterOnly (b.9o4): empty text (the Enter-only
// send the keys-failure advice names, and a dialog's answer on a pending row
// with allow_pending, CSCB's approver) sends one Enter to the agent's pane by
// id, nothing typed: a line left typed is submitted once; a pending row's
// empty input submits nothing.
func TestSendKeysEmptyTextPressesEnterOnly(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name         string
		seed         func(t *testing.T, e *killEnv) killRow
		allowPending bool
		typed        string   // left in the agent's input box before the call
		want         []string // the agent's submissions
	}{
		{name: "live row, text left typed", typed: skaText, want: []string{skaText},
			seed: func(t *testing.T, e *killEnv) killRow { return e.seedRow(t, killRowSpec{}) }},
		{name: "pending row with allow_pending", allowPending: true,
			seed: func(t *testing.T, e *killEnv) killRow { return e.seedPending(t, pendingFresh, pendingOurs) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			r := tc.seed(t, e)
			in := watchPaneInput(e, r.Spawn.Identity.PaneID, false)
			in.box = tc.typed

			_, err := e.sendKeys(api.SendKeysParams{ClaudeInstanceID: r.ID, Text: "", AllowPending: tc.allowPending})

			if err != nil {
				t.Fatalf("send-keys with empty text: %v; want Enter delivered", err)
			}
			e.assertEnterOnly(t, r.Socket, r.Spawn.Identity.PaneID)
			if !slices.Equal(in.submitted, tc.want) || in.box != "" {
				t.Errorf("agent got submissions %q with %q left typed; want %q and nothing typed", in.submitted, in.box, tc.want)
			}
		})
	}
}

// relayGuardWindow is the effective relay window the relay-guard tests pass.
// A short window keeps the backdate ages in SeedUndeliverablePermissionRequest
// small while staying well clear of the safety margin.
const relayGuardWindow = time.Hour

// seedRelayRow seeds a relay-on check_permission row with its own labelled
// session and agent pane, and open permission requests for tokens (none when empty).
func seedRelayRow(t *testing.T, e *killEnv, tokens ...string) killRow {
	t.Helper()
	r := e.seedRow(t, killRowSpec{State: store.StateCheckPermission, RelayOn: true})
	if len(tokens) > 0 {
		storefix.SeedOpenPermissionRequests(t, e.st, r.ID, tokens)
	}
	return r
}

// TestRelayFallenBackIncidentRegression re-anchors the b.kk3 incident (SR-7.1):
// a relay-on row whose open request fell out of its window refuses Decide
// with ErrRelayFallenBack, and a later SendKeys delivers into the agent's pane.
func TestRelayFallenBackIncidentRegression(t *testing.T) {
	t.Parallel()
	e := newKillEnv(t)
	r := seedRelayRow(t, e, storefix.TestRequestTokenA)
	storefix.SeedUndeliverablePermissionRequest(t, e.st, e.dbPath, r.ID, storefix.TestRequestTokenA, 2*relayGuardWindow)
	now := time.Now()

	_, err := api.Decide(e.st, relayGuardWindow, now, api.DecideParams{
		ClaudeInstanceID: r.ID, RequestToken: storefix.TestRequestTokenA, Decision: "allow"})
	if !errors.Is(err, api.ErrRelayFallenBack) {
		t.Fatalf("Decide err = %v; want ErrRelayFallenBack", err)
	}
	if _, err := e.sendKeysAt(relayGuardWindow, now, api.SendKeysParams{ClaudeInstanceID: r.ID, Text: "2"}); err != nil {
		t.Fatalf("SendKeys after fallen-back: %v", err)
	}
	e.assertDelivered(t, r.Socket, r.Spawn.Identity.PaneID, "2")
}

// TestSendKeysRelayGuard pins the per-request guard on a relay-on
// check_permission row (SR-4.2, SR-7.3): it holds while any request is in its
// window, decided or not, and with no request at all (mid-insert); it releases
// once every request aged past the window (and the margin), decided or not.
// A held guard refuses with no tmux call.
func TestSendKeysRelayGuard(t *testing.T) {
	t.Parallel()
	tokens := []string{storefix.TestRequestTokenA, storefix.TestRequestTokenB, storefix.TestRequestTokenC}
	undeliverable := func(idx ...int) func(*testing.T, *killEnv, killRow) time.Time {
		return func(t *testing.T, e *killEnv, r killRow) time.Time {
			for _, i := range idx {
				storefix.SeedUndeliverablePermissionRequest(t, e.st, e.dbPath, r.ID, tokens[i], 2*relayGuardWindow)
			}
			return time.Now()
		}
	}
	decided := func(aged bool) func(*testing.T, *killEnv, killRow) time.Time {
		return func(t *testing.T, e *killEnv, r killRow) time.Time {
			if ok, err := e.st.DecidePermissionRequest(r.ID, tokens[0], "allow", "", store.WriterProcessDecide); err != nil || !ok {
				t.Fatalf("DecidePermissionRequest: updated=%v err=%v", ok, err)
			}
			row, err := e.st.GetPermissionRequest(r.ID, tokens[0])
			if err != nil {
				t.Fatalf("GetPermissionRequest: %v", err)
			}
			if aged {
				return row.CreatedAt.Add(relayGuardWindow + api.RelayKillSafetyMargin + time.Second)
			}
			return time.Now()
		}
	}
	cases := []struct {
		name    string
		tokens  []string
		arrange func(*testing.T, *killEnv, killRow) time.Time // the guard's now; nil: time.Now()
		refuse  bool
	}{
		{"no request (mid-insert) refuses", nil, nil, true},
		{"all in window refuses", tokens, nil, true},
		{"one undeliverable, the rest in window refuses", tokens, undeliverable(0), true},
		{"all but one undeliverable refuses", tokens, undeliverable(0, 1), true},
		{"all undeliverable delivers", tokens, undeliverable(0, 1, 2), false},
		{"sole request decided, still in its window, refuses", tokens[:1], decided(false), true},
		{"sole request decided, aged past window and margin, delivers", tokens[:1], decided(true), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newKillEnv(t)
			r := seedRelayRow(t, e, tc.tokens...)
			now := time.Now()
			if tc.arrange != nil {
				now = tc.arrange(t, e, r)
			}

			_, err := e.sendKeysAt(relayGuardWindow, now, api.SendKeysParams{ClaudeInstanceID: r.ID, Text: "1"})

			if tc.refuse {
				assertOneName(t, err, "ErrSendKeysWhileRelayed")
				e.assertNoTmuxCall(t)
				return
			}
			if err != nil {
				t.Fatalf("SendKeys: %v; want the guard released", err)
			}
			e.assertDelivered(t, r.Socket, r.Spawn.Identity.PaneID, "1")
		})
	}
}
