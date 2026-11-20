package api_test

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/resumedelegate"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/sqliteobserve"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// busyCantSendAdvice is ErrCantSend's agent_not_covered advice (SR-9.2).
const busyCantSendAdvice = "make the same call without them"

// TestAdviceFollow_E22_CantSendSameCallWithout: E22 "make the same call
// without them". A send-keys naming force for an uncovered agent (Codex) is
// refused; the same send-keys without it sends its key.
func TestAdviceFollow_E22_CantSendSameCallWithout(t *testing.T) {
	t.Parallel()
	e := newKillEnv(t)
	e.rowAgent = "codex"
	r := e.seedRow(t, killRowSpec{})
	p := api.SendKeysParams{InstanceID: r.ID, Key: "Escape", Force: ptr(false)}
	_, err := e.sendKeys(p)
	adviceAssertAdvice(t, err, api.ErrCantSend, busyCantSendAdvice)
	p.Force = nil
	if _, err := e.sendKeys(p); err != nil {
		t.Fatalf("the same send-keys without force: %v", err)
	}
	var keys []string
	for _, c := range e.rec.SocketCalls() {
		if c.Call == tmux.CallSendKey {
			keys = append(keys, c.Key)
		}
	}
	if len(keys) != 1 || keys[0] != "Escape" {
		t.Errorf("keys sent = %q; want the one Escape", keys)
	}
}

// TestAdviceFollow_B17_CantSendSameResumeWithout: B17 "make the same call
// without them". A resume setting an Agent Default for an uncovered agent
// (Codex) is refused; the same resume without it moves the row to pending.
func TestAdviceFollow_B17_CantSendSameResumeWithout(t *testing.T) {
	t.Parallel()
	e := newKillEnv(t)
	e.rowAgent = "codex"
	r := e.seedResumableRow(t, e.resumableSpec(rlkSettled(e), agentNotRecorded))
	cfg := config.Default()
	cfg.Tmux = e.cfg
	_, err := api.Resume(e.st, e.rec, e.pc, cfg, e.storeID, e.clock.Now, nil,
		api.ResumeParams{InstanceID: r.ID, ConfirmedDelivery: ptr(false)})
	adviceAssertAdvice(t, err, api.ErrCantSend, busyCantSendAdvice)
	if _, err := e.resumeWith(e.st, r.ID); err != nil {
		t.Fatalf("the same resume without confirmed_delivery: %v", err)
	}
	if row, err := e.st.GetSpawn(r.ID); err != nil || row.State != store.StatePending {
		t.Errorf("row after the resume = %q, %v; want pending", row.State, err)
	}
}

// TestAdviceFollow_A32_CantSendSameLaunchWithout: A32 "make the same call
// without them". A spawn, and a make-template naming its agent, that set an
// Agent Default for an uncovered agent are refused; the same calls without it
// launch the agent and save the template.
func TestAdviceFollow_A32_CantSendSameLaunchWithout(t *testing.T) {
	// Serial: newSpawnEnv sets HOME, TMUX and TMUX_TMPDIR with t.Setenv.
	e := newSpawnEnv(t)
	p := api.SpawnParams{Agent: "codex", CWD: t.TempDir(), NoPreTrust: true, SendMode: ptr("wait_turn")}
	_, err := e.c.Spawn(p)
	adviceAssertAdvice(t, err, api.ErrCantSend, busyCantSendAdvice)
	p.SendMode = nil
	res, err := e.c.Spawn(p)
	if err != nil {
		t.Fatalf("the same spawn without send_mode: %v", err)
	}
	if row, err := e.c.Get(res.InstanceID); err != nil || row.Agent != "codex" {
		t.Errorf("Get(%s) = %+v, %v; want the codex row", res.InstanceID, row, err)
	}
	m := api.MakeTemplateParams{Name: "follow", Agent: "codex", ConfirmFromTranscripts: ptr(false)}
	_, err = e.c.MakeTemplate(m)
	adviceAssertAdvice(t, err, api.ErrCantSend, busyCantSendAdvice)
	m.ConfirmFromTranscripts = nil
	if _, err := e.c.MakeTemplate(m); err != nil {
		t.Fatalf("the same make-template without confirm_from_transcripts: %v", err)
	}
	if tf, err := config.LoadTemplate("follow"); err != nil || tf.Agent != "codex" {
		t.Errorf("saved template = %+v, %v; want codex's", tf, err)
	}
}

// busySteerUnsupported is steer_turn's refusal when the stored Launch offers
// only Wait Turn (or a creating version's own table cannot offer Steer).
var busySteerUnsupported = api.CantSendDetails{Class: "cant_send", Reason: "steer_unsupported", ResendSafe: true, RetryWith: "new_send"}

