package main_test

import (
	"path/filepath"
	"testing"
)

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
			_, stderr1, code1 := runSpawnCLI(t, home, fakeDir,
				"decide",
				"--claude-instance-id", id,
				"--request-token", testRequestToken,
				"--decision", tc.first)
			if code1 != 0 {
				t.Fatalf("first decide (%s) exit = %d; want 0 (stderr=%s)", tc.first, code1, stderr1)
			}

			// Second decide on the same token: must return ErrAlreadyDecided.
			_, stderr2, code2 := runSpawnCLI(t, home, fakeDir,
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
		})
	}
}
