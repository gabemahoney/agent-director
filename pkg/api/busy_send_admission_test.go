package api_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// busySteerUnsupported is steer_turn's refusal while no provider offers it.
var busySteerUnsupported = api.CantSendDetails{Class: "cant_send", Reason: "steer_unsupported", ResendSafe: true, RetryWith: "new_send"}

// assertCantSendDetails fails unless err is ErrCantSend alone with exactly want.
func assertCantSendDetails(t *testing.T, err error, want api.CantSendDetails) {
	t.Helper()
	assertOneName(t, err, "ErrCantSend")
	if got := api.ErrDetails(err); !reflect.DeepEqual(got, any(want)) {
		t.Errorf("err_details = %#v; want %#v", got, want)
	}
}

// seedBusyCovered records id's current Launch and published conversation
// covered, as a covered launch would (no provider is covered in this release).
func seedBusyCovered(t *testing.T, e *killEnv, ids ...string) {
	t.Helper()
	for _, id := range ids {
		if err := apitest.SeedBusySendCoverage(e.dbPath, id, store.BusySendCovered, store.BusySendCovered); err != nil {
			t.Fatal(err)
		}
	}
}

// busyAdmit sends p through c, failing unless it is accepted; it returns the
// stored Result, which the call's JSON carries alone.
func busyAdmit(t *testing.T, c *api.Client, p api.SendKeysParams) api.DeliveryResult {
	t.Helper()
	res, err := c.SendKeys(p)
	if err != nil || res.Delivery == nil {
		t.Fatalf("SendKeys(%q) = %+v, %v; want an accepted Result", p.Text, res, err)
	}
	if got, want := jsonOf(t, res), jsonOf(t, *res.Delivery); got != want {
		t.Errorf("send-keys JSON = %s; want the Result alone %s", got, want)
	}
	if res.RequestID != res.Delivery.RequestID || res.Outcome != res.Delivery.Outcome {
		t.Errorf("legacy fields %+v disagree with the Result %+v", res, *res.Delivery)
	}
	return *res.Delivery
}

// busyWake records an empty wake file and a route for id's current Launch.
// touched reports whether the file's times changed since its last call, and
// sets them back.
func busyWake(t *testing.T, e *killEnv, id string) (path string, touched func() bool) {
	t.Helper()
	path = filepath.Join(t.TempDir(), "wake")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := apitest.SeedBusySendRoute(e.dbPath, id, "claude_hooks", path); err != nil {
		t.Fatal(err)
	}
	old := time.Unix(1_000_000_000, 0)
	touched = func() bool {
		t.Helper()
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Size() != 0 {
			t.Errorf("wake file holds %d bytes; want none", info.Size())
		}
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
		return !info.ModTime().Equal(old)
	}
	touched()
	return path, touched
}

// busyHeld is a new Held Result waiting on reason since since, as admission
// returns it.
func busyHeld(id, caller, request, reason string, since time.Time) api.DeliveryResult {
	return api.DeliveryResult{CallerUUID: caller, RequestID: request, Class: "held", Reason: reason, RetryWith: "none",
		Wait: &api.DeliveryWait{Reason: reason, Since: since}, InstanceID: id, Outcome: "not_started"}
}