// busyAdviceRow is a Client and a covered live row of agent ("" is Claude)
// for a covered send's literal-follow test.
func busyAdviceRow(t *testing.T, agent string) (*killEnv, *api.Client, string) {
	t.Helper()
	e := newKillEnv(t)
	e.rowAgent = agent
	c, _ := e.client(t)
	r := e.seedRow(t, killRowSpec{})
	seedBusyCovered(t, e, r.ID)
	return e, c, r.ID
}

// busyFollowAccepted fails unless the follow-up p is accepted Held and the
// store then holds want Messages.
func busyFollowAccepted(t *testing.T, e *killEnv, c *api.Client, p api.SendKeysParams, want int) api.DeliveryResult {
	t.Helper()
	res := busyAdmit(t, c, p)
	if res.Class != "held" {
		t.Errorf("follow-up Result = %+v; want Held", res)
	}
	if got := storefix.ReadBusySendCounts(t, e.dbPath).Messages; got != want {
		t.Errorf("%d Messages after the follow-up; want %d", got, want)
	}
	return res
}

// TestAdviceFollow_E25_SteerUnsupportedSendWaitTurn: a genuinely Wait-only
// stored Launch refuses Steer, then accepts the literal Wait Turn follow-up.
// E25: "send it with send_mode wait_turn"
func TestAdviceFollow_E25_SteerUnsupportedSendWaitTurn(t *testing.T) {
	t.Parallel()
	e, c, id := busyAdviceRow(t, "codex")
	if err := apitest.SeedBusySendCoverage(e.dbPath, id, store.BusySendCovered, store.BusySendCovered, storefix.BusySendWaitOnlyCapabilities()); err != nil {
		t.Fatal(err)
	}
	p := api.SendKeysParams{InstanceID: id, Text: "steer", SendMode: ptr("steer_turn")}
	_, err := c.SendKeys(p)
	adviceAssertAdvice(t, err, api.ErrCantSend, "send it with send_mode wait_turn")
	assertCantSendDetails(t, err, busySteerUnsupported)
	p.SendMode = ptr("wait_turn")
	busyFollowAccepted(t, e, c, p, 1)
}

// TestAdviceFollow_B18_SteerUnsupportedResumeWaitTurn: a covered resume with Steer Turn of a provider with no capability row (a covered Codex row, as for E25), then the same resume with Wait Turn.
// B18: "use wait_turn"
func TestAdviceFollow_B18_SteerUnsupportedResumeWaitTurn(t *testing.T) {
	// Serial: scratch HOME and trail. The covering runtime supplies B18;
	// this older runtime cannot perform a local covered-Codex resume (SR-9.1).
	e := newDelegateEnv(t)
	e.covering(t)
	e.run(t, func(req resumedelegate.Request) ([]byte, bool, error) {
		var p api.ResumeParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			t.Fatal(err)
		}
		if p.SendMode != nil && *p.SendMode == "steer_turn" {
			details, _ := json.Marshal(busySteerUnsupported)
			b, _ := json.Marshal(resumedelegate.Response{ResponseFormat: 1, Error: &resumedelegate.Error{
				Name: "ErrCantSend", Description: "steer_turn is unsupported; use wait_turn", Details: details}})
			return b, true, nil
		}
		if p.SendMode == nil || *p.SendMode != "wait_turn" {
			t.Fatalf("advice not followed: %+v", p)
		}
		return delegateResult(e.row.ID), true, nil
	})
	check := e.untouched(t)
	p := api.ResumeParams{InstanceID: e.row.ID, SendMode: ptr("steer_turn")}
	_, err := e.c.Resume(p)
	adviceAssertAdvice(t, err, api.ErrCantSend, "use wait_turn")
	b, _ := json.Marshal(api.ErrDetails(err))
	want, _ := json.Marshal(busySteerUnsupported)
	if string(b) != string(want) {
		t.Fatalf("details %s; want %s", b, want)
	}
	p.SendMode = ptr("wait_turn")
	if res, err := e.c.Resume(p); err != nil || res.InstanceID != e.row.ID {
		t.Fatalf("the same resume with send_mode wait_turn: %v", err)
	}
	if len(e.calls) != 2 {
		t.Fatalf("delegation calls = %d; want the original and literal retry", len(e.calls))
	}
	check()
}

// TestAdviceFollow_E26_IDConflictNewMessageID: a covered send reusing an owned pair for other text, then under a new delivery_request_id.
// E26: "send this Message only as a new send under a new delivery_request_id"
func TestAdviceFollow_E26_IDConflictNewMessageID(t *testing.T) {
	t.Parallel()
	e, c, id := busyAdviceRow(t, "")
	p := api.SendKeysParams{InstanceID: id, Text: "first", ConfirmedDelivery: ptr(true), CallerUUID: ptr(busyCaller), DeliveryRequestID: "c-1"}
	owner := busyAdmit(t, c, p)
	p.Text = "second"
	_, err := c.SendKeys(p)
	adviceAssertAdvice(t, err, api.ErrCantSend, "send this Message only as a new send under a new delivery_request_id")
	p.DeliveryRequestID = "c-2"
	if res := busyFollowAccepted(t, e, c, p, 2); res.RequestID != "c-2" {
		t.Errorf("follow-up pair = %q; want c-2", res.RequestID)
	}
	got, err := c.GetDelivery(api.GetDeliveryParams{CallerUUID: busyCaller, DeliveryRequestID: "c-1"})
	if err != nil || got.Result == nil || !reflect.DeepEqual(*got.Result, owner) {
		t.Errorf("owner after the follow-up = %+v, %v; want unchanged %+v", got, err, owner)
	}
}

