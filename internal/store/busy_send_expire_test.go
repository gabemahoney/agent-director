package store_test

// expire's busy-send retention pass at the store (Task t2.xis.52.e6; SR-5.5,
// SR-4.1, SR-2.1; Epic t1.xis.52 decisions 1 and 2), on real SQLite through
// ExpireBusySendRetention with supplied times, over records the inbox,
// completion, force and lifecycle writers made (busy_send_claim_test.go's
// inbox). M1's Lookup store obligation (Plan b.xis decision "M1 during-coding
// source tests") is asserted on the same expired subject. The cutoff's
// selection, the expire verb and the caller projections are pkg/api's; E2's
// seeded expired-record readers keep their own tests.

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/sqliteobserve"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
)

// readBusySendRetentionTables is readBusySendTables with the force tables.
func readBusySendRetentionTables(t *testing.T, path string) map[string][][]any {
	t.Helper()
	return readBusySendTables(t, path, "busy_send_forces", "busy_send_force_members")
}

// busySendColumns maps table's column names to their positions in its rows.
func busySendColumns(t *testing.T, path, table string) map[string]int {
	t.Helper()
	rows, err := openBusySendRaw(t, path).Query("SELECT * FROM " + table + " LIMIT 0")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	names, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	cols := map[string]int{}
	for i, name := range names {
		cols[name] = i
	}
	return cols
}

// busySendExpired is before (readBusySendRetentionTables) with the Messages
// and forces keyed by ids expired at at as v21 requires: a Message's text,
// digest, Result and receipt identity cleared, then either's retention
// expired at at and its revision advanced once. Every other row and column,
// pairs, custody, hand-overs, prompts and memberships included, is kept.
func busySendExpired(t *testing.T, path string, before map[string][][]any, at time.Time, ids ...string) map[string][][]any {
	t.Helper()
	want := map[string][][]any{}
	for table, rows := range before {
		for _, row := range rows {
			want[table] = append(want[table], slices.Clone(row))
		}
	}
	found := 0
	for _, table := range []string{"busy_send_messages", "busy_send_forces"} {
		cols := busySendColumns(t, path, table)
		for _, row := range want[table] {
			if !slices.Contains(ids, row[0].(string)) {
				continue
			}
			found++
			if table == "busy_send_messages" {
				for _, c := range []string{"text", "text_sha256", "result_class", "result_reason", "evidence_kind", "record_id", "record_sha256"} {
					row[cols[c]] = nil
				}
			}
			row[cols["retention"]], row[cols["expired_at_ms"]] = "expired", at.UnixMilli()
			row[cols["revision"]] = row[cols["revision"]].(int64) + 1
		}
	}
	if found != len(ids) {
		t.Fatalf("%d of %q name no Message or force", len(ids)-found, ids)
	}
	return want
}

// diffBusySendTables lists the rows in which got and want differ, each BLOB
// shown by its length; it is empty when they are equal.
func diffBusySendTables(got, want map[string][][]any) string {
	show := func(row []any) string {
		cells := make([]string, len(row))
		for i, v := range row {
			if blob, ok := v.([]byte); ok {
				v = fmt.Sprintf("<%d bytes>", len(blob))
			}
			cells[i] = fmt.Sprint(v)
		}
		return strings.Join(cells, " | ")
	}
	tables := slices.Sorted(maps.Keys(want))
	for table := range got {
		if _, ok := want[table]; !ok {
			tables = append(tables, table)
		}
	}
	var b strings.Builder
	for _, table := range tables {
		g, w := got[table], want[table]
		for i := range max(len(g), len(w)) {
			switch {
			case i >= len(g):
				fmt.Fprintf(&b, "%s row %d missing: %s\n", table, i, show(w[i]))
			case i >= len(w):
				fmt.Fprintf(&b, "%s row %d unexpected: %s\n", table, i, show(g[i]))
			case !reflect.DeepEqual(g[i], w[i]):
				fmt.Fprintf(&b, "%s row %d:\n got  %s\n want %s\n", table, i, show(g[i]), show(w[i]))
			}
		}
	}
	return b.String()
}

// expireBusySend runs the retention pass at cutoff, recording at, and
// requires it to expire exactly ids (busySendExpired) and change nothing else.
func expireBusySend(t *testing.T, in *busySendInbox, cutoff, at time.Time, ids ...string) {
	t.Helper()
	before := readBusySendRetentionTables(t, in.path)
	if err := in.s.ExpireBusySendRetention(cutoff, at); err != nil {
		t.Fatalf("retention pass at cutoff %d µs: %v", cutoff.UnixMicro(), err)
	}
	want := busySendExpired(t, in.path, before, at, ids...)
	if diff := diffBusySendTables(readBusySendRetentionTables(t, in.path), want); diff != "" {
		t.Fatalf("retention pass at cutoff %d µs; want only %q expired at %d:\n%s", cutoff.UnixMicro(), ids, at.UnixMilli(), diff)
	}
}

// busySendMessageRow is Message id's row in tables (readBusySendRetentionTables).
func busySendMessageRow(t *testing.T, tables map[string][][]any, id string) []any {
	t.Helper()
	for _, row := range tables["busy_send_messages"] {
		if row[0] == id {
			return row
		}
	}
	t.Fatalf("no Message %s", id)
	return nil
}