// TestBusySendCoveredAdmission (SR-1.1 to SR-1.4, SR-1.6, SR-5.1, SR-5.3,
// SR-5.4, SR-5.6; M1 during-coding Result field and reason mapping): a Covered
// Agent's send types nothing and returns its Message's stored Held Result,
// which get-delivery finds unchanged: a confirmed one with its canonical or
// AD-made pair, an unconfirmed one with none. The exact bytes are kept, the
// limit counts bytes and allow_pending does nothing. A repeat of the pair,
// with other options too, returns the owner's Result and stores nothing; a
// changed text or target is id_conflict carrying it. get shows the actual Held
// Messages apart from the reservation and hand-overs, and a full reservation
// refuses held_count while repeats are still answered, after the row's
// deletion too.
func TestBusySendCoveredAdmission(t *testing.T) {
	t.Parallel()
	e := newKillEnv(t)
	c, _ := e.client(t)
	r, other := e.seedRow(t, killRowSpec{}), e.seedRow(t, killRowSpec{})
	seedBusyCovered(t, e, r.ID, other.ID)
	calls := len(e.rec.SocketCalls())
	text := "  héllo\twörld\n<tag a=\"1\">✓</tag>\n\n  "
	atLimit := strings.Repeat("é", 4000) // 8,000 bytes
	confirmed := api.SendKeysParams{InstanceID: r.ID, Text: text, ConfirmedDelivery: ptr(true),
		CallerUUID: ptr(strings.ToUpper(busyCaller)), DeliveryRequestID: "m-1", AllowPending: true}

	from := time.Now().Truncate(time.Millisecond)
	first := busyAdmit(t, c, confirmed)
	plain := busyAdmit(t, c, api.SendKeysParams{InstanceID: r.ID, Text: "plain"})
	made := busyAdmit(t, c, api.SendKeysParams{InstanceID: r.ID, Text: "made", ConfirmedDelivery: ptr(true), CallerUUID: ptr(busyCaller)})
	busyAdmit(t, c, api.SendKeysParams{InstanceID: r.ID, Text: atLimit})
	if first.Wait == nil || first.Wait.Since.Before(from) || first.Wait.Since.After(time.Now()) {
		t.Fatalf("first Result wait = %+v; want one since the send", first.Wait)
	}
	since := first.Wait.Since
	// No Launch records a route yet, so each Message waits route_unavailable.
	if want := busyHeld(r.ID, busyCaller, "m-1", "route_unavailable", since); !reflect.DeepEqual(first, want) {
		t.Errorf("confirmed Result = %+v; want %+v", first, want)
	}
	if plain.Wait != nil {
		since = plain.Wait.Since
	}
	if want := busyHeld(r.ID, "", "", "route_unavailable", since); !reflect.DeepEqual(plain, want) {
		t.Errorf("unconfirmed Result = %+v; want %+v with no pair", plain, want)
	}
	for _, key := range []string{"caller_uuid", "request_id"} {
		if raw, ok := jsonField(t, plain, key); ok {
			t.Errorf("unconfirmed Result has %s=%s", key, raw)
		}
	}
	if id, err := uuid.Parse(made.RequestID); err != nil || id.String() != made.RequestID || made.CallerUUID != busyCaller {
		t.Errorf("AD-made pair = %s %q; want %s and a canonical UUID", made.CallerUUID, made.RequestID, busyCaller)
	}
	for _, res := range []api.DeliveryResult{first, made} {
		got, err := c.GetDelivery(api.GetDeliveryParams{CallerUUID: busyCaller, DeliveryRequestID: res.RequestID})
		if err != nil || got.Lookup != "found" || got.Result == nil || !reflect.DeepEqual(*got.Result, res) {
			t.Errorf("GetDelivery(%s) = %+v, %v; want found %+v", res.RequestID, got, err, res)
		}
	}

	facts := busyFacts(t, e.dbPath, r.ID)
	pair := func(caller, request string) [2]string { return [2]string{caller, request} }
	for i, want := range []struct {
		text      string
		confirmed bool
		pair      [2]string
	}{{text, true, pair(busyCaller, "m-1")}, {"plain", false, pair("", "")}, {"made", true, pair(busyCaller, made.RequestID)}, {atLimit, false, pair("", "")}} {
		m := facts.Messages[i]
		if string(m.Text) != want.text || m.Seq != int64(i+1) || m.SendMode != "wait_turn" || m.Confirmed != want.confirmed ||
			m.Place != "held" || !m.Reserved || pair(m.CallerUUID.String, m.RequestID.String) != want.pair || m.Conversation != facts.Binding {
			t.Errorf("Message %d = %+v; want %q (%t) Held and reserved at %d, pair %v", i+1, m, want.text, want.confirmed, i+1, want.pair)
		}
	}
	held := int64(len(text) + len("plain") + len("made") + len(atLimit))
	if conv := facts.Conversations[0]; len(facts.Messages) != 4 || conv.AcceptedSeq != 4 || conv.ReservedCount != 4 || conv.ReservedBytes != held {
		t.Fatalf("conversation %+v with %d Messages; want 4 accepted and reserved, %d bytes", conv, len(facts.Messages), held)
	}

	// A repeat, even with another valid Send Mode, is the owner's Result.
	repeat := confirmed
	repeat.SendMode = ptr("steer_turn")
	if got := busyAdmit(t, c, repeat); !reflect.DeepEqual(got, first) {
		t.Errorf("repeat = %+v; want the owner's %+v", got, first)
	}
	counts := storefix.ReadBusySendCounts(t, e.dbPath)
	for name, p := range map[string]api.SendKeysParams{
		"changed text":   {InstanceID: r.ID, Text: text + " ", ConfirmedDelivery: ptr(true), CallerUUID: ptr(busyCaller), DeliveryRequestID: "m-1"},
		"changed target": {InstanceID: other.ID, Text: text, ConfirmedDelivery: ptr(true), CallerUUID: ptr(busyCaller), DeliveryRequestID: "m-1"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := c.SendKeys(p)
			assertCantSendDetails(t, err, api.CantSendDetails{Class: "cant_send", Reason: "id_conflict", ResendSafe: true, RetryWith: "new_send", Result: &first})
		})
	}
	if got := busyFacts(t, e.dbPath, r.ID); !reflect.DeepEqual(got, facts) {
		t.Errorf("busy-send core after the repeat and conflicts = %+v; want unchanged %+v", got, facts)
	}
	if got := storefix.ReadBusySendCounts(t, e.dbPath); got != counts {
		t.Errorf("records = %+v; want unchanged %+v", got, counts)
	}

	// A Message handed over and a reservation larger than the inbox leave
	// get's held_count the Messages actually Held; the handed-over prompt,
	// with no evidence the agent took it, awaits its turn start (SR-5.6).
	storefix.SeedBusySendResult(t, e.dbPath, storefix.BusySendResultSeed{InstanceID: r.ID, Pair: busyPair("issued"),
		Class: store.BusySendClassQueued, Reason: "awaiting_receipt", Transport: store.BusySendTransportWritten})
	if err := apitest.SeedBusySendReservation(e.dbPath, r.ID, 1000, held); err != nil {
		t.Fatal(err)
	}
	out, err := api.Get(e.st, api.RelayView{}, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if want := (api.DeliveryFacts{HeldCount: 4, HeldBytes: held, FirstWait: first.Wait, HandoverCount: 1, AwaitingTurnStart: 1}); out.Delivery == nil || !reflect.DeepEqual(*out.Delivery, want) {
		t.Errorf("get delivery = %+v; want %+v", out.Delivery, want)
	}
	_, err = c.SendKeys(api.SendKeysParams{InstanceID: r.ID, Text: "one more"})
	assertCantSendDetails(t, err, api.CantSendDetails{Class: "cant_send", Reason: "held_count", ResendSafe: true, RetryWith: "new_send",
		Limit: &api.CantSendLimit{Actual: 1001, Allowed: 1000}})
	if got := busyAdmit(t, c, confirmed); !reflect.DeepEqual(got, first) {
		t.Errorf("repeat with the reservation full = %+v; want the owner's %+v", got, first)
	}

	deleteCoveredRow(t, e.st, r.ID)
	found, err := c.GetDelivery(api.GetDeliveryParams{CallerUUID: busyCaller, DeliveryRequestID: "m-1"})
	if err != nil || found.Result == nil {
		t.Fatalf("GetDelivery after deletion = %+v, %v", found, err)
	}
	if got := busyAdmit(t, c, confirmed); !reflect.DeepEqual(got, *found.Result) {
		t.Errorf("repeat after deletion = %+v; want the owner's current %+v", got, *found.Result)
	}
	fresh := confirmed
	fresh.DeliveryRequestID = "m-2"
	_, err = c.SendKeys(fresh)
	assertOneName(t, err, "ErrSpawnNotFound")
	// A supplied empty Message ID is malformed before any pair is looked up,
	// for a deleted target too (code-review F1).
	fresh.DeliveryRequestID, fresh.DeliveryRequestIDSupplied = "", true
	_, err = c.SendKeys(fresh)
	assertOneName(t, err, "ErrInvalidFlags")
	if got := storefix.ReadBusySendCounts(t, e.dbPath); got.Messages != counts.Messages+1 || got.Pairs != counts.Pairs+1 {
		t.Errorf("records = %+v; want only the seeded Message added to %+v", got, counts)
	}
	if n := len(e.rec.SocketCalls()) - calls; n != 0 {
		t.Errorf("%d tmux calls; want none", n)
	}
}

