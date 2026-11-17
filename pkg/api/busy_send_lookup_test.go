package api_test

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/sqliteobserve"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// forceRefused runs force-delivery on id and checks SR-9.2's answer: a
// canonical AD-made force_id, outcome refused, reason agent_not_covered,
// started and ended at now, carrying nothing.
func forceRefused(t *testing.T, c *api.Client, id string, now time.Time) api.ForceResult {
	t.Helper()
	res, err := c.ForceDelivery(api.ForceDeliveryParams{InstanceID: id})
	if err != nil {
		t.Fatalf("ForceDelivery(%s): %v", id, err)
	}
	f := res.ForceResult
	if parsed, err := uuid.Parse(f.ForceID); err != nil || parsed.String() != f.ForceID {
		t.Errorf("force_id %q is not a canonical UUID (%v)", f.ForceID, err)
	}
	at := time.UnixMilli(now.UnixMilli()).UTC()
	want := api.ForceResult{ForceID: f.ForceID, Outcome: "refused", Reason: "agent_not_covered", StartedAt: at, EndedAt: &at,
		CarriedPairs: []api.DeliveryPair{}, CarriedResults: []api.DeliveryResult{}}
	if !reflect.DeepEqual(f, want) {
		t.Errorf("force_result = %+v; want %+v", f, want)
	}
	return f
}

// currentLaunch is id's published current Launch.
func currentLaunch(t *testing.T, path, id string) apitest.BusyLaunchFact {
	t.Helper()
	for _, l := range busyFacts(t, path, id).Launches {
		if l.Phase == "current" {
			return l
		}
	}
	t.Fatalf("%s has no current Launch", id)
	return apitest.BusyLaunchFact{}
}

// assertForceBound fails unless forceID's stored attempt names want, the
// published Launch the call examined, by its conversation, id, own token and
// provider, and was refused before any hand-over in one instant.
func assertForceBound(t *testing.T, st *store.Store, forceID, instanceID string, want apitest.BusyLaunchFact) {
	t.Helper()
	got, err := st.ReadBusySendForce(forceID)
	if err != nil || got.Lookup != store.BusySendLookupFound {
		t.Fatalf("ReadBusySendForce(%s) = %+v, %v; want found", forceID, got, err)
	}
	f := *got.ForceResult
	if f.InstanceID != instanceID || f.ConversationID != want.Conversation || f.LaunchID != want.ID ||
		f.LaunchToken != want.Token.String || f.Provider != want.Provider {
		t.Errorf("attempt %s binds %s %s/%s/%s/%s; want %s %s/%s/%s/%s", forceID, f.InstanceID, f.ConversationID, f.LaunchID,
			f.LaunchToken, f.Provider, instanceID, want.Conversation, want.ID, want.Token.String, want.Provider)
	}
	if f.OwnerCallID == "" || f.Revision != 1 || f.HandoverID != "" || f.Retention != store.BusySendRetained ||
		f.CallDeadlineMillis != f.StartedAtMillis || f.CallEndedAtMillis != f.StartedAtMillis || f.EndedAtMillis != f.StartedAtMillis {
		t.Errorf("attempt %s = %+v; want its own call, ended and retained at its start", forceID, f)
	}
}

// TestForceDeliveryStoresRefusedAttempt (SR-4.1, SR-9.2): for every agent
// kind, each force-delivery succeeds only with its own committed refused
// attempt, bound to the published current Launch, which get-force reads back
// found and unchanged (either hex case); nothing else changes and no tmux call
// is made.
func TestForceDeliveryStoresRefusedAttempt(t *testing.T) {
	t.Parallel()
	for _, agent := range busyAgents {
		t.Run(agent, func(t *testing.T) {
			t.Parallel()
			e := newKillEnv(t)
			e.rowAgent = agent
			r := e.seedRow(t, killRowSpec{})
			c, _ := e.client(t)
			facts, cols, calls := busyFacts(t, e.dbPath, r.ID), e.columns(t, r.ID), len(e.rec.SocketCalls())
			first, second := forceRefused(t, c, r.ID, e.clock.Now()), forceRefused(t, c, r.ID, e.clock.Now())
			if first.ForceID == second.ForceID {
				t.Errorf("two calls share force_id %s; want an attempt each", first.ForceID)
			}
			for _, f := range []api.ForceResult{first, second} {
				assertForceBound(t, e.st, f.ForceID, r.ID, currentLaunch(t, e.dbPath, r.ID))
				got, err := c.GetForce(api.GetForceParams{ForceID: strings.ToUpper(f.ForceID)})
				if err != nil || got.Lookup != "found" || got.ForceResult == nil || !reflect.DeepEqual(*got.ForceResult, f) {
					t.Errorf("GetForce(%s) = %+v, %v; want found %+v", f.ForceID, got, err, f)
				}
			}
			raw, _ := json.Marshal(api.GetForceResult{Lookup: "found", ForceResult: &first})
			at, _ := json.Marshal(first.StartedAt)
			want := `{"lookup":"found","force_result":{"force_id":"` + first.ForceID +
				`","outcome":"refused","reason":"agent_not_covered","started_at":` + string(at) + `,"ended_at":` + string(at) + `,"carried_pairs":[],"carried_results":[]}}`
			if string(raw) != want {
				t.Errorf("get-force JSON = %s\nwant %s", raw, want)
			}
			if got := busyFacts(t, e.dbPath, r.ID); !reflect.DeepEqual(got, facts) {
				t.Errorf("busy-send core changed: %+v; want %+v", got, facts)
			}
			e.assertRowUnchanged(t, r.ID, cols)
			if got := storefix.ReadBusySendCounts(t, e.dbPath); got != (storefix.BusySendCounts{Forces: 2}) {
				t.Errorf("records = %+v; want only the two attempts", got)
			}
			if n := len(e.rec.SocketCalls()) - calls; n != 0 {
				t.Errorf("%d tmux calls; want none", n)
			}
		})
	}
}

