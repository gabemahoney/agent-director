package store_test

// The inbox's claim and prompt partition (Task t2.xis.oo.xe; SR-2.1 to
// SR-2.3, M1's Claim), on real SQLite through ClaimBusySendHandover. The
// helpers here are this Task's store-test assembly, shared with
// busy_send_emission_test.go. Packing and limit arithmetic are E4's; the
// coordinator's use of these steps is internal/busysend's.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
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

// busySendInbox is a covered Claude agent with a route, its Messages Held in
// order (the first confirmed under pair), and dest its current destination.
// force is the running force whose hand-over commit issues under (forceClaim
// sets it), nil for an ordinary hand-over.
type busySendInbox struct {
	s        *store.Store
	path, id string
	pair     store.BusySendPair
	dest     store.BusySendDestination
	force    *store.BusySendForceRef
}

func newBusySendInbox(t *testing.T, texts ...string) *busySendInbox {
	t.Helper()
	s, path := storefix.OpenTempStore(t)
	in := &busySendInbox{s: s, path: path, id: admissionAgent(t, path, filepath.Join(t.TempDir(), "wake")),
		pair: store.BusySendPair{CallerUUID: uuid.NewString(), DeliveryRequestID: "req-1"}}
	confirmed := true
	for i, text := range texts {
		req := store.BusySendAdmissionRequest{InstanceID: in.id, Text: []byte(text)}
		if i == 0 {
			req.CallerUUID, req.DeliveryRequestID, req.Confirmed = in.pair.CallerUUID, in.pair.DeliveryRequestID, &confirmed
		}
		if _, err := s.AdmitBusySend(req); err != nil {
			t.Fatal(err)
		}
	}
	pub, err := s.GetBusySendPublishedLaunch(in.id)
	if err != nil {
		t.Fatal(err)
	}
	row, err := s.GetSpawn(in.id)
	if err != nil {
		t.Fatal(err)
	}
	in.dest = store.BusySendDestination{LaunchID: pub.LaunchID, LaunchToken: pub.LaunchToken, Provider: pub.Provider,
		SessionID: row.SessionID, RouteKind: store.BusySendRouteClaudeHooks}
	return in
}

// claimRequest is a turn-end claim for dest at at (zero: now) with a one-hour
// issue budget inside a two-hour job.
func (in *busySendInbox) claimRequest(at time.Time, race bool) store.BusySendClaimRequest {
	return store.BusySendClaimRequest{InstanceID: in.id, Destination: in.dest, Boundary: store.BusySendBoundaryTurnEnd,
		RaceExposed: race, IssueBudget: time.Hour, JobBudget: 2 * time.Hour, At: at}
}

// claim claims as claimRequest and requires a new hand-over.
func (in *busySendInbox) claim(t *testing.T, at time.Time, race bool) *store.BusySendHandover {
	t.Helper()
	c, err := in.s.ClaimBusySendHandover(context.Background(), in.claimRequest(at, race))
	if err != nil || c.Outcome != store.BusySendClaimMade {
		t.Fatalf("claim %+v, %v; want a new hand-over", c, err)
	}
	return c.Handover
}

// busySendHelper is the issuing hook's identity every issue records.
var busySendHelper = store.BusySendIssuer{Role: store.BusySendProcessHelper, PID: 4242, StartTime: "100"}

func (in *busySendInbox) commit(ctx context.Context, h *store.BusySendHandover, k int, revision int64) (store.BusySendIssue, error) {
	issuer := busySendHelper
	return in.s.CommitBusySendIssue(ctx, store.BusySendIssueRequest{
		Ref: store.BusySendPromptRef{HandoverID: h.HandoverID, PromptIndex: k, Revision: revision}, Force: in.force, Issuer: &issuer})
}