// TestBusySendCoveredRefusals (SR-1.2 to SR-1.6; M1 during-coding malformed
// input): a Covered Agent's malformed send is ErrInvalidFlags with no details:
// a raw-key parameter even at its zero value, empty or invalid UTF-8 text or
// Message ID (also as a surface's decoding reported it, code-review F4), a
// supplied empty Message ID with or without a Caller UUID (an omitted one is
// made: TestBusySendCoveredAdmission; F1), a Message ID with no Caller UUID,
// caller identity on an unconfirmed send (the call's or the default's) and a
// confirmed send with no Caller UUID. An Agent Command, Steer Turn, a CR, NUL
// or other control byte and text over the byte limit are ErrCantSend with
// their reason, retry and limit. Each stores no Message or pair, takes no
// sequence, types nothing and writes only its call record.
func TestBusySendCoveredRefusals(t *testing.T) {
	// Serial: it checks every record written to the shared trail since its mark.
	e := newKillEnv(t)
	c, _ := e.client(t)
	r := resumeRow{killRow: e.seedRow(t, killRowSpec{})}
	seedBusyCovered(t, e, r.ID)
	caller := func(p api.SendKeysParams) api.SendKeysParams {
		p.ConfirmedDelivery, p.CallerUUID, p.DeliveryRequestID = ptr(true), ptr(busyCaller), "refused"
		return p
	}
	refusal := func(reason string) *api.CantSendDetails {
		return &api.CantSendDetails{Class: "cant_send", Reason: reason, RetryWith: "none"}
	}
	textBytes := refusal("text_bytes")
	textBytes.Limit = &api.CantSendLimit{Actual: 8001, Allowed: 8000}
	for _, tc := range []struct {
		name string
		p    api.SendKeysParams
		want *api.CantSendDetails // nil: ErrInvalidFlags
	}{
		{"key", api.SendKeysParams{Text: "hi", Key: "Escape"}, nil},
		{"no_enter supplied false", api.SendKeysParams{Text: "hi", RawKeysSupplied: []string{"no_enter"}}, nil},
		{"n_lines supplied 0", caller(api.SendKeysParams{Text: "hi", RawKeysSupplied: []string{"n_lines"}}), nil},
		{"empty text", api.SendKeysParams{}, nil},
		{"invalid UTF-8 text", api.SendKeysParams{Text: "a\xffb"}, nil},
		{"text a surface decoded lossily", api.SendKeysParams{Text: "a�b", MalformedUTF8: []string{"text"}}, nil},
		{"invalid UTF-8 Message ID", api.SendKeysParams{Text: "hi", ConfirmedDelivery: ptr(true), CallerUUID: ptr(busyCaller), DeliveryRequestID: "\xff"}, nil},
		{"Message ID a surface decoded lossily", caller(api.SendKeysParams{Text: "hi", MalformedUTF8: []string{"delivery_request_id"}}), nil},
		{"empty Message ID supplied", api.SendKeysParams{Text: "hi", ConfirmedDelivery: ptr(true), CallerUUID: ptr(busyCaller), DeliveryRequestIDSupplied: true}, nil},
		{"empty Message ID supplied without caller_uuid", api.SendKeysParams{Text: "hi", DeliveryRequestIDSupplied: true}, nil},
		{"Message ID without caller_uuid", api.SendKeysParams{Text: "hi", DeliveryRequestID: "m-1"}, nil},
		{"caller_uuid with confirmed_delivery false", api.SendKeysParams{Text: "hi", CallerUUID: ptr(busyCaller), ConfirmedDelivery: ptr(false)}, nil},
		{"caller_uuid under the unconfirmed default", api.SendKeysParams{Text: "hi", CallerUUID: ptr(busyCaller)}, nil},
		{"confirmed_delivery without caller_uuid", api.SendKeysParams{Text: "hi", ConfirmedDelivery: ptr(true)}, nil},
		{"slash command", caller(api.SendKeysParams{Text: " \n/compact"}), refusal("agent_command")},
		{"bang command", api.SendKeysParams{Text: "\t!ls"}, refusal("agent_command")},
		{"steer_turn", caller(api.SendKeysParams{Text: "hi", SendMode: ptr("steer_turn")}), &busySteerUnsupported},
		{"CR kept, not stripped", caller(api.SendKeysParams{Text: "line\r\nnext"}), refusal("text_kind")},
		{"NUL", api.SendKeysParams{Text: "a\x00b"}, refusal("text_kind")},
		{"escape", api.SendKeysParams{Text: "\x1b[A"}, refusal("text_kind")},
		{"8,001 bytes in 4,001 characters", api.SendKeysParams{Text: strings.Repeat("é", 4000) + "x"}, textBytes},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.p.InstanceID = r.ID
			before := e.snapshotBusy(t, tc.name, r)
			res, err := c.SendKeys(tc.p)
			outcome := "ErrInvalidFlags"
			if tc.want == nil {
				assertOneName(t, err, outcome)
				if d := api.ErrDetails(err); d != nil {
					t.Errorf("err_details = %#v; want none", d)
				}
			} else {
				outcome = "ErrCantSend"
				assertCantSendDetails(t, err, *tc.want)
			}
			if !reflect.DeepEqual(res, api.SendKeysResult{}) {
				t.Errorf("refused send returned %+v", res)
			}
			e.assertBusyRefusedAs(t, before, "ad.send_keys.called", outcome)
		})
	}
}

