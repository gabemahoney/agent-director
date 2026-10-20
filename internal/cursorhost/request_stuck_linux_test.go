//go:build linux

package cursorhost

import (
	"encoding/json"
	"io"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/cursorfix"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// observeGet is api.Get judged against the committed row: see
// apitest.ObserveStuckRead.
func observeGet(t *testing.T, s *store.Store, v api.RelayView, id, want string, commit apitest.CommitCheck) api.SpawnRow {
	t.Helper()
	var row api.SpawnRow
	apitest.ObserveStuckRead(t, s, id, want, commit, func() (apitest.StuckReading, error) {
		var err error
		row, err = api.Get(s, v, id)
		return apitest.StuckReading{State: row.State, Reason: row.StuckReason, Since: row.Since, RequestToken: row.RequestToken, ToolName: row.ToolName}, err
	})
	return row
}

func TestPermissionStuckCursorValidatedAdmissions(t *testing.T) {
	for _, relay := range []string{"on", "off"} {
		for _, sample := range cursorfix.Authorizations(t) {
			t.Run(relay+"/"+sample.Name, func(t *testing.T) {
				var params struct {
					Session string `json:"sessionId"`
				}
				if err := json.Unmarshal(sample.Params, &params); err != nil {
					t.Fatal(err)
				}
				if params.Session == "" {
					params.Session = "plan-session"
				}
				e := newRequestEnv(t, relay, params.Session)
				c := e.controller(t, &observedWire{}, io.Discard)
				r := e.admit(t, c, string(sample.ID), sample.Method, sample.Params)
				pr := e.request(t, r.token)
				start := pr.SettledAt
				if relay == "off" {
					start = pr.AdmittedAt
				}
				if start.IsZero() || pr.Hook != (store.ProcessIdentity{}) || pr.AdmittedAt.IsZero() {
					t.Fatalf("missing native clock/null identity: %+v", pr)
				}
				if relay == "on" && !r.settle.Equal(r.timeout.Add(requestReserve)) {
					t.Fatal("settlement reserve changed")
				}
				reopened, err := store.Open(e.path)
				if err != nil {
					t.Fatal(err)
				}
				defer reopened.Close()
				var since string
				for _, offset := range []time.Duration{-time.Millisecond, 0, 30*time.Second - time.Millisecond, 30 * time.Second} {
					now := start.Add(offset)
					v := api.RelayView{Now: func() time.Time { return now }}
					want, commit := store.StateCheckPermission, apitest.CommitCheck(nil)
					if offset == 30*time.Second {
						// The entry is the one read that must commit.
						want, commit = "stuck", apitest.VersionAdvanced
					}
					row := observeGet(t, reopened, v, e.binding.InstanceID, want, commit)
					if offset == 30*time.Second {
						since = row.Since
					}
					delivery, err := api.GetPermission(reopened, v, api.GetPermissionParams{RequestToken: r.token})
					if err != nil {
						t.Fatal(err)
					}
					if delivery.HookAlive != nil || !delivery.ConfirmBy.Equal(pr.SettledAt) {
						t.Fatalf("reader rearmed native deadline/identity: %+v", delivery)
					}
				}
				// Ordinary work and Stop/Interrupt do not prove unknown-origin dialogs gone.
				for _, event := range []string{"PreToolUse", "Stop", "Interrupt"} {
					gate := store.HookGate{Event: event, ParentPID: e.binding.GatewayPID, ParentStart: e.binding.GatewayStart, SessionID: e.binding.SessionID, Cursor: &e.binding}
					target := store.StateWorking
					if event != "PreToolUse" {
						target = store.StateWaiting
					}
					if applied, err := e.s.ApplyHookTransition(e.binding.InstanceID, gate, target, false, event, "", false); err != nil || !applied.Applied {
						t.Fatalf("%s %+v %v", event, applied, err)
					}
				}
				now := start.Add(time.Minute)
				row := observeGet(t, reopened, api.RelayView{Now: func() time.Time { return now }}, e.binding.InstanceID, "stuck", nil)
				if row.Since != since {
					t.Fatalf("native hold restarted: %+v; want since %s", row, since)
				}
				if pr := e.request(t, r.token); !pr.AwaitsAnswer() || pr.ProvenGone() {
					t.Fatalf("native hold consumed: %+v", pr)
				}
				check := func(store.Spawn, []store.PermissionRow) error { return nil }
				if ok, err := e.s.RecordPaneOutside(e.binding.InstanceID, r.token, "unknown", 0, check); err != nil || !ok {
					t.Fatalf("pane outside=%v %v", ok, err)
				}
				row, err = api.Get(reopened, api.RelayView{Now: func() time.Time { return now }}, e.binding.InstanceID)
				if err != nil || row.State == "stuck" || row.StuckInfo != (api.StuckInfo{}) || len(row.UnprovenRequests) != 1 {
					t.Fatalf("answered report/hold=%+v %v", row, err)
				}
			})
		}
	}
}