// TestForceDeliveryBindsExaminedPublishedLaunch (SR-4.1, SR-9.1; code-review
// CR1, product-manager PM1): an attempt names the published current Launch
// the call read, by its own token, never a staged candidate's, and is decided
// from that Launch's own coverage, never its conversation's; when a
// publication lands between the read and the insert, the call reads again and
// binds the new Launch.
func TestForceDeliveryBindsExaminedPublishedLaunch(t *testing.T) {
	t.Parallel()
	stage := func(t *testing.T, e *killEnv, id, token string) int64 {
		t.Helper()
		row, err := e.st.GetSpawn(id)
		if err != nil {
			t.Fatal(err)
		}
		res, version, err := e.st.MoveToPendingWithExit(id, row.Snapshot, e.clock.Now().UnixMilli(), token, "", "", store.LaunchOwner{}, store.BusySendDefaultsChoice{}, storefix.ExitEvidence(t, e.st, id))
		if err != nil || res != store.CondApplied {
			t.Fatalf("stage resume = %v, %v", res, err)
		}
		return version
	}
	t.Run("uncovered Launch in a covered conversation", func(t *testing.T) {
		t.Parallel()
		e := newKillEnv(t)
		r := e.seedRow(t, killRowSpec{})
		c, _ := e.client(t)
		facts := seedBusyCoveredConversation(t, e.dbPath, r.ID)
		f := forceRefused(t, c, r.ID, e.clock.Now())
		assertForceBound(t, e.st, f.ForceID, r.ID, currentLaunch(t, e.dbPath, r.ID))
		if got, err := c.GetForce(api.GetForceParams{ForceID: f.ForceID}); err != nil || got.Lookup != "found" ||
			got.ForceResult == nil || !reflect.DeepEqual(*got.ForceResult, f) {
			t.Errorf("GetForce(%s) = %+v, %v; want found %+v", f.ForceID, got, err, f)
		}
		assertBusyProjection(t, e.dbPath, r.ID, facts)
	})
	t.Run("staged resume", func(t *testing.T) {
		t.Parallel()
		e := newKillEnv(t)
		e.rowAgent = "codex"
		r := e.seedResumable(t, time.Hour, agentGone)
		c, _ := e.client(t)
		published := currentLaunch(t, e.dbPath, r.ID)
		stage(t, e, r.ID, "1122334455667788")
		assertForceBound(t, e.st, forceRefused(t, c, r.ID, e.clock.Now()).ForceID, r.ID, published)
	})
	t.Run("staged reuse switching provider", func(t *testing.T) {
		t.Parallel()
		e := newKillEnv(t)
		e.rowAgent = "codex"
		r := e.seedResumable(t, time.Hour, agentGone)
		c, _ := e.client(t)
		published := currentLaunch(t, e.dbPath, r.ID)
		before, _, err := e.st.ReadForReuse(r.ID)
		if err != nil {
			t.Fatal(err)
		}
		const token = "5566778899aabbcc"
		fresh := store.Spawn{Agent: "cursor", CWD: r.CWD, TmuxSessionName: r.Name, RelayMode: "off", StartedAt: e.clock.Now(),
			LaunchStartedAtMillis: e.clock.Now().UnixMilli(), Identity: store.LaunchIdentity{Token: token}}
		res, _, version, err := e.st.ResetForReuse(r.ID, before.Snapshot, before.Agent, storefix.WithExit(t, e.st, r.ID, fresh))
		if err != nil || res != store.CondApplied {
			t.Fatalf("stage reuse = %v, %v", res, err)
		}
		assertForceBound(t, e.st, forceRefused(t, c, r.ID, e.clock.Now()).ForceID, r.ID, published)
		if res, err := e.st.RecordLaunchIdentity(r.ID, version, token, store.LaunchIdentity{}); err != nil || res != store.CondApplied {
			t.Fatalf("publish reuse = %v, %v", res, err)
		}
		cursor := currentLaunch(t, e.dbPath, r.ID)
		if cursor.Provider != "cursor" || cursor.Conversation == published.Conversation || cursor.Token.String != token {
			t.Fatalf("published reuse Launch = %+v; want cursor's in a new conversation", cursor)
		}
		assertForceBound(t, e.st, forceRefused(t, c, r.ID, e.clock.Now()).ForceID, r.ID, cursor)
	})
	// A publication between the read and the insert: the call decides again
	// from the new Launch, its coverage included. An uncovered replacement gets
	// the refused agent_not_covered attempt; a covered one gets its own force
	// attempt, refused before any hand-over by the covered claim's rules, never
	// the first Launch's agent_not_covered (test-review TR2). The second
	// attempt keeps the call's entry as its start, however much time passed
	// (E5 code review F2).
	for _, covered := range []bool{false, true} {
		t.Run(fmt.Sprintf("publication between read and insert, covered=%t", covered), func(t *testing.T) {
			t.Parallel()
			e := newKillEnv(t)
			e.rowAgent = "codex" // the replacement's coverage is the case's, not Claude's
			r := e.seedResumable(t, time.Hour, agentGone)
			c, _ := e.client(t)
			old := currentLaunch(t, e.dbPath, r.ID)
			const token = "33445566778899aa"
			version := stage(t, e, r.ID, token)
			// The causal barrier: the first insert transaction starts only once the
			// candidate, staged before the call's read, has been published.
			var published atomic.Bool
			stop, err := api.ObserveClientSQLiteForTest(c, func(d driver.Driver) driver.Driver {
				return sqliteobserve.Wrap(d, sqliteobserve.Hooks{Before: func(_ context.Context, q string) error {
					if q != "BEGIN IMMEDIATE" || published.Swap(true) {
						return nil
					}
					if res, err := e.st.PublishBusySendLaunch(r.ID, version, token); err != nil || res != store.CondApplied {
						t.Errorf("publish between read and insert = %v, %v", res, err)
					}
					e.clock.Advance(time.Second)
					if covered {
						if err := apitest.SeedBusySendCoverage(e.dbPath, r.ID, store.BusySendCovered, store.BusySendCovered); err != nil {
							t.Error(err)
						}
					}
					return nil
				}})
			})
			if err != nil {
				t.Fatal(err)
			}
			var f api.ForceResult
			entry := e.clock.Now()
			if covered {
				res, ferr := c.ForceDelivery(api.ForceDeliveryParams{InstanceID: r.ID})
				f = res.ForceResult
				if ferr != nil || f.Outcome != "refused" || f.Reason != "agent_starting" || !f.StartedAt.Equal(entry) {
					t.Errorf("force on a covered replacement = %+v, %v; want the covered claim's own refusal, agent_starting, started at the entry %v",
						res, ferr, entry)
				}
			} else {
				f = forceRefused(t, c, r.ID, entry)
			}
			if err := stop(context.Background()); err != nil {
				t.Fatal(err)
			}
			if !published.Load() {
				t.Fatal("no insert transaction began, so nothing was published between read and insert")
			}
			now := currentLaunch(t, e.dbPath, r.ID)
			if now.ID == old.ID || now.Token.String != token {
				t.Fatalf("current Launch = %+v; want the published candidate", now)
			}
			assertForceBound(t, e.st, f.ForceID, r.ID, now)
			if got := storefix.ReadBusySendCounts(t, e.dbPath); got != (storefix.BusySendCounts{Forces: 1}) {
				t.Errorf("records = %+v; want one attempt and nothing else", got)
			}
		})
	}
}

