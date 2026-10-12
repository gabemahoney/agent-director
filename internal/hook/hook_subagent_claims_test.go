package hook_test

// Claimable launches through hook.Handle (b.xjn; SRD SR-15.3, SR-4.3, SR-4.5,
// SR-7.3, SR-7.5, SR-12.2, N10): a claim only confirms an entry and fills no
// field, whichever launch it takes, and a launch is closed by its claim, its
// result, its failure or a main-agent Stop past the grace. The wiring is
// shown here; the store matrix, grace edges included, is internal/store's.

import (
	"slices"
	"testing"
)

// TestSubagentClaimFillsNoField: launches claimed by starts of another or the
// same launch leave every field but the start's type to the launch results,
// which are applied last; the result is the same whichever start comes first.
func TestSubagentClaimFillsNoField(t *testing.T) {
	t.Run("a launch that opens no claimable launch beside an unnamed one, starts in either order", func(t *testing.T) {
		for name, first := range map[string]func(*testing.T) []byte{
			"named":    namedRequest,
			"typeless": typelessRequest,
		} {
			for _, tc := range []struct {
				name     string
				aAt, bAt int64 // B1's subagent (the first request's) and the unnamed launch's start times
			}{
				{name: "B1 first", aAt: at(3), bAt: at(4)},
				{name: "unnamed launch's first", aAt: at(4), bAt: at(3)},
			} {
				t.Run(name+"/"+tc.name, func(t *testing.T) {
					r := newSubRow(t, subRowID)
					aClaims := tc.aAt < tc.bAt
					a := running(subAgentID, subType, aClaims, tc.aAt)
					b := running(subSecondAgentID, subType, !aClaims, tc.bAt)
					fa := filled(r, subAgentID, subType, subLaunchDesc, subModel, subToolUseID, tc.aAt)
					fb := filled(r, subSecondAgentID, subType, subSecondDesc, subSecondModel, subSecondToolUseID, tc.bAt)
					st1 := step{name: "first start claims the unnamed launch", payload: variant(t, fxStart), at: tc.aAt, entries: []wantEntry{a}}
					st2 := step{name: "second start finds nothing to claim", payload: secondStart(t), at: tc.bAt, entries: []wantEntry{a, b}}
					claimed, hidden := subAgentID, subSecondAgentID
					if !aClaims {
						st1, st2 = st2, st1
						st1.name, st2.name = "first start claims the unnamed launch", "second start finds nothing to claim"
						st1.entries, st2.entries = []wantEntry{b}, []wantEntry{a, b}
						claimed, hidden = subSecondAgentID, subAgentID
					}
					r.script(t, []step{
						{name: "first request", payload: first(t), at: at(1)},
						{name: "unnamed request of the same type", payload: secondRequest(t), at: at(2),
							launches: []wantLaunch{{subSecondToolUseID, subType, at(2)}}},
						st1,
						st2,
					})
					if got := records(r); !slices.Equal(got, []string{claimed}) {
						t.Errorf("records = %v; want only the claimed %q, with %q hidden until its own result", got, claimed, hidden)
					}
					r.script(t, []step{
						{name: "B5 fills B1's subagent only", payload: variant(t, fxResult), at: at(5),
							entries: []wantEntry{fa, b}},
						{name: "second result fills the other", payload: secondResult(t), at: at(6),
							entries: []wantEntry{fa, fb}},
					})
					if got := records(r); len(got) != 2 {
						t.Errorf("records = %v; want both once each result has applied", got)
					}
				})
			}
		}
	})

	t.Run("two launches of one type, starts in either order", func(t *testing.T) {
		for _, tc := range []struct {
			name     string
			aAt, bAt int64 // B1's subagent and the second's start times
		}{
			{name: "B1 first", aAt: at(3), bAt: at(4)},
			{name: "second first", aAt: at(4), bAt: at(3)},
		} {
			t.Run(tc.name, func(t *testing.T) {
				r := newSubRow(t, subRowID)
				a := running(subAgentID, subType, true, tc.aAt)
				b := running(subSecondAgentID, subType, true, tc.bAt)
				fa := filled(r, subAgentID, subType, subLaunchDesc, subModel, subToolUseID, tc.aAt)
				fb := filled(r, subSecondAgentID, subType, subSecondDesc, subSecondModel, subSecondToolUseID, tc.bAt)
				both := []wantLaunch{{subToolUseID, subType, at(1)}, {subSecondToolUseID, subType, at(2)}}
				one := []wantLaunch{{subSecondToolUseID, subType, at(2)}}
				first, second := step{name: "first start", payload: variant(t, fxStart), at: tc.aAt}, step{name: "second start", payload: secondStart(t), at: tc.bAt}
				firstE, secondE := []wantEntry{a}, []wantEntry{a, b}
				resultsE := []wantEntry{fa, b}
				if tc.bAt < tc.aAt {
					first, second = second, first
					firstE, secondE = []wantEntry{b}, []wantEntry{b, a}
					resultsE = []wantEntry{b, fa}
				}
				first.name, second.name = "first start claims the oldest launch", "second start claims the other"
				first.entries, first.launches = firstE, one
				second.entries = secondE
				r.script(t, []step{
					{name: "B9", payload: variant(t, fxRequest), at: at(1), launches: both[:1]},
					{name: "second request", payload: secondRequest(t), at: at(2), launches: both},
					first, second,
					{name: "B5 fills B1's subagent only", payload: variant(t, fxResult), at: at(5), entries: resultsE},
					{name: "second result fills the other", payload: secondResult(t), at: at(6),
						entries: orderedBy(tc.aAt < tc.bAt, fa, fb)},
				})
			})
		}
	})
}

