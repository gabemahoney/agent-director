package store_test

// Lifecycle custody's store wiring (Task t2.xis.oo.sf, t3.xis.oo.sf.2b;
// SR-6.1 to SR-6.3, SR-5.5; M1 G4's Resume, ReusePublish, Delete and Retire,
// M2 NothingTakenAfterExit, R6 and R8): resume, reuse publication, covered
// deletion and expire against the shared exit-evidence operation, on real
// SQLite. EndLaunch's disposition per custody state is TestBusySendCustodyMatrix's.
// Exit evidence here is a scope as read, which the test judges gone: the store
// never probes a process, so nothing here qualifies a process or provider.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

const busySendIntentEnded = "issued sent_unconfirmed final launch_ended member"

// endedBusySendInbox is newBusySendInbox's agent at its Launch End by its own
// end report, with mixed custody: the first Message issued by busySendHelper,
// the second claimed in the same hand-over, a third admitted meanwhile.
func endedBusySendInbox(t *testing.T) (*busySendInbox, *store.BusySendHandover) {
	t.Helper()
	in := newBusySendInbox(t, busySendTwoPrompts...)
	h := in.claim(t, time.Time{}, false)
	in.issue(t, h, 0, h.Revision)
	if _, err := in.s.AdmitBusySend(store.BusySendAdmissionRequest{InstanceID: in.id, Text: []byte("third")}); err != nil {
		t.Fatal(err)
	}
	if a := storefix.ApplyAgentHook(t, in.s, in.id, "SessionEnd", in.dest.SessionID); !a.Applied {
		t.Fatalf("SessionEnd %+v; want applied", a)
	}
	wantBusySendCustody(t, in.path, in.id, busySendIntentEnded, "held held launch_ended reserved", "held held launch_ended reserved")
	return in, h
}

