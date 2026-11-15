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
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
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
