package store_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
)

// TestMarkKeylessPauseEndedGuards (b.nqq) distinguishes a sweep's proven
// process exit from other missing causes and keeps newer Launches untouched.
func TestMarkKeylessPauseEndedGuards(t *testing.T) {
	for _, tc := range []string{"live", "process missing", "tmux missing", "unattributed missing", "malformed state", "stale process", "ended", "new Launch", "changed pane process", "changed pane start"} {
		t.Run(tc, func(t *testing.T) {
			in := newBusySendInbox(t, "held")
			setBusySendFact(t, in.path, `UPDATE spawns SET pane_starttime = '123' WHERE instance_id = ?`, in.id)
			captured := in.row(t)
			pub, err := in.s.GetBusySendPublishedLaunch(in.id)
			if err != nil {
				t.Fatal(err)
			}
			exit := store.ProcessExit{PID: captured.Identity.PanePID, Start: captured.Identity.PaneStarttime, AtMillis: time.Now().UnixMilli()}
			storefix.SeedOpenPermissionRequests(t, in.s, in.id, []string{"keyless-close"})
			changedAgent := strings.HasPrefix(tc, "changed pane")
			if tc != "live" && !changedAgent {
				row := in.row(t)
				src := store.EvidenceProcess
				if tc == "tmux missing" {
					src = store.EvidenceTmux
				}
				if tc == "unattributed missing" {
					src = ""
				}
				if _, res, _, err := in.s.RecoveryWrites(row).MarkMissingIfSameLifeFrom(in.id, row.Snapshot, src, exit); err != nil || res != store.CondApplied {
					t.Fatalf("mark missing = %v, %v; want applied", res, err)
				}
			}
			switch tc {
			case "changed pane process", "changed pane start":
				row := in.row(t)
				identity := row.Identity
				if tc == "changed pane process" {
					identity.PanePID++
				} else {
					identity.PaneStarttime = "different-process-start"
				}
				if res, _, err := in.s.AdoptIdentityIfSameLife(in.id, row.Snapshot, identity); err != nil || res != store.CondApplied {
					t.Fatalf("adopt replacement process = %v, %v; want applied", res, err)
				}
			case "malformed state":
				setBusySendFact(t, in.path, `UPDATE spawns SET state_evidence = '{}' WHERE instance_id = ?`, in.id)
			case "stale process":
				setBusySendFact(t, in.path, `UPDATE spawns SET process_evidence = json_set(process_evidence, '$.token', 'old') WHERE instance_id = ?`, in.id)
			case "ended":
				if a := storefix.ApplyAgentHook(t, in.s, in.id, "SessionEnd", captured.SessionID); !a.Applied {
					t.Fatalf("SessionEnd: %+v", a)
				}
			case "new Launch":
				row := in.row(t)
				res, version, err := in.s.MoveToPendingWithExit(in.id, row.Snapshot, time.Now().UnixMilli(), "resumed", "", "",
					store.LaunchOwner{}, store.BusySendDefaultsChoice{}, exitEvidenceForTest(t, in.s, in.id))
				if err != nil || res != store.CondApplied {
					t.Fatalf("resume = %v, %v", res, err)
				}
				if res, err := in.s.PublishBusySendLaunch(in.id, version, "resumed"); err != nil || res != store.CondApplied {
					t.Fatalf("publish resume = %v, %v", res, err)
				}
			}
			before := in.row(t)
			prior, res, err := in.s.MarkKeylessPauseEndedWithin(context.Background(), captured, pub, exit, time.Second)
			if tc == "new Launch" || changedAgent {
				if !errors.Is(err, store.ErrBusySendLaunchChanged) {
					t.Fatalf("end replaced Launch = %v; want launch changed", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			after := in.row(t)
			if tc != "live" && tc != "process missing" {
				if !reflect.DeepEqual(before, after) || (err == nil && res != store.CondChanged) {
					t.Fatalf("refused end = %s, %v; row changed: %t", prior, res, !reflect.DeepEqual(before, after))
				}
				return
			}
			if res != store.CondApplied || prior != before.State || after.State != store.StateEnded ||
				after.SessionID != captured.SessionID || after.EndedAt == nil || after.RowVersion != before.RowVersion+1 {
				t.Fatalf("end = %s, %v; row %+v; want ended, retained session and one version advance", prior, res, after)
			}
			if value, c := after.ProcessConfirmation(); value != store.ProcessExited || c.Status != store.ConfirmationConfirmed {
				t.Errorf("process = %s, %+v; want confirmed exited", value, c)
			}
			request, err := in.s.GetPermissionRequest(in.id, "keyless-close")
			if err != nil || request.Decision != "deny" || request.ClosedAt.IsZero() {
				t.Errorf("request after end = %+v, %v; want denied and closed", request, err)
			}
			if tc == "live" && request.DecisionReason != store.DecisionReasonEnded {
				t.Errorf("deny reason = %s; want ended", request.DecisionReason)
			}
		})
	}
}

// TestMarkKeylessPauseEndedRollback (b.nqq) keeps row and request closure atomic.
func TestMarkKeylessPauseEndedRollback(t *testing.T) {
	in := newBusySendInbox(t, "held")
	row := in.row(t)
	pub, err := in.s.GetBusySendPublishedLaunch(in.id)
	if err != nil {
		t.Fatal(err)
	}
	storefix.SeedOpenPermissionRequests(t, in.s, in.id, []string{"keyless-rollback"})
	undo := storefix.InjectWriteFailure(t, in.path, storefix.WriteFailPermissionDecision, in.id)
	defer undo()
	before := in.row(t)
	if _, _, err := in.s.MarkKeylessPauseEndedWithin(context.Background(), row, pub, store.ProcessExit{}, time.Second); err == nil {
		t.Fatal("end with failed request close succeeded")
	}
	if after := in.row(t); !reflect.DeepEqual(before, after) {
		t.Fatal("row changed despite failed request close")
	}
}

// TestMarkKeylessPauseEndedAbandonedResult (b.nqq) keeps callback-owned
// outputs private when the budget returns before native SQL unwinds.
func TestMarkKeylessPauseEndedAbandonedResult(t *testing.T) {
	in := newBusySendInbox(t, "held")
	row := in.row(t)
	pub, err := in.s.GetBusySendPublishedLaunch(in.id)
	if err != nil {
		t.Fatal(err)
	}
	f := &v5Store{t: t, path: in.path, s: in.s}
	observe := func(hold func()) func() {
		return store.ObservePromptRowsForTest(t, in.s, in.path, nil, nil, func(_ context.Context, q string) {
			if strings.HasPrefix(q, "SELECT state FROM spawns") {
				hold()
			}
		})
	}
	type result struct {
		prior string
		res   store.CondResult
	}
	call := func(s *store.Store) (any, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()
		prior, res, err := s.MarkKeylessPauseEndedWithin(ctx, row, pub, store.ProcessExit{}, time.Second)
		return result{prior, res}, err
	}
	checkAbandonedResult(t, f, observe, call, result{}, func() {
		fresh, err := in.s.GetSpawnWithin(in.id, 2*time.Second)
		if err != nil || !reflect.DeepEqual(fresh, row) {
			t.Fatalf("abandoned end changed row: %+v, %v", fresh, err)
		}
	})
}