// readBusySendAccepted reads id's Messages' accepted facts, which no custody
// change alters: id, conversation, order, time, resolved options and bytes.
func readBusySendAccepted(t *testing.T, path, id string) [][]any {
	t.Helper()
	rows, err := openBusySendRaw(t, path).Query(`SELECT message_id, conversation_id, accepted_seq, accepted_at_ms, send_mode,
	        confirmed, text, text_sha256 FROM busy_send_messages WHERE instance_id = ? ORDER BY accepted_seq`, id)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out [][]any
	for rows.Next() {
		row := make([]any, 8)
		ptrs := make([]any, len(row))
		for i := range row {
			ptrs[i] = &row[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func (in *busySendInbox) row(t *testing.T) store.Spawn {
	t.Helper()
	row, err := in.s.GetSpawn(in.id)
	if err != nil {
		t.Fatal(err)
	}
	return row
}

// reuse resets in's finished row for a reuse with token and exit evidence ev.
func (in *busySendInbox) reuse(t *testing.T, token string, ev *store.BusySendExitEvidence) (store.CondResult, int64, error) {
	t.Helper()
	prior, _, err := in.s.ReadForReuse(in.id)
	if err != nil {
		t.Fatal(err)
	}
	res, _, version, err := in.s.ResetForReuse(in.id, prior.Snapshot, prior.Agent, store.Spawn{Agent: store.AgentClaude, CWD: t.TempDir(),
		TmuxSessionName: in.row(t).TmuxSessionName, RelayMode: "off", StartedAt: time.Unix(2, 0), LaunchStartedAtMillis: 2000,
		Identity: store.LaunchIdentity{Token: token}, BusySendExit: ev})
	return res, version, err
}

// TestBusySendExitScopeRevalidation (SR-6.2; M1 Exit, M2 R6 and R8; Main
// Agent decision 15): exit evidence covers every process the Launch owns, the
// row's agent identity and every registered issuer, with whether intent ever
// landed. A lifecycle write applies it only while the scope it reads again
// under its write lock is unchanged: evidence that omits an issuer, or that an
// issuer registration, a helper-slot replacement, intent persisted with no new
// issuer or a new agent identity made stale, is refused and writes nothing; a
// row changed since the judgement is the deletion's own not-applied outcome.
// The same write with the scope read again applies.
func TestBusySendExitScopeRevalidation(t *testing.T) {
	t.Parallel()
	in := newBusySendInbox(t, busySendTwoPrompts...)
	h := in.claim(t, time.Time{}, false)
	in.issue(t, h, 0, h.Revision)
	scope, err := in.s.ReadBusySendExitScope(in.id)
	if err != nil {
		t.Fatal(err)
	}
	agent := store.BusySendLaunchProcess{Role: store.BusySendProcessAgent, PID: in.row(t).Identity.PanePID}
	helper := store.BusySendLaunchProcess{Role: busySendHelper.Role, PID: busySendHelper.PID, StartTime: busySendHelper.StartTime}
	want := store.BusySendExitScope{InstanceID: in.id, ConversationID: in.row(t).BusyConversationID, Coverage: store.BusySendCovered,
		Launches: []store.BusySendExitLaunch{{LaunchID: in.dest.LaunchID, Current: true, Processes: []store.BusySendLaunchProcess{agent, helper},
			Routes: []store.BusySendRouteKind{store.BusySendRouteClaudeHooks}, Issued: true}}}
	if !reflect.DeepEqual(scope, want) {
		t.Fatalf("exit scope %+v; want %+v", scope, want)
	}

	issueAs := func(k int, issuer *store.BusySendIssuer) func(*testing.T, *busySendInbox, *store.BusySendHandover) {
		return func(t *testing.T, in *busySendInbox, h *store.BusySendHandover) {
			now, err := in.s.ReadBusySendHandover(context.Background(), h.HandoverID)
			if err != nil {
				t.Fatal(err)
			}
			is, err := in.s.CommitBusySendIssue(context.Background(), store.BusySendIssueRequest{Issuer: issuer,
				Ref: store.BusySendPromptRef{HandoverID: h.HandoverID, PromptIndex: k, Revision: now.Revision}})
			if err != nil {
				t.Fatalf("issue prompt %d: %v", k, err)
			}
			if k == 0 {
				// Order evidence resolves prompt 0, so prompt 1 may issue.
				in.complete(t, h, 0, is.Revision, store.BusySendOutputComplete, store.BusySendOrderEmissionComplete, true)
			}
		}
	}
	other := store.BusySendIssuer{Role: store.BusySendProcessHelper, PID: 4343, StartTime: "200"}
	type write func(in *busySendInbox, examined store.RowSnapshot, ev store.BusySendExitEvidence) string
	deleteCovered := func(in *busySendInbox, examined store.RowSnapshot, ev store.BusySendExitEvidence) string {
		res, err := in.s.DeleteBusySendCovered(in.id, examined, ev)
		switch {
		case errors.Is(err, store.ErrBusySendExitNotEstablished):
			return "refused"
		case err != nil:
			return err.Error()
		}
		return map[store.CondResult]string{store.CondApplied: "applied", store.CondChanged: "changed", store.CondAbsent: "absent"}[res]
	}
	establish := func(in *busySendInbox, _ store.RowSnapshot, ev store.BusySendExitEvidence) string {
		applied, err := in.s.EstablishBusySendExit(ev)
		if err != nil {
			return err.Error()
		}
		return map[bool]string{false: "refused", true: "applied"}[applied]
	}
	for _, tc := range []struct {
		name   string
		judged func(*testing.T, *busySendInbox, *store.BusySendHandover) // before the judgement
		after  func(*testing.T, *busySendInbox, *store.BusySendHandover) // between the judgement and the write
		omit   bool                                                      // the evidence leaves out the issuer
		write  write
		want   string
	}{
		{name: "an issuer registers", judged: issueAs(0, nil), after: issueAs(1, &busySendHelper), write: deleteCovered, want: "refused"},
		{name: "the helper slot is replaced", judged: issueAs(0, &busySendHelper), after: issueAs(1, &other), write: establish, want: "refused"},
		{name: "intent persists with no new issuer", after: issueAs(0, nil), write: deleteCovered, want: "refused"},
		{name: "the agent's SessionStart identity is recorded", after: func(t *testing.T, in *busySendInbox, _ *store.BusySendHandover) {
			if a := storefix.ApplyAgentHook(t, in.s, in.id, "SessionStart", in.dest.SessionID); !a.Applied {
				t.Fatalf("SessionStart %+v; want applied", a)
			}
		}, write: establish, want: "refused"},
		{name: "the evidence omits the issuer", judged: issueAs(0, &busySendHelper), omit: true, write: deleteCovered, want: "refused"},
		{name: "the row changed", judged: func(t *testing.T, in *busySendInbox, _ *store.BusySendHandover) {
			// Its identity recorded, so the turn's report changes only the row.
			if a := storefix.ApplyAgentHook(t, in.s, in.id, "SessionStart", in.dest.SessionID); !a.Applied {
				t.Fatalf("SessionStart %+v; want applied", a)
			}
		}, after: func(t *testing.T, in *busySendInbox, _ *store.BusySendHandover) {
			if a := storefix.ApplyAgentHook(t, in.s, in.id, "UserPromptSubmit", in.dest.SessionID); !a.Applied {
				t.Fatalf("UserPromptSubmit %+v; want applied", a)
			}
		}, write: deleteCovered, want: "changed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			in := newBusySendInbox(t, busySendTwoPrompts...)
			h := in.claim(t, time.Time{}, false)
			if tc.judged != nil {
				tc.judged(t, in, h)
			}
			examined, ev := in.row(t).Snapshot, *exitEvidenceForTest(t, in.s, in.id)
			if tc.omit {
				ev.Scope.Launches[0].Processes = slices.DeleteFunc(slices.Clone(ev.Scope.Launches[0].Processes),
					func(p store.BusySendLaunchProcess) bool { return p.Role != store.BusySendProcessAgent })
			}
			if tc.after != nil {
				tc.after(t, in, h)
			}
			before := readBusySendTables(t, in.path)
			if got := tc.write(in, examined, ev); got != tc.want {
				t.Fatalf("write with the judged evidence: %s; want %s", got, tc.want)
			}
			if !reflect.DeepEqual(before, readBusySendTables(t, in.path)) {
				t.Fatal("a write with stale evidence wrote part of itself")
			}
			if got := tc.write(in, in.row(t).Snapshot, *exitEvidenceForTest(t, in.s, in.id)); got != "applied" {
				t.Fatalf("write with the scope read again: %s; want applied", got)
			}
		})
	}
}

// TestBusySendLaunchEndIsNotExit (SR-6.2, SR-5.5; M1 G4): a Launch ended by
// the agent's own end report, with a fenced intent and Held custody, is not
// exited. Without exit evidence resume, reuse and plain deletion refuse, and
// expire keeps the row, each writing nothing; a resume whose row changed
// since it was examined is the move's own CondChanged, not an exit refusal.
func TestBusySendLaunchEndIsNotExit(t *testing.T) {
	t.Parallel()
	in, _ := endedBusySendInbox(t)
	row := in.row(t)
	before := readBusySendTables(t, in.path)
	for _, tc := range []struct {
		name string
		call func() error
		want error
	}{
		{"resume", func() error {
			_, _, err := in.s.MoveToPendingWithExit(in.id, row.Snapshot, 2000, "1122334455667788", "", "", store.LaunchOwner{},
				store.BusySendDefaultsChoice{}, nil)
			return err
		}, store.ErrBusySendExitNotEstablished},
		{"reuse", func() error { _, _, err := in.reuse(t, "1122334455667788", nil); return err }, store.ErrBusySendExitNotEstablished},
		{"delete", func() error { return in.s.DeleteSpawn(in.id) }, store.ErrBusySendExitNotEstablished},
		{"expire", func() error { _, err := in.s.DeleteFinishedIfSameLife(in.id, row.Snapshot); return err }, store.ErrBusySendCustodyHeld},
	} {
		if err := tc.call(); !errors.Is(err, tc.want) {
			t.Errorf("%s without exit evidence: %v; want %v", tc.name, err, tc.want)
		}
	}
	if !reflect.DeepEqual(before, readBusySendTables(t, in.path)) {
		t.Fatal("a lifecycle write without exit evidence wrote part of itself")
	}
	setBusySendFact(t, in.path, `UPDATE spawns SET row_version = row_version + 1 WHERE instance_id = ?`, in.id)
	if res, _, err := in.s.MoveToPendingWithExit(in.id, row.Snapshot, 2000, "1122334455667788", "", "", store.LaunchOwner{},
		store.BusySendDefaultsChoice{}, nil); err != nil || res != store.CondChanged {
		t.Errorf("resume of a changed row %v, %v; want CondChanged", res, err)
	}
}

// TestBusySendResumeCarriesCustody (SR-6.1, SR-6.2; M1 Resume, Custody in the
// current Launch, old callbacks inert): a covered resume with exit evidence
// lifts the old Launch's fence, leaving its intent Final sent_unconfirmed and
// never carried, and carries the Held Messages, claimed ones returned
// included, to the new Launch: same conversation, accepted order, bytes,
// digests and options, the first waiting agent_starting across a reopen and
// the publication. The new conversation scope keeps the old Launch with its
// issuer, so a later retirement judges it again. The old hand-over's job, an
// event for the old destination and a kill judged for the old Launch change
// nothing; the job's late completion records at most its own prompt's output
// fact, never the successor or the Final Lookup.
func TestBusySendResumeCarriesCustody(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	in, h := endedBusySendInbox(t)
	accepted := readBusySendAccepted(t, in.path, in.id)
	const token = "1122334455667788"
	res, version, err := in.s.MoveToPendingWithExit(in.id, in.row(t).Snapshot, 2000, token, "", "", store.LaunchOwner{},
		store.BusySendDefaultsChoice{}, exitEvidenceForTest(t, in.s, in.id))
	if err != nil || res != store.CondApplied {
		t.Fatalf("resume %v, %v; want applied", res, err)
	}
	carried := []string{"closed sent_unconfirmed final launch_ended", "held held agent_starting reserved", "held held earlier_message reserved"}
	wantBusySendCustody(t, in.path, in.id, carried...)
	in.reopen(t)
	if res, err := in.s.RecordLaunchIdentity(in.id, version, token, store.LaunchIdentity{}); err != nil || res != store.CondApplied {
		t.Fatalf("publication %v, %v; want applied", res, err)
	}
	wantBusySendCustody(t, in.path, in.id, carried...)
	if got := readBusySendAccepted(t, in.path, in.id); !reflect.DeepEqual(got, accepted) {
		t.Errorf("accepted facts after the resume %v; want them as accepted %v", got, accepted)
	}
	pub, err := in.s.GetBusySendPublishedLaunch(in.id)
	if err != nil || pub.ConversationID != in.row(t).BusyConversationID || pub.LaunchID == in.dest.LaunchID || pub.LaunchToken != token {
		t.Fatalf("published %+v, %v; want the same conversation's new Launch", pub, err)
	}
	scope, err := in.s.ReadBusySendExitScope(in.id)
	if err != nil || len(scope.Launches) != 2 {
		t.Fatalf("exit scope %+v, %v; want the old and the new Launch", scope, err)
	}
	launches := map[string]store.BusySendExitLaunch{}
	for _, l := range scope.Launches {
		launches[l.LaunchID] = l
	}
	old, cur := launches[in.dest.LaunchID], launches[pub.LaunchID]
	if old.Current || !old.Ended || !old.Issued ||
		!slices.Contains(old.Processes, store.BusySendLaunchProcess{Role: busySendHelper.Role, PID: busySendHelper.PID, StartTime: busySendHelper.StartTime}) ||
		!cur.Current || cur.Ended || cur.Issued {
		t.Errorf("exit scope %+v; want the old Launch ended with its issuer and the current one", scope.Launches)
	}

	before := readBusySendTables(t, in.path)
	var guard *store.BusySendGuardError
	if _, err := in.commit(ctx, h, 1, h.Revision); !errors.As(err, &guard) {
		t.Errorf("the old job's next prompt: %v; want a guard refusal", err)
	}
	if res, err := in.s.RecordBusySendEvidence(ctx, store.BusySendPromptEvidence{HandoverID: h.HandoverID, LaunchID: in.dest.LaunchID,
		Order: store.BusySendOrderOrderedAcceptance}); err != nil || res.Recorded {
		t.Errorf("old evidence %+v, %v; want nothing recorded", res, err)
	}
	if c, err := in.s.ClaimBusySendHandover(ctx, in.claimRequest(time.Time{}, false)); err != nil || c.Outcome != store.BusySendClaimIneligible {
		t.Errorf("an event for the old destination %+v, %v; want ineligible", c, err)
	}
	if ended, err := in.s.EndBusySendLaunch(in.id, in.dest.LaunchToken); err != nil || ended {
		t.Errorf("a kill judged for the old Launch ended %t, %v; want nothing", ended, err)
	}
	if !reflect.DeepEqual(before, readBusySendTables(t, in.path)) {
		t.Fatal("the old Launch's callbacks changed the successor")
	}
	// The old job's late completion may record its own prompt's output fact,
	// as history after the Final time; nothing else changes.
	found, err := in.s.ReadBusySendResult(in.pair.CallerUUID, in.pair.DeliveryRequestID)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = in.s.RecordBusySendCompletion(ctx, store.BusySendCompletion{Ref: store.BusySendPromptRef{HandoverID: h.HandoverID,
		Revision: h.Revision}, Output: store.BusySendOutputComplete, Order: store.BusySendOrderEmissionComplete})
	after := readBusySendTables(t, in.path)
	for table, rows := range before {
		if table != "busy_send_prompts" && table != "busy_send_handovers" && !reflect.DeepEqual(rows, after[table]) {
			t.Errorf("the old job's completion changed %s", table)
		}
	}
	if again, err := in.s.ReadBusySendResult(in.pair.CallerUUID, in.pair.DeliveryRequestID); err != nil || !reflect.DeepEqual(again, found) {
		t.Errorf("the old job's completion changed the Final Lookup from %+v to %+v, %v", found, again, err)
	}
}

// TestBusySendRetirementFinalizesNeverEmitted (SR-6.3; M1 Retire,
// NotStartedNeverEmitted, NotStartedAfterExit): reuse publication and covered
// deletion, each with exit evidence, end the conversation's Held Messages
// Final not_started (conversation_replaced, or record_deleted), a Message
// admitted while the reuse was pending included, with their reservations
// freed, and keep the issued Message's Final sent_unconfirmed and its Lookup.
// A store failure at the retirement rolls back the publication or deletion
// with it.
func TestBusySendRetirementFinalizesNeverEmitted(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		reuse bool
		fail  bool
	}{
		{"reuse publication", true, false},
		{"covered delete", false, false},
		{"reuse publication, retirement fails", true, true},
		{"covered delete, retirement fails", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			in, _ := endedBusySendInbox(t)
			lookup := func() store.BusySendResult {
				found, err := in.s.ReadBusySendResult(in.pair.CallerUUID, in.pair.DeliveryRequestID)
				if err != nil || found.Result == nil {
					t.Fatalf("Lookup %+v, %v", found, err)
				}
				return *found.Result
			}
			issued := lookup()
			const token = "1122334455667788"
			var version int64
			reason, held := "record_deleted", 2
			if tc.reuse {
				res, v, err := in.reuse(t, token, exitEvidenceForTest(t, in.s, in.id))
				if err != nil || res != store.CondApplied {
					t.Fatalf("reuse %v, %v; want applied", res, err)
				}
				if _, err := in.s.AdmitBusySend(store.BusySendAdmissionRequest{InstanceID: in.id, Text: []byte("fourth")}); err != nil {
					t.Fatal(err)
				}
				version, reason, held = v, "conversation_replaced", 3
			}
			retire := func() error {
				if tc.reuse {
					res, err := in.s.RecordLaunchIdentity(in.id, version, token, store.LaunchIdentity{})
					if err == nil && res != store.CondApplied {
						t.Fatalf("publication %v; want applied", res)
					}
					return err
				}
				res, err := in.s.DeleteBusySendCovered(in.id, in.row(t).Snapshot, *exitEvidenceForTest(t, in.s, in.id))
				if err == nil && res != store.CondApplied {
					t.Fatalf("delete %v; want applied", res)
				}
				return err
			}
			if tc.fail {
				if _, err := openBusySendRaw(t, in.path).Exec(`CREATE TRIGGER fail_retirement BEFORE UPDATE ON busy_send_messages
				    WHEN NEW.result_class = 'not_started' BEGIN SELECT RAISE(ABORT, 'injected retirement failure'); END`); err != nil {
					t.Fatal(err)
				}
				before := readBusySendTables(t, in.path)
				if err := retire(); err == nil || !strings.Contains(err.Error(), "injected retirement failure") {
					t.Fatalf("retirement %v; want the injected failure", err)
				}
				if !reflect.DeepEqual(before, readBusySendTables(t, in.path)) {
					t.Fatal("a failed retirement left part of its publication or deletion")
				}
				return
			}
			if err := retire(); err != nil {
				t.Fatal(err)
			}
			want := []string{"closed sent_unconfirmed final launch_ended"}
			for range held {
				want = append(want, "closed not_started final "+reason)
			}
			wantBusySendCustody(t, in.path, in.id, want...)
			if got := lookup(); !reflect.DeepEqual(got, issued) {
				t.Errorf("the issued Message's Lookup %+v; want it unchanged %+v", got, issued)
			}
		})
	}
}

// TestBusySendRestorationKeepsAdmissions (SR-6.3): a failed creation's
// conditional restoration, after a covered resume or reuse staged with exit
// evidence, keeps the Messages admitted while the row was pending Held in the
// published conversation, in accepted order, waiting launch_ended.
func TestBusySendRestorationKeepsAdmissions(t *testing.T) {
	t.Parallel()
	for _, reuse := range []bool{false, true} {
		t.Run(map[bool]string{false: "resume", true: "reuse"}[reuse], func(t *testing.T) {
			t.Parallel()
			in := newBusySendInbox(t, "first")
			if a := storefix.ApplyAgentHook(t, in.s, in.id, "SessionEnd", in.dest.SessionID); !a.Applied {
				t.Fatalf("SessionEnd %+v; want applied", a)
			}
			old := in.row(t)
			const token = "1122334455667788"
			var res store.CondResult
			var version int64
			var err error
			if reuse {
				var prior store.ReuseRow
				if prior, _, err = in.s.ReadForReuse(in.id); err != nil {
					t.Fatal(err)
				}
				res, version, err = in.reuse(t, token, exitEvidenceForTest(t, in.s, in.id))
				if err == nil && res == store.CondApplied {
					admitAndWait(t, in)
					res, err = in.s.RestoreAfterFailedReuse(in.id, version, prior.Life, time.Unix(3, 0))
				}
			} else {
				res, version, err = in.s.MoveToPendingWithExit(in.id, old.Snapshot, 2000, token, "", "", store.LaunchOwner{},
					store.BusySendDefaultsChoice{}, exitEvidenceForTest(t, in.s, in.id))
				if err == nil && res == store.CondApplied {
					admitAndWait(t, in)
					res, err = in.s.RestoreAfterFailedResume(in.id, version, store.ResumePrior{State: old.State, Identity: old.Identity,
						TurnStarts: old.TurnStarts})
				}
			}
			if err != nil || res != store.CondApplied {
				t.Fatalf("stage and restore %v, %v; want applied", res, err)
			}
			if got := in.row(t).BusyConversationID; got != old.BusyConversationID {
				t.Fatalf("published conversation %s after the restore; want %s", got, old.BusyConversationID)
			}
			var got []string
			for _, c := range readBusySendCustody(t, in.path, in.id) {
				got = append(got, c.String())
			}
			if want := []string{"held held launch_ended reserved", "held held earlier_message reserved"}; !reflect.DeepEqual(got, want) {
				t.Errorf("custody after the restore %q; want %q", got, want)
			}
		})
	}
}

// admitAndWait admits a second Message while in's row is pending and requires
// both Held waiting agent_starting.
func admitAndWait(t *testing.T, in *busySendInbox) {
	t.Helper()
	if _, err := in.s.AdmitBusySend(store.BusySendAdmissionRequest{InstanceID: in.id, Text: []byte("second")}); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, c := range readBusySendCustody(t, in.path, in.id) {
		got = append(got, c.String())
	}
	if want := []string{"held held agent_starting reserved", "held held agent_starting reserved"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("custody while pending %q; want %q", got, want)
	}
}

// TestBusySendExpireKeepsRacingAdmission (SR-5.5): expire's guarded delete
// rechecks custody in its own transaction, so a Message admitted after its
// selection read a finished row with none keeps the row (ErrBusySendCustodyHeld)
// and stays Held.
func TestBusySendExpireKeepsRacingAdmission(t *testing.T) {
	t.Parallel()
	in := newBusySendInbox(t)
	if a := storefix.ApplyAgentHook(t, in.s, in.id, "SessionEnd", in.dest.SessionID); !a.Applied {
		t.Fatalf("SessionEnd %+v; want applied", a)
	}
	selected := in.row(t).Snapshot
	if _, err := in.s.AdmitBusySend(store.BusySendAdmissionRequest{InstanceID: in.id, Text: []byte("late")}); err != nil {
		t.Fatal(err)
	}
	before := readBusySendTables(t, in.path)
	if res, err := in.s.DeleteFinishedIfSameLife(in.id, selected); !errors.Is(err, store.ErrBusySendCustodyHeld) {
		t.Fatalf("expire %v, %v; want the row kept for its Held Message", res, err)
	}
	if !reflect.DeepEqual(before, readBusySendTables(t, in.path)) {
		t.Fatal("expire deleted part of a row with Held custody")
	}
	wantBusySendCustody(t, in.path, in.id, "held held launch_ended reserved")
}

// failPauseClaimEnd makes every end of a pause claim in path fail.
const failPauseClaimEnd = `CREATE TRIGGER fail_claim_end BEFORE UPDATE ON busy_send_pause_claims WHEN NEW.state = 'ended'
    BEGIN SELECT RAISE(ABORT, 'injected claim end failure'); END`

// applied is a CondResult step's error, an unexpected result included.
func applied(res store.CondResult, err error) error {
	if err == nil && res != store.CondApplied {
		return fmt.Errorf("result %v; want applied", res)
	}
	return err
}

// TestBusySendPauseClaimLifetime (SR-6.5; M2 P-C4.7 EndStep and
// EndClaimOnEvidence; Task t2.xis.8a.a4's caller inventory): a standing claim
// ends launch_end only in the transaction of positive evidence that its
// Launch's recorded agent exited, its End recorded then or earlier: kill's
// and keyless pause's verified exit, a guarded mark or move storing the
// agent's exited record (Store's terminal transaction and RecoveryWrites';
// their compatibility aliases share these), an exited record accepted after
// the Launch's End, and validated exit evidence (EstablishBusySendExit, resume
// and reuse staging, whose restore keeps the end, and covered deletion). The
// agent's end report and other logical Ends (an End process_exited with no
// evidence), marks without the agent's exit, an exited record before any End,
// an unknown record keeping an exited value, a stale sample or stale evidence
// and a refused resume end nothing. Ending the claim fails the transition as a
// whole. Accepted Messages' facts never change.
func TestBusySendPauseClaimLifetime(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const token = "1122334455667788"
	type step func(t *testing.T, in *busySendInbox) error
	sessionEnd := func(t *testing.T, in *busySendInbox) error {
		if a := storefix.ApplyAgentHook(t, in.s, in.id, "SessionEnd", in.dest.SessionID); !a.Applied {
			return fmt.Errorf("SessionEnd %+v; want applied", a)
		}
		return nil
	}
	// Synthetic: only a Codex row holding an end report moves to ended.
	codex := func(t *testing.T, in *busySendInbox) error {
		setBusySendFact(t, in.path, `UPDATE spawns SET agent = 'codex', end_reported_at = 1 WHERE instance_id = ?`, in.id)
		return nil
	}
	exitOf := func(row store.Spawn) store.ProcessExit {
		return store.ProcessExit{PID: row.Identity.PanePID, Start: row.Identity.PaneStarttime, AtMillis: time.Now().UnixMilli()}
	}
	record := func(value, reason string) step {
		return func(t *testing.T, in *busySendInbox) error {
			row := in.row(t)
			rec, ok := row.ProcessEvidenceAfter(store.ProcessAttempt{PID: row.Identity.PanePID, Start: row.Identity.PaneStarttime,
				Value: value, Reason: reason}, time.Now().UnixMilli())
			if !ok {
				return errors.New("no process record for the attempt")
			}
			return applied(in.s.RecordProcessEvidence(row, rec))
		}
	}
	mark := func(src string, exit func(store.Spawn) store.ProcessExit) step {
		return func(t *testing.T, in *busySendInbox) error {
			row := in.row(t)
			_, res, _, err := in.s.RecoveryWrites(row).MarkMissingIfSameLifeFrom(in.id, row.Snapshot, src, exit(row))
			return applied(res, err)
		}
	}
	keyless := func(exit func(store.Spawn) store.ProcessExit) step {
		return func(t *testing.T, in *busySendInbox) error {
			row := in.row(t)
			pub, err := in.s.GetBusySendPublishedLaunch(in.id)
			if err != nil {
				return err
			}
			_, res, err := in.s.MarkKeylessPauseEndedWithin(ctx, row, pub, exit(row), time.Minute)
			return applied(res, err)
		}
	}
	for _, tc := range []struct {
		name  string
		pre   step
		step  step
		ended bool
	}{
		{name: "kill's verified exit", step: func(t *testing.T, in *busySendInbox) error {
			_, err := in.s.EndBusySendLaunch(in.id, in.dest.LaunchToken)
			return err
		}, ended: true},
		{name: "kill's verified exit after the agent's end report", pre: sessionEnd, step: func(t *testing.T, in *busySendInbox) error {
			_, err := in.s.EndBusySendLaunch(in.id, in.dest.LaunchToken)
			return err
		}, ended: true},
		{name: "a kill judged for another token", step: func(t *testing.T, in *busySendInbox) error {
			_, err := in.s.EndBusySendLaunch(in.id, "ffffffffffffffff")
			return err
		}},
		{name: "keyless pause's verified end", step: keyless(exitOf), ended: true},
		{name: "keyless pause's end without an exit", step: keyless(func(store.Spawn) store.ProcessExit { return store.ProcessExit{} })},
		{name: "a sweep's proven-exit mark", step: mark(store.EvidenceProcess, exitOf), ended: true},
		{name: "a tmux-only missing mark", step: mark(store.EvidenceTmux, func(store.Spawn) store.ProcessExit { return store.ProcessExit{} })},
		{name: "a mark naming another process", step: mark(store.EvidenceProcess, func(row store.Spawn) store.ProcessExit {
			exit := exitOf(row)
			exit.PID++
			return exit
		})},
		{name: "a missing mark without evidence", step: func(t *testing.T, in *busySendInbox) error {
			_, res, err := in.s.MarkMissingIfSameLife(in.id, in.row(t).Snapshot)
			return applied(res, err)
		}},
		{name: "a move to ended storing the agent's exit", pre: codex, step: func(t *testing.T, in *busySendInbox) error {
			row := in.row(t)
			_, res, recorded, err := in.s.MarkEndedIfSameLifeFrom(in.id, row.Snapshot, exitOf(row))
			if err == nil && !recorded {
				return errors.New("the move stored no exited record")
			}
			return applied(res, err)
		}, ended: true},
		{name: "a move to ended without an exit", pre: codex, step: func(t *testing.T, in *busySendInbox) error {
			_, res, err := in.s.MarkEndedIfSameLife(in.id, in.row(t).Snapshot)
			return applied(res, err)
		}},
		{name: "the agent's end report", step: sessionEnd},
		{name: "an exited record after the agent's end report", pre: sessionEnd, step: record(store.ProcessExited, ""), ended: true},
		{name: "an exited record before any End", step: record(store.ProcessExited, "")},
		{name: "an unknown record keeping an exited value", pre: func(t *testing.T, in *busySendInbox) error {
			if err := sessionEnd(t, in); err != nil {
				return err
			}
			// Synthetic: an exited record no writer accepted, which the
			// unknown attempt keeps with its failure reason.
			row := in.row(t)
			rec, _ := row.ProcessEvidenceAfter(store.ProcessAttempt{PID: row.Identity.PanePID, Start: row.Identity.PaneStarttime,
				Value: store.ProcessExited}, time.Now().UnixMilli())
			setBusySendFact(t, in.path, `UPDATE spawns SET process_evidence = ? WHERE instance_id = ?`, rec, in.id)
			return nil
		}, step: record(store.ProcessUnknown, store.ReasonBusy)},
		{name: "a stale sample's exited record", pre: sessionEnd, step: func(t *testing.T, in *busySendInbox) error {
			row := in.row(t)
			rec, _ := row.ProcessEvidenceAfter(store.ProcessAttempt{PID: row.Identity.PanePID, Start: row.Identity.PaneStarttime,
				Value: store.ProcessExited}, time.Now().UnixMilli())
			setBusySendFact(t, in.path, `UPDATE spawns SET row_version = row_version + 1 WHERE instance_id = ?`, in.id)
			if res, err := in.s.RecordProcessEvidence(row, rec); err != nil || res != store.CondChanged {
				return fmt.Errorf("stale record %v, %v; want CondChanged", res, err)
			}
			return nil
		}},
		{name: "validated exit evidence", step: func(t *testing.T, in *busySendInbox) error {
			if ok, err := in.s.EstablishBusySendExit(*exitEvidenceForTest(t, in.s, in.id)); err != nil || !ok {
				return fmt.Errorf("exit %t, %v; want established", ok, err)
			}
			return nil
		}, ended: true},
		{name: "exit evidence made stale", step: func(t *testing.T, in *busySendInbox) error {
			ev := *exitEvidenceForTest(t, in.s, in.id)
			row := in.row(t)
			identity := row.Identity
			identity.PanePID++
			res, _, err := in.s.AdoptIdentityIfSameLife(in.id, row.Snapshot, identity)
			if err := applied(res, err); err != nil {
				return fmt.Errorf("adopting another agent process: %w", err)
			}
			if ok, err := in.s.EstablishBusySendExit(ev); err != nil || ok {
				return fmt.Errorf("stale exit %t, %v; want refused", ok, err)
			}
			return nil
		}},
		{name: "resume staging, then a restore", pre: sessionEnd, step: func(t *testing.T, in *busySendInbox) error {
			row := in.row(t)
			res, version, err := in.s.MoveToPendingWithExit(in.id, row.Snapshot, 2000, token, "", "", store.LaunchOwner{},
				store.BusySendDefaultsChoice{}, exitEvidenceForTest(t, in.s, in.id))
			if err := applied(res, err); err != nil {
				return err
			}
			return applied(in.s.RestoreAfterFailedResume(in.id, version, store.ResumePrior{State: row.State, Identity: row.Identity,
				TurnStarts: row.TurnStarts}))
		}, ended: true},
		{name: "reuse staging, then a restore", pre: sessionEnd, step: func(t *testing.T, in *busySendInbox) error {
			prior, _, err := in.s.ReadForReuse(in.id)
			if err != nil {
				return err
			}
			res, version, err := in.reuse(t, token, exitEvidenceForTest(t, in.s, in.id))
			if err := applied(res, err); err != nil {
				return err
			}
			return applied(in.s.RestoreAfterFailedReuse(in.id, version, prior.Life, time.Unix(3, 0)))
		}, ended: true},
		{name: "a resume without exit evidence", pre: sessionEnd, step: func(t *testing.T, in *busySendInbox) error {
			_, _, err := in.s.MoveToPendingWithExit(in.id, in.row(t).Snapshot, 2000, token, "", "", store.LaunchOwner{},
				store.BusySendDefaultsChoice{}, nil)
			if !errors.Is(err, store.ErrBusySendExitNotEstablished) {
				return fmt.Errorf("resume %v; want exit not established", err)
			}
			return nil
		}},
		{name: "covered deletion", step: func(t *testing.T, in *busySendInbox) error {
			return applied(in.s.DeleteBusySendCovered(in.id, in.row(t).Snapshot, *exitEvidenceForTest(t, in.s, in.id)))
		}, ended: true},
	} {
		for _, fail := range []bool{false, true} {
			if fail && !tc.ended {
				continue
			}
			t.Run(tc.name+map[bool]string{false: "", true: ", its claim's end failing"}[fail], func(t *testing.T) {
				t.Parallel()
				in := newBusySendInbox(t, "held")
				setBusySendFact(t, in.path, `UPDATE spawns SET pane_starttime = '123' WHERE instance_id = ?`, in.id)
				in.pauseClaim(t, in.pauseRequest(pauseClock(), "call-1", time.Hour))
				if tc.pre != nil {
					if err := tc.pre(t, in); err != nil {
						t.Fatal(err)
					}
				}
				wantPauseClaims(t, in, "standing r1")
				accepted := readBusySendAccepted(t, in.path, in.id)
				if fail {
					if _, err := openBusySendRaw(t, in.path).Exec(failPauseClaimEnd); err != nil {
						t.Fatal(err)
					}
					before := readBusySendTables(t, in.path)
					if err := tc.step(t, in); err == nil || !strings.Contains(err.Error(), "injected claim end failure") {
						t.Fatalf("step %v; want the injected failure", err)
					}
					if !reflect.DeepEqual(before, readBusySendTables(t, in.path)) {
						t.Fatal("a transition whose claim end failed wrote part of itself")
					}
					return
				}
				if err := tc.step(t, in); err != nil {
					t.Fatal(err)
				}
				if tc.ended {
					wantPauseClaims(t, in, "ended launch_end r2")
				} else {
					wantPauseClaims(t, in, "standing r1")
				}
				if got := readBusySendAccepted(t, in.path, in.id); !reflect.DeepEqual(got, accepted) {
					t.Errorf("accepted facts %v; want them unchanged %v", got, accepted)
				}
			})
		}
	}
	// The agent's verified exit ends the claim, but the Launch's issuer, its
	// input resolved, still needs exit evidence that names it before the
	// fences lift; that evidence leaves the ended claim's history as it is.
	t.Run("kill's verified exit keeps the issuer's fence", func(t *testing.T) {
		t.Parallel()
		in := newBusySendInbox(t, "held")
		h := in.claim(t, time.Time{}, false)
		for _, step := range []forceStep{forceIssue(0), forceOutput(0, store.BusySendOutputComplete), forceAdmit(0)} {
			step(t, in, h)
		}
		in.pauseClaim(t, in.pauseRequest(pauseClock(), "call-1", time.Hour))
		if ended, err := in.s.EndBusySendLaunch(in.id, in.dest.LaunchToken); err != nil || !ended {
			t.Fatalf("EndBusySendLaunch %t, %v; want the Launch ended", ended, err)
		}
		wantPauseClaims(t, in, "ended launch_end r2")
		ev := *exitEvidenceForTest(t, in.s, in.id)
		ev.Scope.Launches[0].Processes = slices.DeleteFunc(slices.Clone(ev.Scope.Launches[0].Processes),
			func(p store.BusySendLaunchProcess) bool { return p.Role != store.BusySendProcessAgent })
		before := readBusySendTables(t, in.path)
		if ok, err := in.s.EstablishBusySendExit(ev); err != nil || ok {
			t.Fatalf("exit evidence omitting the issuer %t, %v; want refused", ok, err)
		}
		if !reflect.DeepEqual(before, readBusySendTables(t, in.path)) {
			t.Fatal("refused exit evidence wrote something")
		}
		if ok, err := in.s.EstablishBusySendExit(*exitEvidenceForTest(t, in.s, in.id)); err != nil || !ok {
			t.Fatalf("full exit evidence %t, %v; want established", ok, err)
		}
		wantPauseClaims(t, in, "ended launch_end r2")
	})
}

// TestBusySendPauseClaimPublication (SR-6.5; M2 P-C4.7 ResumeStep and
// ReusePublishStep): resume and reuse staging apply exit evidence, so the old
// Launch's claim ends launch_end before publication, which leaves that history
// alone. A claim still standing on the old Launch at publication, seeded raw
// after staging (synthetic setup: no production path leaves one), ends
// launch_published in the publication's own transaction through each direct
// entrance: the creator's RecordLaunchIdentity, PublishBusySendLaunch after an
// uncertain mark, and an adoption's reported publication, whose helper the
// hook, recovery and relay report-ins share. A restore, a wrong-token
// publication and the uncertain mark end nothing, and a failure of the claim's
// end or of the publication after it rolls both back. The new Launch is
// claimed on its own facts alone, the old owner's ref inert.
func TestBusySendPauseClaimPublication(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const token = "1122334455667788"
	for _, kind := range []string{"resume", "reuse"} {
		for _, entrance := range []string{"creator", "lost reply", "adoption", "restore"} {
			t.Run(kind+"/"+entrance, func(t *testing.T) {
				t.Parallel()
				in := newBusySendInbox(t, "held")
				clock := pauseClock()
				old := in.pauseClaim(t, in.pauseRequest(clock, "call-1", time.Hour))
				if a := storefix.ApplyAgentHook(t, in.s, in.id, "SessionEnd", in.dest.SessionID); !a.Applied {
					t.Fatalf("SessionEnd %+v; want applied", a)
				}
				accepted := readBusySendAccepted(t, in.path, in.id)
				row := in.row(t)
				prior, _, err := in.s.ReadForReuse(in.id)
				if err != nil {
					t.Fatal(err)
				}
				var res store.CondResult
				var version int64
				if kind == "resume" {
					res, version, err = in.s.MoveToPendingWithExit(in.id, row.Snapshot, 2000, token, "", "", store.LaunchOwner{},
						store.BusySendDefaultsChoice{}, exitEvidenceForTest(t, in.s, in.id))
				} else {
					res, version, err = in.reuse(t, token, exitEvidenceForTest(t, in.s, in.id))
				}
				if err := applied(res, err); err != nil {
					t.Fatalf("%s staging: %v", kind, err)
				}
				wantPauseClaims(t, in, "ended launch_end r2")
				storefix.SeedBusySendPauseClaim(t, in.path, in.id)
				seeded := []string{"ended launch_end r2", "standing r1"}
				if res, err := in.s.RecordLaunchIdentity(in.id, version, "ffffffffffffffff", store.LaunchIdentity{}); err != nil || res != store.CondChanged {
					t.Fatalf("wrong-token publication %v, %v; want CondChanged", res, err)
				}
				if entrance == "lost reply" {
					if err := in.s.MarkBusySendLaunchUncertain(in.id, token); err != nil {
						t.Fatal(err)
					}
				}
				wantPauseClaims(t, in, seeded...)
				publish := func() error {
					switch entrance {
					case "creator":
						return applied(in.s.RecordLaunchIdentity(in.id, version, token, store.LaunchIdentity{}))
					case "lost reply":
						return applied(in.s.PublishBusySendLaunch(in.id, version, token))
					case "adoption":
						return applied(in.s.AdoptIdentityIfUnchanged(in.id, in.row(t).Snapshot,
							store.LaunchIdentity{PaneID: "%9", PanePID: 4444, PaneStarttime: apitest.LinuxProcStarttime}))
					}
					if kind == "resume" {
						return applied(in.s.RestoreAfterFailedResume(in.id, version, store.ResumePrior{State: row.State, Identity: row.Identity,
							TurnStarts: row.TurnStarts}))
					}
					return applied(in.s.RestoreAfterFailedReuse(in.id, version, prior.Life, time.Unix(3, 0)))
				}
				if entrance == "restore" {
					if err := publish(); err != nil {
						t.Fatal(err)
					}
					wantPauseClaims(t, in, seeded...)
					return
				}
				raw := openBusySendRaw(t, in.path)
				for _, trigger := range []string{failPauseClaimEnd, `CREATE TRIGGER fail_claim_end BEFORE UPDATE ON busy_send_launches
				    WHEN NEW.phase = 'current' AND OLD.phase <> 'current' BEGIN SELECT RAISE(ABORT, 'injected publication failure'); END`} {
					if _, err := raw.Exec(trigger); err != nil {
						t.Fatal(err)
					}
					before := readBusySendTables(t, in.path)
					if err := publish(); err == nil || !strings.Contains(err.Error(), "injected") {
						t.Fatalf("publication %v; want the injected failure", err)
					}
					if !reflect.DeepEqual(before, readBusySendTables(t, in.path)) {
						t.Fatal("a failed publication wrote part of itself")
					}
					if _, err := raw.Exec(`DROP TRIGGER fail_claim_end`); err != nil {
						t.Fatal(err)
					}
				}
				if err := publish(); err != nil {
					t.Fatal(err)
				}
				wantPauseClaims(t, in, "ended launch_end r2", "ended launch_published r2")
				if got := readBusySendAccepted(t, in.path, in.id); !reflect.DeepEqual(got, accepted) {
					t.Errorf("accepted facts %v; want them unchanged %v", got, accepted)
				}
				pub, err := in.s.GetBusySendPublishedLaunch(in.id)
				if err != nil || pub.LaunchID == in.dest.LaunchID {
					t.Fatalf("published %+v, %v; want the new Launch", pub, err)
				}
				in.dest.LaunchID, in.dest.LaunchToken = pub.LaunchID, pub.LaunchToken
				setBusySendFact(t, in.path, `UPDATE spawns SET state = 'waiting' WHERE instance_id = ?`, in.id)
				in.pauseClaim(t, in.pauseRequest(clock, "call-2", time.Hour))
				if w, err := in.s.PersistBusySendPauseSignalWithin(ctx, store.BusySendPauseWriteRequest{Ref: old.Ref, Now: clock.Now}, time.Minute); err != nil || w.Applied {
					t.Fatalf("the old owner's intent %+v, %v; want nothing applied", w, err)
				}
				wantPauseClaims(t, in, "ended launch_end r2", "ended launch_published r2", "standing r1")
			})
		}
	}
}

// TestBusySendWakeOrderAcrossEnd (SR-6.2; M1 R6/R8, M2 S1) separates
// completion accounting from the physical write. An issuer authorized before
// End can still write afterwards; only positive Exit closes that fence.
func TestBusySendWakeOrderAcrossEnd(t *testing.T) {
	t.Parallel()
	for _, forced := range []bool{false, true} {
		for _, history := range []string{"written before End", "sighted before End", "order before End", "physical write after End"} {
			t.Run(fmt.Sprintf("force=%t/%s", forced, history), func(t *testing.T) {
				t.Parallel()
				ctx := context.Background()
				in := newBusySendInbox(t, "wake")
				var h *store.BusySendHandover
				if forced {
					h = in.forceClaim(t, time.Time{}).Handover
				} else {
					h = in.claim(t, time.Time{}, false)
				}
				revision := in.issue(t, h, 0, h.Revision).Revision
				in.admit(t, store.BusySendSteerTurn, "later")
				sighting := in.sighting(t, h.Prompts[0].Body)
				path := filepath.Join(t.TempDir(), "output")
				write := func() {
					if err := os.WriteFile(path, h.Prompts[0].Body, 0600); err != nil {
						t.Fatal(err)
					}
					if got, err := os.ReadFile(path); err != nil || string(got) != string(h.Prompts[0].Body) {
						t.Fatalf("physical output %q, %v", got, err)
					}
				}
				if history != "physical write after End" {
					write()
				}
				if history == "sighted before End" || history == "order before End" {
					in.sight(t, sighting)
				}
				if history == "written before End" || history == "order before End" {
					in.complete(t, h, 0, revision, store.BusySendOutputComplete, "", true)
				}
				before := readBusySendCustody(t, in.path, in.id)
				forceLaunchEnd(t, in, h)
				ended, err := in.s.ReadBusySendHandover(ctx, h.HandoverID)
				if err != nil {
					t.Fatal(err)
				}
				final, err := in.s.ReadBusySendResult(in.pair.CallerUUID, in.pair.DeliveryRequestID)
				if err != nil || final.Result == nil || !final.Result.Final {
					t.Fatalf("Final after End: %+v, %v", final, err)
				}
				if history == "physical write after End" {
					if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("output exists before late write: %v", err)
					}
					write()
				}
				// Valid issue identity may report a historical stamp even after
				// End. Neither this nor the now-stale hook grants new order.
				completed := in.complete(t, h, 0, revision, store.BusySendOutputComplete, "", true)
				if completed.Revision != 0 || completed.OrderSecured != (history == "order before End") {
					t.Fatalf("late completion grants authority/order: %+v", completed)
				}
				if got := in.sight(t, sighting); got.Outcome != store.BusySendSightingStale {
					t.Fatalf("late sighting: %+v", got)
				}
				after, err := in.s.ReadBusySendHandover(ctx, h.HandoverID)
				if err != nil || after.Revision != ended.Revision || after.PromptCursor != ended.PromptCursor ||
					after.Open != ended.Open || after.Prompts[0].OrderKind != ended.Prompts[0].OrderKind {
					t.Fatalf("late facts changed order: %+v -> %+v, %v", ended, after, err)
				}
				custody := readBusySendCustody(t, in.path, in.id)
				if !custody[0].final || (before[0].final && custody[0].result() != before[0].result()) || custody[1].place != "held" {
					t.Fatalf("End/late fact changed Final or tail: %v -> %v", before, custody)
				}
				fence := storefix.ReadLaunchFence(t, in.s, in.dest.LaunchID)
				want := boolCount(history != "order before End")
				if !fence.Ended || len(fence.Prompts) != want || (want == 1 && !fence.Prompts[0].Fenced) {
					t.Fatalf("End fence: %+v; want %d fenced prompts", fence, want)
				}
				if _, _, err := in.reuse(t, "1122334455667788", nil); !errors.Is(err, store.ErrBusySendExitNotEstablished) {
					t.Fatalf("reuse without physical Exit: %v", err)
				}
				if applied, err := in.s.EstablishBusySendExit(*exitEvidenceForTest(t, in.s, in.id)); err != nil || !applied {
					t.Fatalf("positive Exit: %t, %v", applied, err)
				}
				custody = readBusySendCustody(t, in.path, in.id)
				if custody[0].place != "closed" || custody[1].place != "held" {
					t.Fatalf("Exit custody: %v", custody)
				}
				if got, err := in.s.ReadBusySendResult(in.pair.CallerUUID, in.pair.DeliveryRequestID); err != nil || !reflect.DeepEqual(got, final) {
					t.Fatalf("late writes/Exit changed Final Lookup: %+v -> %+v, %v", final, got, err)
				}
			})
		}
	}
}

