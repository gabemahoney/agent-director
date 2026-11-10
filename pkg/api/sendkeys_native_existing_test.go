package api_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"

	"github.com/gabemahoney/agent-director/internal/inputdelivery"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
)

func TestSendKeysDefaultNativeBackendPreservesExistingOwnership(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"absent", "corrupt capability", "corrupt attempt", "partial input", "supported unready"} {
		t.Run(mode, func(t *testing.T) {
			e, r, launch := sqliteSubmission(t)
			db, err := sql.Open("sqlite", e.dbPath)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			e.env.Input = nil
			params := api.SendKeysParams{InstanceID: r.ID, Text: skpText, AllowPending: true, DeliveryRequestID: "original"}
			if mode == "absent" {
				if _, err := db.Exec(`DELETE FROM input_launches`); err != nil {
					t.Fatal(err)
				}
				// b.qcf: without a launch, ordinary text on this unreported
				// pending Claude row is fallback input, which waits for SessionStart.
				_, err = e.send(params)
				e.assertSessionStartMissing(t, err)
				return
			}
			if mode == "supported unready" {
				if err := os.Remove(launch.EvidenceDir + "/claude-debug.log"); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "partial input" || mode == "corrupt attempt" {
				id := sqliteInputIdentity(r.Spawn, e.storeID)
				l, err := e.st.ReadInputLaunch(context.Background(), r.Token)
				if err != nil {
					t.Fatal(err)
				}
				g := sqliteGuard(r.Spawn, e.storeID)
				l, err = e.st.BindInputLaunch(context.Background(), *l, g)
				if err != nil {
					t.Fatal(err)
				}
				a, err := inputdelivery.NewAttempt(id, params.Text, nil)
				if err != nil {
					t.Fatal(err)
				}
				a.RequestID = params.DeliveryRequestID
				l, a, err = e.st.SaveInputAttempt(context.Background(), *l, g, a)
				if err != nil {
					t.Fatal(err)
				}
				a.Phase, a.BytesAttempted = "text_attempted", len(a.Payload)
				if _, _, err = e.st.SaveInputAttempt(context.Background(), *l, g, a); err != nil {
					t.Fatal(err)
				}
			}
			switch mode {
			case "corrupt capability":
				_, err = db.Exec(`UPDATE input_launches SET record='broken'`)
			case "corrupt attempt":
				_, err = db.Exec(`UPDATE input_attempts SET record='broken'`)
			}
			if err != nil {
				t.Fatal(err)
			}
			_, err = e.send(params)
			if !errors.Is(err, api.ErrSubmissionUnconfirmed) {
				t.Fatalf("unsafe fallback: %v", err)
			}
			e.assertNoCalls(t, tmux.CallSendText, tmux.CallSendKey, tmux.CallSendEnter)
			if mode == "partial input" {
				assertDeliveryError(t, err, "partial_input_unconfirmed", len(params.Text), false)
			}
			if mode == "supported unready" && e.slept.Seconds() != 12 {
				t.Fatalf("readiness budget=%v", e.slept)
			}
			row, err := e.st.GetSpawn(r.ID)
			if err != nil || row.State != store.StatePending {
				t.Fatalf("read changed row: %+v %v", row, err)
			}
		})
	}
}
