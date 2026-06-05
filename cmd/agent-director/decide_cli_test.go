package main_test

import (
	"os"
	"path/filepath"
	"testing"
)

// decideCalledLines filters trail lines for ad.decide.called events.
func decideCalledLines(lines []map[string]any) []map[string]any {
	var out []map[string]any
	for _, l := range lines {
		if l["event"] == "ad.decide.called" {
			out = append(out, l)
		}
	}
	return out
}

// trailLinesOrNil opens the trail file in stateDir and returns parsed lines,
// or nil when the file does not exist. Used by decide and find-missing CLI
// tests to assert row-mutation emission without failing on a missing file.
func trailLinesOrNil(t *testing.T, stateDir string) []map[string]any {
	t.Helper()
	path := filepath.Join(stateDir, "ad-trail.jsonl")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil
	}
	return readTrailLines(t, stateDir)
}

// rowMutationCommittedLines filters lines for ad.row_mutation.committed events.
// Returns nil (not an empty slice) when none match, so callers can distinguish
// "no file" from "file present but no row-mutation lines".
func rowMutationCommittedLines(lines []map[string]any) []map[string]any {
	var out []map[string]any
	for _, l := range lines {
		if l["event"] == "ad.row_mutation.committed" {
			out = append(out, l)
		}
	}
	return out
}

// TestDecideMissingRequestTokenRejected verifies that the decide CLI verb
// requires --request-token: omitting it yields ErrInvalidFlags before any DB
// access, regardless of the spawn's state.
func TestDecideMissingRequestTokenRejected(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	home := t.TempDir()
	bootstrapDB(t, home)
	dbPath := filepath.Join(home, ".agent-director", "state.db")
	const id = "id-no-token-1"
	seedSpawnRow(t, dbPath, id, "cd-no-token-1", "check_permission", "on")

	_, stderr, code := runSpawnCLI(t, home, fakeDir,
		"decide", "--claude-instance-id", id, "--decision", "allow")
	if code == 0 {
		t.Fatalf("expected non-zero exit; got 0 (stderr=%s)", stderr)
	}
	env := parseEnvelope(t, stderr)
	if env.ErrName != "ErrInvalidFlags" {
		t.Errorf("err_name = %q; want ErrInvalidFlags", env.ErrName)
	}
}

// TestDecideRaceLoserSeesErrAlreadyDecided verifies the first-call-wins
// contract at the CLI boundary: once a token has been decided, any
// subsequent decide call on the same token returns ErrAlreadyDecided,
// regardless of the first and second verdict combination. Three sub-cases
// cover allow→allow, deny→deny, and allow→deny.
//
// Trail assertions:
//   - First decide (success): exactly one ad.row_mutation.committed line with
//     writer_process="decide", decision=first, mutation_kind="update".
//   - Second decide (ErrAlreadyDecided): zero ad.row_mutation.committed lines —
//     the no-op UPDATE path must not emit.
func TestDecideRaceLoserSeesErrAlreadyDecided(t *testing.T) {
	cases := []struct {
		name   string
		first  string
		second string
	}{
		{"allow_then_allow", "allow", "allow"},
		{"deny_then_deny", "deny", "deny"},
		{"allow_then_deny", "allow", "deny"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fakeDir := buildFakeTmux(t)
			home := t.TempDir()
			bootstrapDB(t, home)
			dbPath := filepath.Join(home, ".agent-director", "state.db")
			const id = "id-race-1"
			seedSpawnRow(t, dbPath, id, "cd-race-1", "check_permission", "on")
			seedOpenPermissionRequest(t, dbPath, id, testRequestToken, "Bash", `{"cmd":"echo"}`)

			// First decide: must succeed (exit 0).
			// Use a dedicated stateDir so the trail file is isolated per decide call.
			stateDir1 := t.TempDir()
			_, stderr1, code1 := runSpawnCLIEnv(t, home, fakeDir,
				map[string]string{"AGENT_DIRECTOR_STATE_DIR": stateDir1},
				"decide",
				"--claude-instance-id", id,
				"--request-token", testRequestToken,
				"--decision", tc.first)
			if code1 != 0 {
				t.Fatalf("first decide (%s) exit = %d; want 0 (stderr=%s)", tc.first, code1, stderr1)
			}

			// Trail: exactly one ad.row_mutation.committed with writer_process="decide".
			rm1 := rowMutationCommittedLines(readTrailLines(t, stateDir1))
			if len(rm1) != 1 {
				t.Fatalf("first decide: ad.row_mutation.committed count = %d; want 1", len(rm1))
			}
			if rm1[0]["writer_process"] != "decide" {
				t.Errorf("first decide: writer_process = %v; want decide", rm1[0]["writer_process"])
			}
			if rm1[0]["decision"] != tc.first {
				t.Errorf("first decide: decision = %v; want %q", rm1[0]["decision"], tc.first)
			}
			if rm1[0]["mutation_kind"] != "update" {
				t.Errorf("first decide: mutation_kind = %v; want update", rm1[0]["mutation_kind"])
			}

			// Second decide on the same token: must return ErrAlreadyDecided.
			stateDir2 := t.TempDir()
			_, stderr2, code2 := runSpawnCLIEnv(t, home, fakeDir,
				map[string]string{"AGENT_DIRECTOR_STATE_DIR": stateDir2},
				"decide",
				"--claude-instance-id", id,
				"--request-token", testRequestToken,
				"--decision", tc.second)
			if code2 == 0 {
				t.Fatalf("second decide (%s) exit = 0; want non-zero (ErrAlreadyDecided)", tc.second)
			}
			env := parseEnvelope(t, stderr2)
			if env.ErrName != "ErrAlreadyDecided" {
				t.Errorf("err_name = %q; want ErrAlreadyDecided", env.ErrName)
			}

			// Trail: zero ad.row_mutation.committed lines — the no-op UPDATE must not emit.
			rm2 := rowMutationCommittedLines(trailLinesOrNil(t, stateDir2))
			if len(rm2) != 0 {
				t.Errorf("ErrAlreadyDecided path: expected 0 ad.row_mutation.committed lines; got %d: %v", len(rm2), rm2)
			}
		})
	}
}

