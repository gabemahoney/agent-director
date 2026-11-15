package api_test

// busy_send_lifecycle_test.go: a covered row's custody through the lifecycle
// verbs (SR-6.1 to SR-6.3; M1 G4, M2 NothingTakenAfterExit; Task
// t2.xis.oo.sf): kill's and sweep's Launch End carried through a resume on a
// restarted Client, and the lifecycle fixture (lcEnv) and checks the
// owned-issuer exclusion tests share: those run as the literal-follow tests
// of the refusals' advice (advice_follow_busy_send_lifecycle_test.go), and
// administrator deletion's batch, decision 15 included, is delete_test.go's.
// The store owns the state and transaction guards (internal/store
// busy_send_lifecycle_test.go); keyless pause is pause_keyless_test.go's.

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/adminapi"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// lcHelper is the issuing hook job of the old Launch, recorded by its issue.
var lcHelper = store.BusySendIssuer{Role: store.BusySendProcessHelper, PID: 7101, StartTime: "7101"}

// lcEnv is a covered resumable row r (a route and wake file) on a killEnv,
// with c a Client on it, holding three Messages in accepted order: lc-0
// (confirmed), claimed at turn end and issued by lcHelper (ref is its prompt
// as the job holds it), then an unconfirmed one and lc-2 (confirmed),
// admitted after the issue and Held.
type lcEnv struct {
	*killEnv
	r     resumeRow
	c     *api.Client
	woken func() bool
	ref   store.BusySendPromptRef
}

// newLifecycleEnv seeds spec's row (seedResumableRow) as lcEnv describes,
// with lcHelper in the process fake as helper.
func newLifecycleEnv(t *testing.T, spec killRowSpec, helper procfix.Process) *lcEnv {
	t.Helper()
	l := &lcEnv{killEnv: newKillEnv(t)}
	l.r = l.seedResumableRow(t, spec)
	seedBusyCovered(t, l.killEnv, l.r.ID)
	_, l.woken = busyWake(t, l.killEnv, l.r.ID)
	l.c, _ = l.client(t)
	busyAdmit(t, l.c, api.SendKeysParams{InstanceID: l.r.ID, Text: "msg-body-issued ✓\n", ConfirmedDelivery: ptr(true),
		CallerUUID: ptr(busyCaller), DeliveryRequestID: "lc-0"})
	l.ref = l.busyIssue(t, l.r.killRow, &lcHelper)
	l.pc.Set(lcHelper.PID, helper)
	busyAdmit(t, l.c, api.SendKeysParams{InstanceID: l.r.ID, Text: "  msg-body-held\té"})
	busyAdmit(t, l.c, api.SendKeysParams{InstanceID: l.r.ID, Text: "msg-body-pair <tag/>\n", ConfirmedDelivery: ptr(true),
		CallerUUID: ptr(busyCaller), DeliveryRequestID: "lc-2"})
	l.woken()
	return l
}

