package store_test

// Busy-send admission's store transactions (Task t2.xis.oo.tc; SR-1.1, SR-1.2,
// SR-1.6), through AdmitBusySend on real SQLite. M1 (Model Doc t1.kii.91, "Not
// here (during-coding)"; Plan b.xis ## Decisions) leaves these E2 store
// obligations to source tests, each named where it is asserted:
//   - custody: one durable Held record per accepted Message, in one accepted
//     order, with its reservation;
//   - identity: each Message's own private id, and a pair only for a
//     confirmed send;
//   - pair uniqueness: one owning Message per confirmed pair, store-wide.
//
// Pair replay, admission's idempotence (SR-1.2), is asserted with them: a
// repeat of an owned pair returns the owner's current Result and stores
// nothing. M1's repeated acc, a route's acceptance reply for an issued prompt,
// is t3.xis.oo.xe.58's. The public mapping, malformed input and the wake touch
// are pkg/api's (busy_send_*_test.go).

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// admissionAgent seeds a waiting Claude row whose published conversation and
// current Launch are covered, and returns its id. A wake path, when given, is
// recorded with a Claude route on the current Launch.
func admissionAgent(t *testing.T, path, wake string) string {
	t.Helper()
	id, err := apitest.SeedSpawn(path, "", store.StateWaiting, t.TempDir(), "off", uuid.NewString(), false)
	if err != nil {
		t.Fatal(err)
	}
	if err := apitest.SeedBusySendCoverage(path, id, store.BusySendCovered, store.BusySendCovered); err != nil {
		t.Fatal(err)
	}
	if wake != "" {
		if err := apitest.SeedBusySendRoute(path, id, string(store.BusySendRouteClaudeHooks), wake); err != nil {
			t.Fatal(err)
		}
	}
	return id
}

// admissionInbox is what an admission changes: id's latest conversation's
// sequence, reservations and revision, and the store's Message and pair
// counts. A refused, failed or replayed admission leaves it as it was.
type admissionInbox struct {
	seq, count, bytes, revision int64
	counts                      storefix.BusySendCounts
}

func readAdmissionInbox(t *testing.T, path, id string) admissionInbox {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var in admissionInbox
	if err := db.QueryRow(`SELECT accepted_seq, reserved_count, reserved_bytes, revision FROM busy_conversations
	    WHERE instance_id = ? ORDER BY generation DESC LIMIT 1`, id).Scan(&in.seq, &in.count, &in.bytes, &in.revision); err != nil {
		t.Fatal(err)
	}
	in.counts = storefix.ReadBusySendCounts(t, path)
	return in
}

// storedMessage is one Message's durable custody facts.
type storedMessage struct {
	id, mode, sha, place, class, wait string
	seq, at, size, since              int64
	confirmed, reserved               bool
	text                              []byte
}

