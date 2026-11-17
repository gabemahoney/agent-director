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

// busySteerUnsupported is steer_turn's refusal for a covered provider with no
// capability row, whose fallback offers Wait Turn only.
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

// TestAdviceFollow_E25_SteerUnsupportedSendWaitTurn: a covered send with Steer Turn to a provider with no capability row (a later release's covered Codex row, whose fallback offers Wait Turn only), then the same send with Wait Turn.
// E25: "send it with send_mode wait_turn"
func TestAdviceFollow_E25_SteerUnsupportedSendWaitTurn(t *testing.T) {
	t.Parallel()
	e, c, id := busyAdviceRow(t, "codex")
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
func busyFailForce(t *testing.T, c *api.Client) (stop func()) {
	t.Helper()
	var failed atomic.Bool
	end, err := api.ObserveClientSQLiteForTest(c, func(d driver.Driver) driver.Driver {
		return sqliteobserve.Wrap(d, sqliteobserve.Hooks{Before: func(_ context.Context, q string) error {
			if strings.Contains(q, "INSERT INTO busy_send_forces") && !failed.Swap(true) {
				return errors.New("injected force insert failure")
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

// busyForceFailed sends p, with force, to f's agent with its force's insert
// failing, and checks the answer: an unnamed error with no details, never a
// refusal or ErrStoreBusy; the Message accepted, with the ordinary wake of
// any accepted send (SR-7.2), and no force stored.
func busyForceFailed(t *testing.T, f *forceAgent, p api.SendKeysParams) error {
	t.Helper()
	stop := busyFailForce(t, f.c)
	_, err := f.c.SendKeys(p)
	stop()
	assertOneName(t, err, "")
	if api.ErrDetails(err) != nil {
		t.Errorf("err_details = %+v; want none", api.ErrDetails(err))
	}
	if got := storefix.ReadBusySendCounts(t, f.e.dbPath); got.Messages != 1 || got.Forces != 0 {
		t.Errorf("records = %+v; want the one accepted Message and no force", got)
	}
	if !f.touched() {
		t.Error("the accepted Message's wake was never touched")
	}
	return err
}

// TestAdviceFollow_E33_ForceFailedRepeatPair: a confirmed covered send with force whose force fails after its Message was accepted; get-delivery, then the same send.
// E33: "do not send it as new: repeat it with the same pair to force again, or read its Result with get-delivery"
func TestAdviceFollow_E33_ForceFailedRepeatPair(t *testing.T) {
	t.Parallel()
	e := newKillEnv(t)
	f := newForceAgent(t, e)
	p := api.SendKeysParams{InstanceID: f.id, Text: "forced", ConfirmedDelivery: ptr(true), CallerUUID: ptr(busyCaller), DeliveryRequestID: "f-1",
		Force: ptr(true)}

	err := busyForceFailed(t, f, p)

	adviceAssertPhrase(t, err, `the Message was accepted (caller_uuid `+busyCaller+`, delivery_request_id "f-1"), but its force could not be made`)
	adviceAssertPhrase(t, err, "do not send it as new: repeat it with the same pair to force again, or read its Result with get-delivery")
	got, err := f.c.GetDelivery(api.GetDeliveryParams{CallerUUID: busyCaller, DeliveryRequestID: "f-1"})
	if err != nil || got.Lookup != "found" || got.Result == nil {
		t.Fatalf("get-delivery as advised = %+v, %v; want found", got, err)
	}
	again, err := f.c.SendKeys(p)
	if err != nil || again.Delivery == nil || !reflect.DeepEqual(*again.Delivery, *got.Result) || again.Force == nil ||
		!reflect.DeepEqual(again.Force.CarriedPairs, []api.DeliveryPair{{CallerUUID: busyCaller, RequestID: "f-1"}}) {
		t.Errorf("the same send as advised = %+v, %v; want the found Result and a force carrying it", again, err)
	}
	if got := storefix.ReadBusySendCounts(t, e.dbPath); got.Messages != 1 || got.Pairs != 1 || got.Forces != 1 {
		t.Errorf("records = %+v; want the one Message, accepted once, and its force", got)
	}
}

// TestAdviceFollow_E34_ForceFailedForceDelivery: an unconfirmed covered send with force whose force fails after its Message was accepted; force-delivery, never the send again.
// E34: "do not send it again: call force-delivery to force the agent's Held Messages"
func TestAdviceFollow_E34_ForceFailedForceDelivery(t *testing.T) {
	t.Parallel()
	e := newKillEnv(t)
	f := newForceAgent(t, e)

	err := busyForceFailed(t, f, api.SendKeysParams{InstanceID: f.id, Text: "forced", Force: ptr(true)})

	adviceAssertPhrase(t, err, "the Message was accepted, but its force could not be made")
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