// busyIssue claims r's Held Messages at turn end and issues the first prompt,
// recording issuer as the Launch's process (nil: none), through the
// coordinator's store writes (r needs a route, busyWake). It returns the
// prompt as the issuing job holds it; the caller puts issuer in the fake.
func (e *killEnv) busyIssue(t *testing.T, r killRow, issuer *store.BusySendIssuer) store.BusySendPromptRef {
	t.Helper()
	ctx := context.Background()
	pub, err := e.st.GetBusySendPublishedLaunch(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	dest := store.BusySendDestination{LaunchID: pub.LaunchID, LaunchToken: pub.LaunchToken, Provider: pub.Provider,
		SessionID: r.Spawn.SessionID, RouteKind: store.BusySendRouteClaudeHooks}
	claim, err := e.st.ClaimBusySendHandover(ctx, store.BusySendClaimRequest{InstanceID: r.ID, Destination: dest,
		Boundary: store.BusySendBoundaryTurnEnd, IssueBudget: time.Hour, JobBudget: 2 * time.Hour})
	if err != nil || claim.Outcome != store.BusySendClaimMade {
		t.Fatalf("claim = %+v, %v; want a hand-over", claim, err)
	}
	issue, err := e.st.CommitBusySendIssue(ctx, store.BusySendIssueRequest{Issuer: issuer,
		Ref: store.BusySendPromptRef{HandoverID: claim.Handover.HandoverID, Revision: claim.Handover.Revision}})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	return store.BusySendPromptRef{HandoverID: claim.Handover.HandoverID, Revision: issue.Revision}
}

// endBySessionEnd ends r's agent: its process exits and its own SessionEnd
// finishes the row and records its Launch End.
func (e *killEnv) endBySessionEnd(t *testing.T, r killRow) {
	t.Helper()
	e.pc.Set(r.AgentPID, procfix.Gone())
	if a := apitest.ApplyAgentHook(t, e.dbPath, r.ID, "SessionEnd", r.Spawn.SessionID); !a.Applied {
		t.Fatalf("SessionEnd not applied: %s", a.Reason)
	}
}

// sweep runs c's sweep and fails unless get then reads the row ended or missing.
func (l *lcEnv) sweep(t *testing.T, c *api.Client) {
	t.Helper()
	if _, err := c.Sweep(context.Background()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if row, err := c.Get(l.r.ID); err != nil || (row.State != store.StateEnded && row.State != store.StateMissing) {
		t.Fatalf("get after sweep = %+v, %v; want ended or missing", row, err)
	}
}

// held is r's Held Messages as stored, in accepted order.
func (l *lcEnv) held(t *testing.T) []apitest.BusyMessageFact {
	t.Helper()
	var out []apitest.BusyMessageFact
	for _, m := range busyFacts(t, l.dbPath, l.r.ID).Messages {
		if m.Place == "held" {
			out = append(out, m)
		}
	}
	return out
}

// lcLookup is c's Result for busyCaller's request id, which must be found.
func lcLookup(t *testing.T, c *api.Client, request string) api.DeliveryResult {
	t.Helper()
	got, err := c.GetDelivery(api.GetDeliveryParams{CallerUUID: busyCaller, DeliveryRequestID: request})
	if err != nil || got.Result == nil {
		t.Fatalf("GetDelivery(%s) = %+v, %v; want a Result", request, got, err)
	}
	return *got.Result
}

// lcNotStarted is the Result of id's Held Message with busyCaller's request
// once retirement finalized it with reason.
func lcNotStarted(id, request, reason string) api.DeliveryResult {
	return api.DeliveryResult{CallerUUID: busyCaller, RequestID: request, Class: "not_started", Final: true, Reason: reason,
		ResendSafe: true, RetryWith: "new_id", InstanceID: id, Outcome: "not_started"}
}

// assertNoInput fails unless no Message reached the agent: no pane input, no
// input journal attempt and no wake.
func (l *lcEnv) assertNoInput(t *testing.T) {
	t.Helper()
	if n := len(l.rec.SocketCallsOf(tmux.CallSendText)) + len(l.rec.SocketCallsOf(tmux.CallSendEnter)) + len(l.rec.SocketCallsOf(tmux.CallSendKey)); n != 0 {
		t.Errorf("%d pane inputs; want none", n)
	}
	if n := storefix.ReadBusySendCounts(t, l.dbPath).InputAttempts; n != 0 {
		t.Errorf("%d input journal attempts; want none", n)
	}
	if l.woken() {
		t.Error("the route's wake file was touched")
	}
}

// TestBusySendLifecycleResumeCarriesCustody (SR-6.1, SR-6.2; M1
// NoOldIssuerAfterResume, OldCallbacksInert): a Launch End learned from
// kill's verified exit or from sweep finalizes the issued Message
// sent_unconfirmed and keeps the later ones Held; after AD restarts, resume on
// a new Client carries them to the new Launch in accepted order with the same
// bytes, pair and options, its route recorded, waiting agent_starting and
// earlier_message, and replays nothing at startup. The old job's late
// completion changes neither the new Launch, its delivery facts nor the old
// Final Result, and no lifecycle step emits a Message or wakes the route.
func TestBusySendLifecycleResumeCarriesCustody(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		end  func(t *testing.T, l *lcEnv)
	}{
		{"kill's verified exit, then sweep", func(t *testing.T, l *lcEnv) {
			l.setAfterCall(tmux.CallKillPane, procfix.Gone(), l.r.AgentPID)
			if res, err := l.c.Kill(api.KillParams{InstanceID: l.r.ID}); err != nil || !res.KillSent {
				t.Fatalf("kill = %+v, %v; want the agent's pane killed", res, err)
			}
			if got := currentLaunch(t, l.dbPath, l.r.ID).EndCause.String; got != "process_exited" {
				t.Errorf("Launch End after kill = %q; want process_exited", got)
			}
			l.sweep(t, l.c)
		}},
		{"sweep finds the agent gone", func(t *testing.T, l *lcEnv) {
			l.pc.Set(l.r.AgentPID, procfix.Gone())
			adviceEndSession(t, l.rec, l.r.Socket, l.r.Session.ID)
			l.sweep(t, l.c)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			l := newLifecycleEnv(t, killRowSpec{State: store.StateWaiting}, procfix.Gone())
			held, binding := l.held(t), busyFacts(t, l.dbPath, l.r.ID).Binding

			tc.end(t, l)

			if got := currentLaunch(t, l.dbPath, l.r.ID).EndCause.String; got != "process_exited" {
				t.Fatalf("Launch End = %q; want process_exited", got)
			}
			issued := lcLookup(t, l.c, "lc-0")
			if issued.Class != "sent_unconfirmed" || !issued.Final || issued.Reason != "launch_ended" {
				t.Errorf("issued Message after Launch End = %+v; want Final sent_unconfirmed launch_ended", issued)
			}
			// AD restarts: the resume runs on a new Client, from the store alone.
			if err := l.c.Close(); err != nil {
				t.Fatal(err)
			}
			c, _ := l.client(t)
			if _, err := c.Resume(api.ResumeParams{InstanceID: l.r.ID}); err != nil {
				t.Fatalf("resume: %v", err)
			}

			creates := l.rec.SocketCallsOf(tmux.CallCreate)
			if len(creates) != 1 || strings.Contains(strings.Join(creates[0].Command, "\x00"), "msg-body-") {
				t.Fatalf("creates = %+v; want one, replaying no Message", creates)
			}
			facts := busyFacts(t, l.dbPath, l.r.ID)
			now := currentLaunch(t, l.dbPath, l.r.ID)
			if facts.Binding != binding || now.Kind != "resume" || now.Token.String != creates[0].Token ||
				now.Coverage != string(store.BusySendCovered) || now.RouteKind.String != string(store.BusySendRouteClaudeHooks) {
				t.Errorf("binding %s, current Launch %+v; want the resume's covered Launch with its route in conversation %s", facts.Binding, now, binding)
			}
			if after := l.held(t); !reflect.DeepEqual(after, held) {
				t.Errorf("Held Messages after resume = %+v; want carried unchanged %+v", after, held)
			}
			row, err := c.Get(l.r.ID)
			if err != nil || row.Delivery == nil || row.Delivery.HeldCount != 2 || row.Delivery.AwaitingTurnStart != 0 ||
				row.Delivery.FirstWait == nil || row.Delivery.FirstWait.Reason != "agent_starting" {
				t.Fatalf("get delivery = %+v, %v; want 2 Held, the first waiting agent_starting, none awaiting", row.Delivery, err)
			}
			pair := lcLookup(t, c, "lc-2")
			if pair.Class != "held" || pair.Final || pair.Wait == nil || pair.Wait.Reason != "earlier_message" {
				t.Errorf("lc-2 after resume = %+v; want Held behind the earlier Message", pair)
			}
			if got := lcLookup(t, c, "lc-0"); !reflect.DeepEqual(got, issued) {
				t.Errorf("issued Message after resume = %+v; want unchanged %+v", got, issued)
			}

			// The old job's late completion of its issued prompt.
			_, _ = l.st.RecordBusySendCompletion(context.Background(), store.BusySendCompletion{Ref: l.ref,
				Output: store.BusySendOutputComplete, Order: store.BusySendOrderEmissionComplete})
			if got := currentLaunch(t, l.dbPath, l.r.ID); !reflect.DeepEqual(got, now) {
				t.Errorf("new Launch after the old completion = %+v; want unchanged %+v", got, now)
			}
			if again, err := c.Get(l.r.ID); err != nil || !reflect.DeepEqual(again.Delivery, row.Delivery) {
				t.Errorf("get delivery after the old completion = %+v, %v; want unchanged %+v", again.Delivery, err, row.Delivery)
			}
			if got := lcLookup(t, c, "lc-0"); !reflect.DeepEqual(got, issued) {
				t.Errorf("issued Message after the old completion = %+v; want unchanged %+v", got, issued)
			}
			if after := l.held(t); !reflect.DeepEqual(after, held) {
				t.Errorf("Held Messages after the old completion = %+v; want unchanged %+v", after, held)
			}
			l.assertNoInput(t)
		})
	}
}

// lcVerb is a lifecycle verb on l's row through c: its refusal's err_name,
// and lc-2's Final reason once it retires the row's custody ("" when it
// carries it).
type lcVerb struct {
	name, refusal, retired string
	run                    func(t *testing.T, l *lcEnv, c *api.Client) error
}

// The lifecycle verbs that need the old Launch's exit; delete's per-id result
// other than "ok" is returned as an error carrying it.
var (
	lcResume = lcVerb{name: "resume", refusal: "ErrSpawnNotResumable", run: func(_ *testing.T, l *lcEnv, c *api.Client) error {
		_, err := c.Resume(api.ResumeParams{InstanceID: l.r.ID})
		return err
	}}
	lcReuse = lcVerb{name: "reuse", refusal: "ErrInstanceIdCollision", retired: "conversation_replaced", run: func(t *testing.T, l *lcEnv, c *api.Client) error {
		_, err := c.Spawn(reuseParams(t, reuseRow{resumeRow: l.r}, reuseRequest{}))
		return err
	}}
	lcDelete = lcVerb{name: "delete", refusal: "ErrSpawnNotDeletable", retired: "record_deleted", run: func(t *testing.T, l *lcEnv, c *api.Client) error {
		res, err := adminapi.Delete(c, []string{l.r.ID})
		if err != nil {
			t.Fatalf("delete: %v", err)
		}
		if got := res.Results[l.r.ID]; got != "ok" {
			return errors.New(got)
		}
		return nil
	}}
)

// lcSnapshot is what a refused lifecycle verb must leave unchanged: l's row,
// its busy-send facts and the tmux creates.
type lcSnapshot struct {
	facts   apitest.BusySendFacts
	cols    apitest.SpawnColumns
	creates int
}

func (l *lcEnv) snapshot(t *testing.T) lcSnapshot {
	t.Helper()
	return lcSnapshot{busyFacts(t, l.dbPath, l.r.ID), l.columns(t, l.r.ID), len(l.rec.SocketCallsOf(tmux.CallCreate))}
}

// assertRefused fails unless err is verb's refusal (for resume and reuse,
// naming lcHelper as state) and nothing changed since before.
func (l *lcEnv) assertRefused(t *testing.T, verb lcVerb, err error, state string, before lcSnapshot) {
	t.Helper()
	if verb.name == "delete" {
		if err == nil || err.Error() != verb.refusal {
			t.Fatalf("delete result = %v; want %s", err, verb.refusal)
		}
	} else {
		assertOneName(t, err, verb.refusal)
		adviceAssertPhrase(t, err, fmt.Sprintf("an owned helper process (pid %d) of the agent's Launch %s", lcHelper.PID, state))
	}
	if after := l.snapshot(t); !reflect.DeepEqual(after, before) {
		t.Errorf("after the refusal: %+v; want unchanged %+v", after, before)
	}
}

// assertRetired fails unless verb, gone on through c, left what its success
// promises (SR-6.1, SR-6.3; M1 NotStartedAfterExit): resume carries held
// unchanged; reuse and deletion finalize lc-2 not_started with verb.retired,
// reuse closing the unconfirmed Message's custody in the old conversation
// binding with no pair and deletion removing the row; the issued Message keeps
// its Final Result issued, found after the row's deletion too.
func (l *lcEnv) assertRetired(t *testing.T, verb lcVerb, c *api.Client, issued api.DeliveryResult, held []apitest.BusyMessageFact, binding string) {
	t.Helper()
	if got := lcLookup(t, c, "lc-0"); !reflect.DeepEqual(got, issued) {
		t.Errorf("issued Message after %s = %+v; want unchanged %+v", verb.name, got, issued)
	}
	if verb.retired == "" {
		if after := l.held(t); !reflect.DeepEqual(after, held) {
			t.Errorf("Held Messages after resume = %+v; want carried unchanged %+v", after, held)
		}
		return
	}
	if got, want := lcLookup(t, c, "lc-2"), lcNotStarted(l.r.ID, "lc-2", verb.retired); !reflect.DeepEqual(got, want) {
		t.Errorf("lc-2 after %s = %+v; want %+v", verb.name, got, want)
	}
	if verb.name == "delete" {
		assertRowGone(t, l.killEnv, l.r.ID)
		return
	}
	unconfirmed := 0
	for _, m := range busyFacts(t, l.dbPath, l.r.ID).Messages {
		if m.Conversation != binding || m.Confirmed {
			continue
		}
		if unconfirmed++; m.Place != "closed" || m.CallerUUID.Valid || m.RequestID.Valid {
			t.Errorf("unconfirmed Message after reuse = %+v; want its custody closed with no pair", m)
		}
	}
	if unconfirmed != 1 {
		t.Errorf("%d unconfirmed Messages in the old conversation; want 1", unconfirmed)
	}
}