// TestBusySendHeldWhileBusy (SR-1.1, SR-2.6, SR-3.9): a Covered Agent's send
// is admitted Held whatever the row's state, where today's send-keys refuses:
// on a running turn it waits turn_running, behind an open Dialog dialog and
// on an ended row launch_ended. The row and its permission request are
// untouched, and neither input path is reached: the native-input backend is
// never opened and tmux is never called (assertNoInputPath).
func TestBusySendHeldWhileBusy(t *testing.T) {
	t.Parallel()
	ne := newNativeSubmissionEnv(t)
	e := ne.killEnv
	working := e.seedRow(t, killRowSpec{State: store.StateWorking})
	dialog := e.seedRow(t, killRowSpec{State: store.StateCheckPermission, RelayOn: true})
	e.seedRequest(t, dialog.ID)
	ended := e.seedResumable(t, time.Hour, agentGone)
	seedBusyCovered(t, e, working.ID, dialog.ID, ended.ID)
	for _, id := range []string{working.ID, dialog.ID} {
		busyWake(t, e, id) // a recorded route, so the row's own state decides the wait
	}
	calls := len(e.rec.SocketCalls())
	for _, tc := range []struct{ id, wait string }{
		{working.ID, "turn_running"}, {dialog.ID, "dialog"}, {ended.ID, "launch_ended"},
	} {
		cols := e.columns(t, tc.id)
		requests, err := e.st.PermissionRequestsForSpawn(tc.id)
		if err != nil {
			t.Fatal(err)
		}
		if res := coveredSend(t, ne, api.SendKeysParams{InstanceID: tc.id, Text: "while busy"}); res.Class != "held" || res.Wait == nil || res.Wait.Reason != tc.wait {
			t.Errorf("Result on the %s row = %+v; want Held, waiting %s", tc.wait, res, tc.wait)
		}
		e.assertRowUnchanged(t, tc.id, cols)
		if after, err := e.st.PermissionRequestsForSpawn(tc.id); err != nil || !reflect.DeepEqual(after, requests) {
			t.Errorf("permission requests after the send = %+v, %v; want unchanged %+v", after, err, requests)
		}
	}
	if n := len(e.rec.SocketCalls()) - calls; n != 0 {
		t.Errorf("%d tmux calls; want no pane input", n)
	}
	assertNoInputPath(t, ne)
	if got := storefix.ReadBusySendCounts(t, e.dbPath); got.Messages != 3 || got.InputAttempts != 0 {
		t.Errorf("records = %+v; want three Messages and no input journal attempt", got)
	}
}