// readStoredMessages reads id's Messages in accepted order.
func readStoredMessages(t *testing.T, path, id string) []storedMessage {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query(`SELECT message_id, accepted_seq, accepted_at_ms, send_mode, confirmed, text, text_bytes, text_sha256,
	        place, reserved, result_class, wait_reason, wait_since_ms
	    FROM busy_send_messages WHERE instance_id = ? ORDER BY accepted_seq`, id)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []storedMessage
	for rows.Next() {
		var m storedMessage
		if err := rows.Scan(&m.id, &m.seq, &m.at, &m.mode, &m.confirmed, &m.text, &m.size, &m.sha,
			&m.place, &m.reserved, &m.class, &m.wait, &m.since); err != nil {
			t.Fatal(err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// exitEvidenceForTest is id's exit scope as read, taken as positive exit
// evidence: the test judges every owned process in it gone, since the store
// never probes one.
func exitEvidenceForTest(t *testing.T, s *store.Store, id string) *store.BusySendExitEvidence {
	t.Helper()
	scope, err := s.ReadBusySendExitScope(id)
	if err != nil {
		t.Fatal(err)
	}
	return &store.BusySendExitEvidence{Scope: scope}
}

// deleteCoveredForTest deletes id's covered row as administrator deletion
// does once it established exit evidence (exitEvidenceForTest).
func deleteCoveredForTest(t *testing.T, s *store.Store, id string) {
	t.Helper()
	row, err := s.GetSpawn(id)
	if err != nil {
		t.Fatal(err)
	}
	if res, err := s.DeleteBusySendCovered(id, row.Snapshot, *exitEvidenceForTest(t, s, id)); err != nil || res != store.CondApplied {
		t.Fatalf("DeleteBusySendCovered(%s) = %v, %v; want applied", id, res, err)
	}
}

// awaitSignal waits for ch, failing the test after 10 s.
func awaitSignal(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(10 * time.Second):
		t.Fatalf("%s was not reached within 10 s", what)
	}
}

// TestBusySendAdmissionHoldsCustody: admission makes its lease WAL
// synchronous=FULL before BEGIN IMMEDIATE (SR-1.1), stores each accepted
// Message Held with its exact text bytes, digest, resolved options, acceptance
// time and the next accepted sequence, reserves its count and bytes, and
// returns the Result a Lookup reads after reopening. M1 custody: one durable
// record per accepted Message, in accepted order. M1 identity: each Message has
// its own private id, and only the confirmed send owns a pair, so the
// unconfirmed one's Result names none and the later Message waits behind the
// earlier one's pair.
func TestBusySendAdmissionHoldsCustody(t *testing.T) {
	t.Parallel()
	s, path := storefix.OpenTempStore(t)
	wake := filepath.Join(t.TempDir(), "wake")
	id := admissionAgent(t, path, wake)
	before := readAdmissionInbox(t, path, id)
	var mu sync.Mutex
	var statements []string
	store.ObserveDecisionBoundaryForTest(t, s, path, func(_ context.Context, q string) error {
		mu.Lock()
		defer mu.Unlock()
		statements = append(statements, q)
		return nil
	}, nil)
	pair := store.BusySendPair{CallerUUID: uuid.NewString(), DeliveryRequestID: "req-1"}
	mode, confirmed := store.BusySendWaitTurn, true
	texts := []string{" keep TAB\t, LF and café 🌍 as sent\n", "second"}
	start := time.Now().UnixMilli()
	first, err := s.AdmitBusySend(store.BusySendAdmissionRequest{InstanceID: id, Text: []byte(texts[0]),
		CallerUUID: pair.CallerUUID, DeliveryRequestID: pair.DeliveryRequestID, SendMode: &mode, Confirmed: &confirmed})
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.AdmitBusySend(store.BusySendAdmissionRequest{InstanceID: id, Text: []byte(texts[1])})
	if err != nil {
		t.Fatal(err)
	}
	end := time.Now().UnixMilli()

	mu.Lock()
	full, begin := slices.Index(statements, "PRAGMA synchronous = FULL"), slices.Index(statements, "BEGIN IMMEDIATE")
	insert := slices.IndexFunc(statements, func(q string) bool { return strings.Contains(q, "INSERT INTO busy_send_messages") })
	if full < 0 || begin <= full || insert <= begin {
		t.Errorf("the first admission ran %q; want PRAGMA synchronous = FULL, then BEGIN IMMEDIATE, then its Message insert", statements)
	}
	mu.Unlock()
	for _, a := range []store.BusySendAdmission{first, second} {
		if a.Outcome != store.BusySendAdmitted || a.WakePath != wake || a.Result.InstanceID != id || a.Result.SendMode != mode ||
			a.Result.Class != store.BusySendClassHeld || a.Result.Final || a.Result.Wait == nil {
			t.Fatalf("admission %+v; want admitted Held on %s, wake %s", a, id, wake)
		}
	}
	if first.Result.Pair != pair || first.Result.Wait.Reason != store.BusySendWaitReadinessUnknown {
		t.Errorf("confirmed Result pair %+v, wait %+v; want %+v waiting readiness_unknown", first.Result.Pair, *first.Result.Wait, pair)
	}
	if w := second.Result.Wait; second.Result.Pair != (store.BusySendPair{}) || w.Reason != store.BusySendWaitEarlierMessage ||
		w.Earlier == nil || *w.Earlier != pair {
		t.Errorf("unconfirmed Result pair %+v, wait %+v; want no pair, waiting earlier_message behind %+v", second.Result.Pair, *w, pair)
	}

	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	found, err := reopened.ReadBusySendResult(pair.CallerUUID, pair.DeliveryRequestID)
	if err != nil || found.Lookup != store.BusySendLookupFound || !reflect.DeepEqual(*found.Result, first.Result) {
		t.Errorf("reopened Lookup %+v, %v; want found %+v", found, err, first.Result)
	}
	want := admissionInbox{seq: before.seq + 2, count: before.count + 2, bytes: before.bytes + int64(len(texts[0])+len(texts[1])),
		revision: before.revision + 2, counts: storefix.BusySendCounts{Messages: 2, Pairs: 1}}
	if got := readAdmissionInbox(t, path, id); got != want {
		t.Errorf("reopened inbox %+v; want %+v", got, want)
	}
	messages := readStoredMessages(t, path, id)
	if len(messages) != 2 || messages[0].id == messages[1].id || messages[0].at != first.Result.AcceptedAtMillis ||
		messages[1].at != second.Result.AcceptedAtMillis {
		t.Fatalf("reopened Messages %+v; want two, each with its own id and its Result's acceptance time", messages)
	}
	for i, m := range messages {
		sum := sha256.Sum256([]byte(texts[i]))
		wait := []string{"readiness_unknown", "earlier_message"}[i]
		held := storedMessage{id: m.id, mode: "wait_turn", sha: hex.EncodeToString(sum[:]), place: "held", class: "held", wait: wait,
			seq: int64(i + 1), at: m.at, size: int64(len(texts[i])), since: m.at, confirmed: i == 0, reserved: true, text: []byte(texts[i])}
		if _, err := uuid.Parse(m.id); err != nil || m.id == pair.DeliveryRequestID || m.at < start || m.at > end || !reflect.DeepEqual(m, held) {
			t.Errorf("Message %d %+v; want %+v with a private UUID, accepted in [%d, %d]", i+1, m, held, start, end)
		}
	}
}

// TestBusySendAdmissionArbitratesPairFirst: a confirmed pair is arbitrated
// store-wide before any new-send check (SR-1.2). Pair replay: a repeat with
// the same target and exact text bytes returns the owner's current Result,
// with no wake, whatever its options, also once the target is full or deleted.
// M1 pair uniqueness: another target (covered, or no row at all) or a text one
// byte longer is id_conflict carrying the owner's Result, never a second
// owner. No step changes the conversation's sequence, reservations or revision,
// or the store's Messages and pairs.
func TestBusySendAdmissionArbitratesPairFirst(t *testing.T) {
	t.Parallel()
	s, path := storefix.OpenTempStore(t)
	id, other := admissionAgent(t, path, filepath.Join(t.TempDir(), "wake")), admissionAgent(t, path, "")
	pair := store.BusySendPair{CallerUUID: uuid.NewString(), DeliveryRequestID: "req-1"}
	fresh := store.BusySendPair{CallerUUID: pair.CallerUUID, DeliveryRequestID: "req-2"}
	const text = "owned text"
	confirmed := true
	send := func(target, text string, p store.BusySendPair, mode *store.BusySendMode) (store.BusySendAdmission, error) {
		return s.AdmitBusySend(store.BusySendAdmissionRequest{InstanceID: target, Text: []byte(text),
			CallerUUID: p.CallerUUID, DeliveryRequestID: p.DeliveryRequestID, SendMode: mode, Confirmed: &confirmed})
	}
	if _, err := send(id, text, pair, nil); err != nil {
		t.Fatal(err)
	}
	held := store.BusySendCapabilitiesFor(store.AgentClaude).HeldCount
	steer := store.BusySendSteerTurn
	for _, step := range []struct {
		name         string
		setup        func()
		target, text string
		pair         store.BusySendPair
		mode         *store.BusySendMode
		want         store.BusySendRefusalReason // "" replays the owner
		limit        *store.BusySendLimit
		notFound     bool
	}{
		{name: "same target and text with an unoffered Send Mode", target: id, text: text, pair: pair, mode: &steer},
		{name: "text one byte longer", target: id, text: text + " ", pair: pair, want: store.BusySendRefuseIDConflict},
		{name: "another covered target", target: other, text: text, pair: pair, want: store.BusySendRefuseIDConflict},
		{name: "a target with no row", target: "no-such-row", text: text, pair: pair, want: store.BusySendRefuseIDConflict},
		{name: "a new pair once the target is full", setup: func() {
			if err := apitest.SeedBusySendReservation(path, id, held, int64(len(text))); err != nil {
				t.Fatal(err)
			}
		},
			target: id, text: text, pair: fresh, want: store.BusySendRefuseHeldCount, limit: &store.BusySendLimit{Actual: held + 1, Allowed: held}},
		{name: "same target and text once the target is full", target: id, text: text, pair: pair},
		{name: "a new pair once the target is deleted", setup: func() {
			deleteCoveredForTest(t, s, id)
		}, target: id, text: text, pair: fresh, notFound: true},
		{name: "same target and text once the target is deleted", target: id, text: text, pair: pair},
	} {
		t.Run(step.name, func(t *testing.T) {
			if step.setup != nil {
				step.setup()
			}
			before := readAdmissionInbox(t, path, id)
			owner, err := s.ReadBusySendResult(pair.CallerUUID, pair.DeliveryRequestID)
			if err != nil || owner.Result == nil {
				t.Fatalf("owner Lookup %+v, %v", owner, err)
			}
			got, err := send(step.target, step.text, step.pair, step.mode)
			var refusal *store.BusySendRefusal
			switch {
			case step.notFound:
				if !errors.Is(err, store.ErrSpawnNotFound) {
					t.Errorf("got %+v, %v; want ErrSpawnNotFound", got, err)
				}
			case step.want == "":
				if err != nil || got.Outcome != store.BusySendReplayed || got.WakePath != "" || !reflect.DeepEqual(got.Result, *owner.Result) {
					t.Errorf("got %+v, %v; want the owner's Result %+v replayed with no wake", got, err, *owner.Result)
				}
			case !errors.As(err, &refusal) || refusal.Reason != step.want || !reflect.DeepEqual(refusal.Limit, step.limit):
				t.Errorf("got %+v, %v; want %s refused with limit %+v", got, err, step.want, step.limit)
			case step.want == store.BusySendRefuseIDConflict && (refusal.Owner == nil || !reflect.DeepEqual(*refusal.Owner, *owner.Result)):
				t.Errorf("id_conflict owner %+v; want %+v", refusal.Owner, *owner.Result)
			}
			if after := readAdmissionInbox(t, path, id); after != before {
				t.Errorf("inbox %+v after the call; want it unchanged at %+v", after, before)
			}
		})
	}
}

// TestBusySendAdmissionRaces: two handles of one store admit at once, the
// second begun while the first holds its write transaction (a causal barrier
// at the first's Message insert), and each is decided on committed facts.
// M1 custody: two distinct sends take sequences 1 and 2 in commit order, each
// reserved. M1 pair uniqueness and pair replay: competing admissions of one
// confirmed pair store one Message, and the later one replays the owner's
// Result with no wake. With one held slot left under the published limit,
// reserved by custody that can return to Held rather than by Held Messages,
// one is admitted and the other refused held_count, so reservations never
// overshoot (SR-1.6).
func TestBusySendAdmissionRaces(t *testing.T) {
	t.Parallel()
	held := store.BusySendCapabilitiesFor(store.AgentClaude).HeldCount
	pair := store.BusySendPair{CallerUUID: uuid.NewString(), DeliveryRequestID: "req-1"}
	plain := func(text string) func(string) store.BusySendAdmissionRequest {
		return func(id string) store.BusySendAdmissionRequest {
			return store.BusySendAdmissionRequest{InstanceID: id, Text: []byte(text)}
		}
	}
	owned := func(id string) store.BusySendAdmissionRequest {
		confirmed := true
		return store.BusySendAdmissionRequest{InstanceID: id, Text: []byte("owned"), CallerUUID: pair.CallerUUID,
			DeliveryRequestID: pair.DeliveryRequestID, Confirmed: &confirmed}
	}
	for _, tc := range []struct {
		name          string
		reserved      int64
		first, second func(string) store.BusySendAdmissionRequest
		want          store.BusySendAdmissionOutcome // the second's; "" when refused held_count
		texts         []string                       // the race's Messages in accepted order
		pairs         int
	}{
		{"distinct sends", 0, plain("one"), plain("two"), store.BusySendAdmitted, []string{"one", "two"}, 0},
		{"one confirmed pair", 0, owned, owned, store.BusySendReplayed, []string{"owned"}, 1},
		{"the last held slot", held - 1, plain("one"), plain("two"), "", []string{"one"}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s, path := storefix.OpenTempStore(t)
			wake := filepath.Join(t.TempDir(), "wake")
			id := admissionAgent(t, path, wake)
			if tc.reserved > 0 {
				if err := apitest.SeedBusySendReservation(path, id, tc.reserved, tc.reserved); err != nil {
					t.Fatal(err)
				}
			}
			rival, err := store.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = rival.Close() })
			holding, release, begun := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var holdOnce, releaseOnce, begunOnce sync.Once
			store.ObserveDecisionBoundaryForTest(t, s, path, func(_ context.Context, q string) error {
				if strings.Contains(q, "INSERT INTO busy_send_messages") {
					holdOnce.Do(func() { close(holding); <-release })
				}
				return nil
			}, nil)
			store.ObserveDecisionBoundaryForTest(t, rival, path, func(_ context.Context, q string) error {
				if q == "BEGIN IMMEDIATE" {
					begunOnce.Do(func() { close(begun) })
				}
				return nil
			}, nil)
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			t.Cleanup(unblock)
			type result struct {
				a   store.BusySendAdmission
				err error
			}
			firstDone, secondDone := make(chan result, 1), make(chan result, 1)
			go func() { a, err := s.AdmitBusySend(tc.first(id)); firstDone <- result{a, err} }()
			awaitSignal(t, holding, "the first admission's Message insert")
			go func() { a, err := rival.AdmitBusySend(tc.second(id)); secondDone <- result{a, err} }()
			awaitSignal(t, begun, "the second admission's BEGIN IMMEDIATE")
			unblock()
			receive := func(ch chan result) result {
				select {
				case r := <-ch:
					return r
				case <-time.After(10 * time.Second):
					t.Fatal("an admission did not return within 10 s of the barrier's release")
					return result{}
				}
			}
			first, second := receive(firstDone), receive(secondDone)
			if first.err != nil || first.a.Outcome != store.BusySendAdmitted || first.a.WakePath != wake {
				t.Fatalf("first %+v, %v; want admitted with wake %s", first.a, first.err, wake)
			}
			var refusal *store.BusySendRefusal
			switch tc.want {
			case store.BusySendAdmitted:
				if second.err != nil || second.a.Outcome != store.BusySendAdmitted || second.a.WakePath != wake {
					t.Errorf("second %+v, %v; want admitted with wake %s", second.a, second.err, wake)
				}
			case store.BusySendReplayed:
				if second.err != nil || second.a.Outcome != store.BusySendReplayed || second.a.WakePath != "" ||
					!reflect.DeepEqual(second.a.Result, first.a.Result) {
					t.Errorf("second %+v, %v; want the first's Result %+v replayed with no wake", second.a, second.err, first.a.Result)
				}
			default:
				if !errors.As(second.err, &refusal) || refusal.Reason != store.BusySendRefuseHeldCount ||
					!reflect.DeepEqual(refusal.Limit, &store.BusySendLimit{Actual: held + 1, Allowed: held}) {
					t.Errorf("second %+v, %v; want held_count at %d", second.a, second.err, held)
				}
			}
			n := int64(len(tc.texts))
			if got := readAdmissionInbox(t, path, id); got.seq != n || got.count != tc.reserved+n ||
				got.counts != (storefix.BusySendCounts{Messages: int(n), Pairs: tc.pairs}) {
				t.Errorf("inbox %+v; want sequence %d, reservations %d, %d Messages and %d pairs", got, n, tc.reserved+n, n, tc.pairs)
			}
			var texts []string
			for _, m := range readStoredMessages(t, path, id) {
				texts = append(texts, string(m.text))
			}
			if !slices.Equal(texts, tc.texts) {
				t.Errorf("the race's Messages in accepted order %q; want %q", texts, tc.texts)
			}
		})
	}
}