// PM1 option 1: lower new admission limits do not re-admit carried custody.
// 8,001 bytes (even worst-case escaped tags) still fit the destination's
// 9,600-byte delivery envelope. Both original modes can cross a Wait boundary.
func TestBusySendResumeDifferentCapabilities(t *testing.T) {
	t.Parallel()
	for _, boundary := range []string{"turn_end", "force"} {
		for _, mode := range []store.BusySendMode{store.BusySendWaitTurn, store.BusySendSteerTurn} {
			t.Run(boundary+"/"+string(mode), func(t *testing.T) {
				t.Parallel()
				local := store.BusySendCapabilitiesFor(store.AgentClaude)
				old := &store.BusySendLaunchCapabilities{HeldCount: 7, HeldBytes: 16384, Modes: local.Modes}
				for i := range old.Modes {
					old.Modes[i].MessageBytes = 8001
				}
				in := newBusySendInboxWithCapabilities(t, old)
				text := strings.Repeat("x", 8001)
				confirmed := true
				_, err := in.s.AdmitBusySend(store.BusySendAdmissionRequest{InstanceID: in.id, Text: []byte(text), SendMode: &mode,
					Confirmed: &confirmed, CallerUUID: in.pair.CallerUUID, DeliveryRequestID: in.pair.DeliveryRequestID})
				if err != nil {
					t.Fatal(err)
				}
				in.admit(t, store.BusySendWaitTurn, "tail")
				accepted := readBusySendAccepted(t, in.path, in.id)
				oldID := in.dest.LaunchID
				if a := storefix.ApplyAgentHook(t, in.s, in.id, "SessionEnd", in.dest.SessionID); !a.Applied {
					t.Fatalf("End: %+v", a)
				}
				const token = "1122334455667788"
				res, v, err := in.s.MoveToPendingWithExit(in.id, in.row(t).Snapshot, 2000, token, "", "", store.LaunchOwner{},
					store.BusySendDefaultsChoice{}, exitEvidenceForTest(t, in.s, in.id))
				if err != nil || res != store.CondApplied {
					t.Fatalf("resume %v, %v", res, err)
				}
				if res, err := in.s.RecordLaunchIdentity(in.id, v, token, store.LaunchIdentity{PaneID: "%42", PanePID: 4243, PaneStarttime: "101"}); err != nil || res != store.CondApplied {
					t.Fatalf("publish %v, %v", res, err)
				}
				pub, err := in.s.GetBusySendPublishedLaunch(in.id)
				if err != nil || pub.LaunchID == oldID {
					t.Fatalf("new Launch %+v, %v", pub, err)
				}
				in.dest.LaunchID, in.dest.LaunchToken = pub.LaunchID, pub.LaunchToken
				// Report-in and a repeated report/reconnect must not reauthor either snapshot.
				for i := 0; i < 2; i++ {
					if a := storefix.ApplyAgentHook(t, in.s, in.id, "SessionStart", in.dest.SessionID); !a.Applied {
						t.Fatalf("SessionStart: %+v", a)
					}
				}
				route, err := in.s.ReadBusySendRouteFacts(context.Background(), in.id, store.HookGate{Event: "Stop", ParentPID: 4243, ParentStart: "101"})
				if err != nil || !route.GateHolds || !route.Covered {
					t.Fatalf("resumed route %+v, %v", route, err)
				}
				in.dest = route.Destination

				facts, err := apitest.ReadBusySendFacts(in.path, in.id)
				if err != nil || len(facts.Launches) != 2 {
					t.Fatalf("Launches %+v, %v", facts.Launches, err)
				}
				current := store.BusySendCapabilitiesFor(store.AgentClaude)
				wantCurrent := &store.BusySendLaunchCapabilities{HeldCount: current.HeldCount, HeldBytes: current.HeldBytes, Modes: current.Modes}
				for _, l := range facts.Launches {
					want := wantCurrent
					if l.ID == oldID {
						want = old
					}
					if !reflect.DeepEqual(l.Capabilities, want) {
						t.Fatalf("Launch %s snapshot %+v; want %+v", l.ID, l.Capabilities, want)
					}
				}
				before := readBusySendTables(t, in.path)
				_, err = in.s.AdmitBusySend(store.BusySendAdmissionRequest{InstanceID: in.id, Text: []byte(text), SendMode: &mode})
				var refusal *store.BusySendRefusal
				if !errors.As(err, &refusal) || refusal.Reason != store.BusySendRefuseTextBytes || !reflect.DeepEqual(refusal.Limit, &store.BusySendLimit{Actual: 8001, Allowed: 8000}) {
					t.Fatalf("new admission: %v", err)
				}
				if !reflect.DeepEqual(before, readBusySendTables(t, in.path)) {
					t.Fatal("refusal changed carried Messages")
				}
				var h *store.BusySendHandover
				if boundary == "force" {
					h = in.forceClaim(t, time.Time{}).Handover
				} else {
					h = in.claim(t, time.Time{}, false)
				}
				if len(h.Prompts) != 1 || string(h.Prompts[0].Body) != busySendBody(text, "tail") || h.Prompts[0].RenderedBytes != 8242 {
					t.Fatalf("carried prompt %+v; want both whole Messages in 8,242 Wait bytes", h.Prompts)
				}
				if !slices.Equal(h.Prompts[0].MessageIDs, []string{accepted[0][0].(string), accepted[1][0].(string)}) {
					t.Fatal("carried identity/order changed")
				}
				in.issue(t, h, 0, h.Revision)
				issued, err := in.s.ReadBusySendHandover(context.Background(), h.HandoverID)
				if err != nil || !reflect.DeepEqual(issued.Prompts[0].Body, h.Prompts[0].Body) {
					t.Fatalf("issued bytes differ: %v", err)
				}
				if got := readBusySendAccepted(t, in.path, in.id); !reflect.DeepEqual(got, accepted) {
					t.Fatal("delivery rewrote accepted facts")
				}
				after, err := apitest.ReadBusySendFacts(in.path, in.id)
				if err != nil {
					t.Fatal(err)
				}
				for i, l := range after.Launches {
					if !reflect.DeepEqual(l.Capabilities, facts.Launches[i].Capabilities) {
						t.Fatal("delivery rewrote snapshots")
					}
				}
			})
		}
	}
}
