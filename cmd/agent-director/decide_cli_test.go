package main_test

import (
	"os"
	"path/filepath"
	"testing"
)

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
