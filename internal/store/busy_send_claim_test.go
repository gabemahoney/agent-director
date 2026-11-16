package store_test

// The inbox's claim and prompt partition (Task t2.xis.oo.xe; SR-2.1 to
// SR-2.3, M1's Claim), on real SQLite through ClaimBusySendHandover, with
// E4's packing and limit arithmetic for both offered Send Modes (Task
// t2.xis.wm.o7). The helpers here are the store tests' shared assembly; the
// coordinator's use of these steps is internal/busysend's.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
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
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// busySendInbox is a covered Claude agent with a route, its Messages Held in
// order (the first confirmed under pair), and dest its current destination.
// force is the running force whose hand-over commit issues under (forceClaim
// sets it), nil for an ordinary hand-over. clock, when set, is the time its
// issue commits and completions record (nil: now).
type busySendInbox struct {
	s        *store.Store
	path, id string
	pair     store.BusySendPair
	dest     store.BusySendDestination
	force    *store.BusySendForceRef
	clock    *tmuxfix.Clock
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

// admit admits each text as an unconfirmed Message in mode, Held behind the
// inbox's earlier Messages.
func (in *busySendInbox) admit(t *testing.T, mode store.BusySendMode, texts ...string) {
	t.Helper()
	for _, text := range texts {
		if _, err := in.s.AdmitBusySend(store.BusySendAdmissionRequest{InstanceID: in.id, Text: []byte(text), SendMode: &mode}); err != nil {
			t.Fatalf("admit %s %.40q: %v", mode, text, err)
		}
	}
}

// claimRequest is a turn-end claim for dest at at (zero: now) with a one-hour
// issue budget inside a two-hour job.
func (in *busySendInbox) claimRequest(at time.Time, race bool) store.BusySendClaimRequest {
	return store.BusySendClaimRequest{InstanceID: in.id, Destination: in.dest, Boundary: store.BusySendBoundaryTurnEnd,
		RaceExposed: race, IssueBudget: time.Hour, JobBudget: 2 * time.Hour, At: at}
}

// steerRequest is claimRequest at a parent tool boundary of the running turn,
// as Claude's PostToolBatch route claims: not race-exposed, under the Steer
// Turn row's own measure.
func (in *busySendInbox) steerRequest(at time.Time) store.BusySendClaimRequest {
	req := in.claimRequest(at, false)
	req.Boundary = store.BusySendBoundaryToolBoundary
	return req
}

// claim claims as claimRequest and requires a new hand-over.
func (in *busySendInbox) claim(t *testing.T, at time.Time, race bool) *store.BusySendHandover {
	t.Helper()
	return in.claimed(t, in.claimRequest(at, race))
}

// steer claims as steerRequest and requires a new hand-over.
func (in *busySendInbox) steer(t *testing.T, at time.Time) *store.BusySendHandover {
	t.Helper()
	return in.claimed(t, in.steerRequest(at))
}

// claimed claims req and requires a new hand-over.
func (in *busySendInbox) claimed(t *testing.T, req store.BusySendClaimRequest) *store.BusySendHandover {
	t.Helper()
	c, err := in.s.ClaimBusySendHandover(context.Background(), req)
	if err != nil || c.Outcome != store.BusySendClaimMade {
		t.Fatalf("claim at %s %+v, %v; want a new hand-over", req.Boundary, c, err)
	}
	return c.Handover
}

// busySendHelper is the issuing hook's identity every issue records.
var busySendHelper = store.BusySendIssuer{Role: store.BusySendProcessHelper, PID: 4242, StartTime: "100"}

// now is in's clock, nil for the wall clock.
func (in *busySendInbox) now() func() time.Time {
	if in.clock == nil {
		return nil
	}
	return in.clock.Now
}

func (in *busySendInbox) commit(ctx context.Context, h *store.BusySendHandover, k int, revision int64) (store.BusySendIssue, error) {
	issuer := busySendHelper
	return in.s.CommitBusySendIssue(ctx, store.BusySendIssueRequest{
		Ref: store.BusySendPromptRef{HandoverID: h.HandoverID, PromptIndex: k, Revision: revision}, Force: in.force, Issuer: &issuer,
		Now: in.now()})
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

// complete records the issuing job's completion for prompt k, at in's clock.
func (in *busySendInbox) complete(t *testing.T, h *store.BusySendHandover, k int, revision int64, out store.BusySendOutput,
	order store.BusySendOrderKind, unproven bool) store.BusySendCompletionResult {
	t.Helper()
	var at time.Time
	if in.clock != nil {
		at = in.clock.Now()
	}
	res, err := in.s.RecordBusySendCompletion(context.Background(), store.BusySendCompletion{
		Ref: store.BusySendPromptRef{HandoverID: h.HandoverID, PromptIndex: k, Revision: revision}, Output: out, Order: order,
		UnprovenBoundary: unproven, At: at})
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

// busySendRoute is how a published Claude Send Mode's route delivers a prompt,
// counted by hand from what the agent receives rather than by the table's
// measure (SR-1.6): the wake's fixed text around the body at turn end and
// none at a parent tool boundary, and the bytes each reserved tag grows by
// where Claude escapes it (Research b.p8z / t1.p8z.9e for the wake; b.87a /
// t1.87a.12 P6 for PostToolBatch additionalContext, where both reminder tags
// were escaped). The hook output's JSON quoting is never what the agent gets.
type busySendRoute struct {
	mode  store.BusySendMode
	fixed int64
	grows map[string]int
}

var busySendRoutes = []busySendRoute{
	{store.BusySendWaitTurn, 169, map[string]int{"</system-reminder>": 3, "</task-notification>": 1}},
	{store.BusySendSteerTurn, 0, map[string]int{"<system-reminder>": 3, "</system-reminder>": 3}},
}

// rendered is the complete size of the prompt carrying texts (SR-2.3): each
// separator with local N of M and the text, LF LF between Messages, the fixed
// text and each escaped tag's growth.
func (r busySendRoute) rendered(texts ...string) int64 {
	n := r.fixed + int64(2*(len(texts)-1))
	for i, s := range texts {
		n += int64(len(fmt.Sprintf("[agent-director: message %d of %d]\n", i+1, len(texts))) + len(s))
		for tag, grows := range r.grows {
			n += int64(strings.Count(s, tag) * grows)
		}
	}
	return n
}

// busySendBody is the body of the prompt carrying texts, written by hand:
// each separator line, then the original bytes, LF LF between Messages.
func busySendBody(texts ...string) string {
	var b strings.Builder
	for i, s := range texts {
		if i > 0 {
			b.WriteString("\n\n")
		}
		fmt.Fprintf(&b, "[agent-director: message %d of %d]\n%s", i+1, len(texts), s)
	}
	return b.String()
}

// busySendFill is n bytes of one- to four-byte characters with no tag.
func busySendFill(n int) string {
	return strings.Repeat("é🙂漢abc", n/12) + strings.Repeat("a", n%12)
}

// busySendCorpus is the PRD's 1,900-byte multi-line Unicode Message with
// markup-like tags, made as Research b.87a / t1.87a.12 P6's steer case was:
// its first lines, then mixed characters. Its one opening and one closing
// reminder tag grew Claude's PostToolBatch additionalContext by 6 bytes.
var busySendCorpus = func() string {
	head := "line-one\n<system-reminder>literal</system-reminder>\n<note>α🙂漢é</note>\n"
	return head + busySendFill(1900-len(head))
}()

// busySendTagText is a Message at the 8,000-byte text limit made of tag, the
// most a route that escapes tag can grow a valid Message.
func busySendTagText(tag string) string {
	return strings.Repeat(tag, 8000/len(tag)) + strings.Repeat("x", 8000%len(tag))
}

// TestBusySendPartitionPrompts (SR-1.6, SR-2.3; M1's during-coding packing and
// limit arithmetic, Model Doc t1.kii.91 "Not here (during-coding)", E2 decision
// 2): the one renderer and partitioner pack whole Messages, in order, into the
// fewest prompts, each the longest prefix that fits Claude's published
// 9,600-byte prompt, in both offered modes under each mode's own measure: Wait
// Turn's wake text and escapes, Steer Turn's escaping of both reminder tags
// with no wrapper. Expected sizes and bodies are counted by hand
// (busySendRoute, busySendBody): separator lines with local N of M, the
// original bytes, LF LF between Messages. One byte under and exactly at the
// limit fit one prompt and one byte over moves the last Message to the next,
// for two Messages and across the separators' 9-to-10 and 99-to-100 digit
// growth. Five of the research corpus pack four and one. A Message at the
// 8,000-byte text limit fits alone even made of the tag a mode escapes most.
// Under a route's explicit measure, that measure, not the table's wrapper
// budget alone, decides a fit; a route wrapper over the budget leaves a
// maximum-size Message unfit; and a measure below the body is an error.
func TestBusySendPartitionPrompts(t *testing.T) {
	t.Parallel()
	partition := func(texts []string, limits store.BusySendModeLimits, measure store.BusySendPromptMeasure) ([]store.BusySendPromptPart, error) {
		b := make([][]byte, len(texts))
		for i, s := range texts {
			b[i] = []byte(s)
		}
		return store.PartitionBusySendPrompts(b, limits, measure)
	}
	letters := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = string(rune('a' + i%26))
		}
		return out
	}
	shapes := []struct {
		name  string
		texts []string // the first grows to put the prompt at the limit
	}{
		{"two Messages", []string{"", busySendCorpus}},
		{"ten Messages", append([]string{"", "[agent-director: message 9 of 9]\n<system-reminder>not a separator</system-reminder>"},
			slices.Repeat([]string{busySendFill(300)}, 8)...)},
		{"a hundred Messages", append([]string{"", "</system-reminder>"}, letters(98)...)},
	}
	for _, route := range busySendRoutes {
		limits, ok := store.BusySendCapabilitiesFor(store.AgentClaude).Mode(route.mode)
		if !ok || limits.MessageBytes != 8000 || limits.PromptBytes != 9600 || limits.TextKind != store.BusySendTextUTF8TabLF {
			t.Fatalf("Claude's %s %+v, offered %t; want 8,000-byte Messages in 9,600-byte prompts of utf8_tab_lf", route.mode, limits, ok)
		}
		type packing struct {
			name   string
			texts  []string
			counts []int // Messages per prompt, in order
		}
		var cases []packing
		for _, over := range []int64{-1, 0, 1} {
			for _, shape := range shapes {
				texts := slices.Clone(shape.texts)
				texts[0] = busySendFill(int(9600 + over - route.rendered(texts...)))
				counts := []int{len(texts)}
				if over > 0 {
					counts = []int{len(texts) - 1, 1}
				}
				cases = append(cases, packing{fmt.Sprintf("%s at the limit %+d", shape.name, over), texts, counts})
			}
		}
		cases = append(cases,
			packing{"five research corpus Messages", slices.Repeat([]string{busySendCorpus}, 5), []int{4, 1}},
			packing{"each tag's most escaping at the text limit", []string{busySendTagText("<system-reminder>"),
				busySendTagText("</system-reminder>")}, []int{1, 1}})
		for _, tc := range cases {
			t.Run(string(route.mode)+"/"+tc.name, func(t *testing.T) {
				var want []store.BusySendPromptPart
				first := 0
				for _, n := range tc.counts {
					want = append(want, store.BusySendPromptPart{First: first, Count: n, RenderedBytes: route.rendered(tc.texts[first : first+n]...)})
					first += n
				}
				got, err := partition(tc.texts, limits, nil)
				if err != nil || !reflect.DeepEqual(got, want) {
					t.Fatalf("partition %+v, %v; want %+v", got, err, want)
				}
				for _, p := range got {
					part := tc.texts[p.First : p.First+p.Count]
					b := make([][]byte, len(part))
					for i, s := range part {
						b[i] = []byte(s)
					}
					if body := string(store.RenderBusySendPromptBody(b)); body != busySendBody(part...) {
						t.Errorf("prompt of Messages %d to %d: body %.200q; want %.200q", p.First, p.First+p.Count-1, body, busySendBody(part...))
					}
				}
			})
		}
	}

	limits, _ := store.BusySendCapabilitiesFor(store.AgentClaude).Mode(store.BusySendWaitTurn)
	wrapped := func(n int64) store.BusySendPromptMeasure { return func(b []byte) int64 { return int64(len(b)) + n } }
	sep1, sep2 := int64(len("[agent-director: message 1 of 1]\n")), int64(len("[agent-director: message 1 of 2]\n"))
	// Two Messages of half bytes fill one prompt under the table's wrapper
	// budget; a route wrapper one byte larger than that leaves no room for both.
	half := (limits.PromptBytes - limits.WrapperBytes - 2*sep2 - 2) / 2
	both := 2*sep2 + 2 + 2*half
	over := limits.PromptBytes - both + 1
	for _, tc := range []struct {
		name    string
		texts   []string
		measure store.BusySendPromptMeasure
		want    []store.BusySendPromptPart
		wantErr func(error) bool
	}{
		{name: "the table's wrapper budget fits two", texts: []string{strings.Repeat("a", int(half)), strings.Repeat("b", int(half))},
			measure: wrapped(limits.WrapperBytes), want: []store.BusySendPromptPart{{First: 0, Count: 2, RenderedBytes: both + limits.WrapperBytes}}},
		{name: "the route's larger wrapper splits them", texts: []string{strings.Repeat("a", int(half)), strings.Repeat("b", int(half))},
			measure: wrapped(over), want: []store.BusySendPromptPart{{First: 0, Count: 1, RenderedBytes: sep1 + half + over},
				{First: 1, Count: 1, RenderedBytes: sep1 + half + over}}},
		{name: "a route wrapper leaving no room for a maximum-size Message", texts: []string{strings.Repeat("m", int(limits.MessageBytes))},
			measure: wrapped(limits.PromptBytes - sep1 - limits.MessageBytes + 1),
			wantErr: func(err error) bool { return errors.Is(err, store.ErrBusySendPromptUnfit) }},
		{name: "a measure below the body", texts: []string{"x"}, measure: func(b []byte) int64 { return int64(len(b)) - 1 },
			wantErr: func(err error) bool { return err != nil && !errors.Is(err, store.ErrBusySendPromptUnfit) }},
	} {
		t.Run("route measure/"+tc.name, func(t *testing.T) {
			got, err := partition(tc.texts, limits, tc.measure)
			if tc.wantErr != nil {
				if !tc.wantErr(err) || got != nil {
					t.Fatalf("partition %+v, %v; want the error", got, err)
				}
				return
			}
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("partition %+v, %v; want %+v", got, err, tc.want)
			}
		})
	}
}

