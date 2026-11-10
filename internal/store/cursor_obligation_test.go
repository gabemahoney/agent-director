package store_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
)

// Records use the shared verified store binding; no fixture reconstructs a queue.
func (e *cursorTrailEnv) input(id, state string) store.CursorInput {
	v := store.CursorInput{Owner: store.CursorOwner{StoreID: e.f.s.StoreID(), Runtime: e.b},
		Generation: 7, SubmissionID: id, Order: 11, Epoch: 3,
		Text: "line one\n\tquoted \"文字\" ", CapturedAt: time.Now().UTC(), State: state}
	if state == store.CursorInputAdmitted {
		v.TargetSession = e.b.SessionID
	}
	return v
}

func (e *cursorTrailEnv) proof(id, token string, ran bool) store.CursorProof {
	output := `{"exitCode":7, "stdout":"a\\nb\u0020", "stderr":"", "optional":null}`
	if !ran {
		output = `{"error":"Tool execution error"}`
	}
	return store.CursorProof{Owner: store.CursorOwner{StoreID: e.f.s.StoreID(), Runtime: e.b},
		ID: id, Token: token, Tool: "tool-" + token, Generation: 7, Kind: "tool_call_update",
		CapturedAt: time.Now().UTC(), BusyUntil: e.until.UTC(), Ran: ran, TurnOpen: true,
		Raw: []byte(fmt.Sprintf(" { \"sessionId\":%q, \"update\": {\"sessionUpdate\":\"tool_call_update\", \"toolCallId\":%q, \"status\":\"completed\", \"rawOutput\": %s }} \n", e.b.SessionID, "tool-"+token, output))}
}

func TestCursorInputDurableLifecycleAndExactCAS(t *testing.T) {
	for _, initial := range []string{store.CursorInputHeld, store.CursorInputAdmitted} {
		t.Run(initial, func(t *testing.T) {
			e := newCursorTrailEnv(t)
			v := e.input("submission", initial)
			ctx := context.Background()
			if err := e.f.s.CaptureCursorInput(ctx, v, e.until, cursorTrailCheck); err != nil {
				t.Fatal(err)
			}
			if err := e.f.s.CaptureCursorInput(ctx, v, e.until, cursorTrailCheck); err == nil {
				t.Fatal("duplicate capture acquired authority")
			}
			states := []string{store.CursorInputStarted, store.CursorInputIssued, store.CursorInputCompleted}
			if initial == store.CursorInputHeld {
				states = append([]string{store.CursorInputAdmitted}, states...)
			}
			for _, state := range states {
				next := v
				next.State, next.TargetSession = state, e.b.SessionID
				if state == store.CursorInputStarted {
					next.RequestID, next.Deadline = "prompt-17", e.until.UTC()
				}
				if err := e.f.s.AdvanceCursorInput(ctx, v, next, e.until, cursorTrailCheck); err != nil {
					t.Fatalf("%s: %v", state, err)
				}
				if err := e.f.s.AdvanceCursorInput(ctx, v, next, e.until, cursorTrailCheck); err == nil {
					t.Fatalf("%s replayed", state)
				}
				s, err := store.Open(e.f.path)
				if err != nil {
					t.Fatal(err)
				}
				got, err := s.ReadCursorInput(e.b.Connection, v.SubmissionID)
				s.Close()
				if err != nil || !reflect.DeepEqual(got, next) {
					t.Fatalf("reopened %s: got=%+v want=%+v error=%v", state, got, next, err)
				}
				v = next
			}
			if err := e.f.s.AdvanceCursorInput(ctx, v, v, e.until, cursorTrailCheck); err == nil {
				t.Fatal("terminal record became runnable")
			}
		})
	}
}