// TestAdviceFollow_E27_EmptyMessageIDOmitIt: a covered send with an empty delivery_request_id, then the same send without it.
// E27: "omit it to have one made"
func TestAdviceFollow_E27_EmptyMessageIDOmitIt(t *testing.T) {
	t.Parallel()
	e, c, id := busyAdviceRow(t, "")
	p := api.SendKeysParams{InstanceID: id, Text: "made", ConfirmedDelivery: ptr(true), CallerUUID: ptr(busyCaller), DeliveryRequestIDSupplied: true}
	_, err := c.SendKeys(p)
	adviceAssertAdvice(t, err, api.ErrInvalidFlags, "omit it to have one made")
	p.DeliveryRequestIDSupplied = false
	res := busyFollowAccepted(t, e, c, p, 1)
	if made, err := uuid.Parse(res.RequestID); err != nil || made.String() != res.RequestID {
		t.Errorf("made delivery_request_id = %q; want a canonical UUID", res.RequestID)
	}
}

// TestAdviceFollow_E28_HeldCountRetryAfterDrain: a covered send refused for a full reservation, then the same send once Held Messages drain.
// E28: "send it again once Held Messages drain"
func TestAdviceFollow_E28_HeldCountRetryAfterDrain(t *testing.T) {
	t.Parallel()
	e, c, id := busyAdviceRow(t, "")
	if err := apitest.SeedBusySendReservation(e.dbPath, id, 1000, 1000*int64(len("earlier"))); err != nil {
		t.Fatal(err)
	}
	p := api.SendKeysParams{InstanceID: id, Text: "later"}
	_, err := c.SendKeys(p)
	adviceAssertAdvice(t, err, api.ErrCantSend, "send it again once Held Messages drain")
	// The drain: custody leaves the reservation, as delivered Messages do.
	if err := apitest.SeedBusySendReservation(e.dbPath, id, 0, 0); err != nil {
		t.Fatal(err)
	}
	busyFollowAccepted(t, e, c, p, 1)
}

