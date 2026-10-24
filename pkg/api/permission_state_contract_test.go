package api_test

// b.zpv regression: a row with an open, relay-tracked permission request
// stays check_permission however long it waits, and every read keeps the
// real lifecycle state in state with any Displayed State in displayed_state
// (apitest.CheckDisplayedStateContract).

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// TestPermissionRequestStaysCheckPermission: Claude, Codex and Cursor with the
// relay on, and Cursor with it off, each with one open request fallen back
// at start, read check_permission with no Displayed State detail on status,
// get and list, are listed by --state check_permission (and by the widened
// --state check_permission,needs_intelligence) and not by --state
// needs_intelligence, and get and each list row keep exactly the one open
// request: at the timeout and 24 h on,
// with [state] needs_intelligence_timeout_seconds at its default and its
// minimum.
func TestPermissionRequestStaysCheckPermission(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ agent, relay string }{
		{store.AgentClaude, "on"}, {store.AgentCodex, "on"}, {store.AgentCursor, "on"}, {store.AgentCursor, "off"},
	} {
		for _, timeout := range []int64{0, 1} {
			tc, timeout := tc, timeout
			t.Run(fmt.Sprintf("%s/relay-%s/timeout-%d", tc.agent, tc.relay, timeout), func(t *testing.T) {
				t.Parallel()
				e := newRelayEnvAgent(t, relayEnvHook, tc.agent)
				start := displayAt
				e.now = start
				token := storefix.TestRequestTokenA
				if tc.relay == "on" {
					// Hook uncheckable and confirm_by passed: fallen back at start.
					e.ns = ""
					requestFacts(t, e, apitest.PermissionTimes(start.Add(-24*time.Hour), time.Time{}, start, time.Time{}, time.Time{}, ""))
				} else {
					id, err := apitest.SeedSpawn(e.dbPath, "cursor-off", store.StateCheckPermission, "", "off", "", false, apitest.WithAgent(tc.agent), apitest.WithNoPane())
					if err != nil {
						t.Fatal(err)
					}
					seed, err := apitest.SeedPermissionRequest(e.dbPath, id, "Bash", apitest.PermissionTimes(start.Add(-24*time.Hour), start, time.Time{}, time.Time{}, time.Time{}, ""))
					if err != nil {
						t.Fatal(err)
					}
					e.id, token = id, seed.RequestToken
				}
				c := needsIntelligenceClient(t, e, 0, timeout)
				first := time.Duration(config.DefaultNeedsIntelligenceTimeoutSeconds) * time.Second
				if timeout > 0 {
					first = time.Duration(timeout) * time.Second
				}
				for _, wait := range []time.Duration{first, 24 * time.Hour} {
					e.now = start.Add(wait)
					label := fmt.Sprintf("wait %s", wait)
					st, err := c.Status(e.id)
					if err != nil {
						t.Fatal(err)
					}
					apitest.CheckRealStateWire(t, label+" status", zpvJSON(t, st), "status", store.StateCheckPermission, "")
					row, err := c.Get(e.id)
					if err != nil {
						t.Fatal(err)
					}
					apitest.CheckRealStateWire(t, label+" get", zpvJSON(t, row), "get", store.StateCheckPermission, "")
					zpvOneOpenRequest(t, label+" get", row.PermissionRequests, token)
					if all, ok := zpvListed(t, c, e.id); !ok {
						t.Errorf("ZPV-FAIL %s list omits the row", label)
					} else {
						apitest.CheckRealStateWire(t, label+" list", zpvJSON(t, all), "list", store.StateCheckPermission, "")
						zpvOneOpenRequest(t, label+" list", all.PermissionRequests, token)
					}
					if filtered, ok := zpvListed(t, c, e.id, store.StateCheckPermission); !ok {
						t.Errorf("ZPV-FAIL %s list --state check_permission omits the row", label)
					} else {
						apitest.CheckRealStateWire(t, label+" list --state check_permission", zpvJSON(t, filtered), "list", store.StateCheckPermission, "")
						zpvOneOpenRequest(t, label+" list --state check_permission", filtered.PermissionRequests, token)
					}
					// Naming a Displayed State widens the fetch, so the row's
					// requests are read only after it matches.
					if widened, ok := zpvListed(t, c, e.id, store.StateCheckPermission, api.StateNeedsIntelligence); !ok {
						t.Errorf("ZPV-FAIL %s list --state check_permission,needs_intelligence omits the row", label)
					} else {
						apitest.CheckRealStateWire(t, label+" list --state check_permission,needs_intelligence", zpvJSON(t, widened), "list", store.StateCheckPermission, "")
						zpvOneOpenRequest(t, label+" list --state check_permission,needs_intelligence", widened.PermissionRequests, token)
					}
					if _, ok := zpvListed(t, c, e.id, api.StateNeedsIntelligence); ok {
						t.Errorf("ZPV-FAIL %s list --state needs_intelligence lists the row", label)
					}
				}
			})
		}
	}
}

// TestDisplayedStateContract: the Go client keeps
// apitest.CheckDisplayedStateContract over displaySurfaceClient's rows.
func TestDisplayedStateContract(t *testing.T) {
	t.Parallel()
	c := displaySurfaceClient(t)
	apitest.CheckDisplayedStateContract(t, apitest.DisplayedStateSurface{
		Name: "go",
		Read: func(verb, id string) []byte {
			if verb == "status" {
				res, err := c.Status(id)
				if err != nil {
					t.Fatal(err)
				}
				return zpvJSON(t, res)
			}
			res, err := c.Get(id)
			if err != nil {
				t.Fatal(err)
			}
			return zpvJSON(t, res)
		},
		List: func(states []string, limit int) []byte {
			res, err := c.List(api.ListParams{State: states, Limit: limit})
			if err != nil {
				t.Fatal(err)
			}
			return zpvJSON(t, res)
		},
	})
}

// zpvListed returns id's row of c's list for states (none: no filter).
func zpvListed(t *testing.T, c *api.Client, id string, states ...string) (api.ListRow, bool) {
	t.Helper()
	res, err := c.List(api.ListParams{State: states})
	if err != nil {
		t.Fatalf("List(%v): %v", states, err)
	}
	for _, r := range res.Spawns {
		if r.InstanceID == id {
			return r, true
		}
	}
	return api.ListRow{}, false
}

// zpvOneOpenRequest fails unless reqs is exactly the one open (undecided)
// request token.
func zpvOneOpenRequest(t *testing.T, label string, reqs []api.PermissionRequestInfo, token string) {
	t.Helper()
	if len(reqs) != 1 || reqs[0].RequestToken != token || reqs[0].Decision != nil {
		t.Errorf("ZPV-FAIL %s permission_requests=%+v; want the one open request %s", label, reqs, token)
	}
}

func zpvJSON(t testing.TB, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
