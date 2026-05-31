package api_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// TestGetPermissionOpenRow pins SR-9.2 case 1: an open (undecided)
// permission_requests row surfaces all eight result fields, with the three
// nullable fields (Decision, DecisionReason, DecidedAt) carrying nil
// pointers that marshal to literal JSON null. tool_input passes through
// byte-identical to the raw JSON seed.
func TestGetPermissionOpenRow(t *testing.T) {
	s, _ := apitest.SeedDecideFixture(t, "on")
	const rawInput = `{"file":"/tmp/x","mode":"rw"}`
	if err := s.UpsertOpenPermissionRequest("id-d-1", storefix.TestRequestTokenA, "Read", rawInput, 0); err != nil {
		t.Fatalf("UpsertOpenPermissionRequest: %v", err)
	}

	got, err := api.GetPermission(s, api.GetPermissionParams{
		RequestToken: storefix.TestRequestTokenA,
	})
	if err != nil {
		t.Fatalf("GetPermission: %v", err)
	}
	if got.RequestToken != storefix.TestRequestTokenA {
		t.Errorf("RequestToken = %q; want %q", got.RequestToken, storefix.TestRequestTokenA)
	}
	if got.RequestID == 0 {
		t.Errorf("RequestID = 0; want non-zero autoincrement id")
	}
	if got.ToolName != "Read" {
		t.Errorf("ToolName = %q; want Read", got.ToolName)
	}
	if got.ToolInput != rawInput {
		t.Errorf("ToolInput = %q; want %q (raw JSON, no re-encode)", got.ToolInput, rawInput)
	}
	if got.RequestedAt.IsZero() {
		t.Errorf("RequestedAt is zero; want populated created_at")
	}
	if got.Decision != nil {
		t.Errorf("Decision = %v; want nil pointer for open row", *got.Decision)
	}
	if got.DecisionReason != nil {
		t.Errorf("DecisionReason = %v; want nil pointer for open row", *got.DecisionReason)
	}
	if got.DecidedAt != nil {
		t.Errorf("DecidedAt = %v; want nil pointer for open row", *got.DecidedAt)
	}

	// JSON shape: nullable fields must marshal to literal `null`, not be absent.
	out, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	for _, want := range [][]byte{
		[]byte(`"decision":null`),
		[]byte(`"decision_reason":null`),
		[]byte(`"decided_at":null`),
	} {
		if !bytes.Contains(out, want) {
			t.Errorf("JSON missing %q; got %s", want, out)
		}
	}
}

// TestGetPermissionClosedAllow pins SR-9.2 case 2: an allow row carries
// a non-nil Decision pointer ("allow"), a nil DecisionReason pointer
// (SR-1.3 allow rows carry no reason annotation), and a non-nil
// DecidedAt pointer parseable as RFC3339.
func TestGetPermissionClosedAllow(t *testing.T) {
	s, _ := apitest.SeedDecideFixture(t, "on")
	apitest.SeedPermissionRow(t, s, "id-d-1")
	updated, err := s.DecidePermissionRequest("id-d-1", storefix.TestRequestTokenA, "allow", "")
	if err != nil {
		t.Fatalf("DecidePermissionRequest: %v", err)
	}
	if !updated {
		t.Fatalf("DecidePermissionRequest reported updated=false; want true")
	}

	got, err := api.GetPermission(s, api.GetPermissionParams{
		RequestToken: storefix.TestRequestTokenA,
	})
	if err != nil {
		t.Fatalf("GetPermission: %v", err)
	}
	if got.Decision == nil || *got.Decision != "allow" {
		t.Errorf("Decision = %v; want pointer to \"allow\"", got.Decision)
	}
	if got.DecisionReason != nil {
		t.Errorf("DecisionReason = %v; want nil for allow row (SR-1.3)", *got.DecisionReason)
	}
	if got.DecidedAt == nil {
		t.Fatal("DecidedAt is nil; want non-nil for decided row")
	}
	// The DecidedAt time round-trips through RFC3339 — the API layer
	// stores it as a *time.Time, so json.Marshal emits RFC3339.
	raw, err := json.Marshal(got.DecidedAt)
	if err != nil {
		t.Fatalf("json.Marshal(DecidedAt): %v", err)
	}
	var ts string
	if err := json.Unmarshal(raw, &ts); err != nil {
		t.Fatalf("DecidedAt JSON not a string: %v (raw=%s)", err, raw)
	}
	if _, err := time.Parse(time.RFC3339, ts); err != nil {
		t.Errorf("DecidedAt %q not RFC3339-parseable: %v", ts, err)
	}

	// JSON shape: decision_reason MUST still appear (as null), never omitted.
	out, _ := json.Marshal(got)
	if !bytes.Contains(out, []byte(`"decision_reason":null`)) {
		t.Errorf("allow row JSON missing decision_reason:null; got %s", out)
	}
}

