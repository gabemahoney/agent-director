package store_test

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// b.ob8 B1: the read whose observation commits an exit reports that exit,
// with the member's and ancestor's families from the same commit, even while
// the confirmed COMMIT's connection cleanup still holds the store. The exit
// record is written, so this is not b.c55's late COMMIT, which answers the
// previous view with no record (TestDisplayObservationUnconfirmedCommit).
func TestDisplayCommittedExitReportedDuringCleanup(t *testing.T) {
	type projection struct {
		state, family string
		detail        api.StateDetail
	}
	for _, verb := range []string{"status", "get", "list"} {
		t.Run(verb, func(t *testing.T) {
			s, path, id, samples, now := permissionObservation(t)
			parent, err := apitest.SeedSpawn(path, "parent", store.StateWaiting, "", "off", "", false)
			if err != nil {
				t.Fatal(err)
			}
			if err := apitest.SeedParentChild(path, parent, id); err != nil {
				t.Fatal(err)
			}
			view := api.RelayView{Now: func() time.Time { return now }}
			read := func() (map[string]projection, error) {
				out := map[string]projection{}
				switch verb {
				case "status":
					r, err := api.Status(s, view, id)
					out[id] = projection{r.State, r.FamilyState, r.StateDetail}
					return out, err
				case "get":
					r, err := api.Get(s, view, id)
					out[id] = projection{r.State, r.FamilyState, r.StateDetail}
					return out, err
				default:
					r, err := api.List(s, view, api.ListParams{})
					for _, row := range r.Spawns {
						out[row.InstanceID] = projection{state: row.State, family: row.FamilyState}
					}
					return out, err
				}
			}
			var entered api.StatusResult
			if _, _, ok := apitest.RepeatUntilCommitted(func() bool {
				entered, err = api.Status(s, view, id)
				return err != nil || entered.State == store.StateNeedsIntelligence
			}); !ok || err != nil || entered.State != store.StateNeedsIntelligence || entered.FamilyState != store.StateWorking || entered.Since == "" {
				t.Fatalf("entry=%+v %v", entered, err)
			}
			if err := apitest.SetPermissionFacts(path, samples[0].Request.RequestID, apitest.PermissionProof(now)); err != nil {
				t.Fatal(err)
			}
			if a := storefix.ApplyAgentHook(t, s, id, "Stop", ""); !a.Applied {
				t.Fatalf("Stop=%+v", a)
			}
			if row, err := s.GetSpawn(id); err != nil || row.State != store.StateWaiting || row.CommittedDisplay().State != store.StateNeedsIntelligence {
				t.Fatalf("Stop did not retain the display on raw waiting: %+v %v", row, err)
			}
			now = now.Add(time.Hour)
			reached, free, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var once sync.Once
			release := func() { once.Do(func() { close(free) }) }
			// Park the first busy_timeout restoration after the COMMIT of the
			// transaction that wrote the display observation, and no other.
			var observing, committed, parked atomic.Bool
			store.ObserveDecisionBoundaryForTest(t, s, path, func(_ context.Context, q string) error {
				switch {
				case q == "BEGIN IMMEDIATE":
					observing.Store(false)
				case strings.HasPrefix(q, "UPDATE spawns SET display_observation="):
					observing.Store(true)
				case q == fmt.Sprintf("PRAGMA busy_timeout = %d", store.DefaultBusyTimeoutMs) && committed.Load() && parked.CompareAndSwap(false, true):
					close(reached)
					<-free
				}
				return nil
			}, func(context.Context) error {
				if observing.Load() {
					committed.Store(true)
				}
				return nil
			})
			mark := store.TrailMark(t)
			var got map[string]projection
			go func() {
				defer close(done)
				apitest.RepeatUntilCommitted(func() bool {
					got, err = read()
					return committed.Load() || err != nil
				})
			}()
			t.Cleanup(func() {
				release()
				<-done
			})
			select {
			case <-reached:
			case <-done:
				select {
				case <-reached:
				default:
					t.Fatalf("confirmed cleanup was not reached: %+v %v", got, err)
				}
			}
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Errorf("%s waited for confirmed-COMMIT cleanup", verb)
				release()
				<-done
			}
			want := map[string]projection{id: {state: store.StateWaiting, family: store.StateWaiting}}
			if verb == "list" {
				want[parent] = projection{state: store.StateWaiting, family: store.StateWaiting}
			}
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Errorf("committing %s during cleanup=%+v %v; want its committed exit %+v", verb, got, err, want)
			}
			release()
			after, err := s.GetSpawn(id)
			if err != nil || after.State != store.StateWaiting || after.DisplayObservation != "" || after.FamilyState != store.StateWaiting {
				t.Errorf("confirmed exit not durable: %+v %v", after, err)
			}
			if family, _, _, err := apitest.ReadFamilyHistory(path, parent); err != nil || family != store.StateWaiting {
				t.Errorf("ancestor history=%s %v; want waiting", family, err)
			}
			assertDisplayRecords(t, mark, id, displayRecord{store.StateNeedsIntelligence, store.StateWaiting, now})
		})
	}
}
