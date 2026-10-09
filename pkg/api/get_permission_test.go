package api_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// TestGetPermission pins SR-9.2 on a row by token: an open row with tool_input
// byte for byte (SR-7.4) and decision, decision_reason and decided_at null; an
// allow row with no reason (SR-1.3); a deny row per canonical reason; decided_at
// an RFC3339 string.
func TestGetPermission(t *testing.T) {
	t.Parallel()
	const input = `{ "command" : "ls" , "extra":"x" }` // non-canonical: any re-encode changes it
	cases := []struct {
		name, decision, reason string
		wantJSON               []string
	}{
		{name: "open", wantJSON: []string{`"decision":null`, `"decision_reason":null`, `"decided_at":null`}},
		{name: "allow", decision: "allow", wantJSON: []string{`"decision":"allow"`, `"decision_reason":null`, `"decided_at":"`}},
		{name: "deny operator", decision: "deny", reason: store.DecisionReasonOperator},
		{name: "deny timeout", decision: "deny", reason: store.DecisionReasonTimeout},
		{name: "deny find_missing", decision: "deny", reason: store.DecisionReasonFindMissing},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := apitest.SeedDecideFixture(t, "on")
			openAgentRequest(t, s, "id-d-1", storefix.TestRequestTokenA, "Bash", input, 0)
			if tc.decision != "" {
				if ok, err := s.DecidePermissionRequest("id-d-1", storefix.TestRequestTokenA, tc.decision, tc.reason, ""); err != nil || !ok {
					t.Fatalf("DecidePermissionRequest = %v, %v", ok, err)
				}
			}
			got, err := api.GetPermission(s, api.RelayView{}, api.GetPermissionParams{RequestToken: storefix.TestRequestTokenA})
			if err != nil {
				t.Fatalf("GetPermission: %v", err)
			}
			if got.RequestToken != storefix.TestRequestTokenA || got.RequestID == 0 || got.ToolName != "Bash" ||
				got.ToolInput != input || got.RequestedAt.IsZero() {
				t.Errorf("row = %+v; want token A, a request id, Bash, tool_input %q byte for byte, a requested_at", got, input)
			}
			if deref(got.Decision) != tc.decision || deref(got.DecisionReason) != tc.reason ||
				(got.DecidedAt == nil) != (tc.decision == "") {
				t.Errorf("decision %v, reason %v, decided_at %v; want %q, %q, set %t",
					got.Decision, got.DecisionReason, got.DecidedAt, tc.decision, tc.reason, tc.decision != "")
			}
			if got.DecidedAt != nil {
				if _, err := time.Parse(time.RFC3339, strings.Trim(jsonOf(t, got.DecidedAt), `"`)); err != nil {
					t.Errorf("decided_at %s: %v; want RFC3339", jsonOf(t, got.DecidedAt), err)
				}
			}
			for _, w := range tc.wantJSON {
				if out := jsonOf(t, got); !strings.Contains(out, w) {
					t.Errorf("JSON %s; want %s", out, w)
				}
			}
		})
	}
}

// deref is *p, or "" for nil.
func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// TestGetPermissionNotFound pins SR-9.2 case 6 and SR-11.6: a token never
// written and one evicted by the cap both return ErrPermissionRequestNotFound,
// and the open row beside them is still readable and undecided.
func TestGetPermissionNotFound(t *testing.T) {
	t.Parallel()
	s, dbPath := apitest.SeedDecideFixture(t, "on")
	tokens := storefix.SeedClosedPermissionRequests(t, s, dbPath, "id-d-1", 5,
		time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC), time.Minute)
	// The sixth row, under a cap of 5, evicts the oldest closed one, tokens[0].
	openAgentRequest(t, s, "id-d-1", storefix.TestRequestTokenB, "Bash", `{"cmd":"ls"}`,
		config.Relay{PermissionRequestCap: 5}.PermissionRequestCap)
	for _, tok := range []string{"deadbeef-dead-4dea-adea-deadbeefdead", tokens[0]} {
		if _, err := api.GetPermission(s, api.RelayView{}, api.GetPermissionParams{RequestToken: tok}); !errors.Is(err, api.ErrPermissionRequestNotFound) {
			t.Errorf("GetPermission(%s) err = %v; want ErrPermissionRequestNotFound", tok, err)
		}
	}
	if got, err := api.GetPermission(s, api.RelayView{}, api.GetPermissionParams{RequestToken: storefix.TestRequestTokenB}); err != nil || got.Decision != nil {
		t.Errorf("open row = %+v, %v; want readable and undecided", got, err)
	}
}