func TestCursorInputRefusesChangedEvidenceAndDialog(t *testing.T) {
	for _, change := range []string{"store", "launch", "life", "connection", "session", "generation", "order", "epoch", "text", "deadline", "dialog", "retired epoch"} {
		t.Run(change, func(t *testing.T) {
			e := newCursorTrailEnv(t)
			ctx := context.Background()
			v := e.input("submission", store.CursorInputAdmitted)
			if err := e.f.s.CaptureCursorInput(ctx, v, e.until, cursorTrailCheck); err != nil {
				t.Fatal(err)
			}
			started := v
			started.State, started.RequestID, started.Deadline = store.CursorInputStarted, "prompt", e.until.UTC()
			if err := e.f.s.AdvanceCursorInput(ctx, v, started, e.until, cursorTrailCheck); err != nil {
				t.Fatal(err)
			}
			next := started
			next.State = store.CursorInputIssued
			check := store.CursorCheck(cursorTrailCheck)
			switch change {
			case "store":
				next.Owner.StoreID = "0000000000000000"
			case "launch":
				next.Owner.Runtime.Token += "replacement"
			case "life":
				next.Owner.Runtime.Life++
			case "connection":
				next.Owner.Runtime.Connection += "replacement"
			case "session":
				next.TargetSession += "replacement"
			case "generation":
				next.Generation++
			case "order":
				next.Order++
			case "epoch":
				next.Epoch++
			case "text":
				next.Text += "changed"
			case "deadline":
				next.Deadline = next.Deadline.Add(time.Second)
			case "dialog":
				e.insert(t, "unanswered")
			case "retired epoch":
				check = func(store.Spawn) error { return errors.New("original operation retired") }
			}
			if err := e.f.s.AdvanceCursorInput(ctx, started, next, e.until, check); err == nil {
				t.Fatal("changed input issued")
			}
			got, err := e.f.s.ReadCursorInput(e.b.Connection, v.SubmissionID)
			if err != nil || !reflect.DeepEqual(got, started) {
				t.Fatalf("refusal changed evidence: %+v %v", got, err)
			}
		})
	}
}

func TestCursorInputUnknownCommitRetainsOnlyCommittedEvidence(t *testing.T) {
	for _, phase := range []string{"capture", "start", "issue"} {
		for _, cut := range []string{"before commit", "lost result", "late result"} {
			t.Run(phase+"/"+cut, func(t *testing.T) {
				e := newCursorTrailEnv(t)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				old := e.input("submission", store.CursorInputAdmitted)
				if phase != "capture" {
					if err := e.f.s.CaptureCursorInput(ctx, old, e.until, cursorTrailCheck); err != nil {
						t.Fatal(err)
					}
				}
				next := old
				if phase != "capture" {
					next.State, next.RequestID, next.Deadline = store.CursorInputStarted, "prompt", e.until.UTC()
				}
				if phase == "issue" {
					if err := e.f.s.AdvanceCursorInput(ctx, old, next, e.until, cursorTrailCheck); err != nil {
						t.Fatal(err)
					}
					old = next
					next.State = store.CursorInputIssued
				}
				lost := errors.New("synthetic lost input commit result")
				entered, release := make(chan struct{}), make(chan struct{})
				var releaseOnce sync.Once
				unblock := func() { releaseOnce.Do(func() { close(release) }) }
				defer unblock()
				store.ObserveDecisionBoundaryForTest(t, e.f.s, e.f.path, func(_ context.Context, q string) error {
					if cut == "before commit" && q == "COMMIT" {
						return lost
					}
					return nil
				}, func(context.Context) error {
					if cut == "late result" {
						close(entered)
						<-release
					}
					return lost
				})
				if cut == "late result" {
					// Cancellation preserves the original absolute return bound.
					// Give this operation its own short bound before parking COMMIT.
					var end context.CancelFunc
					ctx, end = context.WithTimeout(ctx, 500*time.Millisecond)
					defer end()
				}
				done := make(chan error, 1)
				go func() {
					if phase == "capture" {
						done <- e.f.s.CaptureCursorInput(ctx, next, e.until, cursorTrailCheck)
					} else {
						done <- e.f.s.AdvanceCursorInput(ctx, old, next, e.until, cursorTrailCheck)
					}
				}()
				if cut == "late result" {
					select {
					case <-entered:
						cancel()
					case <-time.After(3 * time.Second):
						t.Fatal("actual input COMMIT did not reach its result barrier")
					}
				}
				var result error
				select {
				case result = <-done:
				case <-time.After(3 * time.Second):
					t.Fatal("input caller waited for abandoned native result")
				}
				if result == nil {
					t.Fatal("unknown persistence reported confirmed")
				}
				frozen := result.Error()
				if cut == "late result" && !strings.Contains(frozen, "unconfirmed") {
					t.Fatalf("late COMMIT lost uncertainty: %v", result)
				}
				s, err := store.Open(e.f.path)
				if err != nil {
					t.Fatal(err)
				}
				defer s.Close()
				got, err := s.ReadCursorInput(e.b.Connection, old.SubmissionID)
				if phase == "capture" && cut == "before commit" {
					if !errors.Is(err, sql.ErrNoRows) {
						t.Fatalf("uncommitted capture survived: %+v %v", got, err)
					}
					return
				}
				want := old
				if cut != "before commit" {
					want = next
				}
				if err != nil || !reflect.DeepEqual(got, want) {
					t.Fatalf("independent evidence: got=%+v want=%+v error=%v", got, want, err)
				}
				if cut == "late result" {
					// A second actual obligation is admitted through an independent
					// handle while the old native response remains parked.
					newer := e.input("newer-submission", store.CursorInputAdmitted)
					newer.Order = next.Order + 1
					if err := s.CaptureCursorInput(context.Background(), newer, e.until, cursorTrailCheck); err != nil {
						t.Fatal(err)
					}
					unblock()
					// Reusing the old pool waits for its native cleanup; this read
					// never promotes the returned result into confirmation.
					if _, err := e.f.s.GetSpawn(e.b.InstanceID); err != nil {
						t.Fatal(err)
					}
					if result.Error() != frozen {
						t.Fatal("late result changed the caller's returned uncertainty")
					}
					if current, err := s.ReadCursorInput(e.b.Connection, newer.SubmissionID); err != nil || !reflect.DeepEqual(current, newer) {
						t.Fatalf("old completion changed newer obligation: %+v %v", current, err)
					}
					if current, err := s.ReadCursorInput(e.b.Connection, next.SubmissionID); err != nil || !reflect.DeepEqual(current, next) {
						t.Fatalf("late input evidence changed: %+v %v", current, err)
					}
				}
				if cut != "before commit" {
					if phase == "capture" {
						err = s.CaptureCursorInput(context.Background(), next, e.until, cursorTrailCheck)
					} else {
						err = s.AdvanceCursorInput(context.Background(), old, next, e.until, cursorTrailCheck)
					}
					if err == nil {
						t.Fatal("lost result replayed captured operation")
					}
				}
				if result == nil {
					t.Fatal("later observation rewrote returned uncertainty")
				}
			})
		}
	}
}