// TestForceDeliveryFailuresStoreNothing (SR-4.1, SR-9.2): an unknown id, a
// missing instance id, a row with no published binding and a write lock held
// past the store's busy timeout (ErrStoreBusy) are errors with no err_details
// that store nothing, and a lost commit acknowledgement is an error, never a
// refused success.
func TestForceDeliveryFailuresStoreNothing(t *testing.T) {
	t.Parallel()
	e := newKillEnv(t)
	e.rowAgent = "codex" // force on an agent busy send doesn't cover; a covered force is E5's
	r, unbound := e.seedRow(t, killRowSpec{}), e.seedRow(t, killRowSpec{})
	storefix.UnbindBusySend(t, e.dbPath, unbound.ID)
	c, _ := e.client(t)
	for _, tc := range []struct{ id, name string }{
		{"no-such-" + uuid.NewString()[:8], "ErrSpawnNotFound"},
		{"", "ErrInvalidFlags"},
		{unbound.ID, "ErrInternal"},
	} {
		res, err := c.ForceDelivery(api.ForceDeliveryParams{InstanceID: tc.id})
		assertOneName(t, err, tc.name)
		if api.ErrDetails(err) != nil || !reflect.DeepEqual(res, api.ForceDeliveryResult{}) {
			t.Errorf("ForceDelivery(%q) = %+v, details %+v; want a bare error", tc.id, res, api.ErrDetails(err))
		}
	}
	busyConfig := filepath.Join(t.TempDir(), "config.toml")
	apitest.WriteKeysConfig(t, busyConfig, apitest.ConfigKeys{StoreBusyTimeoutMs: 100})
	busy, _, err := e.clientFor(t, busyConfig)
	if err != nil {
		t.Fatal(err)
	}
	release := apitest.HoldWriteLock(t, e.dbPath, time.Minute)
	busyRes, busyErr := busy.ForceDelivery(api.ForceDeliveryParams{InstanceID: r.ID})
	release()
	assertOneName(t, busyErr, "ErrStoreBusy")
	if api.ErrDetails(busyErr) != nil || !reflect.DeepEqual(busyRes, api.ForceDeliveryResult{}) {
		t.Errorf("ForceDelivery under a held write lock = %+v, details %+v; want a bare ErrStoreBusy", busyRes, api.ErrDetails(busyErr))
	}
	if got := storefix.ReadBusySendCounts(t, e.dbPath); got != (storefix.BusySendCounts{}) {
		t.Errorf("records after refusals = %+v; want none", got)
	}
	stop, err := api.ObserveClientSQLiteForTest(c, func(d driver.Driver) driver.Driver {
		return sqliteobserve.Wrap(d, sqliteobserve.Hooks{AfterCommit: func(context.Context) error {
			return errors.New("lost real COMMIT acknowledgement")
		}})
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := c.ForceDelivery(api.ForceDeliveryParams{InstanceID: r.ID})
	if err := stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertOneName(t, err, "ErrInternal")
	if !reflect.DeepEqual(res, api.ForceDeliveryResult{}) {
		t.Errorf("unacknowledged commit returned %+v; want no force_result", res)
	}
}

// TestForceAttemptOutlivesLaunchAndRow (SR-4.1): an attempt reads back found
// and unchanged after its Launch is replaced, after the agent row is deleted
// and from a reopened store; force-delivery on the deleted id is
// ErrSpawnNotFound and stores nothing.
func TestForceAttemptOutlivesLaunchAndRow(t *testing.T) {
	t.Parallel()
	e := newKillEnv(t)
	e.rowAgent = "codex" // force on an agent busy send doesn't cover; a covered force is E5's
	r := e.seedResumable(t, time.Hour, agentGone)
	c, _ := e.client(t)
	f := forceRefused(t, c, r.ID, e.clock.Now())
	read := func(c *api.Client, after string) {
		t.Helper()
		got, err := c.GetForce(api.GetForceParams{ForceID: f.ForceID})
		if err != nil || got.Lookup != "found" || got.ForceResult == nil || !reflect.DeepEqual(*got.ForceResult, f) {
			t.Errorf("GetForce after %s = %+v, %v; want found %+v", after, got, err, f)
		}
	}
	row, err := e.st.GetSpawn(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	const token = "99aabbccddeeff00"
	_, version, err := e.st.MoveToPendingWithExit(r.ID, row.Snapshot, e.clock.Now().UnixMilli(), token, "", "", store.LaunchOwner{}, store.BusySendDefaultsChoice{}, storefix.ExitEvidence(t, e.st, r.ID))
	if err == nil {
		_, err = e.st.PublishBusySendLaunch(r.ID, version, token)
	}
	if err != nil {
		t.Fatal(err)
	}
	read(c, "a new Launch published")
	if err := storefix.DeleteWithExit(t, e.st, r.ID); err != nil {
		t.Fatal(err)
	}
	read(c, "the row's deletion")
	_, err = c.ForceDelivery(api.ForceDeliveryParams{InstanceID: r.ID})
	assertOneName(t, err, "ErrSpawnNotFound")
	reopened, _ := e.client(t)
	read(reopened, "reopening the store")
	if got := storefix.ReadBusySendCounts(t, e.dbPath).Forces; got != 1 {
		t.Errorf("%d attempts; want only the first", got)
	}
}

// busyCaller is a canonical Caller UUID.
const busyCaller = "0f8fad5b-d9cb-469f-a165-70867728950e"

// busyPair is busyCaller's pair for Message ID id.
func busyPair(id string) store.BusySendPair {
	return store.BusySendPair{CallerUUID: busyCaller, DeliveryRequestID: id}
}

// TestBusySendLookups (SR-4.1, SR-5.1, SR-5.3, SR-5.4, SR-5.5; M1
// during-coding Result field and reason mapping): get-delivery projects each
// stored Result class and compatibility outcome exactly, holding no text, raw
// evidence or path, for either hex case of the Caller UUID; unknown pairs and force ids
// and expired ones answer with a null payload. The reads start no work and
// write nothing: neither elapsed idle time, the row's deletion nor reopening
// the store changes an answer.
func TestBusySendLookups(t *testing.T) {
	t.Parallel()
	e := newKillEnv(t)
	e.rowAgent = "codex" // force on an agent busy send doesn't cover; a covered force is E5's
	r := e.seedRow(t, killRowSpec{})
	c, _ := e.client(t)
	calls, nameCalls := len(e.rec.SocketCalls()), len(e.rec.Calls())
	since := int64(1_700_000_000_000)
	token, evidence, sha := "aabbccddeeff0011", "codex_item_started", strings.Repeat("ab", 32)
	base := api.DeliveryResult{CallerUUID: busyCaller, InstanceID: r.ID, RetryWith: "none"}
	for _, tc := range []struct {
		seed storefix.BusySendResultSeed
		want func(api.DeliveryResult) api.DeliveryResult
	}{
		{storefix.BusySendResultSeed{Pair: busyPair("delivered"), Class: store.BusySendClassDeliveredConfirmed, Final: true,
			Reason: "exact_receipt", LaunchToken: token, SessionID: "sess-1", EvidenceKind: store.BusySendEvidenceKind(evidence),
			RecordID: "rec-1", RecordSHA256: sha},
			func(d api.DeliveryResult) api.DeliveryResult {
				d.Class, d.Final, d.Reason, d.Outcome = "delivered_confirmed", true, "exact_receipt", "submitted"
				d.LaunchToken, d.SessionID, d.EvidenceKind, d.RecordID, d.RecordSHA256 = &token, "sess-1", &evidence, "rec-1", sha
				return d
			}},
		{storefix.BusySendResultSeed{Pair: busyPair("held"), Class: store.BusySendClassHeld, Reason: "turn_running",
			Wait: &store.BusySendWait{Reason: store.BusySendWaitTurnRunning, SinceMillis: since}},
			func(d api.DeliveryResult) api.DeliveryResult {
				d.Class, d.Reason, d.Outcome = "held", "turn_running", "not_started"
				d.Wait = &api.DeliveryWait{Reason: "turn_running", Since: time.UnixMilli(since).UTC()}
				return d
			}},
		{storefix.BusySendResultSeed{Pair: busyPair("queued"), Class: store.BusySendClassQueued, Reason: "earlier_message",
			Forced: true, Wait: &store.BusySendWait{Reason: store.BusySendWaitEarlierMessage, SinceMillis: since, Earlier: ptr(busyPair("held"))}},
			func(d api.DeliveryResult) api.DeliveryResult {
				d.Class, d.Reason, d.Forced, d.Outcome = "queued", "earlier_message", true, "submission_unconfirmed"
				d.Wait = &api.DeliveryWait{Reason: "earlier_message", Since: time.UnixMilli(since).UTC(),
					Earlier: &api.DeliveryPair{CallerUUID: busyCaller, RequestID: "held"}}
				return d
			}},
		{storefix.BusySendResultSeed{Pair: busyPair("unconfirmed"), Class: store.BusySendClassSentUnconfirmed, Final: true,
			Reason: "confirmation_off", LaunchToken: token, Transport: store.BusySendTransportWritten},
			func(d api.DeliveryResult) api.DeliveryResult {
				d.Class, d.Final, d.Reason, d.LaunchToken, d.Outcome = "sent_unconfirmed", true, "confirmation_off", &token, "submission_unconfirmed"
				return d
			}},
		// SR-5.3: only the stored fact that the latest prompt was partly
		// written maps to partial_input_unconfirmed, whatever the class.
		{storefix.BusySendResultSeed{Pair: busyPair("partial"), Class: store.BusySendClassSentUnconfirmed, Final: true,
			Reason: "transport_partial", LaunchToken: token, Transport: store.BusySendTransportPartial},
			func(d api.DeliveryResult) api.DeliveryResult {
				d.Class, d.Final, d.Reason, d.LaunchToken, d.Outcome = "sent_unconfirmed", true, "transport_partial", &token, "partial_input_unconfirmed"
				return d
			}},
		{storefix.BusySendResultSeed{Pair: busyPair("not-started"), Class: store.BusySendClassNotStarted, Final: true,
			Reason: "launch_ended"},
			func(d api.DeliveryResult) api.DeliveryResult {
				d.Class, d.Final, d.Reason, d.ResendSafe, d.RetryWith, d.Outcome = "not_started", true, "launch_ended", true, "new_id", "not_started"
				return d
			}},
		{storefix.BusySendResultSeed{Pair: busyPair("expired"), Expired: true}, nil},
	} {
		tc.seed.InstanceID = r.ID
		storefix.SeedBusySendResult(t, e.dbPath, tc.seed)
		got, err := c.GetDelivery(api.GetDeliveryParams{CallerUUID: strings.ToUpper(busyCaller), DeliveryRequestID: tc.seed.Pair.DeliveryRequestID})
		want := api.GetDeliveryResult{Lookup: "retention_expired"}
		if tc.want != nil {
			b := base
			b.RequestID = tc.seed.Pair.DeliveryRequestID
			result := tc.want(b)
			want = api.GetDeliveryResult{Lookup: "found", Result: &result}
		}
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("GetDelivery(%s) = %+v, %v; want %+v", tc.seed.Pair.DeliveryRequestID, got, err, want)
		}
	}
	for _, tc := range []struct {
		id   string
		want string
	}{
		{"delivered", `{"lookup":"found","result":{"caller_uuid":"` + busyCaller + `","request_id":"delivered","class":"delivered_confirmed","final":true,"reason":"exact_receipt","resend_safe":false,"retry_with":"none","wait":null,"forced":false,"instance_id":"` + r.ID + `","launch_token":"` + token + `","session_id":"sess-1","evidence_kind":"codex_item_started","record_id":"rec-1","record_sha256":"` + sha + `","outcome":"submitted"}}`},
		{"held", `{"lookup":"found","result":{"caller_uuid":"` + busyCaller + `","request_id":"held","class":"held","final":false,"reason":"turn_running","resend_safe":false,"retry_with":"none","wait":{"reason":"turn_running","since":"2023-11-14T22:13:20Z"},"forced":false,"instance_id":"` + r.ID + `","launch_token":null,"evidence_kind":null,"outcome":"not_started"}}`},
		{"expired", `{"lookup":"retention_expired","result":null}`},
		{"never-sent", `{"lookup":"unknown","result":null}`},
	} {
		got, err := c.GetDelivery(api.GetDeliveryParams{CallerUUID: busyCaller, DeliveryRequestID: tc.id})
		if raw, _ := json.Marshal(got); err != nil || string(raw) != tc.want {
			t.Errorf("get-delivery %s JSON = %s, %v\nwant %s", tc.id, raw, err, tc.want)
		}
	}
	expired := forceRefused(t, c, r.ID, e.clock.Now())
	storefix.ExpireBusySendForce(t, e.dbPath, expired.ForceID, e.clock.Now().UnixMilli())
	for id, want := range map[string]string{
		expired.ForceID:     `{"lookup":"retention_expired","force_result":null}`,
		uuid.New().String(): `{"lookup":"unknown","force_result":null}`,
	} {
		got, err := c.GetForce(api.GetForceParams{ForceID: id})
		if raw, _ := json.Marshal(got); err != nil || string(raw) != want {
			t.Errorf("get-force JSON = %s, %v\nwant %s", raw, err, want)
		}
	}

	ids := []string{"delivered", "held", "queued", "unconfirmed", "partial", "not-started", "expired", "never-sent"}
	answers := func(c *api.Client) map[string]api.GetDeliveryResult {
		t.Helper()
		out := map[string]api.GetDeliveryResult{}
		for _, id := range ids {
			got, err := c.GetDelivery(api.GetDeliveryParams{CallerUUID: busyCaller, DeliveryRequestID: id})
			if err != nil {
				t.Fatalf("GetDelivery(%s): %v", id, err)
			}
			out[id] = got
		}
		return out
	}
	first, facts, cols, counts := answers(c), busyFacts(t, e.dbPath, r.ID), e.columns(t, r.ID), storefix.ReadBusySendCounts(t, e.dbPath)
	e.clock.Advance(400 * 24 * time.Hour)
	if got := answers(c); !reflect.DeepEqual(got, first) {
		t.Errorf("answers after idle time = %+v; want unchanged %+v", got, first)
	}
	if got := busyFacts(t, e.dbPath, r.ID); !reflect.DeepEqual(got, facts) {
		t.Errorf("busy-send core changed by reads: %+v; want %+v", got, facts)
	}
	e.assertRowUnchanged(t, r.ID, cols)
	if got := storefix.ReadBusySendCounts(t, e.dbPath); got != counts {
		t.Errorf("records = %+v; want unchanged %+v", got, counts)
	}
	if got := e.rec.SocketCalls()[calls:]; len(got) != 0 || len(e.rec.Calls()) != nameCalls {
		t.Errorf("tmux calls %+v; want none", got)
	}
	if err := storefix.DeleteWithExit(t, e.st, r.ID); err != nil {
		t.Fatal(err)
	}
	reopened, _ := e.client(t)
	for _, id := range []string{"delivered", "unconfirmed", "not-started", "expired", "never-sent"} {
		if got := answers(reopened)[id]; !reflect.DeepEqual(got, first[id]) {
			t.Errorf("%s after the row's deletion and a reopen = %+v; want %+v", id, got, first[id])
		}
	}
}

// TestBusySendLookupRefusals (SR-1.3, SR-4.1, SR-5.4): a malformed force_id,
// Caller UUID or Message ID is ErrInvalidFlags. A failure of any read a
// lookup makes, its own query or a follow-up one, is an unnamed error with no
// details or payload, never found or unknown, and writes nothing; a store that
// cannot be reached is ErrStoreBusy.
func TestBusySendLookupRefusals(t *testing.T) {
	t.Parallel()
	e := newKillEnv(t)
	e.rowAgent = "codex" // force on an agent busy send doesn't cover; a covered force is E5's
	r := e.seedRow(t, killRowSpec{})
	c, _ := e.client(t)
	for _, id := range []string{"", "x", "{" + busyCaller + "}", "urn:uuid:" + busyCaller, strings.ReplaceAll(busyCaller, "-", "")} {
		_, err := c.GetForce(api.GetForceParams{ForceID: id})
		assertOneName(t, err, "ErrInvalidFlags")
		_, err = c.GetDelivery(api.GetDeliveryParams{CallerUUID: id, DeliveryRequestID: "m-1"})
		assertOneName(t, err, "ErrInvalidFlags")
	}
	for _, id := range []string{"", "\xff"} {
		_, err := c.GetDelivery(api.GetDeliveryParams{CallerUUID: busyCaller, DeliveryRequestID: id})
		assertOneName(t, err, "ErrInvalidFlags")
	}

	// Read failures, injected at the SQLite driver into the one statement each
	// case names (test-review TR1).
	force := forceRefused(t, c, r.ID, e.clock.Now())
	since := e.clock.Now().UnixMilli()
	storefix.SeedBusySendResult(t, e.dbPath, storefix.BusySendResultSeed{InstanceID: r.ID, Pair: busyPair("held"),
		Class: store.BusySendClassHeld, Reason: "turn_running", Wait: &store.BusySendWait{Reason: store.BusySendWaitTurnRunning, SinceMillis: since}})
	storefix.SeedBusySendResult(t, e.dbPath, storefix.BusySendResultSeed{InstanceID: r.ID, Pair: busyPair("queued"),
		Class: store.BusySendClassQueued, Reason: "earlier_message",
		Wait: &store.BusySendWait{Reason: store.BusySendWaitEarlierMessage, SinceMillis: since, Earlier: ptr(busyPair("held"))}})
	facts, counts, calls := busyFacts(t, e.dbPath, r.ID), storefix.ReadBusySendCounts(t, e.dbPath), len(e.rec.SocketCalls())
	var failing atomic.Value
	failing.Store("")
	var hits atomic.Int32
	stop, err := api.ObserveClientSQLiteForTest(c, func(d driver.Driver) driver.Driver {
		return sqliteobserve.Wrap(d, sqliteobserve.Hooks{Before: func(_ context.Context, q string) error {
			if s := failing.Load().(string); s != "" && strings.Contains(q, s) {
				hits.Add(1)
				return errors.New("injected lookup read failure")
			}
			return nil
		}})
	})
	if err != nil {
		t.Fatal(err)
	}
	getForce := func() (any, error) { return c.GetForce(api.GetForceParams{ForceID: force.ForceID}) }
	getDelivery := func(id string) func() (any, error) {
		return func() (any, error) {
			return c.GetDelivery(api.GetDeliveryParams{CallerUUID: busyCaller, DeliveryRequestID: id})
		}
	}
	for _, tc := range []struct {
		name, statement string
		call            func() (any, error)
		zero            any
	}{
		{"get-force attempt", "FROM busy_send_forces WHERE force_id = ?", getForce, api.GetForceResult{}},
		{"get-force carried pairs", "FROM busy_send_force_members m JOIN", getForce, api.GetForceResult{}},
		{"get-delivery Result", "FROM busy_send_results p JOIN busy_send_messages m", getDelivery("held"), api.GetDeliveryResult{}},
		{"get-delivery Result with its earlier pair", "LEFT JOIN busy_send_results e ON e.message_id = m.wait_earlier_message_id", getDelivery("queued"), api.GetDeliveryResult{}},
		{"get-delivery absent pair", "SELECT EXISTS(SELECT 1 FROM busy_send_results", getDelivery("never-sent"), api.GetDeliveryResult{}},
	} {
		hits.Store(0)
		failing.Store(tc.statement)
		got, err := tc.call()
		failing.Store("")
		if hits.Load() == 0 {
			t.Errorf("%s: the injected statement never ran", tc.name)
			continue
		}
		assertOneName(t, err, "ErrInternal")
		if api.ErrDetails(err) != nil || !reflect.DeepEqual(got, tc.zero) {
			t.Errorf("%s = %+v, details %+v; want a bare error and no lookup", tc.name, got, api.ErrDetails(err))
		}
	}
	if err := stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := busyFacts(t, e.dbPath, r.ID); !reflect.DeepEqual(got, facts) {
		t.Errorf("busy-send core changed by failed reads: %+v; want %+v", got, facts)
	}
	if got := storefix.ReadBusySendCounts(t, e.dbPath); got != counts {
		t.Errorf("records = %+v; want unchanged %+v", got, counts)
	}
	if got := e.rec.SocketCalls()[calls:]; len(got) != 0 {
		t.Errorf("tmux calls %+v; want none", got)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	defer api.SetStoreContextForTest(c, ctx)()
	_, err = c.GetForce(api.GetForceParams{ForceID: busyCaller})
	assertOneName(t, err, "ErrStoreBusy")
	_, err = c.GetDelivery(api.GetDeliveryParams{CallerUUID: busyCaller, DeliveryRequestID: "m-1"})
	assertOneName(t, err, "ErrStoreBusy")
}

// TestBusySendRetentionLookups exercises durable Lookup and permanent pair
// ownership after real expiry, including a never-issued deleted Message.
func TestBusySendRetentionLookups(t *testing.T) {
	t.Parallel()
	e := newKillEnv(t)
	f := newForceAgent(t, e)
	const otherCaller = "1f8fad5b-d9cb-469f-a165-70867728950e"
	f.send(t, "same", "first")
	f.drive = forceTurns(t, f, 0, false)
	first, _ := f.force(t)
	if len(first.CarriedResults) != 1 || !first.CarriedResults[0].Final {
		t.Fatalf("first force = %+v; want one Final Result", first)
	}
	cutoff := e.clock.Now().Add(time.Millisecond)
	e.clock.Advance(time.Hour)
	busyAdmit(t, f.c, api.SendKeysParams{InstanceID: f.id, Text: "second", ConfirmedDelivery: ptr(true),
		CallerUUID: ptr(otherCaller), DeliveryRequestID: "same"})
	f.touched()
	f.drive = forceTurns(t, f, 0, false)
	second, _ := f.force(t)
	f.drive = nil
	if len(second.CarriedResults) != 1 || !second.CarriedResults[0].Final {
		t.Fatalf("second force = %+v; want one independent Final Result", second)
	}
	f.send(t, "deleted", "never issued")
	inputs, issued := paneInputs(e), len(f.route.issued)
	counts := storefix.ReadBusySendCounts(t, e.dbPath)
	other := e.seedRow(t, killRowSpec{})
	seedBusyCovered(t, e, other.ID)
	_, otherTouched := busyWake(t, e, other.ID)
	// Close every long-lived connection before opening the store again.
	reopen := func() {
		t.Helper()
		if err := f.c.Close(); err != nil {
			t.Fatal(err)
		}
		if err := e.st.Close(); err != nil {
			t.Fatal(err)
		}
		st, err := store.Open(e.dbPath)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = st.Close() })
		e.st, e.store.st = st, st
		f.c, _ = e.client(t)
	}
	want := map[store.BusySendPair]api.GetDeliveryResult{busyPair("same"): {Lookup: "found", Result: &first.CarriedResults[0]}}
	want[store.BusySendPair{CallerUUID: otherCaller, DeliveryRequestID: "same"}] = api.GetDeliveryResult{Lookup: "found", Result: &second.CarriedResults[0]}
	read := func() {
		t.Helper()
		for pair, expected := range want {
			got, err := f.c.GetDelivery(api.GetDeliveryParams{CallerUUID: pair.CallerUUID, DeliveryRequestID: pair.DeliveryRequestID})
			if err != nil || !reflect.DeepEqual(got, expected) {
				t.Errorf("Lookup(%+v) = %+v, %v; want %+v", pair, got, err, expected)
			}
		}
	}
	reopen()
	read()
	if a := apitest.ApplyAgentHook(t, e.dbPath, f.id, "SessionEnd", f.session); !a.Applied {
		t.Fatalf("SessionEnd = %+v; want applied", a)
	}
	read()
	if err := storefix.DeleteWithExit(t, e.st, f.id); err != nil {
		t.Fatal(err)
	}
	deleted := api.DeliveryResult{CallerUUID: busyCaller, RequestID: "deleted", InstanceID: f.id,
		Class: "not_started", Final: true, Reason: "record_deleted", ResendSafe: true, RetryWith: "new_id", Outcome: "not_started"}
	want[busyPair("deleted")] = api.GetDeliveryResult{Lookup: "found", Result: &deleted}
	read()
	reopen()
	read()
	if res, err := f.c.Expire(olderThan(e.clock.Now().Sub(cutoff))); err != nil || res.Count != 0 || res.Kept != 0 {
		t.Fatalf("Expire after deletion = %+v, %v; want retention only", res, err)
	}
	want[busyPair("same")] = api.GetDeliveryResult{Lookup: "retention_expired"}
	read() // the identical Message ID owned by otherCaller still has its Result
	reopen()
	read()
	for _, tc := range []struct{ id, text string }{
		{f.id, "first"}, // valid repeat, despite the deleted target
		{f.id, "changed text"},
		{other.ID, "first"},
	} {
		res, err := f.c.SendKeys(api.SendKeysParams{InstanceID: tc.id, Text: tc.text, ConfirmedDelivery: ptr(true),
			CallerUUID: ptr(busyCaller), DeliveryRequestID: "same"})
		assertCantSendDetails(t, err, api.CantSendDetails{Class: "cant_send", Reason: "retention_expired", RetryWith: "none"})
		if !reflect.DeepEqual(res, api.SendKeysResult{}) {
			t.Errorf("expired-pair refusal returned %+v; want no acceptance", res)
		}
	}
	if _, err := f.c.Expire(olderThan(0)); err != nil {
		t.Fatal(err)
	}
	for pair := range want {
		want[pair] = api.GetDeliveryResult{Lookup: "retention_expired"}
	}
	reopen()
	read()
	if storefix.ReadBusySendCounts(t, e.dbPath) != counts || paneInputs(e) != inputs || len(f.route.issued) != issued || f.touched() || otherTouched() {
		t.Error("retention, Lookup or an expired-pair refusal created a Message, pair, force, input or wake")
	}
}

// TestForceCarriedResults (SR-4.1, SR-4.3, SR-5.5, SR-5.6; E5 Main Agent
// decision 6): a force that handed over a, an unconfirmed u and b lists a and
// b in carried_pairs and their Results in carried_results, u in neither.
// Once Client.Expire removes a's Result,
// get-force stays found with a's pair kept and only b's Result, nothing made
// up for a, and get's and status's delivery.force filter the same; a's
// get-delivery reads retention_expired. The force's own expiry clears the
// summary without falling back to an older retained force. Lookup survives
// Launch End, deletion and reopening.
func TestForceCarriedResults(t *testing.T) {
	t.Parallel()
	e := newKillEnv(t)
	f := newForceAgent(t, e)
	// An older force with a later end timestamp catches a fallback to an
	// older retained force when the latest expires (a clock adjustment).
	e.clock.Advance(time.Hour)
	f.send(t, "older", "older")
	f.drive = forceTurns(t, f, 0, false)
	older, _ := f.force(t)
	e.clock.Advance(-time.Hour)
	f.send(t, "a", forceText)
	f.send(t, "", "unconfirmed")
	f.send(t, "b", forceText)
	turns := forceTurns(t, f, 2, false)
	priorPrompts := len(f.route.issued)
	var firstFinal time.Time
	f.drive = func(pause int, woke bool) {
		turns(pause, woke)
		if firstFinal.IsZero() && len(f.route.issued) > priorPrompts {
			firstFinal = e.clock.Now()
		}
	}
	got, _ := f.force(t)
	f.drive = nil
	if got.Outcome != "handed_over" || len(got.CarriedPairs) != 2 || len(got.CarriedResults) != 2 {
		t.Fatalf("force = %+v; want a and b handed over with their Results", got)
	}

	inputs, issued := paneInputs(e), len(f.route.issued)
	counts := storefix.ReadBusySendCounts(t, e.dbPath)
	f.touched()
	if res, err := f.c.Expire(olderThan(e.clock.Now().Sub(firstFinal.Add(time.Millisecond)))); err != nil || res.Count != 0 || res.Kept != 0 {
		t.Fatalf("member expiry = %+v, %v; want retention only", res, err)
	}

	want := got
	want.CarriedResults = got.CarriedResults[1:]
	f.assertRecorded(t, want)
	if a, err := f.c.GetDelivery(api.GetDeliveryParams{CallerUUID: busyCaller, DeliveryRequestID: "a"}); err != nil || a.Lookup != "retention_expired" || a.Result != nil {
		t.Errorf("get-delivery a = %+v, %v; want retention_expired", a, err)
	}
	read := func(c *api.Client, after string) {
		t.Helper()
		if found, err := c.GetForce(api.GetForceParams{ForceID: got.ForceID}); err != nil || found.Lookup != "found" ||
			found.ForceResult == nil || !reflect.DeepEqual(*found.ForceResult, want) {
			t.Errorf("get-force after %s = %+v, %v; want found %+v", after, found, err, want)
		}
	}
	if a := storefix.ApplyAgentHook(t, e.st, f.id, "SessionEnd", f.session); !a.Applied {
		t.Fatalf("SessionEnd %+v; want applied", a)
	}
	read(f.c, "Launch End")
	if err := f.c.Close(); err != nil {
		t.Fatal(err)
	}
	f.c, _ = e.client(t)
	read(f.c, "a reopen")
	// End time, not the member's Final time, decides the force's retention.
	e.clock.Advance(time.Second)
	if _, err := f.c.Expire(olderThan(e.clock.Now().Sub(got.EndedAt.Add(time.Millisecond)))); err != nil {
		t.Fatal(err)
	}
	readExpired := func(c *api.Client) {
		t.Helper()
		if found, err := c.GetForce(api.GetForceParams{ForceID: got.ForceID}); err != nil || found.Lookup != "retention_expired" || found.ForceResult != nil {
			t.Errorf("get-force = %+v, %v; want retention_expired with no force", found, err)
		}
	}
	readExpired(f.c)
	if found, err := f.c.GetForce(api.GetForceParams{ForceID: older.ForceID}); err != nil || found.Lookup != "found" || found.ForceResult == nil {
		t.Errorf("older force = %+v, %v; want still retained", found, err)
	}
	row, err := f.c.Get(f.id)
	if err != nil || row.Delivery == nil || row.Delivery.Force != nil {
		t.Errorf("get after force expiry = %+v, %v; want no force", row.Delivery, err)
	}
	status, err := f.c.Status(f.id)
	if err != nil || status.Delivery == nil || status.Delivery.Force != nil {
		t.Errorf("status after force expiry = %+v, %v; want no force", status.Delivery, err)
	}
	if err := storefix.DeleteWithExit(t, e.st, f.id); err != nil {
		t.Fatal(err)
	}
	readExpired(f.c)
	if err := f.c.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, _ := e.client(t)
	readExpired(reopened)
	if f.touched() || paneInputs(e) != inputs || len(f.route.issued) != issued || storefix.ReadBusySendCounts(t, e.dbPath) != counts {
		t.Error("retention or its projections created a wake, input, Message or force")
	}
}

// TestForceReadsResultsWithItsFacts (SR-4.1, SR-5.6; M1 G3
// returned-equals-recorded): get-force and get's delivery.force read a
// force's outcome and its carried Messages' Results in one read transaction.
// A release of the force, committed while the read that answers is between
// them (its last read of the carried Results), shows in neither: the answer
// is the running force with its Messages still claimed, as before, and the
// next read returns both changed.
func TestForceReadsResultsWithItsFacts(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		read func(c *api.Client, id, forceID string) (api.ForceResult, error)
	}{
		{"get-force", func(c *api.Client, _, forceID string) (api.ForceResult, error) {
			got, err := c.GetForce(api.GetForceParams{ForceID: forceID})
			if err == nil && got.ForceResult == nil {
				err = fmt.Errorf("lookup %s", got.Lookup)
			}
			if err != nil {
				return api.ForceResult{}, err
			}
			return *got.ForceResult, nil
		}},
		{"get delivery.force", func(c *api.Client, id, _ string) (api.ForceResult, error) {
			row, err := c.Get(id)
			if err == nil && (row.Delivery == nil || row.Delivery.Force == nil) {
				err = errors.New("no delivery.force")
			}
			if err != nil {
				return api.ForceResult{}, err
			}
			return *row.Delivery.Force, nil
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newKillEnv(t)
			f := newForceAgent(t, e)
			f.send(t, "a", "hello")
			f.send(t, "b", "again")
			pub, err := e.st.GetBusySendPublishedLaunch(f.id)
			if err != nil {
				t.Fatal(err)
			}
			claim, err := e.st.ClaimBusySendForce(context.Background(), store.BusySendForceClaimRequest{InstanceID: f.id, Launch: pub,
				ForceID: uuid.NewString(), OwnerCallID: uuid.NewString(), ActiveBudget: 12 * time.Second, CleanupBudget: 3 * time.Second, At: e.clock.Now()})
			if err != nil || claim.Handover == nil {
				t.Fatalf("force claim = %+v, %v; want running", claim, err)
			}
			e.clock.Advance(15*time.Second + time.Millisecond)
			// read reads through tc.read while counting its reads of the carried
			// Results, releasing the force just before the at-th (0: never).
			read := func(at int32) (api.ForceResult, int32) {
				t.Helper()
				var seen atomic.Int32
				stop, err := api.ObserveClientSQLiteForTest(f.c, func(d driver.Driver) driver.Driver {
					return sqliteobserve.Wrap(d, sqliteobserve.Hooks{Before: func(_ context.Context, q string) error {
						if strings.Contains(q, "JOIN busy_send_force_members fm ON fm.message_id") && seen.Add(1) == at {
							if rel, err := e.st.ReleaseAbandonedBusySendForce(context.Background(), f.id, e.clock.Now()); err != nil || !rel.Released || rel.Returned != 2 {
								t.Errorf("release between the force's facts and its Results = %+v, %v; want both Messages returned", rel, err)
							}
						}
						return nil
					}})
				})
				if err != nil {
					t.Fatal(err)
				}
				got, rerr := tc.read(f.c, f.id, claim.Force.ForceID)
				if err := stop(context.Background()); err != nil {
					t.Fatal(err)
				}
				if rerr != nil {
					t.Fatalf("read: %v", rerr)
				}
				return got, seen.Load()
			}
			before, reads := read(0)
			if before.Outcome != "running" || len(before.CarriedResults) != 2 || reads == 0 {
				t.Fatalf("read before = %+v after %d reads of its Results; want the running force with two Results", before, reads)
			}
			if during, _ := read(reads); !reflect.DeepEqual(during, before) {
				t.Errorf("read across the release = %+v; want it as before %+v", during, before)
			}
			after, _ := read(0)
			if after.Outcome != "refused" || after.Reason != "not_issued" || reflect.DeepEqual(after.CarriedResults, before.CarriedResults) {
				t.Errorf("read after the release = %+v; want refused not_issued with the Messages' new Results", after)
			}
		})
	}
}
