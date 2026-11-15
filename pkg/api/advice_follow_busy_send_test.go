package api_test

import (
	"context"
	"database/sql/driver"
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

// busyAdviceRow is a Client and a covered live row for a covered send's
// literal-follow test.
func busyAdviceRow(t *testing.T) (*killEnv, *api.Client, string) {
	t.Helper()
	e := newKillEnv(t)
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

// TestAdviceFollow_E25_SteerUnsupportedSendWaitTurn: a covered send with Steer Turn, then the same send with Wait Turn.
// E25: "send it with send_mode wait_turn"
func TestAdviceFollow_E25_SteerUnsupportedSendWaitTurn(t *testing.T) {
	t.Parallel()
	e, c, id := busyAdviceRow(t)
	p := api.SendKeysParams{InstanceID: id, Text: "steer", SendMode: ptr("steer_turn")}
	_, err := c.SendKeys(p)
	adviceAssertAdvice(t, err, api.ErrCantSend, "send it with send_mode wait_turn")
	p.SendMode = ptr("wait_turn")
	busyFollowAccepted(t, e, c, p, 1)
}

// TestAdviceFollow_B18_SteerUnsupportedResumeWaitTurn: a covered resume with Steer Turn, then the same resume with Wait Turn.
// B18: "use wait_turn"
func TestAdviceFollow_B18_SteerUnsupportedResumeWaitTurn(t *testing.T) {
	t.Parallel()
	e := newKillEnv(t)
	r := e.seedResumable(t, time.Hour, agentGone)
	seedBusyCovered(t, e, r.ID)
	cfg := config.Default()
	cfg.Tmux = e.cfg
	p := api.ResumeParams{InstanceID: r.ID, SendMode: ptr("steer_turn")}
	_, err := api.Resume(e.st, e.rec, e.pc, cfg, e.storeID, e.clock.Now, nil, p)
	adviceAssertAdvice(t, err, api.ErrCantSend, "use wait_turn")
	p.SendMode = ptr("wait_turn")
	if _, err := api.Resume(e.st, e.rec, e.pc, cfg, e.storeID, e.clock.Now, nil, p); err != nil {
		t.Fatalf("the same resume with send_mode wait_turn: %v", err)
	}
	assertBusyLaunchPhase(t, e.dbPath, r.ID, "resume", "current", store.BusySendCovered)
}

// TestAdviceFollow_E26_IDConflictNewMessageID: a covered send reusing an owned pair for other text, then under a new delivery_request_id.
// E26: "send this Message only as a new send under a new delivery_request_id"
func TestAdviceFollow_E26_IDConflictNewMessageID(t *testing.T) {
	t.Parallel()
	e, c, id := busyAdviceRow(t)
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
	e, c, id := busyAdviceRow(t)
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
	e, c, id := busyAdviceRow(t)
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
