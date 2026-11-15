package store

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestBusySendDeliveriesShareTheRowReadTransaction (repair sf2o-2 CR1, CR7):
// a row read's delivery facts are read in the row's own SQLite read
// transaction. A second connection publishes a reuse's new conversation and
// Launch after the row SELECT and before the first delivery query; the facts
// taken are still the row's: its published conversation's Held Message and
// Agent Defaults, not the new conversation's. That holds for list's batched
// defaults (ListSpawns, NewBusySendDefaults) and for the full observation
// through both readers (ListSpawns for get, RecoveryRowsWithDelivery for
// status).
func TestBusySendDeliveriesShareTheRowReadTransaction(t *testing.T) {
	for _, tc := range []struct {
		name           string
		full, recovery bool
	}{
		{"list defaults", false, false},
		{"get observation", true, false},
		{"status observation", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, path := openTempStore(t)
			id := busyLaunchInsert(t, s, AgentClaude)
			if _, err := s.AdmitBusySend(BusySendAdmissionRequest{InstanceID: id, Text: []byte("held")}); err != nil {
				t.Fatal(err)
			}
			old := busyLaunchFinish(t, s, id)
			if old.BusySend != BusySendCovered {
				t.Fatalf("fixture row coverage = %s; want covered", old.BusySend)
			}
			other, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer other.Close()
			// The second connection's publication: a reuse whose new
			// conversation confirms by default, and that retires the old one's
			// Held Message.
			publish := func() {
				prior, _, err := other.ReadForReuse(id)
				if err != nil {
					t.Fatal(err)
				}
				confirmed := true
				const token = "1122334455667788"
				res, _, v, err := other.ResetForReuse(id, prior.Snapshot, prior.Agent, Spawn{Agent: AgentClaude, CWD: t.TempDir(),
					TmuxSessionName: old.TmuxSessionName, RelayMode: "off", StartedAt: time.Unix(2, 0), LaunchStartedAtMillis: 2000,
					Identity: LaunchIdentity{Token: token}, BusySendDefaults: BusySendDefaultsChoice{Confirmed: &confirmed},
					BusySendExit: busyLaunchExit(t, other, id)})
				if err != nil || res != CondApplied {
					t.Fatalf("reuse = %v, %v", res, err)
				}
				if res, err := other.RecordLaunchIdentity(id, v, token, LaunchIdentity{}); err != nil || res != CondApplied {
					t.Fatalf("publication = %v, %v", res, err)
				}
			}
			var armed, begun, published atomic.Bool
			var selects atomic.Int32
			ObserveDecisionBoundaryForTest(t, s, path, func(_ context.Context, q string) error {
				switch {
				case !armed.Load():
				case q == "BEGIN":
					begun.Store(true)
				case begun.Load() && strings.HasPrefix(q, "SELECT") && selects.Add(1) == 2:
					publish()
					published.Store(true)
				}
				return nil
			}, nil)

			d := NewBusySendDefaults()
			if tc.full {
				d = NewBusySendDeliveries()
			}
			armed.Store(true)
			var rows []Spawn
			if tc.recovery {
				rows, err = s.RecoveryRowsWithDelivery([]string{id}, SettleRead{}, d)
			} else {
				rows, err = s.ListSpawns(ListFilters{IDs: []string{id}, Delivery: d})
			}
			armed.Store(false)
			if err != nil || len(rows) != 1 {
				t.Fatalf("row read = %+v, %v", rows, err)
			}
			if !published.Load() {
				t.Fatal("the second connection did not publish between the row read and the delivery read")
			}
			if rows[0].Snapshot != old.Snapshot {
				t.Fatalf("row read after the publication: %+v; want the row as it was", rows[0].Snapshot)
			}
			if defaults, ok, found := d.LookupDefaults(rows[0]); !found || !ok || defaults.Confirmed {
				t.Errorf("defaults taken with the row = %+v ok=%v found=%v; want the row's conversation's (unconfirmed)", defaults, ok, found)
			}
			if tc.full {
				o, found, err := d.Lookup(rows[0])
				if !found || err != nil || o == nil || o.HeldCount != 1 {
					t.Errorf("observation taken with the row = %+v found=%v %v; want the row's conversation's one Held Message", o, found, err)
				}
			}
			// The publication did land: a fresh read sees the new conversation.
			now, err := s.ListSpawns(ListFilters{IDs: []string{id}, Delivery: NewBusySendDefaults()})
			if err != nil || len(now) != 1 || now[0].BusyConversationID == old.BusyConversationID {
				t.Fatalf("fresh read after the publication = %+v, %v", now, err)
			}
		})
	}
}