// issue commits prompt k's issue at revision and requires it to pass.
func (in *busySendInbox) issue(t *testing.T, h *store.BusySendHandover, k int, revision int64) store.BusySendIssue {
	t.Helper()
	got, err := in.commit(context.Background(), h, k, revision)
	if err != nil {
		t.Fatalf("issue prompt %d: %v", k, err)
	}
	return got
}

// complete records the issuing job's completion for prompt k.
func (in *busySendInbox) complete(t *testing.T, h *store.BusySendHandover, k int, revision int64, out store.BusySendOutput,
	order store.BusySendOrderKind, unproven bool) store.BusySendCompletionResult {
	t.Helper()
	res, err := in.s.RecordBusySendCompletion(context.Background(), store.BusySendCompletion{
		Ref: store.BusySendPromptRef{HandoverID: h.HandoverID, PromptIndex: k, Revision: revision}, Output: out, Order: order,
		UnprovenBoundary: unproven})
	if err != nil {
		t.Fatalf("completion %s of prompt %d: %v", out, k, err)
	}
	return res
}

// reopen closes the store and opens its file again.
func (in *busySendInbox) reopen(t *testing.T) {
	t.Helper()
	if err := in.s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(in.path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	in.s = s
}

// busySendCustody is one Message's stored custody and Result.
type busySendCustody struct {
	id, place, class, reason, wait, handover string
	final, reserved, forced                  bool
	since, finalAt, size                     int64
}

// String is the custody compared by value: place, class, Final, reason (and a
// wait that differs from it), reservation, current membership and forced.
func (c busySendCustody) String() string {
	s := c.place + " " + c.class
	if c.final {
		s += " final"
	}
	s += " " + c.reason
	if c.wait != "" && c.wait != c.reason {
		s += " wait=" + c.wait
	}
	if c.reserved {
		s += " reserved"
	}
	if c.handover != "" {
		s += " member"
	}
	if c.forced {
		s += " forced"
	}
	return s
}

// result is the Message's public Result facts, which never change once Final.
func (c busySendCustody) result() string {
	return fmt.Sprintf("%s final=%t %s at %d", c.class, c.final, c.reason, c.finalAt)
}

func openBusySendRaw(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// readBusySendCustody reads id's Messages in accepted order.
func readBusySendCustody(t *testing.T, path, id string) []busySendCustody {
	t.Helper()
	rows, err := openBusySendRaw(t, path).Query(`SELECT message_id, place, result_class, result_final, COALESCE(result_reason, ''),
	        COALESCE(wait_reason, ''), COALESCE(wait_since_ms, 0), COALESCE(handover_id, ''), reserved, COALESCE(final_at_ms, 0), text_bytes,
	        forced
	    FROM busy_send_messages WHERE instance_id = ? ORDER BY accepted_seq`, id)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []busySendCustody
	for rows.Next() {
		var c busySendCustody
		if err := rows.Scan(&c.id, &c.place, &c.class, &c.final, &c.reason, &c.wait, &c.since, &c.handover, &c.reserved,
			&c.finalAt, &c.size, &c.forced); err != nil {
			t.Fatal(err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// wantBusySendCustody requires id's Messages to read want in accepted order,
// and the reservation to match the reserved Messages' count and text bytes.
func wantBusySendCustody(t *testing.T, path, id string, want ...string) []busySendCustody {
	t.Helper()
	got := readBusySendCustody(t, path, id)
	var states []string
	var count, bytes int64
	for _, c := range got {
		states = append(states, c.String())
		if c.reserved {
			count, bytes = count+1, bytes+c.size
		}
	}
	inbox := readAdmissionInbox(t, path, id)
	if !reflect.DeepEqual(states, want) || inbox.count != count || inbox.bytes != bytes {
		t.Fatalf("custody %q reserving %d Messages, %d bytes; want %q reserving the %d reserved Messages' %d bytes",
			states, inbox.count, inbox.bytes, want, count, bytes)
	}
	return got
}

// readBusySendTables reads every row of the inbox's tables and the agent rows,
// so a refused or inert call can be shown to have written nothing.
func readBusySendTables(t *testing.T, path string) map[string][][]any {
	t.Helper()
	db := openBusySendRaw(t, path)
	out := map[string][][]any{}
	for _, table := range []string{"spawns", "busy_conversations", "busy_send_launches", "busy_send_launch_processes",
		"busy_send_messages", "busy_send_handovers", "busy_send_prompts", "busy_send_handover_members"} {
		rows, err := db.Query("SELECT * FROM " + table + " ORDER BY 1, 2")
		if err != nil {
			t.Fatal(err)
		}
		cols, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			row := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range row {
				ptrs[i] = &row[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			out[table] = append(out[table], row)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		_ = rows.Close()
	}
	return out
}

// setBusySendFact changes one recorded fact as its later writer (Tasks 3, 4)
// would, requiring exactly one row changed.
func setBusySendFact(t *testing.T, path, query string, args ...any) {
	t.Helper()
	r, err := openBusySendRaw(t, path).Exec(query, args...)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := r.RowsAffected(); err != nil || n != 1 {
		t.Fatalf("%s changed %d rows, %v; want one", query, n, err)
	}
}

// TestBusySendPartitionPrompts: the one renderer and partitioner pack whole
// Messages, in order, into the longest prefix whose complete measured prompt
// fits the published Claude Wait Turn limits (SR-2.3): each body is its
// separator line with local N of M and the original bytes, Messages joined by
// LF LF; a two-digit M is counted (under explicit local limits); sizes derive
// from the table's limits; the route's measure, not the table's
// wrapper budget alone, decides a fit; a Message at the text limit fits alone
// exactly; a route wrapper over the budget leaves it unfit; and a measure
// below the body is an error.
func TestBusySendPartitionPrompts(t *testing.T) {
	t.Parallel()
	limits, ok := store.BusySendCapabilitiesFor(store.AgentClaude).Mode(store.BusySendWaitTurn)
	if !ok {
		t.Fatal("Claude offers no Wait Turn")
	}
	wrapped := func(n int64) store.BusySendPromptMeasure { return func(b []byte) int64 { return int64(len(b)) + n } }
	// budget measures the table's whole wrapper budget, which the arithmetic
	// below is built on; a nil measure is Claude's exact wrapping instead.
	budget := wrapped(limits.WrapperBytes)
	sep1, sep2 := int64(len("[agent-director: message 1 of 1]\n")), int64(len("[agent-director: message 1 of 2]\n"))
	largest := strings.Repeat("m", int(limits.MessageBytes))
	// Two Messages of half bytes fill one prompt under the table's wrapper
	// budget; a route wrapper one byte larger than that leaves no room for both.
	half := (limits.PromptBytes - limits.WrapperBytes - 2*sep2 - 2) / 2
	both := 2*sep2 + 2 + 2*half
	over := limits.PromptBytes - both + 1
	// The decimal-width case has its own limits: nine 10-byte Messages render
	// 403 bytes; a tenth of 55 needs 504 with two-digit M, 493 without.
	local := store.BusySendModeLimits{Mode: store.BusySendWaitTurn, MessageBytes: 100, PromptBytes: 500, TextKind: store.BusySendTextUTF8TabLF}
	nine := make([]string, 9)
	for i := range nine {
		nine[i] = strings.Repeat("n", 10)
	}
	tagged := "[agent-director: message 1 of 2]\ncafé 🌍\tTAB\nand LF\n\n[agent-director: message 2 of 2]\n[agent-director: message 9 of 9]\nnot a separator"
	ten := "[agent-director: message 1 of 10]\na\n\n[agent-director: message 2 of 10]\nb\n\n" +
		"[agent-director: message 3 of 10]\nc\n\n[agent-director: message 4 of 10]\nd\n\n" +
		"[agent-director: message 5 of 10]\ne\n\n[agent-director: message 6 of 10]\nf\n\n" +
		"[agent-director: message 7 of 10]\ng\n\n[agent-director: message 8 of 10]\nh\n\n" +
		"[agent-director: message 9 of 10]\ni\n\n[agent-director: message 10 of 10]\nj"
	for _, tc := range []struct {
		name    string
		limits  *store.BusySendModeLimits // nil: Claude's published Wait Turn
		texts   []string
		measure store.BusySendPromptMeasure
		want    []store.BusySendPromptPart
		body    string // the first prompt's exact body, when given
		wantErr func(error) bool
	}{
		{name: "whole bodies, local N of M", texts: []string{"café 🌍\tTAB\nand LF", "[agent-director: message 9 of 9]\nnot a separator"},
			measure: budget, want: []store.BusySendPromptPart{{First: 0, Count: 2, RenderedBytes: int64(len(tagged)) + limits.WrapperBytes}}, body: tagged},
		{name: "the table's wrapper budget fits two", texts: []string{strings.Repeat("a", int(half)), strings.Repeat("b", int(half))},
			measure: budget, want: []store.BusySendPromptPart{{First: 0, Count: 2, RenderedBytes: both + limits.WrapperBytes}}},
		{name: "the route's larger wrapper splits them", texts: []string{strings.Repeat("a", int(half)), strings.Repeat("b", int(half))},
			measure: wrapped(over), want: []store.BusySendPromptPart{{First: 0, Count: 1, RenderedBytes: sep1 + half + over},
				{First: 1, Count: 1, RenderedBytes: sep1 + half + over}}},
		// R2-4: a nil measure is the table's exact one, Claude's fixed
		// wrapping plus a reserved closing tag counted escaped.
		{name: "a nil measure is the table's exact one", texts: []string{"</system-reminder>"},
			want: []store.BusySendPromptPart{{First: 0, Count: 1,
				RenderedBytes: sep1 + int64(len("&lt;/system-reminder>")) + limits.Measure(nil)}}},
		{name: "a two-digit M is counted", limits: &local, texts: append(nine, strings.Repeat("x", 55)),
			want: []store.BusySendPromptPart{{First: 0, Count: 9, RenderedBytes: 403}, {First: 9, Count: 1, RenderedBytes: 88}}},
		{name: "ten Messages in one prompt", texts: strings.Split("abcdefghij", ""),
			measure: budget, want: []store.BusySendPromptPart{{First: 0, Count: 10, RenderedBytes: int64(len(ten)) + limits.WrapperBytes}}, body: ten},
		{name: "a maximum-size Message alone, then the longest prefix", texts: []string{largest, "x", "y"},
			measure: budget, want: []store.BusySendPromptPart{{First: 0, Count: 1, RenderedBytes: sep1 + limits.MessageBytes + limits.WrapperBytes},
				{First: 1, Count: 2, RenderedBytes: 2*sep2 + 2 + 2 + limits.WrapperBytes}}},
		{name: "a route wrapper leaving no room for a maximum-size Message", texts: []string{largest},
			measure: wrapped(limits.PromptBytes - sep1 - limits.MessageBytes + 1),
			wantErr: func(err error) bool { return errors.Is(err, store.ErrBusySendPromptUnfit) }},
		{name: "a measure below the body", texts: []string{"x"}, measure: func(b []byte) int64 { return int64(len(b)) - 1 },
			wantErr: func(err error) bool { return err != nil && !errors.Is(err, store.ErrBusySendPromptUnfit) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			texts := make([][]byte, len(tc.texts))
			for i, s := range tc.texts {
				texts[i] = []byte(s)
			}
			lim := limits
			if tc.limits != nil {
				lim = *tc.limits
			}
			got, err := store.PartitionBusySendPrompts(texts, lim, tc.measure)
			if tc.wantErr != nil {
				if !tc.wantErr(err) || got != nil {
					t.Fatalf("partition %+v, %v; want the error", got, err)
				}
				return
			}
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("partition %+v, %v; want %+v", got, err, tc.want)
			}
			if body := store.RenderBusySendPromptBody(texts[:got[0].Count]); tc.body != "" && string(body) != tc.body {
				t.Errorf("first prompt body %q; want %q", body, tc.body)
			}
		})
	}
}

// TestBusySendClaimFreezesPrefixAtCutoff (SR-2.2, SR-2.3; M1 Claim): a claim
// freezes every Held Message at its accepted cutoff into one hand-over that
// owns the conversation, partitioned under the route's measure with the exact
// bodies, the Messages claimed with their reservations kept. An admission
// that commits while the claim holds its write transaction (a causal barrier
// at the hand-over insert) takes the next sequence and stays Held outside the
// group; it neither joins nor stales it, so the owner still issues. A repeated
// eligible event finds the owner and changes nothing. A reopened store reads
// the same frozen partition, byte for byte.
func TestBusySendClaimFreezesPrefixAtCutoff(t *testing.T) {
	t.Parallel()
	limits, _ := store.BusySendCapabilitiesFor(store.AgentClaude).Mode(store.BusySendWaitTurn)
	texts := []string{"café 🌍\tTAB\nand LF", "[agent-director: message 1 of 1]\na tag in the text", strings.Repeat("z", int(limits.MessageBytes))}
	// The route's wrapper, one byte under the table's budget, fits the
	// maximum-size third Message alone but not after the first two.
	wrapper := limits.WrapperBytes - 1
	in := newBusySendInbox(t, texts...)
	rival, err := store.Open(in.path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rival.Close() })
	holding, release, begun := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var holdOnce, releaseOnce, begunOnce sync.Once
	store.ObserveDecisionBoundaryForTest(t, in.s, in.path, func(_ context.Context, q string) error {
		if strings.Contains(q, "INSERT INTO busy_send_handovers") {
			holdOnce.Do(func() { close(holding); <-release })
		}
		return nil
	}, nil)
	store.ObserveDecisionBoundaryForTest(t, rival, in.path, func(_ context.Context, q string) error {
		if q == "BEGIN IMMEDIATE" {
			begunOnce.Do(func() { close(begun) })
		}
		return nil
	}, nil)
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	req := in.claimRequest(time.Time{}, false)
	req.Measure = func(b []byte) int64 { return int64(len(b)) + wrapper }
	type claimed struct {
		c   store.BusySendClaim
		err error
	}
	claimDone, admitDone := make(chan claimed, 1), make(chan error, 1)
	go func() { c, err := in.s.ClaimBusySendHandover(context.Background(), req); claimDone <- claimed{c, err} }()
	awaitSignal(t, holding, "the claim's hand-over insert")
	go func() {
		_, err := rival.AdmitBusySend(store.BusySendAdmissionRequest{InstanceID: in.id, Text: []byte("late")})
		admitDone <- err
	}()
	awaitSignal(t, begun, "the late admission's BEGIN IMMEDIATE")
	unblock()
	var got claimed
	select {
	case got = <-claimDone:
	case <-time.After(10 * time.Second):
		t.Fatal("the claim did not return within 10 s of the barrier's release")
	}
	select {
	case err := <-admitDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the late admission did not return within 10 s of the barrier's release")
	}
	if got.err != nil || got.c.Outcome != store.BusySendClaimMade {
		t.Fatalf("claim %+v, %v; want a new hand-over", got.c, got.err)
	}
	h := got.c.Handover
	held := wantBusySendCustody(t, in.path, in.id,
		"claimed held handover_in_progress reserved member", "claimed held handover_in_progress reserved member",
		"claimed held handover_in_progress reserved member", "held held handover_in_progress reserved")
	first := "[agent-director: message 1 of 2]\n" + texts[0] + "\n\n[agent-director: message 2 of 2]\n" + texts[1]
	third := "[agent-director: message 1 of 1]\n" + texts[2]
	want := []store.BusySendPrompt{
		{Index: 0, MessageIDs: []string{held[0].id, held[1].id}, Body: []byte(first), RenderedBytes: int64(len(first)) + wrapper, Emission: store.BusySendEmissionNone},
		{Index: 1, MessageIDs: []string{held[2].id}, Body: []byte(third), RenderedBytes: int64(len(third)) + wrapper, Emission: store.BusySendEmissionNone},
	}
	brief := func(ps []store.BusySendPrompt) (out []string) {
		for _, p := range ps {
			out = append(out, fmt.Sprintf("%d %v %d bytes %s %q", p.Index, p.MessageIDs, p.RenderedBytes, p.Emission, p.Body[:min(len(p.Body), 120)]))
		}
		return out
	}
	if h.CutoffSeq != 3 || h.HandoverSeq != 1 || h.Destination != in.dest || !h.Open || h.PromptCursor != 0 || !reflect.DeepEqual(h.Prompts, want) {
		t.Fatalf("hand-over %d cutoff %d at %+v, open %t, cursor %d, prompts %q; want 1, cutoff 3, open at cursor 0 with %q",
			h.HandoverSeq, h.CutoffSeq, h.Destination, h.Open, h.PromptCursor, brief(h.Prompts), brief(want))
	}

	before := readBusySendTables(t, in.path)
	again, err := in.s.ClaimBusySendHandover(context.Background(), in.claimRequest(time.Time{}, false))
	if err != nil || again.Outcome != store.BusySendClaimOwned || again.Wait != store.BusySendWaitHandoverInProgress ||
		again.Handover != nil || again.ReleasedExpired != 0 {
		t.Errorf("repeated event %+v, %v; want owned, waiting handover_in_progress", again, err)
	}
	if !reflect.DeepEqual(before, readBusySendTables(t, in.path)) {
		t.Error("a repeated event changed the inbox")
	}

	in.reopen(t)
	read, err := in.s.ReadBusySendHandover(context.Background(), h.HandoverID)
	if err != nil || !reflect.DeepEqual(read, *h) {
		t.Fatalf("reopened hand-over %+v, %v; want %+v", read, err, *h)
	}
	in.issue(t, h, 0, h.Revision)
}

// TestBusySendClaimRefusals (SR-2.2): an event for another session, an
// uncovered Launch, a missing agent row, a Launch with no recorded route or an
// open permission request claims nothing, with the wait that prohibits an
// attempt or none for a stale destination, and writes nothing.
func TestBusySendClaimRefusals(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		setup   func(t *testing.T, in *busySendInbox)
		session string
		wait    store.BusySendWaitReason
	}{
		{name: "another session's event", session: "another-session"},
		{name: "an uncovered Launch", setup: func(t *testing.T, in *busySendInbox) {
			if err := apitest.SeedBusySendCoverage(in.path, in.id, store.BusySendNotCovered, store.BusySendCovered); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "a missing agent row", wait: store.BusySendWaitLaunchEnded, setup: func(t *testing.T, in *busySendInbox) {
			setBusySendFact(t, in.path, `UPDATE spawns SET state = 'missing' WHERE instance_id = ?`, in.id)
		}},
		{name: "no recorded route", wait: store.BusySendWaitRouteUnavailable, setup: func(t *testing.T, in *busySendInbox) {
			setBusySendFact(t, in.path, `UPDATE busy_send_launches SET route_kind = NULL WHERE instance_id = ? AND phase = 'current'`, in.id)
		}},
		{name: "an open permission request", wait: store.BusySendWaitDialog, setup: func(t *testing.T, in *busySendInbox) {
			storefix.SeedOpenPermissionRequests(t, in.s, in.id, []string{"tok-1"})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			in := newBusySendInbox(t, "one", "two")
			if tc.setup != nil {
				tc.setup(t, in)
			}
			req := in.claimRequest(time.Time{}, false)
			if tc.session != "" {
				req.Destination.SessionID = tc.session
			}
			before := readBusySendTables(t, in.path)
			got, err := in.s.ClaimBusySendHandover(context.Background(), req)
			if err != nil || got.Outcome != store.BusySendClaimIneligible || got.Wait != tc.wait || got.Handover != nil {
				t.Errorf("claim %+v, %v; want ineligible with wait %q", got, err, tc.wait)
			}
			if !reflect.DeepEqual(before, readBusySendTables(t, in.path)) {
				t.Error("a refused claim changed the inbox")
			}
		})
	}
}

// TestBusySendClaimCommitOutcome: a claim that fails before COMMIT returns
// its failure and writes nothing, so the next event claims. A claim whose
// COMMIT lands with its acknowledgment lost is unconfirmed and grants nothing
// (no hand-over returned); after reopening, its landed hand-over keeps the
// Messages claimed and reserved and owns the conversation, so a later event
// finds it owned until its original issue deadline passes, when a claim
// releases its never-issued Messages and freezes them again.
func TestBusySendClaimCommitOutcome(t *testing.T) {
	t.Parallel()
	injected := errors.New("injected membership insert failure")
	for _, lost := range []bool{false, true} {
		t.Run(map[bool]string{false: "failure before COMMIT", true: "lost COMMIT acknowledgment"}[lost], func(t *testing.T) {
			t.Parallel()
			in := newBusySendInbox(t, "one", "two")
			before := readBusySendTables(t, in.path)
			var fired atomic.Bool
			store.ObserveDecisionBoundaryForTest(t, in.s, in.path, func(_ context.Context, q string) error {
				if !lost && strings.Contains(q, "INSERT INTO busy_send_handover_members") && fired.CompareAndSwap(false, true) {
					return injected
				}
				return nil
			}, func(context.Context) error {
				if lost && fired.CompareAndSwap(false, true) {
					return errors.New("COMMIT acknowledgment lost")
				}
				return nil
			})
			got, err := in.s.ClaimBusySendHandover(context.Background(), in.claimRequest(time.Time{}, false))
			wantErr := store.CommitUnconfirmed(err)
			if !lost {
				wantErr = errors.Is(err, injected) && !store.CommitUnconfirmed(err)
			}
			if !wantErr || !reflect.DeepEqual(got, store.BusySendClaim{}) {
				t.Fatalf("claim %+v, %v; want only the %s error", got, err, map[bool]string{false: "injected", true: "unconfirmed"}[lost])
			}
			in.reopen(t)
			if !lost {
				if !reflect.DeepEqual(before, readBusySendTables(t, in.path)) {
					t.Fatal("a claim that failed before COMMIT wrote part of itself")
				}
				in.claim(t, time.Time{}, false)
				return
			}
			landed := wantBusySendCustody(t, in.path, in.id,
				"claimed held handover_in_progress reserved member", "claimed held handover_in_progress reserved member")
			h, err := in.s.ReadBusySendHandover(context.Background(), landed[0].handover)
			if err != nil {
				t.Fatal(err)
			}
			owned, err := in.s.ClaimBusySendHandover(context.Background(), in.claimRequest(time.Time{}, false))
			if err != nil || owned.Outcome != store.BusySendClaimOwned || owned.ReleasedExpired != 0 {
				t.Fatalf("claim before the deadline %+v, %v; want owned", owned, err)
			}
			again, err := in.s.ClaimBusySendHandover(context.Background(), in.claimRequest(h.IssueDeadline.Add(time.Millisecond), false))
			if err != nil || again.Outcome != store.BusySendClaimMade || again.ReleasedExpired != 2 || again.Handover.HandoverSeq != 2 ||
				!reflect.DeepEqual(again.Handover.Prompts[0].MessageIDs, []string{landed[0].id, landed[1].id}) {
				t.Fatalf("claim after the deadline %+v, %v; want both Messages released and claimed again", again, err)
			}
			wantBusySendCustody(t, in.path, in.id,
				"claimed held handover_in_progress reserved member", "claimed held handover_in_progress reserved member")
		})
	}
}