// TestBusySendClaimFreezesPrefixAtCutoff (SR-2.2, SR-2.3; M1 Claim): a claim
// freezes the eligible Held prefix at its accepted cutoff into one hand-over
// that owns the conversation, partitioned under the route's measure with the
// exact bodies, the Messages claimed with their reservations kept: every Held
// Message at turn end; at a working agent's parent tool boundary only the
// Steer Turn Messages before the first Wait Turn Message, under the Steer Turn
// row's own measure, leaving that Wait Turn Message and every later one Held.
// An admission that commits while the claim holds its write transaction (a
// causal barrier at the hand-over insert) takes the next sequence and stays
// Held outside the group; it neither joins nor stales it, so the owner still
// issues. A repeated eligible event finds the owner and changes nothing. A
// reopened store reads the same frozen partition, byte for byte. A Wait Turn
// Message first leaves a tool boundary nothing to claim, and nothing changes.
func TestBusySendClaimFreezesPrefixAtCutoff(t *testing.T) {
	t.Parallel()
	limits, _ := store.BusySendCapabilitiesFor(store.AgentClaude).Mode(store.BusySendWaitTurn)
	texts := []string{"café 🌍\tTAB\nand LF <system-reminder>kept</system-reminder>", "[agent-director: message 1 of 1]\na tag in the text",
		busySendTagText("<system-reminder>")}
	// At turn end the route's wrapper, one byte under the table's budget, fits
	// the maximum-size third Message alone but not after the first two; at a
	// tool boundary Steer Turn's escaping of its tags does the same.
	wrapper := limits.WrapperBytes - 1
	for _, tc := range []struct {
		name    string
		mode    store.BusySendMode
		request func(in *busySendInbox) store.BusySendClaimRequest
		size    func(texts ...string) int64
		behind  int // Messages Held behind the prefix: a Wait Turn one, then a Steer Turn one
	}{
		{"turn end", store.BusySendWaitTurn, func(in *busySendInbox) store.BusySendClaimRequest {
			req := in.claimRequest(time.Time{}, false)
			req.Measure = func(b []byte) int64 { return int64(len(b)) + wrapper }
			return req
		}, func(texts ...string) int64 { return int64(len(busySendBody(texts...))) + wrapper }, 0},
		{"a working agent's tool boundary", store.BusySendSteerTurn, func(in *busySendInbox) store.BusySendClaimRequest {
			return in.steerRequest(time.Time{})
		}, busySendRoutes[1].rendered, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			in := newBusySendInbox(t)
			in.admit(t, tc.mode, texts...)
			if tc.behind > 0 {
				in.admit(t, store.BusySendWaitTurn, "a Wait Turn Message")
				in.admit(t, store.BusySendSteerTurn, "a later Steer Turn Message")
				setBusySendFact(t, in.path, `UPDATE spawns SET state = 'working' WHERE instance_id = ?`, in.id)
			}
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
			req := tc.request(in)
			type claimed struct {
				c   store.BusySendClaim
				err error
			}
			claimDone, admitDone := make(chan claimed, 1), make(chan error, 1)
			go func() { c, err := in.s.ClaimBusySendHandover(context.Background(), req); claimDone <- claimed{c, err} }()
			awaitSignal(t, holding, "the claim's hand-over insert")
			go func() {
				_, err := rival.AdmitBusySend(store.BusySendAdmissionRequest{InstanceID: in.id, Text: []byte("late"), SendMode: &tc.mode})
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
			custody := slices.Repeat([]string{"claimed held handover_in_progress reserved member"}, 3)
			custody = append(custody, slices.Repeat([]string{"held held handover_in_progress reserved"}, tc.behind+1)...)
			held := wantBusySendCustody(t, in.path, in.id, custody...)
			want := []store.BusySendPrompt{
				{Index: 0, MessageIDs: []string{held[0].id, held[1].id}, Body: []byte(busySendBody(texts[0], texts[1])),
					RenderedBytes: tc.size(texts[0], texts[1]), Emission: store.BusySendEmissionNone},
				{Index: 1, MessageIDs: []string{held[2].id}, Body: []byte(busySendBody(texts[2])), RenderedBytes: tc.size(texts[2]),
					Emission: store.BusySendEmissionNone},
			}
			brief := func(ps []store.BusySendPrompt) (out []string) {
				for _, p := range ps {
					out = append(out, fmt.Sprintf("%d %v %d bytes %s %q", p.Index, p.MessageIDs, p.RenderedBytes, p.Emission, p.Body[:min(len(p.Body), 120)]))
				}
				return out
			}
			if h.CutoffSeq != 3 || h.HandoverSeq != 1 || h.Boundary != req.Boundary || h.Destination != in.dest || !h.Open ||
				h.PromptCursor != 0 || !reflect.DeepEqual(h.Prompts, want) {
				t.Fatalf("hand-over %d at %s cutoff %d at %+v, open %t, cursor %d, prompts %q; want 1 at %s, cutoff 3, open at cursor 0 with %q",
					h.HandoverSeq, h.Boundary, h.CutoffSeq, h.Destination, h.Open, h.PromptCursor, brief(h.Prompts), req.Boundary, brief(want))
			}

			before := readBusySendTables(t, in.path)
			again, err := in.s.ClaimBusySendHandover(context.Background(), tc.request(in))
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
			if tc.behind == 0 {
				return
			}

			lead := newBusySendInbox(t, "a Wait Turn Message first")
			lead.admit(t, store.BusySendSteerTurn, "then a Steer Turn Message")
			before = readBusySendTables(t, lead.path)
			if c, err := lead.s.ClaimBusySendHandover(context.Background(), lead.steerRequest(time.Time{})); err != nil ||
				c.Outcome != store.BusySendClaimEmpty || c.Handover != nil {
				t.Errorf("tool-boundary claim behind a Wait Turn Message %+v, %v; want empty", c, err)
			}
			if !reflect.DeepEqual(before, readBusySendTables(t, lead.path)) {
				t.Error("an empty tool-boundary claim changed the inbox")
			}
		})
	}
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