// busySendRetentionState is one Message's or force's retention, expiry time
// (0 while retained) and revision.
type busySendRetentionState struct {
	retention string
	at, rev   int64
}

// expired is s as a retention pass at at leaves it once it expires the record.
func (s busySendRetentionState) expired(at time.Time) busySendRetentionState {
	return busySendRetentionState{"expired", at.UnixMilli(), s.rev + 1}
}

// readBusySendRetentionStates reads every Message's and force's retention
// state by its key.
func readBusySendRetentionStates(t *testing.T, path string) map[string]busySendRetentionState {
	t.Helper()
	rows, err := openBusySendRaw(t, path).Query(`SELECT message_id, retention, COALESCE(expired_at_ms, 0), revision FROM busy_send_messages
	    UNION ALL SELECT force_id, retention, COALESCE(expired_at_ms, 0), revision FROM busy_send_forces`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]busySendRetentionState{}
	for rows.Next() {
		var key string
		var s busySendRetentionState
		if err := rows.Scan(&key, &s.retention, &s.at, &s.rev); err != nil {
			t.Fatal(err)
		}
		out[key] = s
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// observeBusySendSQL installs hooks on s's real SQLite driver through the
// canonical seam (store.ObserveSQLiteForTest) and returns its stop, which the
// test's cleanup also runs.
func observeBusySendSQL(t *testing.T, s *store.Store, hooks sqliteobserve.Hooks) func() {
	t.Helper()
	stop, err := store.ObserveSQLiteForTest(s, func(d driver.Driver) driver.Driver { return sqliteobserve.Wrap(d, hooks) })
	if err != nil {
		t.Fatal(err)
	}
	end := func() {
		if err := stop(context.Background()); err != nil {
			t.Errorf("stop observing SQLite: %v", err)
		}
	}
	t.Cleanup(end)
	return end
}

// busySendFinalSeed makes the Message its last argument names Final
// not_started at its second, in the place its first names (NULL keeps the
// place). It is a defensive seed within v21's CHECKs: no built path makes a
// Held, claimed or not-yet-issued Message Final.
const busySendFinalSeed = `UPDATE busy_send_messages SET place = COALESCE(?, place), result_class = 'not_started', result_final = 1,
        final_at_ms = ?, result_reason = 'record_deleted', wait_reason = NULL, wait_since_ms = NULL, wait_earlier_message_id = NULL
    WHERE message_id = ?`

// newBusySendRetentionInbox is an inbox of n Messages, each confirmed under
// its own pair (req-1 to req-n), all Final at base+1 ms in one prompt through
// Claude's complete emission at its unproven boundary, then one ended force
// attempt per forceIDs, refused at base+1 ms with nothing Held. It returns the
// Messages' ids in key order, the order retention pages them in, and the
// admission that stored each, which a matching repeat sends again.
func newBusySendRetentionInbox(t *testing.T, n int, forceIDs ...string) (*busySendInbox, time.Time, []string, map[string]store.BusySendAdmissionRequest) {
	t.Helper()
	in := newBusySendInbox(t)
	confirmed := true
	byText := map[string]store.BusySendAdmissionRequest{}
	for i := range n {
		req := store.BusySendAdmissionRequest{InstanceID: in.id, Text: []byte(fmt.Sprintf("message %d", i)), CallerUUID: in.pair.CallerUUID,
			DeliveryRequestID: fmt.Sprintf("req-%d", i+1), Confirmed: &confirmed}
		if a, err := in.s.AdmitBusySend(req); err != nil || a.Outcome != store.BusySendAdmitted {
			t.Fatalf("admission %d = %+v, %v; want admitted", i, a, err)
		}
		byText[string(req.Text)] = req
	}
	reqs := map[string]store.BusySendAdmissionRequest{}
	for _, m := range readStoredMessages(t, in.path, in.id) {
		reqs[m.id] = byText[string(m.text)]
	}
	base := time.Now().UTC().Truncate(time.Millisecond)
	in.clock = tmuxfix.NewClock(base)
	h := in.claim(t, base, false)
	in.clock.Advance(time.Millisecond)
	is := in.issue(t, h, 0, h.Revision)
	if res := in.complete(t, h, 0, is.Revision, store.BusySendOutputComplete, store.BusySendOrderEmissionComplete, true); !res.Closed {
		t.Fatalf("completion %+v; want all %d Messages Final and the hand-over closed", res, n)
	}
	for _, id := range forceIDs {
		req := in.forceRequest(t, in.clock.Now())
		req.ForceID = id
		if c, err := in.s.ClaimBusySendForce(context.Background(), req); err != nil || c.Force.Outcome != store.BusySendForceRefused {
			t.Fatalf("force claim %s = %+v, %v; want an ended refusal", id, c, err)
		}
	}
	return in, base, slices.Sorted(maps.Keys(reqs)), reqs
}

// TestBusySendRetentionExpiresFinalMessages (SR-5.5, SR-2.1; Epic decision 1;
// M1's Lookup store obligation): with a fractional cutoff and a later
// expiredAt, a retained Final Message expires only when its Final time is
// strictly before the cutoff in whole milliseconds. A confirmed
// delivered_confirmed Message with a receipt and an unconfirmed one, both
// Final 1 ms before it, expire; one Final at the cutoff's millisecond, one
// 1 ms after and an older non-Final one awaiting proof stay, though every
// Message was accepted before the cutoff. Expiry clears exactly the text,
// digest, Result class and reason and receipt identity, records expiredAt and
// advances revision once, keeping every other column and row, pairs, custody,
// reservations, hand-overs, prompts and memberships included; a second pass
// changes nothing. Lookup (ReadBusySendResult) reads durable facts and writes
// nothing: found before, retention_expired with no Result after, and a failed
// owner or Result read is an error with no answer, never unknown.
func TestBusySendRetentionExpiresFinalMessages(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	in := newBusySendInbox(t, "a", busySendFill(2000), strings.Repeat("c", 8000))
	second := store.BusySendPair{CallerUUID: in.pair.CallerUUID, DeliveryRequestID: "req-2"}
	confirmed := true
	if _, err := in.s.AdmitBusySend(store.BusySendAdmissionRequest{InstanceID: in.id, Text: []byte(strings.Repeat("d", 8000)),
		CallerUUID: second.CallerUUID, DeliveryRequestID: second.DeliveryRequestID, Confirmed: &confirmed}); err != nil {
		t.Fatal(err)
	}
	in.admit(t, store.BusySendWaitTurn, strings.Repeat("e", 8000))
	base := time.Now().UTC().Truncate(time.Millisecond)
	ms := func(n int) time.Time { return base.Add(time.Duration(n) * time.Millisecond) }
	in.clock = tmuxfix.NewClock(base)
	h := in.claim(t, base, false)
	var sizes []int
	for _, p := range h.Prompts {
		sizes = append(sizes, len(p.MessageIDs))
	}
	if !slices.Equal(sizes, []int{2, 1, 1, 1}) {
		t.Fatalf("prompts of %v Messages; want the two short ones together, then each long one alone", sizes)
	}

	// Prompt 0's exact receipt at 9 ms makes its confirmed Message
	// delivered_confirmed and its unconfirmed one Final; prompt 1's Message is
	// Final at 10 ms; prompt 2's confirmed one, with no unproven boundary,
	// awaits proof; prompt 3's is Final at 11 ms.
	in.clock.Advance(time.Millisecond)
	in.issue(t, h, 0, h.Revision)
	if _, err := in.s.RecordBusySendEvidence(ctx, store.BusySendPromptEvidence{HandoverID: h.HandoverID, LaunchID: in.dest.LaunchID,
		Receipt: &store.BusySendReceipt{Kind: store.BusySendEvidenceCodexItemStarted, RecordID: "item-1", RecordSHA256: strings.Repeat("ab", 32)},
		At:      ms(9)}); err != nil {
		t.Fatal(err)
	}
	in.clock.Advance(8 * time.Millisecond)
	is := in.issue(t, h, 1, in.revision(t, h))
	in.clock.Advance(time.Millisecond)
	in.complete(t, h, 1, is.Revision, store.BusySendOutputComplete, store.BusySendOrderEmissionComplete, true)
	is = in.issue(t, h, 2, in.revision(t, h))
	in.clock.Advance(time.Millisecond)
	in.complete(t, h, 2, is.Revision, store.BusySendOutputComplete, store.BusySendOrderEmissionComplete, false)
	is = in.issue(t, h, 3, in.revision(t, h))
	in.complete(t, h, 3, is.Revision, store.BusySendOutputComplete, store.BusySendOrderEmissionComplete, true)
	custody := wantBusySendCustody(t, in.path, in.id, "closed delivered_confirmed final exact_receipt",
		"closed sent_unconfirmed final confirmation_off", "closed sent_unconfirmed final confirmation_off",
		"closed sent_unconfirmed awaiting_proof", "closed sent_unconfirmed final confirmation_off")
	var finals []int64
	for _, c := range custody {
		finals = append(finals, c.finalAt)
	}
	var accepted int64
	if err := openBusySendRaw(t, in.path).QueryRow(`SELECT MAX(accepted_at_ms) FROM busy_send_messages`).Scan(&accepted); err != nil {
		t.Fatal(err)
	}
	if want := []int64{ms(9).UnixMilli(), ms(9).UnixMilli(), ms(10).UnixMilli(), 0, ms(11).UnixMilli()}; !slices.Equal(finals, want) ||
		accepted > base.UnixMilli() {
		t.Fatalf("Final times %v, last accepted at %d; want %v, every Message accepted by %d", finals, accepted, want, base.UnixMilli())
	}

	lookup := func(p store.BusySendPair) store.BusySendResultLookup {
		t.Helper()
		l, err := in.s.ReadBusySendResult(p.CallerUUID, p.DeliveryRequestID)
		if err != nil {
			t.Fatalf("Lookup %+v: %v", p, err)
		}
		return l
	}
	before := readBusySendRetentionTables(t, in.path)
	found, awaiting := lookup(in.pair), lookup(second)
	if found.Lookup != store.BusySendLookupFound || found.Result == nil || !found.Result.Final ||
		awaiting.Lookup != store.BusySendLookupFound || awaiting.Result == nil || awaiting.Result.Final {
		t.Fatalf("Lookups %+v and %+v; want both found, the first Final", found, awaiting)
	}
	if diff := diffBusySendTables(readBusySendRetentionTables(t, in.path), before); diff != "" {
		t.Fatalf("Lookups wrote:\n%s", diff)
	}
	cutoff, at := ms(10).Add(600*time.Microsecond), base.Add(time.Hour+300*time.Microsecond)
	expireBusySend(t, in, cutoff, at, h.Prompts[0].MessageIDs...)
	expired := readBusySendRetentionTables(t, in.path)
	if got := lookup(in.pair); got != (store.BusySendResultLookup{Lookup: store.BusySendLookupRetentionExpired}) {
		t.Errorf("Lookup of the expired pair %+v; want retention_expired with no Result", got)
	}
	if got := lookup(second); !reflect.DeepEqual(got, awaiting) {
		t.Errorf("Lookup of the kept pair %+v; want %+v", got, awaiting)
	}
	expireBusySend(t, in, cutoff, at.Add(time.Hour))
	if diff := diffBusySendTables(readBusySendRetentionTables(t, in.path), expired); diff != "" {
		t.Errorf("Lookups or a second pass changed the store:\n%s", diff)
	}

	injected := errors.New("injected read failure")
	var fail atomic.Value
	fail.Store("")
	observeBusySendSQL(t, in.s, sqliteobserve.Hooks{Before: func(_ context.Context, q string) error {
		if prefix := fail.Load().(string); prefix != "" && strings.HasPrefix(q, prefix) {
			return injected
		}
		return nil
	}})
	for _, tc := range []struct {
		name string
		pair store.BusySendPair
		read string
		want string
	}{
		{"the expired owner's read", in.pair, "SELECT p.message_id, m.instance_id, m.text, m.retention",
			"store: read busy-send result: injected read failure"},
		{"the retained Result's read", second, "SELECT p.caller_uuid, p.delivery_request_id, m.instance_id, m.send_mode",
			"store: read busy-send result: store: read busy-send Message Result: injected read failure"},
	} {
		fail.Store(tc.read)
		got, err := in.s.ReadBusySendResult(tc.pair.CallerUUID, tc.pair.DeliveryRequestID)
		if err == nil || err.Error() != tc.want || !errors.Is(err, injected) || got != (store.BusySendResultLookup{}) {
			t.Errorf("Lookup with %s failing = %+v, %v; want no answer and the error %q", tc.name, got, err, tc.want)
		}
	}
	fail.Store("")
	if diff := diffBusySendTables(readBusySendRetentionTables(t, in.path), expired); diff != "" {
		t.Errorf("failed Lookups wrote:\n%s", diff)
	}
}

// TestBusySendRetentionKeepsTextAnIssueNeeds (SR-5.5; Epic decision 2):
// retention never removes text a later issue can still need. A Final Message
// still Held, claimed, or issued into a prompt not yet issued is kept; each
// is a defensive seed within v21's CHECKs, since no built path makes one. A
// Message of an issued prompt expires on schedule even with its transport
// partial and no order or admission recorded, and an earlier attempt's
// released membership never defers a closed Message. Nothing else changes,
// memberships and fences included.
func TestBusySendRetentionKeepsTextAnIssueNeeds(t *testing.T) {
	t.Parallel()
	final := func(t *testing.T, in *busySendInbox, place any, id string) {
		t.Helper()
		setBusySendFact(t, in.path, busySendFinalSeed, place, time.Now().UnixMilli(), id)
	}
	for _, tc := range []struct {
		name    string
		build   func(t *testing.T) (*busySendInbox, []string)
		custody []string
	}{
		{name: "Final but still Held (defensive seed)", custody: []string{"held not_started final record_deleted reserved"},
			build: func(t *testing.T) (*busySendInbox, []string) {
				in := newBusySendInbox(t, "held")
				final(t, in, nil, readBusySendCustody(t, in.path, in.id)[0].id)
				return in, nil
			}},
		{name: "Final but claimed (defensive seed)", custody: []string{"claimed not_started final record_deleted reserved member"},
			build: func(t *testing.T) (*busySendInbox, []string) {
				in := newBusySendInbox(t, "claimed")
				final(t, in, nil, in.claim(t, time.Time{}, false).Prompts[0].MessageIDs[0])
				return in, nil
			}},
		{name: "Final and issued into a prompt not yet issued (defensive seed)",
			custody: []string{"issued not_started final record_deleted reserved member"},
			build: func(t *testing.T) (*busySendInbox, []string) {
				in := newBusySendInbox(t, "early")
				final(t, in, "issued", in.claim(t, time.Time{}, false).Prompts[0].MessageIDs[0])
				return in, nil
			}},
		{name: "issued, with its transport partial and no order or admission", custody: []string{busySendIntentEnded},
			build: func(t *testing.T) (*busySendInbox, []string) {
				in := newBusySendInbox(t, "partial")
				h := in.claim(t, time.Time{}, false)
				in.complete(t, h, 0, in.issue(t, h, 0, h.Revision).Revision, store.BusySendOutputPartial, "", false)
				if err := store.EndBusySendLaunchForTest(in.s, in.dest.LaunchID, time.Now()); err != nil {
					t.Fatal(err)
				}
				return in, h.Prompts[0].MessageIDs
			}},
		{name: "closed after an earlier attempt was released",
			custody: []string{"closed sent_unconfirmed final handed_over_not_proven", "closed sent_unconfirmed final confirmation_off"},
			build: func(t *testing.T) (*busySendInbox, []string) {
				ctx := context.Background()
				in := newBusySendInbox(t, busySendTwoPrompts...)
				h := in.claim(t, time.Time{}, false)
				is := in.issue(t, h, 0, h.Revision)
				if late, err := in.s.ClaimBusySendHandover(ctx, in.claimRequest(h.IssueDeadline.Add(time.Millisecond), false)); err != nil ||
					late.ReleasedExpired != 1 {
					t.Fatalf("an event after the issue deadline %+v, %v; want prompt 1 released", late, err)
				}
				in.complete(t, h, 0, is.Revision, store.BusySendOutputComplete, store.BusySendOrderEmissionComplete, true)
				if _, err := in.s.RecordBusySendEvidence(ctx, store.BusySendPromptEvidence{HandoverID: h.HandoverID, LaunchID: in.dest.LaunchID,
					Admission: store.BusySendAdmissionPromptSighting}); err != nil {
					t.Fatal(err)
				}
				again := in.claim(t, time.Time{}, false)
				if again.Prompts[0].MessageIDs[0] != h.Prompts[1].MessageIDs[0] {
					t.Fatalf("the next hand-over carries %q; want the released Message %s", again.Prompts[0].MessageIDs, h.Prompts[1].MessageIDs[0])
				}
				in.complete(t, again, 0, in.issue(t, again, 0, again.Revision).Revision, store.BusySendOutputComplete,
					store.BusySendOrderEmissionComplete, true)
				return in, []string{h.Prompts[0].MessageIDs[0], again.Prompts[0].MessageIDs[0]}
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			in, expired := tc.build(t)
			wantBusySendCustody(t, in.path, in.id, tc.custody...)
			now := time.Now()
			expireBusySend(t, in, now.Add(time.Hour), now.Add(2*time.Hour), expired...)
		})
	}
}

// TestBusySendRetentionKeepsWaitingItemFacts (SR-5.5; b.7si t1.7si.nc SR-1.3;
// Epic decision 2): a real b.7si Waiting Item, the Final sent_unconfirmed
// Message of an Idle Hand-over issued in the current Launch with no Turn
// Start since, expires on schedule. It keeps its acceptance time, custody and
// membership, and its hand-over keeps its Launch, intent_running_turn and
// intent_turn_starts; awaiting_turn_start stands until Launch End. With its
// text gone its prompt's sighting is ambiguous, admitting and rewriting
// nothing, and the prompt cannot issue again. The hand-over's later prompt,
// not yet issued, keeps its Message (Final by a defensive seed) and its whole
// body, and issues normally with the hand-over's turn facts. Launch End
// leaves the expired Message as it was.
func TestBusySendRetentionKeepsWaitingItemFacts(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	in := newBusySendInbox(t, busySendTwoPrompts...)
	base := time.Now().UTC().Truncate(time.Millisecond)
	in.clock = tmuxfix.NewClock(base)
	h := in.claim(t, base, false)
	in.clock.Advance(time.Millisecond)
	is := in.issue(t, h, 0, h.Revision)
	in.clock.Advance(time.Millisecond)
	in.complete(t, h, 0, is.Revision, store.BusySendOutputComplete, store.BusySendOrderEmissionComplete, true)
	item, later := h.Prompts[0].MessageIDs[0], h.Prompts[1].MessageIDs[0]
	setBusySendFact(t, in.path, busySendFinalSeed, nil, base.UnixMilli(), later)
	wantBusySendCustody(t, in.path, in.id, "closed sent_unconfirmed final handed_over_not_proven",
		"claimed not_started final record_deleted reserved member")
	var launch string
	var running, intentStarts, turnStarts int64
	if err := openBusySendRaw(t, in.path).QueryRow(`SELECT h.launch_id, h.intent_running_turn, h.intent_turn_starts, s.turn_starts
	    FROM busy_send_handovers h JOIN spawns s ON s.instance_id = h.instance_id WHERE h.handover_id = ?`, h.HandoverID).Scan(
		&launch, &running, &intentStarts, &turnStarts); err != nil {
		t.Fatal(err)
	}
	pub, err := in.s.GetBusySendPublishedLaunch(in.id)
	if err != nil || launch != pub.LaunchID || running != 0 || intentStarts != turnStarts {
		t.Fatalf("hand-over in Launch %s (published %+v, %v), running turn %d, Turn Starts %d now %d; want an Idle Hand-over of the current Launch with no Turn Start since",
			launch, pub, err, running, intentStarts, turnStarts)
	}
	in.wantAwaiting(t, 1, "before retention")

	expireBusySend(t, in, base.Add(3*time.Millisecond), base.Add(4*time.Millisecond), item)
	in.wantAwaiting(t, 1, "after retention")
	expired := readBusySendRetentionTables(t, in.path)
	if got := in.sight(t, in.sighting(t, h.Prompts[0].Body)); got != (store.BusySendSightingResult{Outcome: store.BusySendSightingAmbiguous}) {
		t.Errorf("the expired prompt's sighting = %+v; want ambiguous, binding nothing", got)
	}
	var guard *store.BusySendGuardError
	if _, err := in.commit(ctx, h, 0, in.revision(t, h)); !errors.As(err, &guard) {
		t.Errorf("issuing the expired prompt again: %v; want a guard refusal", err)
	}
	if diff := diffBusySendTables(readBusySendRetentionTables(t, in.path), expired); diff != "" {
		t.Errorf("the sighting or the repeated issue wrote:\n%s", diff)
	}
	in.wantAwaiting(t, 1, "after the sighting")

	read, err := in.s.ReadBusySendHandover(ctx, h.HandoverID)
	if err != nil || len(read.Prompts) != 2 || string(read.Prompts[1].Body) != string(h.Prompts[1].Body) {
		t.Fatalf("hand-over read %v; want the later prompt's whole body", err)
	}
	if got := in.issue(t, h, 1, read.Revision); got.TurnStarts != intentStarts || got.RunningTurn {
		t.Errorf("the later prompt's issue %+v; want Turn Starts %d and no running turn, as its hand-over's", got, intentStarts)
	}
	in.wantAwaiting(t, 1, "with both prompts issued")
	before := readBusySendRetentionTables(t, in.path)
	if err := store.EndBusySendLaunchForTest(in.s, in.dest.LaunchID, base.Add(5*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if n := in.awaiting(t); n != 0 {
		t.Errorf("awaiting_turn_start %d after Launch End; want 0", n)
	}
	if got, want := busySendMessageRow(t, readBusySendRetentionTables(t, in.path), item), busySendMessageRow(t, before, item); !reflect.DeepEqual(got, want) {
		t.Errorf("Launch End changed the expired Message from %v to %v", want, got)
	}
}

// TestBusySendRetentionForces (SR-4.1, SR-5.5): a running and then a pending
// force are kept whatever the cutoff, while the pending force's Final member
// expires. Once the force has ended, its ended_at_ms alone decides: at a
// cutoff equal to it the force is kept, though it started and its call ended
// earlier, and 1 ms later it expires, changing only its retention, expiry
// time and revision and keeping its outcome, reason, times, hand-over and
// carried membership; get-force then reads retention_expired.
func TestBusySendRetentionForces(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	in := newBusySendInbox(t, busySendTwoPrompts...)
	base := time.Now().UTC().Truncate(time.Millisecond)
	ms := func(n int) time.Time { return base.Add(time.Duration(n) * time.Millisecond) }
	in.clock = tmuxfix.NewClock(base)
	c := in.forceClaim(t, base)
	h, id := c.Handover, c.Force.ForceID
	expireBusySend(t, in, ms(100), ms(200))

	in.clock.Advance(time.Millisecond)
	for _, step := range []forceStep{forceIssue(0), forceOutput(0, store.BusySendOutputComplete), forceAdmit(0), forceIssue(1)} {
		step(t, in, h)
	}
	if end, err := in.s.EndBusySendForceCall(ctx, c.Ref, ms(2)); err != nil || end.Force.Outcome != store.BusySendForcePending {
		t.Fatalf("call end %+v, %v; want pending", end, err)
	}
	expireBusySend(t, in, ms(100), ms(200), h.Prompts[0].MessageIDs[0])

	if _, err := in.s.RecordBusySendEvidence(ctx, store.BusySendPromptEvidence{HandoverID: h.HandoverID, PromptIndex: 1,
		LaunchID: in.dest.LaunchID, ConnectionGeneration: in.dest.ConnectionGeneration, Admission: store.BusySendAdmissionPromptSighting,
		At: ms(20)}); err != nil {
		t.Fatal(err)
	}
	ended := in.readForce(t, id)
	if ended.Outcome != store.BusySendForceHandedOver || ended.StartedAtMillis != base.UnixMilli() ||
		ended.CallEndedAtMillis != ms(2).UnixMilli() || ended.EndedAtMillis != ms(20).UnixMilli() {
		t.Fatalf("force %+v; want handed_over, started at %d, its call ended at %d and ended at %d", ended, base.UnixMilli(),
			ms(2).UnixMilli(), ms(20).UnixMilli())
	}
	expireBusySend(t, in, ms(20), ms(200))
	if got := in.readForce(t, id); !reflect.DeepEqual(got, ended) {
		t.Errorf("get-force at a cutoff equal to its end reads %+v; want %+v", got, ended)
	}
	expireBusySend(t, in, ms(21), ms(300), id)
	if l, err := in.s.ReadBusySendForce(id); err != nil || l != (store.BusySendForceLookup{Lookup: store.BusySendLookupRetentionExpired}) {
		t.Errorf("get-force after expiry = %+v, %v; want retention_expired with no force", l, err)
	}
}

// TestBusySendRetentionRacesWriters (SR-5.5, SR-1.2): a pass over forty Final
// Messages (two pages; busySendExpirePage is 32) and two ended forces, with
// another handle of the store working at causal barriers. A matching repeat
// sent after the first page's read gets the owner's retained Result
// (replayed, no wake), and one begun while that page's write holds the lock
// is refused retention_expired: neither accepts, sequences or wakes anything.
// A Message and a force whose revision a later writer advanced after their
// page's read are skipped, not retried, and the cursor goes on to expire the
// later records. Between pages no lock is held, so ordinary writes on the
// other handle (two force attempts) complete within their waits; the one
// keyed past the largest key read at entry is left to the next pass, which
// expires it and the skipped records and nothing else.
func TestBusySendRetentionRacesWriters(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const (
		skipped = "10000000-0000-4000-8000-000000000001"
		inside  = "15000000-0000-4000-8000-000000000002"
		ended   = "20000000-0000-4000-8000-000000000003"
		beyond  = "f0000000-0000-4000-8000-000000000004"
	)
	in, base, ids, reqs := newBusySendRetentionInbox(t, 40, skipped, ended)
	subject, stale := ids[0], ids[1]
	cutoff, at := base.Add(10*time.Millisecond), base.Add(time.Hour)
	owner, err := in.s.ReadBusySendResult(reqs[subject].CallerUUID, reqs[subject].DeliveryRequestID)
	if err != nil || owner.Result == nil {
		t.Fatalf("owner Lookup %+v, %v", owner, err)
	}
	attempts := []store.BusySendForceClaimRequest{in.forceRequest(t, base.Add(2*time.Millisecond)), in.forceRequest(t, base.Add(2*time.Millisecond))}
	attempts[0].ForceID, attempts[1].ForceID = inside, beyond
	rival, err := store.Open(in.path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rival.Close() })
	raw := openBusySendRaw(t, in.path)
	bump := func(q, key string) error {
		r, err := raw.Exec(q, key)
		if err != nil {
			return err
		}
		if n, err := r.RowsAffected(); err != nil || n != 1 {
			return fmt.Errorf("%s changed %d rows, %v; want one", q, n, err)
		}
		return nil
	}
	inbox, before := readAdmissionInbox(t, in.path, in.id), readBusySendRetentionStates(t, in.path)

	begun := make(chan struct{})
	var armed atomic.Bool
	var begunOnce sync.Once
	observeBusySendSQL(t, rival, sqliteobserve.Hooks{Before: func(_ context.Context, q string) error {
		if q == "BEGIN IMMEDIATE" && armed.Load() {
			begunOnce.Do(func() { close(begun) })
		}
		return nil
	}})
	type admitted struct {
		a   store.BusySendAdmission
		err error
	}
	raced := make(chan admitted, 1)
	var mu sync.Mutex
	var pages []string
	var early admitted
	var claims []store.BusySendForceClaim
	var hookErrs []error
	var bumped, forceBumped bool
	stop := observeBusySendSQL(t, in.s, sqliteobserve.Hooks{Before: func(_ context.Context, q string) error {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case strings.HasPrefix(q, "SELECT m.message_id, m.revision FROM busy_send_messages"):
			pages = append(pages, "Messages")
			if len(pages) == 2 {
				// Between pages, holding no lock: ordinary writes on the other handle.
				for _, req := range attempts {
					c, err := rival.ClaimBusySendForce(ctx, req)
					claims, hookErrs = append(claims, c), append(hookErrs, err)
				}
			}
		case strings.HasPrefix(q, "SELECT f.force_id, f.revision FROM busy_send_forces"):
			pages = append(pages, "forces")
		case q == "BEGIN IMMEDIATE" && len(pages) == 1 && !bumped:
			// The first page is read and not yet written: a later writer
			// advances a selected Message, and a matching repeat sees the
			// retained owner.
			bumped = true
			hookErrs = append(hookErrs, bump(`UPDATE busy_send_messages SET revision = revision + 1 WHERE message_id = ?`, stale))
			early.a, early.err = rival.AdmitBusySend(reqs[subject])
		case q == "BEGIN IMMEDIATE" && slices.Contains(pages, "forces") && !forceBumped:
			forceBumped = true
			hookErrs = append(hookErrs, bump(`UPDATE busy_send_forces SET revision = revision + 1 WHERE force_id = ?`, skipped))
		case strings.HasPrefix(q, "UPDATE busy_send_messages AS m SET retention") && !armed.Load():
			// The first page's write holds the lock at its first guarded
			// write, the subject's: a matching repeat begins and waits.
			armed.Store(true)
			go func() {
				a, err := rival.AdmitBusySend(reqs[subject])
				raced <- admitted{a, err}
			}()
			select {
			case <-begun:
			case <-time.After(10 * time.Second):
				return errors.New("the racing repeat never reached its BEGIN IMMEDIATE")
			}
		}
		return nil
	}})
	if err := in.s.ExpireBusySendRetention(cutoff, at); err != nil {
		t.Fatalf("retention pass: %v", err)
	}
	var race admitted
	select {
	case race = <-raced:
	case <-time.After(10 * time.Second):
		t.Fatal("the racing repeat did not return within 10 s of the pass")
	}
	stop()
	mu.Lock()
	defer mu.Unlock()
	if err := errors.Join(hookErrs...); err != nil {
		t.Fatalf("work at the barriers: %v", err)
	}
	if i := slices.Index(pages, "forces"); i < 2 || len(pages) != i+1 {
		t.Fatalf("pages read %q; want two or more Messages pages, then one forces page", pages)
	}
	if early.err != nil || early.a.Outcome != store.BusySendReplayed || early.a.WakePath != "" || !reflect.DeepEqual(early.a.Result, *owner.Result) {
		t.Errorf("repeat after the page's read %+v, %v; want the owner's retained Result %+v replayed with no wake", early.a, early.err, *owner.Result)
	}
	var refusal *store.BusySendRefusal
	if !errors.As(race.err, &refusal) || refusal.Reason != store.BusySendRefuseRetentionExpired || !reflect.DeepEqual(race.a, store.BusySendAdmission{}) {
		t.Errorf("repeat racing the page's write %+v, %v; want only refused retention_expired", race.a, race.err)
	}
	for _, c := range claims {
		if c.Force.Outcome != store.BusySendForceRefused {
			t.Errorf("force attempt between pages %+v; want an ended refusal", c.Force)
		}
	}
	want := inbox
	want.counts.Forces += 2
	if got := readAdmissionInbox(t, in.path, in.id); got != want {
		t.Errorf("inbox %+v; want %+v, no Message, pair or sequence added", got, want)
	}
	states := maps.Clone(before)
	for _, id := range ids {
		states[id] = before[id].expired(at)
	}
	states[stale] = busySendRetentionState{"retained", 0, before[stale].rev + 1}
	states[skipped] = busySendRetentionState{"retained", 0, before[skipped].rev + 1}
	states[ended] = before[ended].expired(at)
	states[inside] = busySendRetentionState{"expired", at.UnixMilli(), 2}
	states[beyond] = busySendRetentionState{"retained", 0, 1}
	if got := readBusySendRetentionStates(t, in.path); !maps.Equal(got, states) {
		t.Fatalf("after the pass %v; want %v", got, states)
	}

	again := at.Add(time.Hour)
	if err := in.s.ExpireBusySendRetention(cutoff, again); err != nil {
		t.Fatalf("second pass: %v", err)
	}
	for _, key := range []string{stale, skipped, beyond} {
		states[key] = states[key].expired(again)
	}
	if got := readBusySendRetentionStates(t, in.path); !maps.Equal(got, states) {
		t.Errorf("after a second pass %v; want %v", got, states)
	}
}

// TestBusySendRetentionFailures (SR-5.5): a real store fault stops the pass
// and is returned, wrapped, never as a complete pass. A failed last-key read,
// or a guarded write failing after another in its page (rolled back whole),
// leaves every record retained. A failed page read after the Messages' pages
// committed, or a lost acknowledgment of the second page's COMMIT, leaves
// those pages expired and the force untouched, with no write retried. A rerun
// completes the pass from whatever the failure left.
func TestBusySendRetentionFailures(t *testing.T) {
	t.Parallel()
	injected := errors.New("injected retention fault")
	const prefix = "store: expire busy-send retention: "
	// Each case fails the statement starting fault at its nth run, or loses
	// the acknowledgment of the commit-th COMMIT; updates is the guarded
	// writes run, none retried, and progress whether the Messages' pages
	// committed first.
	for _, tc := range []struct {
		name     string
		fault    string
		nth      int32
		commit   int32
		updates  int32
		progress bool
		want     string
	}{
		{name: "the last-key read", fault: "SELECT MAX(message_id) FROM busy_send_messages", nth: 1, want: "read last keys: injected retention fault"},
		{name: "a guarded write after another in its page", fault: "UPDATE busy_send_messages AS m SET retention", nth: 2, updates: 2,
			want: "Messages: guarded write: injected retention fault"},
		{name: "a page read after the Messages' pages committed", fault: "SELECT f.force_id, f.revision FROM busy_send_forces", nth: 1,
			updates: 40, progress: true, want: "forces: read page: injected retention fault"},
		{name: "a lost COMMIT acknowledgment after an earlier page", commit: 2, updates: 40, progress: true,
			want: "Messages: store: commit outcome unconfirmed; read the request before retrying (branch=commit_exec): injected retention fault"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			force := uuid.NewString()
			in, base, ids, _ := newBusySendRetentionInbox(t, 40, force)
			cutoff, at := base.Add(10*time.Millisecond), base.Add(time.Hour)
			before := readBusySendRetentionStates(t, in.path)
			var runs, updates, commits atomic.Int32
			stop := observeBusySendSQL(t, in.s, sqliteobserve.Hooks{
				Before: func(_ context.Context, q string) error {
					if strings.HasPrefix(q, "UPDATE busy_send_messages AS m SET retention") {
						updates.Add(1)
					}
					if tc.fault != "" && strings.HasPrefix(q, tc.fault) && runs.Add(1) == tc.nth {
						return injected
					}
					return nil
				},
				AfterCommit: func(context.Context) error {
					if commits.Add(1) == tc.commit {
						return injected
					}
					return nil
				},
			})
			err := in.s.ExpireBusySendRetention(cutoff, at)
			stop()
			if err == nil || err.Error() != prefix+tc.want || !errors.Is(err, injected) || errors.Is(err, store.ErrStoreBusy) ||
				store.CommitUnconfirmed(err) != (tc.commit > 0) {
				t.Fatalf("pass = %v; want only %q", err, prefix+tc.want)
			}
			if n := updates.Load(); n != tc.updates {
				t.Errorf("%d guarded writes ran; want %d, none retried", n, tc.updates)
			}
			failed := maps.Clone(before)
			if tc.progress {
				for _, id := range ids {
					failed[id] = before[id].expired(at)
				}
			}
			if got := readBusySendRetentionStates(t, in.path); !maps.Equal(got, failed) {
				t.Fatalf("after the failure %v; want %v", got, failed)
			}

			again := at.Add(time.Hour)
			if err := in.s.ExpireBusySendRetention(cutoff, again); err != nil {
				t.Fatalf("rerun: %v", err)
			}
			done := maps.Clone(failed)
			for key, s := range failed {
				if s.retention == "retained" {
					done[key] = s.expired(again)
				}
			}
			if got := readBusySendRetentionStates(t, in.path); !maps.Equal(got, done) {
				t.Errorf("after a rerun %v; want %v", got, done)
			}
		})
	}
}