// TestBusySendWakeTouch (SR-7.2, SR-2.6): only a newly committed admission
// touches the current Launch's recorded wake file, and only its times: a
// repeat, a refusal and the reads do not, and a missing file is neither
// created nor a failure. With a route recorded, a Message on a waiting row
// waits readiness_unknown and the next waits behind it by its pair.
func TestBusySendWakeTouch(t *testing.T) {
	t.Parallel()
	e := newKillEnv(t)
	c, _ := e.client(t)
	r := e.seedRow(t, killRowSpec{})
	seedBusyCovered(t, e, r.ID)
	wake, touched := busyWake(t, e, r.ID)
	send := func(id, text string) api.SendKeysParams {
		return api.SendKeysParams{InstanceID: r.ID, Text: text, ConfirmedDelivery: ptr(true), CallerUUID: ptr(busyCaller), DeliveryRequestID: id}
	}
	first := busyAdmit(t, c, send("w-1", "first"))
	if woke := touched(); !woke || first.Wait == nil || first.Wait.Reason != "readiness_unknown" {
		t.Errorf("first admission: wake touched %t, wait %+v; want touched, readiness_unknown", woke, first.Wait)
	}
	second := busyAdmit(t, c, send("w-2", "second"))
	if woke := touched(); !woke || second.Wait == nil || second.Wait.Reason != "earlier_message" ||
		!reflect.DeepEqual(second.Wait.Earlier, &api.DeliveryPair{CallerUUID: busyCaller, RequestID: "w-1"}) {
		t.Errorf("second admission: wake touched %t, wait %+v; want touched, behind w-1", woke, second.Wait)
	}
	busyAdmit(t, c, send("w-1", "first"))
	if touched() {
		t.Error("a repeat touched the wake file")
	}
	_, err := c.SendKeys(send("w-3", "bad\rtext"))
	assertOneName(t, err, "ErrCantSend")
	if _, err := c.Get(r.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := c.GetDelivery(api.GetDeliveryParams{CallerUUID: busyCaller, DeliveryRequestID: "w-1"}); err != nil {
		t.Fatal(err)
	}
	if touched() {
		t.Error("a refusal or a read touched the wake file")
	}
	if err := os.Remove(wake); err != nil {
		t.Fatal(err)
	}
	busyAdmit(t, c, send("w-4", "no wake file"))
	if _, err := os.Stat(wake); !os.IsNotExist(err) {
		t.Errorf("wake file after admission with none = %v; want none created", err)
	}
	if got := storefix.ReadBusySendCounts(t, e.dbPath); got.Messages != 3 || got.Pairs != 3 {
		t.Errorf("records = %+v; want the three admitted Messages", got)
	}
}