// TestBusySendAdmissionCommitOutcome (SR-1.1, SR-1.2): an admission whose
// caller is cancelled while its lease is made durable, before BEGIN, is
// ErrStoreBusy, and one that fails before COMMIT returns that failure; either
// way nothing is stored (the Message inserted before the failing pair insert is
// rolled back), and the same pair sent again after reopening is admitted new.
// An admission whose COMMIT lands but whose acknowledgment is lost is
// unconfirmed, never ErrStoreBusy or a refusal, and its zero admission grants
// no Result or wake. Sent again, the same pair finds the committed Message and
// replays it, so nothing is accepted twice (pair replay).
func TestBusySendAdmissionCommitOutcome(t *testing.T) {
	t.Parallel()
	injected := errors.New("injected pair insert failure")
	for _, tc := range []struct {
		name    string
		before  func(cancel context.CancelFunc, ctx context.Context, q string) error
		lost    bool // the COMMIT lands and its acknowledgment is lost
		noBegin bool
		wantErr func(error) bool
	}{
		{name: "caller cancelled before BEGIN", noBegin: true,
			before: func(cancel context.CancelFunc, ctx context.Context, q string) error {
				if q != "PRAGMA synchronous = FULL" {
					return nil
				}
				cancel()
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(10 * time.Second):
					return errors.New("the caller's cancellation never reached the statement")
				}
			},
			wantErr: func(err error) bool { return errors.Is(err, store.ErrStoreBusy) && errors.Is(err, context.Canceled) }},
		{name: "failure before COMMIT",
			before: func(_ context.CancelFunc, _ context.Context, q string) error {
				if strings.Contains(q, "INSERT INTO busy_send_results") {
					return injected
				}
				return nil
			},
			wantErr: func(err error) bool {
				return errors.Is(err, injected) && !errors.Is(err, store.ErrStoreBusy) && !store.CommitUnconfirmed(err)
			}},
		{name: "lost COMMIT acknowledgment", lost: true,
			wantErr: func(err error) bool { return store.CommitUnconfirmed(err) && !errors.Is(err, store.ErrStoreBusy) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s, path := storefix.OpenTempStore(t)
			wake := filepath.Join(t.TempDir(), "wake")
			id := admissionAgent(t, path, wake)
			pair := store.BusySendPair{CallerUUID: uuid.NewString(), DeliveryRequestID: "req-1"}
			confirmed := true
			req := store.BusySendAdmissionRequest{InstanceID: id, Text: []byte("once only"), CallerUUID: pair.CallerUUID,
				DeliveryRequestID: pair.DeliveryRequestID, Confirmed: &confirmed}
			before := readAdmissionInbox(t, path, id)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var begins atomic.Int32
			store.ObserveDecisionBoundaryForTest(t, s, path, func(sqlCtx context.Context, q string) error {
				if q == "BEGIN IMMEDIATE" {
					begins.Add(1)
				}
				if tc.before != nil {
					return tc.before(cancel, sqlCtx, q)
				}
				return nil
			}, func(context.Context) error {
				if tc.lost {
					return errors.New("COMMIT acknowledgment lost")
				}
				return nil
			})
			got, err := s.WithContext(ctx).AdmitBusySend(req)
			var refusal *store.BusySendRefusal
			if !tc.wantErr(err) || errors.As(err, &refusal) || !reflect.DeepEqual(got, store.BusySendAdmission{}) {
				t.Fatalf("admission %+v, %v; want only the %s error", got, err, tc.name)
			}
			if tc.noBegin && begins.Load() != 0 {
				t.Errorf("BEGIN IMMEDIATE ran %d times; want none", begins.Load())
			}

			reopened, err := store.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = reopened.Close() })
			want := before
			if tc.lost {
				want = admissionInbox{seq: before.seq + 1, count: before.count + 1, bytes: before.bytes + int64(len(req.Text)),
					revision: before.revision + 1, counts: storefix.BusySendCounts{Messages: 1, Pairs: 1}}
			}
			if got := readAdmissionInbox(t, path, id); got != want {
				t.Errorf("reopened inbox %+v; want %+v", got, want)
			}
			again, err := reopened.AdmitBusySend(req)
			if err != nil {
				t.Fatal(err)
			}
			if tc.lost {
				found, err := reopened.ReadBusySendResult(pair.CallerUUID, pair.DeliveryRequestID)
				if err != nil || found.Result == nil || again.Outcome != store.BusySendReplayed || again.WakePath != "" ||
					!reflect.DeepEqual(again.Result, *found.Result) {
					t.Errorf("same pair again %+v (Lookup %+v, %v); want the committed Message's Result replayed with no wake", again, found, err)
				}
			} else if again.Outcome != store.BusySendAdmitted || again.WakePath != wake {
				t.Errorf("same pair again %+v; want admitted new with wake %s", again, wake)
			}
			if counts := storefix.ReadBusySendCounts(t, path); counts != (storefix.BusySendCounts{Messages: 1, Pairs: 1}) {
				t.Errorf("counts %+v; want one Message and one pair", counts)
			}
		})
	}
}