// busyLoseCommit makes c's next busy-send admission COMMIT land and lose its
// acknowledgement. Its stop fails the test unless that happened.
func busyLoseCommit(t *testing.T, c *api.Client) (stop func()) {
	t.Helper()
	var admitting, lost atomic.Bool
	end, err := api.ObserveClientSQLiteForTest(c, func(d driver.Driver) driver.Driver {
		return sqliteobserve.Wrap(d, sqliteobserve.Hooks{
			Before: func(_ context.Context, q string) error {
				if strings.Contains(q, "INSERT INTO busy_send_messages") {
					admitting.Store(true)
				}
				return nil
			},
			AfterCommit: func(context.Context) error {
				if admitting.Swap(false) && !lost.Swap(true) {
					return errors.New("lost COMMIT acknowledgement")
				}
				return nil
			},
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	return func() {
		t.Helper()
		if err := end(context.Background()); err != nil {
			t.Fatal(err)
		}
		if !lost.Load() {
			t.Fatal("no admission COMMIT lost its acknowledgement")
		}
	}
}

// busyUncertain sends p through c with its admission's COMMIT acknowledgement
// lost, and checks the answer: an unnamed error, never
// ErrSubmissionUnconfirmed, with no details or Result, and no wake.
func busyUncertain(t *testing.T, c *api.Client, p api.SendKeysParams, touched func() bool) error {
	t.Helper()
	stop := busyLoseCommit(t, c)
	res, err := c.SendKeys(p)
	stop()
	assertOneName(t, err, "")
	if api.ErrDetails(err) != nil || !reflect.DeepEqual(res, api.SendKeysResult{}) {
		t.Errorf("uncertain admission = %+v, details %+v; want a bare error", res, api.ErrDetails(err))
	}
	if touched() {
		t.Error("an uncertain admission touched the wake file")
	}
	return err
}

// TestAdviceFollow_E23_AdmissionUncertainRepeatPair: a covered send whose admission COMMIT outcome is unknown; get-delivery, then the same send.
// E23: "send it again with caller_uuid <uuid> and delivery_request_id "<id>", or call get-delivery with them, to read its Result; a repeat never accepts it twice"
func TestAdviceFollow_E23_AdmissionUncertainRepeatPair(t *testing.T) {
	t.Parallel()
	for _, made := range []bool{false, true} {
		t.Run(fmt.Sprintf("AD-made Message ID %t", made), func(t *testing.T) {
			t.Parallel()
			e := newKillEnv(t)
			c, _ := e.client(t)
			r := e.seedRow(t, killRowSpec{})
			seedBusyCovered(t, e, r.ID)
			_, touched := busyWake(t, e, r.ID)
			p := api.SendKeysParams{InstanceID: r.ID, Text: "uncertain", ConfirmedDelivery: ptr(true), CallerUUID: ptr(busyCaller)}
			if !made {
				p.DeliveryRequestID = "u-1"
			}

			err := busyUncertain(t, c, p, touched)

			id := p.DeliveryRequestID
			if made {
				m := regexp.MustCompile(`delivery_request_id ("[^"]+")`).FindStringSubmatch(err.Error())
				if m == nil {
					t.Fatalf("description %q names no delivery_request_id", err)
				}
				id, _ = strconv.Unquote(m[1])
			}
			adviceAssertPhrase(t, err, "send it again with caller_uuid "+busyCaller+" and delivery_request_id "+strconv.Quote(id)+
				", or call get-delivery with them, to read its Result; a repeat never accepts it twice")
			got, err := c.GetDelivery(api.GetDeliveryParams{CallerUUID: busyCaller, DeliveryRequestID: id})
			if err != nil || got.Lookup != "found" || got.Result == nil {
				t.Fatalf("get-delivery as advised = %+v, %v; want found", got, err)
			}
			p.DeliveryRequestID = id
			if again := busyAdmit(t, c, p); !reflect.DeepEqual(again, *got.Result) || again.RequestID != id {
				t.Errorf("the same send as advised = %+v; want the found %+v", again, *got.Result)
			}
			if touched() {
				t.Error("the repeat touched the wake file")
			}
			if counts := storefix.ReadBusySendCounts(t, e.dbPath); counts.Messages != 1 || counts.Pairs != 1 {
				t.Errorf("records = %+v; want the one accepted Message", counts)
			}
		})
	}
}

// TestAdviceFollow_E24_UnconfirmedUncertainUseConfirmed: an unconfirmed covered send whose admission COMMIT outcome is unknown is not resent; the next send, confirmed under the caller's own pair, is recoverable after the same loss.
// E24: "it must not be resent automatically; use confirmed delivery with your own caller_uuid and delivery_request_id where a send must be recoverable"
func TestAdviceFollow_E24_UnconfirmedUncertainUseConfirmed(t *testing.T) {
	t.Parallel()
	e := newKillEnv(t)
	c, _ := e.client(t)
	r := e.seedRow(t, killRowSpec{})
	seedBusyCovered(t, e, r.ID)
	_, touched := busyWake(t, e, r.ID)

	err := busyUncertain(t, c, api.SendKeysParams{InstanceID: r.ID, Text: "uncertain"}, touched)

	adviceAssertPhrase(t, err, "an unconfirmed send has no identity, so nothing can resolve this one (get's delivery counts are aggregates) and it "+
		"must not be resent automatically; use confirmed delivery with your own caller_uuid and delivery_request_id where a send must be recoverable")
	// Not resent: the landed Message is the one copy.
	if got := storefix.ReadBusySendCounts(t, e.dbPath); got.Messages != 1 || got.Pairs != 0 {
		t.Errorf("records after no resend = %+v; want the one unconfirmed Message", got)
	}
	p := api.SendKeysParams{InstanceID: r.ID, Text: "recoverable", ConfirmedDelivery: ptr(true), CallerUUID: ptr(busyCaller), DeliveryRequestID: "own-1"}
	busyUncertain(t, c, p, touched)
	got, err := c.GetDelivery(api.GetDeliveryParams{CallerUUID: busyCaller, DeliveryRequestID: "own-1"})
	if err != nil || got.Lookup != "found" || got.Result == nil {
		t.Fatalf("the confirmed send's pair after the same loss = %+v, %v; want found", got, err)
	}
	if again := busyAdmit(t, c, p); !reflect.DeepEqual(again, *got.Result) {
		t.Errorf("its repeat = %+v; want the found %+v", again, *got.Result)
	}
	if got := storefix.ReadBusySendCounts(t, e.dbPath); got.Messages != 2 || got.Pairs != 1 {
		t.Errorf("records = %+v; want two Messages, one pair", got)
	}
}

// busyFailForce makes the next force attempt's insert on c's store fail, after
// the send's admission committed. Its stop fails the test unless it did.
func busyFailForce(t *testing.T, c *api.Client, cause error) (stop func()) {
	t.Helper()
	var failed atomic.Bool
	end, err := api.ObserveClientSQLiteForTest(c, func(d driver.Driver) driver.Driver {
		return sqliteobserve.Wrap(d, sqliteobserve.Hooks{Before: func(_ context.Context, q string) error {
			if strings.Contains(q, "INSERT INTO busy_send_forces") && !failed.Swap(true) {
				return cause
			}
			return nil
		}})
	})
	if err != nil {
		t.Fatal(err)
	}
	return func() {
		t.Helper()
		if err := end(context.Background()); err != nil {
			t.Fatal(err)
		}
		if !failed.Load() {
			t.Fatal("no force attempt was inserted")
		}
	}
}

// busyAcceptedForceError checks the Go value, exact public details and trail
// after known acceptance, independently of the force's current stored state.
func busyAcceptedForceError(t *testing.T, f *forceAgent, res api.SendKeysResult, err error, want api.DeliveryResult) {
	t.Helper()
	assertOneName(t, err, "ErrSendAcceptedForceFailed")
	if !errors.Is(err, api.ErrSendAcceptedForceFailed) || errors.Is(err, api.ErrStoreBusy) || errors.Is(err, api.ErrSubmissionUnconfirmed) {
		t.Errorf("accepted force failure classification = %v", err)
	}
	wantGo := api.SendKeysResult{Delivery: &want, CallerUUID: want.CallerUUID, Class: want.Class, RequestID: want.RequestID,
		Outcome: want.Outcome, SessionID: want.SessionID, RecordID: want.RecordID, RecordSHA256: want.RecordSHA256}
	if !reflect.DeepEqual(res, wantGo) {
		t.Errorf("Go result = %+v (delivery %+v); want %+v", res, res.Delivery, wantGo)
	}
	if got := api.ErrDetails(err); !reflect.DeepEqual(got, api.SendAcceptedForceFailedDetails{Result: want}) {
		t.Errorf("err_details = %#v; want exactly the accepted Result %+v", got, want)
	}
	// An explicit wire oracle pins nulls, omissions and the absence of force,
	// text, raw evidence, paths or a duplicate pair outside result.
	wire := map[string]any{"class": want.Class, "final": want.Final, "reason": want.Reason, "resend_safe": want.ResendSafe,
		"retry_with": want.RetryWith, "wait": want.Wait, "forced": want.Forced, "instance_id": want.InstanceID,
		"launch_token": want.LaunchToken, "evidence_kind": want.EvidenceKind, "outcome": want.Outcome}
	if want.CallerUUID != "" {
		wire["caller_uuid"], wire["request_id"] = want.CallerUUID, want.RequestID
	}
	for key, value := range map[string]string{"session_id": want.SessionID, "record_id": want.RecordID, "record_sha256": want.RecordSHA256} {
		if value != "" {
			wire[key] = value
		}
	}
	var gotJSON, wantJSON any
	if err := json.Unmarshal([]byte(jsonOf(t, api.ErrDetails(err))), &gotJSON); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(jsonOf(t, map[string]any{"result": wire})), &wantJSON); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotJSON, wantJSON) {
		t.Errorf("details JSON = %s; want %s", jsonOf(t, gotJSON), jsonOf(t, wantJSON))
	}
	called := sktCalled(t, f.id)
	if len(called) == 0 || called[len(called)-1]["outcome"] != "ErrSendAcceptedForceFailed" {
		t.Errorf("send trail = %+v; want ErrSendAcceptedForceFailed", called)
	}
}

// busyForceFailed fails the insert after real admission/replay. A new send
// wakes ordinary delivery; a repeat does not create another admission wake.
func busyForceFailed(t *testing.T, f *forceAgent, p api.SendKeysParams, owner *api.DeliveryResult, cause error) (api.DeliveryResult, error) {
	t.Helper()
	stop := busyFailForce(t, f.c, cause)
	res, err := f.c.SendKeys(p)
	stop()
	if res.Delivery == nil || res.Delivery.Wait == nil {
		t.Fatalf("accepted Go result = %+v, %v; want Held", res, err)
	}
	caller, request := "", ""
	if p.CallerUUID != nil {
		caller, request = busyCaller, p.DeliveryRequestID
		if request == "" {
			request = res.Delivery.RequestID
			if id, parseErr := uuid.Parse(request); parseErr != nil || id.String() != request {
				t.Errorf("AD-generated request_id = %q; want canonical UUID", request)
			}
		}
	}
	want := busyHeld(f.id, caller, request, "readiness_unknown", res.Delivery.Wait.Since)
	if owner != nil {
		want = *owner
	}
	busyAcceptedForceError(t, f, res, err, want)
	pairs := 0
	if caller != "" {
		pairs = 1
	}
	if got := storefix.ReadBusySendCounts(t, f.e.dbPath); got.Messages != 1 || got.Pairs != pairs || got.Forces != 0 {
		t.Errorf("records = %+v; want the one accepted Message and no force", got)
	}
	if touched := f.touched(); touched != (owner == nil) {
		t.Errorf("admission wake = %t; want %t", touched, owner == nil)
	}
	return want, err
}

// TestAdviceFollow_E33_ForceFailedRepeatPair: a confirmed covered send with force whose force fails after its Message was accepted; get-delivery, then the same send.
// E33: "do not send it as new: repeat it with the same pair to force again, or read its Result with get-delivery"
func TestAdviceFollow_E33_ForceFailedRepeatPair(t *testing.T) {
	t.Parallel()
	for _, request := range []string{"f-1", ""} {
		t.Run("request="+request, func(t *testing.T) {
			t.Parallel()
			e := newKillEnv(t)
			f := newForceAgent(t, e)
			p := api.SendKeysParams{InstanceID: f.id, Text: "forced", ConfirmedDelivery: ptr(true), CallerUUID: ptr(busyCaller),
				DeliveryRequestID: request, Force: ptr(true)}
			accepted, err := busyForceFailed(t, f, p, nil, errors.New("injected force insert failure"))
			adviceAssertPhrase(t, err, "the Message was accepted (caller_uuid "+busyCaller+", delivery_request_id "+strconv.Quote(accepted.RequestID)+"), but its force failed")
			adviceAssertPhrase(t, err, "do not send it as new: repeat it with the same pair to force again, or read its Result with get-delivery")
			// Recover the generated identity from the structured Result alone.
			got, err := f.c.GetDelivery(api.GetDeliveryParams{CallerUUID: accepted.CallerUUID, DeliveryRequestID: accepted.RequestID})
			if err != nil || got.Lookup != "found" || got.Result == nil || !reflect.DeepEqual(*got.Result, accepted) {
				t.Fatalf("get-delivery as advised = %+v, %v; want %+v", got, err, accepted)
			}
			p.DeliveryRequestID = accepted.RequestID
			// The force-phase busy error is diagnostic only, after the pair's
			// owner was found. The prior admission wake was drained above.
			e.clock.Advance(time.Second)
			_, err = busyForceFailed(t, f, p, got.Result, store.ErrStoreBusy)
			adviceAssertPhrase(t, err, store.ErrStoreBusy.Error())
			again, err := f.c.SendKeys(p)
			if err != nil || again.Delivery == nil || !reflect.DeepEqual(*again.Delivery, *got.Result) || again.Force == nil ||
				!reflect.DeepEqual(again.Force.CarriedPairs, []api.DeliveryPair{{CallerUUID: busyCaller, RequestID: accepted.RequestID}}) {
				t.Errorf("the same send as advised = %+v, %v; want the found Result and a force carrying it", again, err)
			}
			if got := storefix.ReadBusySendCounts(t, e.dbPath); got.Messages != 1 || got.Pairs != 1 || got.Forces != 1 {
				t.Errorf("records = %+v; want the one Message, accepted once, and its force", got)
			}
		})
	}
}

// TestAdviceFollow_E34_ForceFailedForceDelivery: an unconfirmed covered send with force whose force fails after its Message was accepted; force-delivery, never the send again.
// E34: "do not send it again: call force-delivery to force the agent's Held Messages"
func TestAdviceFollow_E34_ForceFailedForceDelivery(t *testing.T) {
	t.Parallel()
	e := newKillEnv(t)
	f := newForceAgent(t, e)

	_, err := busyForceFailed(t, f, api.SendKeysParams{InstanceID: f.id, Text: "forced", ConfirmedDelivery: ptr(false), Force: ptr(true)}, nil,
		errors.New("injected force insert failure"))

	adviceAssertPhrase(t, err, "the Message was accepted, but its force failed")
	adviceAssertPhrase(t, err, "do not send it again: call force-delivery to force the agent's Held Messages")
	// The Held Message is claimed, then (no forced wake yet) returned unissued.
	if got, _ := f.force(t); got.Outcome != "refused" || got.Reason != "not_issued" {
		t.Errorf("force-delivery as advised = %+v; want the Held Message claimed, then refused not_issued", got)
	}
	if got := storefix.ReadBusySendCounts(t, e.dbPath); got.Messages != 1 || got.Forces != 1 {
		t.Errorf("records = %+v; want the one Message and the advised force", got)
	}
}

// TestAdviceFollow_E35_ForceEndFailedFinishByCommand: a force call whose caller cancels it mid-wait, so its end is never recorded; get-force, then an explicit send-keys or force-delivery once its recorded deadline has passed.
// E35: "get-force reads it, and the agent's next eligible event or an explicit send-keys or force-delivery finishes it once its recorded deadline has passed; do not treat it as refused"
//
// SR-3.2, SR-4.3; M1 G3 NoOrphanForceClaim; MI decision 10. The error is
// unnamed, never a refusal or ErrStoreBusy (which promises nothing was
// stored), and names the stored force: get-force and get read it running
// (reason "", no end), holding a and u. Past the deadline a refused send with
// force forces and releases nothing; an accepted send, a repeat of a's pair
// or force-delivery finishes it through the store's one release path, and
// force-delivery then makes its own new force.
func TestAdviceFollow_E35_ForceEndFailedFinishByCommand(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		explicit func(t *testing.T, f *forceAgent)
		forces   int // force attempts stored after the command
	}{
		{"an accepted send", func(t *testing.T, f *forceAgent) {
			busyAdmit(t, f.c, api.SendKeysParams{InstanceID: f.id, Text: "next"})
		}, 1},
		{"a repeat of a's pair", func(t *testing.T, f *forceAgent) {
			busyAdmit(t, f.c, api.SendKeysParams{InstanceID: f.id, Text: "hello", ConfirmedDelivery: ptr(true), CallerUUID: ptr(busyCaller),
				DeliveryRequestID: "a"})
		}, 1},
		{"force-delivery", func(t *testing.T, f *forceAgent) {
			if got, _ := f.force(t); got.Outcome != "refused" || got.Reason != "not_issued" ||
				!reflect.DeepEqual(got.CarriedPairs, []api.DeliveryPair{{CallerUUID: busyCaller, RequestID: "a"}}) {
				t.Errorf("force-delivery after the release = %+v; want its own force of a and u, refused not_issued", got)
			}
		}, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newKillEnv(t)
			f := newForceAgent(t, e)
			f.send(t, "a", "hello")
			f.send(t, "", "unconfirmed")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			entry := e.clock.Now()

			_, err := api.ForceDeliveryForTest(e.st, ctx, e.clock.Now, func(d time.Duration) {
				if e.clock.Now().Sub(entry) >= 400*time.Millisecond {
					cancel()
				}
				e.clock.Advance(d)
			}, nil, api.ForceDeliveryParams{InstanceID: f.id})

			assertOneName(t, err, "")
			if api.ErrDetails(err) != nil {
				t.Errorf("err_details = %+v; want none", api.ErrDetails(err))
			}
			adviceAssertPhrase(t, err, "get-force reads it, and the agent's next eligible event or an explicit send-keys or force-delivery "+
				"finishes it once its recorded deadline has passed; do not treat it as refused")
			named := regexp.MustCompile(`force ([0-9a-f-]{36}) is stored and ran`).FindStringSubmatch(err.Error())
			if named == nil {
				t.Fatalf("description %q names no stored force", err)
			}
			if !f.touched() {
				t.Error("the cancelled call never notified the route after its claim")
			}
			read := func() api.ForceResult {
				t.Helper()
				found, err := f.c.GetForce(api.GetForceParams{ForceID: named[1]})
				if err != nil || found.ForceResult == nil {
					t.Fatalf("get-force as advised = %+v, %v; want found", found, err)
				}
				return *found.ForceResult
			}
			running := read()
			want := api.ForceResult{ForceID: named[1], Outcome: "running", StartedAt: time.UnixMilli(entry.UnixMilli()).UTC(),
				CarriedPairs: []api.DeliveryPair{{CallerUUID: busyCaller, RequestID: "a"}}, CarriedResults: running.CarriedResults}
			if !reflect.DeepEqual(running, want) || len(running.CarriedResults) != 1 {
				t.Errorf("get-force = %+v; want %+v with a's Result", running, want)
			}
			if row, err := f.c.Get(f.id); err != nil || row.Delivery == nil || !reflect.DeepEqual(row.Delivery.Force, &running) {
				t.Errorf("get delivery = %+v, %v; want force %+v", row.Delivery, err, running)
			}

			e.clock.Advance(entry.Add(15*time.Second + time.Millisecond).Sub(e.clock.Now()))
			counts := storefix.ReadBusySendCounts(t, e.dbPath)
			_, err = f.c.SendKeys(api.SendKeysParams{InstanceID: f.id, Text: "bad\rtext", Force: ptr(true)})
			assertOneName(t, err, "ErrCantSend")
			if got := storefix.ReadBusySendCounts(t, e.dbPath); got != counts || !reflect.DeepEqual(read(), running) || f.touched() {
				t.Errorf("after a refused send with force: records %+v, force %+v; want %+v and the force still running, no wake", got, read(), counts)
			}

			f.assertReleased(t, named[1], tc.explicit)

			if got := storefix.ReadBusySendCounts(t, e.dbPath).Forces; got != tc.forces {
				t.Errorf("%d force attempts; want %d", got, tc.forces)
			}
		})
	}
}