func TestCursorProofExactRawAtomicLedgerAndReopen(t *testing.T) {
	for _, ran := range []bool{true, false} {
		t.Run(fmt.Sprint(ran), func(t *testing.T) {
			e := newCursorTrailEnv(t)
			e.insert(t, "target")
			e.insert(t, "independent")
			other := e.read(t, "independent")
			p := e.proof("proof-one", "target", ran)
			if err := e.f.s.CommitCursorProof(p, true, cursorTrailCheck); err != nil {
				t.Fatal(err)
			}
			s, err := store.Open(e.f.path)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			got, err := s.ReadCursorProof(e.b.Connection, p.ID)
			if err != nil || !reflect.DeepEqual(got, p) || !bytes.Equal(got.Raw, p.Raw) {
				t.Fatalf("raw/owner round trip: %+v %v", got, err)
			}
			pr := e.read(t, "target")
			if pr.ProvenGoneHow != store.ProvenGoneToolRan || !pr.DeliveredAt.IsZero() || pr.ResponseState != "" {
				t.Fatalf("proof invented delivery: %+v", pr)
			}
			if ran && (pr.Decision != "allow" || pr.DecisionOrigin != store.DecisionOriginObservation) || !ran && (pr.Decision != "" || pr.PaneAnswered()) {
				t.Fatalf("outcome semantics: %+v", pr)
			}
			mark := store.TrailMark(t)
			if err := s.CommitCursorProof(p, true, cursorTrailCheck); err != nil {
				t.Fatal(err)
			}
			if pr != e.read(t, "target") || other != e.read(t, "independent") || len(e.events(t, mark)) != 0 {
				t.Fatal("duplicate changed current/history or independent hold")
			}
			for _, change := range []string{"raw", "owner", "deadline"} {
				changed := p
				switch change {
				case "raw":
					changed.Raw = append([]byte("\n"), p.Raw...)
				case "owner":
					changed.Owner.Runtime.Life++
				case "deadline":
					changed.BusyUntil = p.BusyUntil.Add(time.Second)
				}
				if err := s.CommitCursorProof(changed, true, cursorTrailCheck); err == nil {
					t.Fatalf("reused %s overwrote exact proof", change)
				}
			}
			p.ID = "proof-two"
			p.Raw = append([]byte("\t"), p.Raw...)
			if err := s.CommitCursorProof(p, true, cursorTrailCheck); err != nil {
				t.Fatalf("distinct valid event: %v", err)
			}
			if _, err := s.ReadCursorProof(e.b.Connection, "proof-one"); err != nil {
				t.Fatal(err)
			}
			if got, err := s.ReadCursorProof(e.b.Connection, p.ID); err != nil || !bytes.Equal(got.Raw, p.Raw) {
				t.Fatalf("second proof lost: %+v %v", got, err)
			}
			if ok, err := s.ClaimCursorPaneAnswer(e.b, "target", "allow", e.until, 0, cursorTrailCheck); err != nil || ok {
				t.Fatalf("proof granted response authority: %v %v", ok, err)
			}
		})
	}
}