// TestDecideCalledEmitsTrailLine is a table-driven test that covers four
// decide-verb outcomes and asserts the ad.decide.called trail line emitted
// on each path. Required top-level fields (source, ts, caller_*, outcome) are
// validated for every row. The ErrAlreadyDecided row additionally asserts zero
// ad.row_mutation.committed lines (Epic 3 no-op contract).
//
// ErrAmbiguousRequest is skipped: the store guard only fires when requestToken
// is empty, but the API layer (pkg/api/decide.go) rejects an empty token with
// ErrMissingRequestToken before the store is reached. There is no CLI path
// that triggers ErrAmbiguousRequest.
func TestDecideCalledEmitsTrailLine(t *testing.T) {
	cases := []struct {
		name        string
		wantOutcome string
	}{
		{name: "ok", wantOutcome: "ok"},
		{name: "ErrAlreadyDecided", wantOutcome: "ErrAlreadyDecided"},
		{name: "ErrInvalidFlags", wantOutcome: "ErrInvalidFlags"},
		{name: "ErrAmbiguousRequest", wantOutcome: "ErrAmbiguousRequest"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "ErrAmbiguousRequest" {
				t.Skip("ErrAmbiguousRequest unreachable via CLI: " +
					"API layer (pkg/api/decide.go) rejects empty request_token with " +
					"ErrMissingRequestToken before the store ambiguity guard fires. " +
					"No CLI path can supply an empty token (flag validator enforces --request-token).")
			}

			stateDir := t.TempDir()
			t.Setenv("AGENT_DIRECTOR_STATE_DIR", stateDir)
			fakeDir := buildFakeTmux(t)
			home := t.TempDir()
			bootstrapDB(t, home)
			dbPath := filepath.Join(home, ".agent-director", "state.db")

			switch tc.name {
			case "ok":
				const id = "id-dc-trail-ok-1"
				seedSpawnRow(t, dbPath, id, "cd-dc-trail-ok-1", "check_permission", "on")
				seedOpenPermissionRequest(t, dbPath, id, testRequestToken, "Bash", `{"cmd":"ls"}`)
				_, _, code := runSpawnCLIEnv(t, home, fakeDir,
					map[string]string{"AGENT_DIRECTOR_STATE_DIR": stateDir},
					"decide",
					"--claude-instance-id", id,
					"--request-token", testRequestToken,
					"--decision", "allow",
				)
				if code != 0 {
					t.Fatalf("decide exit = %d; want 0", code)
				}

			case "ErrAlreadyDecided":
				const id = "id-dc-trail-ad-1"
				seedSpawnRow(t, dbPath, id, "cd-dc-trail-ad-1", "check_permission", "on")
				seedOpenPermissionRequest(t, dbPath, id, testRequestToken, "Bash", `{"cmd":"ls"}`)

				// First decide: route trail to a throwaway dir so stateDir contains
				// only the second decide's events (used for all trail assertions below).
				firstStateDir := t.TempDir()
				_, _, code1 := runSpawnCLIEnv(t, home, fakeDir,
					map[string]string{"AGENT_DIRECTOR_STATE_DIR": firstStateDir},
					"decide",
					"--claude-instance-id", id,
					"--request-token", testRequestToken,
					"--decision", "allow",
				)
				if code1 != 0 {
					t.Fatalf("first decide exit = %d; want 0", code1)
				}

				// Second decide on the already-decided token: must return ErrAlreadyDecided.
				_, _, code2 := runSpawnCLIEnv(t, home, fakeDir,
					map[string]string{"AGENT_DIRECTOR_STATE_DIR": stateDir},
					"decide",
					"--claude-instance-id", id,
					"--request-token", testRequestToken,
					"--decision", "allow",
				)
				if code2 == 0 {
					t.Fatalf("second decide exit = 0; want non-zero (ErrAlreadyDecided)")
				}

			case "ErrInvalidFlags":
				const id = "id-dc-trail-inv-1"
				seedSpawnRow(t, dbPath, id, "cd-dc-trail-inv-1", "check_permission", "on")
				// Omit --request-token: CLI validates it as required before calling the API,
				// so the emission comes from decideHandlerWith (outcome="ErrInvalidFlags").
				runSpawnCLIEnv(t, home, fakeDir,
					map[string]string{"AGENT_DIRECTOR_STATE_DIR": stateDir},
					"decide",
					"--claude-instance-id", id,
					"--decision", "allow",
					// intentionally no --request-token
				)
				// exit code is non-zero by design; ignore it here — the trail is the contract.
			}

			// ---- Trail assertions (common to all non-skipped cases) ----

			lines := readTrailLines(t, stateDir)
			dc := decideCalledLines(lines)
			if len(dc) != 1 {
				t.Fatalf("ad.decide.called line count = %d; want 1 (all trail lines: %v)", len(dc), lines)
			}
			row := dc[0]

			// outcome must match the expected value for this case.
			if row["outcome"] != tc.wantOutcome {
				t.Errorf("outcome = %v; want %q", row["outcome"], tc.wantOutcome)
			}
			// source is always "ad_decide" per SR-A-2.4.
			if row["source"] != "ad_decide" {
				t.Errorf("source = %v; want ad_decide", row["source"])
			}
			// ts must be a non-empty string.
			if ts, ok := row["ts"].(string); !ok || ts == "" {
				t.Errorf("ts = %v; want non-empty string", row["ts"])
			}
			// caller identity fields must be non-empty for all outcomes.
			if p, _ := row["caller_process"].(string); p == "" {
				t.Errorf("caller_process empty")
			}
			if h, _ := row["caller_hostname"].(string); h == "" {
				t.Errorf("caller_hostname empty")
			}
			if u, _ := row["caller_user"].(string); u == "" {
				t.Errorf("caller_user empty")
			}
			// caller_pid is the subprocess PID — assert non-zero.
			// (JSON unmarshals all numbers as float64 in map[string]any.)
			if pid, _ := row["caller_pid"].(float64); pid == 0 {
				t.Errorf("caller_pid = 0; want non-zero subprocess pid")
			}
			// required payload fields must be present (may be empty string for error paths).
			for _, field := range []string{"claude_instance_id", "request_token", "submitted_decision", "submitted_decision_reason"} {
				if _, present := row[field]; !present {
					t.Errorf("required field %q missing from ad.decide.called line", field)
				}
			}

			// ErrAlreadyDecided: the no-op UPDATE must not emit ad.row_mutation.committed.
			if tc.name == "ErrAlreadyDecided" {
				rm := rowMutationCommittedLines(lines)
				if len(rm) != 0 {
					t.Errorf("ErrAlreadyDecided: expected 0 ad.row_mutation.committed lines; got %d: %v", len(rm), rm)
				}
			}
		})
	}
}