// PM2(b), option A (t2.xis.ec.9e): send acceptance survives a failed call-end
// write. Unlike an insert failure, this leaves a real owned force to recover.
// Follow E35's explicit-send advice once, without duplicating its command matrix.
func TestAdviceFollow_E35_SendForceEndFailedFinishByCommand(t *testing.T) {
	t.Parallel()
	e := newKillEnv(t)
	f := newForceAgent(t, e)
	f.send(t, "a", "hello")
	entry := e.clock.Now()
	var claimed store.BusySendForce
	f.drive = func(pause int, woke bool) {
		if pause != 0 {
			return
		}
		if !woke {
			t.Error("committed force claim did not notify its route")
		}
		row, err := f.c.Get(f.id)
		if err != nil || row.Delivery == nil || row.Delivery.Force == nil {
			t.Fatalf("get after claim = %+v, %v; want stored force", row.Delivery, err)
		}
		got, err := e.st.ReadBusySendForce(row.Delivery.Force.ForceID)
		if err != nil || got.ForceResult == nil {
			t.Fatalf("read committed claim = %+v, %v", got, err)
		}
		claimed = *got.ForceResult
	}
	var failed atomic.Bool
	stop, err := api.ObserveClientSQLiteForTest(f.c, func(d driver.Driver) driver.Driver {
		return sqliteobserve.Wrap(d, sqliteobserve.Hooks{Before: func(_ context.Context, q string) error {
			if strings.Contains(q, "UPDATE busy_send_forces SET outcome") && strings.Contains(q, "call_ended_at_ms") && !failed.Swap(true) {
				return errors.New("injected force call-end write failure")
			}
			return nil
		}})
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := f.c.SendKeys(api.SendKeysParams{InstanceID: f.id, Text: "unconfirmed", ConfirmedDelivery: ptr(false), Force: ptr(true)})
	if endErr := stop(context.Background()); endErr != nil {
		t.Fatal(endErr)
	}
	if !failed.Load() || claimed.ForceID == "" {
		t.Fatal("fault did not reach call-end after a committed claim")
	}
	if res.Delivery == nil || res.Delivery.Wait == nil {
		t.Fatalf("accepted send = %+v, %v; want its pre-force Held Result", res, err)
	}
	want := busyHeld(f.id, "", "", "earlier_message", res.Delivery.Wait.Since)
	want.Wait.Earlier = &api.DeliveryPair{CallerUUID: busyCaller, RequestID: "a"}
	busyAcceptedForceError(t, f, res, err, want)
	adviceAssertPhrase(t, err, "do not send it again: call force-delivery to force the agent's Held Messages")
	adviceAssertPhrase(t, err, "get-force reads it, and the agent's next eligible event or an explicit send-keys or force-delivery "+
		"finishes it once its recorded deadline has passed; do not treat it as refused")
	if claimed.OwnerCallID == "" || claimed.Revision != 1 || claimed.LaunchID != f.route.dest.LaunchID ||
		claimed.LaunchToken != f.route.dest.LaunchToken || claimed.CallDeadlineMillis != entry.Add(15*time.Second).UnixMilli() ||
		claimed.CallEndedAtMillis != 0 || claimed.EndedAtMillis != 0 || claimed.Outcome != store.BusySendForceRunning {
		t.Errorf("committed claim = %+v; want owned running force with a finite 15s deadline and no end", claimed)
	}
	if stored, err := e.st.ReadBusySendForce(claimed.ForceID); err != nil || stored.ForceResult == nil ||
		!reflect.DeepEqual(*stored.ForceResult, claimed) {
		t.Errorf("force after failed cleanup = %+v, %v; want unchanged claim %+v", stored, err, claimed)
	}
	found, err := f.c.GetForce(api.GetForceParams{ForceID: claimed.ForceID})
	if err != nil || found.ForceResult == nil {
		t.Fatalf("get-force as advised = %+v, %v", found, err)
	}
	running := *found.ForceResult
	wantForce := api.ForceResult{ForceID: claimed.ForceID, Outcome: "running", StartedAt: time.UnixMilli(entry.UnixMilli()).UTC(),
		CarriedPairs: []api.DeliveryPair{{CallerUUID: busyCaller, RequestID: "a"}}, CarriedResults: running.CarriedResults}
	if !reflect.DeepEqual(running, wantForce) || len(running.CarriedResults) != 1 {
		t.Errorf("stored force = %+v; want %+v with a's current Result", running, wantForce)
	}
	f.assertRecorded(t, running)
	before := busyFacts(t, e.dbPath, f.id)
	// Crossing the deadline alone and reading never release or replay anything.
	e.clock.Advance(entry.Add(15*time.Second + time.Millisecond).Sub(e.clock.Now()))
	f.assertRecorded(t, running)
	if !reflect.DeepEqual(busyFacts(t, e.dbPath, f.id), before) {
		t.Error("time or a read changed Message custody")
	}
	f.assertReleased(t, claimed.ForceID, func(t *testing.T, f *forceAgent) {
		// The advised explicit send may repeat a's pair: it accepts nothing new.
		busyAdmit(t, f.c, api.SendKeysParams{InstanceID: f.id, Text: "hello", ConfirmedDelivery: ptr(true), CallerUUID: ptr(busyCaller),
			DeliveryRequestID: "a"})
	})
	if got := storefix.ReadBusySendCounts(t, e.dbPath); got.Messages != 2 || got.Pairs != 1 || got.Forces != 1 ||
		len(f.route.issued) != 0 || paneInputs(e) != 0 {
		t.Errorf("after recovery records = %+v, issued = %d, pane input = %d; want two Messages, one pair, one force, no replay",
			got, len(f.route.issued), paneInputs(e))
	}
}