// orderedBy returns a, b when inOrder, else b, a (start-time order).
func orderedBy(inOrder bool, a, b wantEntry) []wantEntry {
	if inOrder {
		return []wantEntry{a, b}
	}
	return []wantEntry{b, a}
}

// TestSubagentClaimStaleRequest: a request whose call never produced a subagent
// (denied or blocked) is the oldest of its type, so a later launch's start
// claims it; the later launch's own result then closes that launch's request,
// and the record carries the result's tool_use_id, not the claimed one's.
func TestSubagentClaimStaleRequest(t *testing.T) {
	r := newSubRow(t, subRowID)
	later := variant(t, fxResult, setKey("tool_use_id", subSecondToolUseID))
	want := filled(r, subAgentID, subType, subLaunchDesc, subModel, subSecondToolUseID, at(3))
	r.script(t, []step{
		{name: "stale request", payload: variant(t, fxRequest), at: at(1),
			launches: []wantLaunch{{subToolUseID, subType, at(1)}}},
		{name: "later request", payload: secondRequest(t), at: at(2),
			launches: []wantLaunch{{subToolUseID, subType, at(1)}, {subSecondToolUseID, subType, at(2)}}},
		{name: "its start claims the stale one", payload: variant(t, fxStart), at: at(3),
			entries:  []wantEntry{running(subAgentID, subType, true, at(3))},
			launches: []wantLaunch{{subSecondToolUseID, subType, at(2)}}},
		{name: "its result closes its own request", payload: later, at: at(4),
			entries: []wantEntry{want}},
	})
}

// TestSubagentClaimTeammate: a launch request of the teammate's type without a
// name (N10) is claimed by the teammate's first start, which makes one record
// with no claim fields; its per-message starts and stops stay one record.
func TestSubagentClaimTeammate(t *testing.T) {
	r := newSubRow(t, subRowID)
	first := running(subTeammateID, subTeammateType, true, at(2))
	restarted := first
	restarted.started, restarted.opened = at(5), at(5)
	restarted.stopped = at(4)
	r.script(t, []step{
		{name: "request of the teammate's type", payload: teammateRequest(t), at: at(1),
			launches: []wantLaunch{{subToolUseID, subTeammateType, at(1)}}},
		{name: "B2 claims it", payload: variant(t, fxStartTeammate), at: at(2),
			entries: []wantEntry{first}},
		{name: "a later per-message start", payload: variant(t, fxStartTeammate), at: at(3),
			entries: []wantEntry{first}},
		{name: "its stop", payload: teammateStop(t), at: at(4),
			entries: []wantEntry{first.stoppedAt(at(4))}},
		{name: "the next message's start", payload: variant(t, fxStartTeammate), at: at(5),
			entries: []wantEntry{restarted}},
	})
}

// TestSubagentClaimClosedByStopPastGrace: a main-agent Stop closes a claimable
// launch only once its receipt time is past the grace after the launch's; the
// edges of the grace are the store's. The Stop takes its receipt time from
// HandleConfig.Start.
func TestSubagentClaimClosedByStopPastGrace(t *testing.T) {
	r := newSubRow(t, subRowID)
	leftover := wantLaunch{subToolUseID, subType, at(1)}
	r.script(t, []step{
		{name: "request", payload: variant(t, fxRequest), at: at(1), launches: []wantLaunch{leftover}},
		{name: "Stop within the grace", payload: mainStop(t), at: at(1) + 1000, launches: []wantLaunch{leftover}},
		{name: "Stop past the grace", payload: mainStop(t), at: at(1) + 5000},
	})
}

// TestSubagentClaimClosedByOtherLaunchStatus (SR-15.1): a launch result whose
// tool_response.status is neither async_launched nor completed closes its
// request's claimable launch and creates no record, so the start that follows
// claims nothing and is an unconfirmed observation (no record in the verb's
// list).
func TestSubagentClaimClosedByOtherLaunchStatus(t *testing.T) {
	r := newSubRow(t, subRowID)
	r.script(t, []step{
		{name: "B9", payload: variant(t, fxRequest), at: at(1),
			launches: []wantLaunch{{subToolUseID, subType, at(1)}}},
		{name: "B5 with status error", payload: variant(t, fxResult, setKey("tool_response.status", "error")), at: at(2)},
		{name: "B1 claims nothing", payload: variant(t, fxStart), at: at(3),
			entries: []wantEntry{running(subAgentID, subType, false, at(3))}},
	})
	if got := records(r); len(got) != 0 {
		t.Errorf("records = %v; want none", got)
	}
}

// TestSubagentClaimClosedByFailure: a PostToolUseFailure(Agent) with the
// request's tool_use_id closes its claimable launch, so a start claims
// nothing: it is an observation until its result confirms it.
func TestSubagentClaimClosedByFailure(t *testing.T) {
	r := newSubRow(t, subRowID)
	r.script(t, []step{
		{name: "request", payload: variant(t, fxRequest), at: at(1),
			launches: []wantLaunch{{subToolUseID, subType, at(1)}}},
		{name: "failure of the call", payload: agentFailure(t, subToolUseID), at: at(2)},
		{name: "a start claims nothing", payload: variant(t, fxStart), at: at(3),
			entries: []wantEntry{running(subAgentID, subType, false, at(3))}},
		{name: "B5 confirms it", payload: variant(t, fxResult), at: at(4),
			entries: []wantEntry{filled(r, subAgentID, subType, subLaunchDesc, subModel, subToolUseID, at(3))}},
	})
}
