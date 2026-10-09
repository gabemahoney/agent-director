package main_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// TestDecideCalledEmitsTrailLine: each decide outcome, the CLI's own
// ErrInvalidFlags refusals included (a missing --request-token, a negative
// --max-wait-ms; decideHandlerWith emits those itself, SR-A-2.4), writes
// exactly one ad.decide.called line with the outcome, source ad_decide, a ts
// and the caller identity; only an applied decide writes an
// ad.row_mutation.committed line. An applied decide prints the request's
// delivery facts (b.146 rule 15); with --max-wait-ms under a write lock held
// past it, decide is ErrStoreBusy within the bound plus 1 s (and the process's
// start) and records nothing (decision 9 B). ErrAmbiguousRequest has no CLI
// path: the flag check requires --request-token.
func TestDecideCalledEmitsTrailLine(t *testing.T) {
	cases := []struct {
		outcome  string
		prepare  func(t *testing.T, home, id, token string) // runs before the asserted decide
		argv     func(id, token string) []string
		mutation bool
	}{
		{outcome: "ok", mutation: true},
		{outcome: "ErrStoreBusy", prepare: func(t *testing.T, home, _, _ string) {
			apitest.HoldWriteLock(t, stateDB(home), time.Minute)
		}, argv: func(id, token string) []string { return append(decideArgv(id, token), "--max-wait-ms", "300") }},
		{outcome: "ErrInvalidFlags", argv: func(id, token string) []string {
			return append(decideArgv(id, token), "--max-wait-ms", "-1")
		}},
		{outcome: "ErrAlreadyDecided", prepare: func(t *testing.T, home, id, token string) {
			if _, stderr, code := runCLIWithHome(t, home, decideArgv(id, token)...); code != 0 {
				t.Fatalf("first decide exit = %d; stderr=%q", code, stderr)
			}
		}},
		{outcome: "ErrRelayFallenBack", prepare: func(t *testing.T, home, id, token string) {
			// Backdated 48h, far past the binary's default window plus margin (SR-4.4).
			s, err := store.Open(stateDB(home))
			if err != nil {
				t.Fatalf("store.Open: %v", err)
			}
			defer s.Close()
			storefix.SeedUndeliverablePermissionRequest(t, s, stateDB(home), id, token, 48*time.Hour)
		}},
		{outcome: "ErrInvalidFlags", argv: func(id, _ string) []string {
			return []string{"decide", "--claude-instance-id", id, "--decision", "allow"}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.outcome, func(t *testing.T) {
			home := t.TempDir()
			id, err := apitest.SeedSpawn(stateDB(home), "", store.StateCheckPermission, "", "on", "", true)
			if err != nil {
				t.Fatalf("SeedSpawn: %v", err)
			}
			req, err := apitest.SeedPermissionRequest(stateDB(home), id, "Bash")
			if err != nil {
				t.Fatalf("SeedPermissionRequest: %v", err)
			}
			if tc.prepare != nil {
				tc.prepare(t, home, id, req.RequestToken)
			}
			before := trailOrNil(t, home)
			argv := decideArgv(id, req.RequestToken)
			if tc.argv != nil {
				argv = tc.argv(id, req.RequestToken)
			}

			start := time.Now()
			stdout, stderr, code := runCLIWithHome(t, home, argv...)
			took := time.Since(start)

			if tc.outcome == "ok" {
				var res map[string]any
				if code != 0 || json.Unmarshal([]byte(stdout), &res) != nil || res["delivery"] != "not_confirmed" {
					t.Fatalf("decide exit = %d, stdout = %q; want 0 and the delivery facts, not_confirmed (stderr=%q)", code, stdout, stderr)
				}
				assertDeliveryKeys(t, "decide", res)
			} else {
				assertOnlyEnvelope(t, stdout, stderr, code, tc.outcome)
			}
			if tc.outcome == "ErrStoreBusy" {
				if limit := 300*time.Millisecond + time.Second + cliStartAllowance; took > limit {
					t.Errorf("decide --max-wait-ms 300 under a held lock took %v; want within %v", took, limit)
				}
				if pr, err := openDBForRead(t, home).GetPermissionRequest(id, req.RequestToken); err != nil || pr.Decision != "" {
					t.Errorf("request after ErrStoreBusy = decision %q, %v; want nothing recorded", pr.Decision, err)
				}
			}
			after := readTrailLines(t, home)
			dc := eventsOf(after[len(before):], "ad.decide.called")
			if len(dc) != 1 {
				t.Fatalf("ad.decide.called lines = %v; want exactly one", dc)
			}
			row := dc[0]
			if ts, _ := row["ts"].(string); row["outcome"] != tc.outcome || row["source"] != "ad_decide" || ts == "" {
				t.Errorf("outcome = %v, source = %v, ts = %v; want %s, ad_decide and a ts", row["outcome"], row["source"], row["ts"], tc.outcome)
			}
			for _, field := range []string{"caller_process", "caller_hostname", "caller_user"} {
				if s, _ := row[field].(string); s == "" {
					t.Errorf("%s empty", field)
				}
			}
			if pid, _ := row["caller_pid"].(float64); pid == 0 {
				t.Errorf("caller_pid = 0; want the subprocess pid")
			}
			for _, field := range []string{"claude_instance_id", "request_token", "submitted_decision", "submitted_decision_reason"} {
				if _, present := row[field]; !present {
					t.Errorf("required field %q missing", field)
				}
			}
			rm := eventsOf(after[len(before):], "ad.row_mutation.committed")
			if tc.mutation != (len(rm) == 1) || len(rm) > 1 {
				t.Errorf("ad.row_mutation.committed lines = %v; want one only when the decide applied", rm)
			} else if tc.mutation && (rm[0]["writer_process"] != "decide" || rm[0]["decision"] != "allow") {
				t.Errorf("row mutation = %v; want writer_process decide, decision allow", rm[0])
			}
		})
	}
}

// cliStartAllowance is how long the built CLI may take to start and open its
// store, on top of a bound it was given.
const cliStartAllowance = 2 * time.Second

// deliveryKeys are a permission request's delivery facts (b.146 rule 15).
var deliveryKeys = []string{"delivery", "confirm_by", "hook_alive", "hook_gone_at", "attempted_decision", "attempted_at", "tool_use_id"}

// assertDeliveryKeys fails unless m, what's JSON object, carries every
// delivery fact as a key (null when unset, never omitted).
func assertDeliveryKeys(t *testing.T, what string, m map[string]any) {
	t.Helper()
	for _, k := range deliveryKeys {
		if _, ok := m[k]; !ok {
			t.Errorf("%s JSON %v has no %q", what, m, k)
		}
	}
}

// openDBForRead opens home's store for the rest of the test.
func openDBForRead(t *testing.T, home string) *store.Store {
	t.Helper()
	s, err := store.Open(stateDB(home))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// decideArgv is decide allow on id's request token.
func decideArgv(id, token string) []string {
	return []string{"decide", "--claude-instance-id", id, "--request-token", token, "--decision", "allow"}
}