func TestCursorProofLedgerRollsBackWithRequest(t *testing.T) {
	for _, cut := range []string{"request update", "commit", "lost result"} {
		t.Run(cut, func(t *testing.T) {
			e := newCursorTrailEnv(t)
			e.insert(t, "target")
			before := e.read(t, "target")
			row, err := e.f.s.GetSpawn(e.b.InstanceID)
			if err != nil {
				t.Fatal(err)
			}
			p := e.proof("proof", "target", true)
			switch cut {
			case "request update":
				storefix.InjectWriteFailure(t, e.f.path, storefix.WriteFailPermissionDecision, e.b.InstanceID)
			case "commit":
				e.rejectCommitOnUpdate(t)
			case "lost result":
				store.ObserveDecisionBoundaryForTest(t, e.f.s, e.f.path, nil, func(context.Context) error { return errors.New("synthetic lost proof commit result") })
			}
			result := e.f.s.CommitCursorProof(p, true, cursorTrailCheck)
			if result == nil {
				t.Fatal("failed/unknown proof reported confirmed")
			}
			s, err := store.Open(e.f.path)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			got, err := s.ReadCursorProof(e.b.Connection, p.ID)
			after, rowErr := s.GetSpawn(e.b.InstanceID)
			if rowErr != nil {
				t.Fatal(rowErr)
			}
			if cut == "lost result" {
				if err != nil || !reflect.DeepEqual(got, p) || !e.read(t, "target").ProvenGone() || after.State != store.StateWorking {
					t.Fatalf("committed evidence lost: %+v %v %+v", got, err, after)
				}
			} else if !errors.Is(err, sql.ErrNoRows) || e.read(t, "target") != before || !reflect.DeepEqual(after, row) {
				t.Fatalf("ledger/request/state not atomic: proof=%+v error=%v row=%+v", got, err, after)
			}
		})
	}
}

func TestCursorProofRefusesAmbiguousRawSource(t *testing.T) {
	for _, field := range []string{"session", "result", "Unicode session alias"} {
		t.Run(field, func(t *testing.T) {
			e := newCursorTrailEnv(t)
			e.insert(t, "target")
			before := e.read(t, "target")
			p := e.proof("ambiguous", "target", true)
			switch field {
			case "session":
				p.Raw = bytes.Replace(p.Raw, []byte(`"sessionId"`), []byte(`"sessionId":"wrong","sessionId"`), 1)
			case "Unicode session alias":
				p.Raw = bytes.Replace(p.Raw, []byte(`"sessionId"`), []byte(`"ſessionId":"wrong","sessionId"`), 1)
			case "result":
				p.Raw = bytes.Replace(p.Raw, []byte(`"exitCode"`), []byte(`"exitCode":null,"exitCode"`), 1)
			}
			original := bytes.Clone(p.Raw)
			if err := e.f.s.CommitCursorProof(p, true, cursorTrailCheck); err == nil {
				t.Fatal("ambiguous raw source admitted")
			}
			if !bytes.Equal(p.Raw, original) {
				t.Fatal("refusal rewrote original raw source")
			}
			if _, err := e.f.s.ReadCursorProof(e.b.Connection, p.ID); !errors.Is(err, sql.ErrNoRows) {
				t.Fatalf("ambiguous source reached ledger: %v", err)
			}
			if e.read(t, "target") != before {
				t.Fatal("ambiguous source changed request")
			}
		})
	}
}
