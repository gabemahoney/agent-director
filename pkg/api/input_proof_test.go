package api_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/gabemahoney/agent-director/internal/store"
	"os"
	"reflect"
	"testing"

	"github.com/gabemahoney/agent-director/internal/inputdelivery"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
)

func TestInputProofOnePassKeepsExactCapturedProvenance(t *testing.T) {
	t.Parallel()
	e, r, launch := sqliteSubmission(t)
	p := api.SendKeysParams{InstanceID: r.ID, Text: "late <>& 🌍", AllowPending: true, DeliveryRequestID: "original"}
	_, err := e.send(p)
	assertDeliveryError(t, err, "submission_unconfirmed", len(p.Text), true)
	l, owner := sqliteAttempt(t, e, r)
	if owner.Confirmation.Source != "store" {
		t.Fatalf("intent became receipt: %+v", owner.Confirmation)
	}
	before := len(e.rec.SocketCalls())
	first, err := api.ReconcileInputProof(context.Background(), e.st, r.Spawn)
	if err != nil || !reflect.DeepEqual(first.Attempt, owner) || first.LaunchRevision != l.Revision {
		t.Fatalf("empty pass=%+v %v", first, err)
	}
	raw := sqliteProofRecord(t, launch, p.Text)
	if err := os.WriteFile(launch.Capability.Transcript, append(append([]byte(nil), raw...), '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := api.ReconcileInputProof(context.Background(), e.st, r.Spawn)
	if err != nil || got.Attempt == nil || got.Attempt.Phase != "submitted" {
		t.Fatalf("proof=%+v %v", got, err)
	}
	a := got.Attempt
	if got.Identity != owner.Identity || got.LaunchRevision != l.Revision+1 || a.RequestID != owner.RequestID || a.Revision != owner.Revision+1 || !bytes.Equal(a.Proof.Raw, raw) || a.Proof.RawSHA256 != inputdelivery.Hash(raw) || a.Confirmation.Source != "transcript" || a.Confirmation.At.IsZero() {
		t.Fatalf("lost provenance: %+v", got)
	}
	if a.Proof.Path != launch.Capability.Transcript || a.Proof.SessionID != r.Spawn.SessionID || a.Proof.Offset != 0 || a.Confirmation.Value != "submitted" || a.Confirmation.Revision != a.Revision {
		t.Fatalf("incorrect proof basis: %+v", a)
	}
	again, err := api.ReconcileInputProof(context.Background(), e.st, r.Spawn)
	if err != nil || !reflect.DeepEqual(again, got) || len(e.rec.SocketCalls()) != before {
		t.Fatalf("repeat pass changed evidence or dispatched: %+v %v", again, err)
	}
}

type staleProofStore struct {
	*apiProofPersistence
	before func()
}
type apiProofPersistence struct {
	inputdelivery.Persistence
	id string
}

func (s *apiProofPersistence) StoreID() string { return s.id }
func (s *staleProofStore) SaveInputAttempt(ctx context.Context, l inputdelivery.Launch, g inputdelivery.Guard, a *inputdelivery.Attempt) (*inputdelivery.Launch, *inputdelivery.Attempt, error) {
	if s.before != nil {
		f := s.before
		s.before = nil
		f()
	}
	return s.Persistence.SaveInputAttempt(ctx, l, g, a)
}

func TestInputProofRejectsStaleCaptureAndNeverStartsWork(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"empty", "cancelled", "wrong launch", "wrong life", "wrong process", "wrong session", "stale commit"} {
		t.Run(mode, func(t *testing.T) {
			e, r, launch := sqliteSubmission(t)
			if mode == "empty" {
				before, err := e.st.ReadInputLaunch(context.Background(), r.Token)
				if err != nil {
					t.Fatal(err)
				}
				got, err := api.ReconcileInputProof(context.Background(), e.st, r.Spawn)
				if err != nil || got.Attempt != nil {
					t.Fatalf("empty=%+v %v", got, err)
				}
				after, err := e.st.ReadInputLaunch(context.Background(), r.Token)
				if err != nil || !reflect.DeepEqual(before, after) || after.Identity.PaneID != "" {
					t.Fatalf("empty proof pass bound or changed capability: %+v %v", after, err)
				}
				e.assertNoTmuxCall(t)
				return
			}
			p := api.SendKeysParams{InstanceID: r.ID, Text: "late", AllowPending: true, DeliveryRequestID: "original"}
			_, err := e.send(p)
			assertDeliveryError(t, err, "submission_unconfirmed", len(p.Text), true)
			_, owner := sqliteAttempt(t, e, r)
			raw := sqliteProofRecord(t, launch, p.Text)
			if err := os.WriteFile(launch.Capability.Transcript, append(raw, '\n'), 0600); err != nil {
				t.Fatal(err)
			}
			row := r.Spawn
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			s := &staleProofStore{apiProofPersistence: &apiProofPersistence{Persistence: e.st, id: e.storeID}}
			switch mode {
			case "cancelled":
				cancel()
			case "wrong launch":
				row.Identity.Token = "0123456789abcdef"
			case "wrong life":
				row.LifeNumber++
			case "wrong process":
				row.Identity.PaneStarttime += "0"
			case "wrong session":
				row.SessionID = "wrong"
			case "stale commit":
				s.before = func() {
					l, a := sqliteAttempt(t, e, r)
					a.ManualInterference = true
					g := sqliteGuard(r.Spawn, e.storeID)
					g.Manual = true
					if _, _, err := e.st.SaveInputAttempt(context.Background(), *l, g, a); err != nil {
						t.Fatal(err)
					}
				}
			}
			before := len(e.rec.SocketCalls())
			got, err := api.ReconcileInputProof(ctx, s, row)
			if err == nil || got.Attempt != nil {
				t.Fatalf("stale proof accepted: %+v %v", got, err)
			}
			_, after := sqliteAttempt(t, e, r)
			if after.RequestID != owner.RequestID || after.Phase != "enter_attempted" || after.Proof != nil || len(e.rec.SocketCalls()) != before {
				t.Fatalf("stale pass changed intent: %+v", after)
			}
			if mode == "stale commit" && !errors.Is(err, inputdelivery.ErrStale) {
				t.Fatalf("wrong stale result: %v", err)
			}
			e.assertNoCalls(t, tmux.CallKillSession)
		})
	}
}

// b.x9o: each first public check must retain the complete native identity,
// including cwd, and record late proof without reacquiring input authority.
func TestInputProofSharedFirstRead(t *testing.T) {
	t.Parallel()
	for _, surface := range []string{"status", "get", "list", "find-missing"} {
		for _, finished := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/finished=%v", surface, finished), func(t *testing.T) {
				e, r, launch := sqliteSubmission(t)
				p := api.SendKeysParams{InstanceID: r.ID, Text: "late <>& 🌍", AllowPending: true, DeliveryRequestID: "original"}
				_, err := e.send(p)
				assertDeliveryError(t, err, "submission_unconfirmed", len(p.Text), true)
				if err := seedAgentState(e.st, e.dbPath, r.ID, store.StateWaiting); err != nil {
					t.Fatal(err)
				}
				if finished {
					row, err := e.st.GetSpawn(r.ID)
					if err != nil {
						t.Fatal(err)
					}
					if _, applied, err := e.st.MarkMissingIfSameLife(r.ID, row.Snapshot); err != nil || applied != store.CondApplied {
						t.Fatalf("finish=%v %v", applied, err)
					}
				}
				before, owner := sqliteAttempt(t, e, r)
				raw := sqliteProofRecord(t, launch, p.Text)
				if err := os.WriteFile(launch.Capability.Transcript, append(append([]byte(nil), raw...), '\n'), 0600); err != nil {
					t.Fatal(err)
				}
				callMark := len(e.rec.SocketCalls())
				c, _ := e.client(t)
				switch surface {
				case "status":
					_, err = c.Status(r.ID)
				case "get":
					_, err = c.Get(r.ID)
				case "list":
					_, err = c.List(api.ListParams{})
				case "find-missing":
					_, err = c.FindMissing(context.Background())
				}
				if err != nil {
					t.Fatal(err)
				}
				after, proof := sqliteAttempt(t, e, r)
				if proof.Phase != "submitted" || proof.Proof == nil || !bytes.Equal(proof.Proof.Raw, raw) || proof.Proof.RawSHA256 != inputdelivery.Hash(raw) || proof.Identity != owner.Identity || proof.Identity.CWD == "" || proof.Revision != owner.Revision+1 || after.Revision != before.Revision+1 || proof.Confirmation.Source != "transcript" {
					t.Fatalf("shared proof lost identity/provenance: %+v", proof)
				}
				for _, call := range e.rec.SocketCalls()[callMark:] {
					switch call.Call {
					case tmux.CallLookup, tmux.CallListPanes, tmux.CallCapture:
					default:
						t.Fatalf("read gained command authority: %+v", call)
					}
				}
			})
		}
	}
}