// TestGetPermissionClosedDeny is table-driven across the three canonical
// decision_reason values per SR-1.3. Each variant pins decision="deny" and
// the exact reason string surfacing through the *string pointer.
func TestGetPermissionClosedDeny(t *testing.T) {
	cases := []struct {
		name   string
		reason string
	}{
		{"operator", store.DecisionReasonOperator},
		{"timeout", store.DecisionReasonTimeout},
		{"find_missing", store.DecisionReasonFindMissing},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := apitest.SeedDecideFixture(t, "on")
			apitest.SeedPermissionRow(t, s, "id-d-1")
			updated, err := s.DecidePermissionRequest("id-d-1", storefix.TestRequestTokenA, "deny", tc.reason)
			if err != nil {
				t.Fatalf("DecidePermissionRequest: %v", err)
			}
			if !updated {
				t.Fatalf("DecidePermissionRequest updated=false; want true")
			}

			got, err := api.GetPermission(s, api.GetPermissionParams{
				RequestToken: storefix.TestRequestTokenA,
			})
			if err != nil {
				t.Fatalf("GetPermission: %v", err)
			}
			if got.Decision == nil || *got.Decision != "deny" {
				t.Errorf("Decision = %v; want pointer to \"deny\"", got.Decision)
			}
			if got.DecisionReason == nil || *got.DecisionReason != tc.reason {
				t.Errorf("DecisionReason = %v; want pointer to %q", got.DecisionReason, tc.reason)
			}
			if got.DecidedAt == nil {
				t.Errorf("DecidedAt is nil; want non-nil for decided row")
			}
		})
	}
}

// TestGetPermissionMissingRow pins SR-9.2 case 6: an unknown request_token
// surfaces ErrPermissionRequestNotFound (via the store sentinel re-exported
// through pkg/api). errors.Is must match against the api.* name. An unrelated
// open row in the same store must be unaffected — the lookup is token-scoped,
// no other rows should be touched.
func TestGetPermissionMissingRow(t *testing.T) {
	s, _ := apitest.SeedDecideFixture(t, "on")
	// Unrelated open row — separate token, must survive untouched.
	apitest.SeedPermissionRow(t, s, "id-d-1") // uses TestRequestTokenA

	// A valid UUIDv4 shape but never written.
	const missingToken = "deadbeef-dead-4dea-adea-deadbeefdead"
	_, err := api.GetPermission(s, api.GetPermissionParams{RequestToken: missingToken})
	if !errors.Is(err, api.ErrPermissionRequestNotFound) {
		t.Fatalf("err = %v; want ErrPermissionRequestNotFound (via api.* alias)", err)
	}

	// Cross-check: the unrelated open row is still readable and undecided.
	survivor, err := api.GetPermission(s, api.GetPermissionParams{
		RequestToken: storefix.TestRequestTokenA,
	})
	if err != nil {
		t.Fatalf("unrelated open row should still be readable: %v", err)
	}
	if survivor.Decision != nil {
		t.Errorf("unrelated row Decision = %v; want nil (a miss on another token must not mutate state)", *survivor.Decision)
	}
}

// TestGetPermissionToolInputBytePassthrough pins the byte-identical
// ToolInput contract (SR-7.4): the verb stores the seeded JSON string
// verbatim and never re-encodes, re-orders keys, or normalizes whitespace.
// Seeded with deliberately non-canonical key ordering and embedded
// whitespace to flush any rewrite that round-trips through json.Marshal.
func TestGetPermissionToolInputBytePassthrough(t *testing.T) {
	s, _ := apitest.SeedDecideFixture(t, "on")
	// Non-canonical whitespace + key order. Any JSON round-trip would
	// produce {"command":"ls","extra":"x"} with no spaces.
	const noncanonical = `{ "command" : "ls" , "extra":"x" }`
	if err := s.UpsertOpenPermissionRequest("id-d-1", storefix.TestRequestTokenA, "Bash", noncanonical, 0); err != nil {
		t.Fatalf("UpsertOpenPermissionRequest: %v", err)
	}

	got, err := api.GetPermission(s, api.GetPermissionParams{
		RequestToken: storefix.TestRequestTokenA,
	})
	if err != nil {
		t.Fatalf("GetPermission: %v", err)
	}
	if got.ToolInput != noncanonical {
		t.Errorf("ToolInput = %q; want byte-identical %q (no re-encode, no whitespace normalization)",
			got.ToolInput, noncanonical)
	}
}
