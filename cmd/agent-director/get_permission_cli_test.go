package main_test

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/gabemahoney/agent-director/internal/store"
)

// closePermissionRequest writes decision/decision_reason/decided_at on the
// open row for the (instanceID, requestToken) pair, simulating a prior-cycle
// decision with a specific canonical reason. The decide CLI verb only writes
// DecisionReasonOperator for deny, so the get-permission CLI tests that need
// closed-deny rows with timeout/find_missing reasons fall back to raw SQL.
// Empty reason maps to NULL decision_reason (matches Store.DecidePermissionRequest).
func closePermissionRequest(t *testing.T, dbPath, instanceID, requestToken, decision, reason string) {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()
	var reasonArg any
	if reason != "" {
		reasonArg = reason
	} else {
		reasonArg = nil
	}
	res, err := db.Exec(`
        UPDATE permission_requests
           SET decision = ?, decision_reason = ?, decided_at = CURRENT_TIMESTAMP
         WHERE claude_instance_id = ? AND request_token = ?
    `, decision, reasonArg, instanceID, requestToken)
	if err != nil {
		t.Fatalf("close permission: %v", err)
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		t.Fatalf("close permission affected %d rows; want 1 ((%s, %s) not found?)", n, instanceID, requestToken)
	}
}

// TestGetPermissionOpenRow pins SR-9.2 case 1 at the CLI boundary: an open
// (undecided) permission_requests row → exit 0; stdout is valid JSON
// carrying all eight fields; the three nullable fields (decision,
// decision_reason, decided_at) marshal as literal `null`; stderr is empty.
func TestGetPermissionOpenRow(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	home := t.TempDir()
	bootstrapDB(t, home)
	dbPath := filepath.Join(home, ".agent-director", "state.db")

	const id = "id-gpc-1"
	const toolName = "Read"
	const toolInput = `{"file":"/tmp/x","mode":"rw"}`
	seedSpawnRow(t, dbPath, id, "cd-gpc-1", "check_permission", "on")
	seedOpenPermissionRequest(t, dbPath, id, testRequestToken, toolName, toolInput)

	stdout, stderr, code := runSpawnCLI(t, home, fakeDir,
		"get-permission", "--request-token", testRequestToken)
	if code != 0 {
		t.Fatalf("exit = %d; stderr=%s", code, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr non-empty for success path: %q", stderr)
	}

	// JSON must carry all eight keys, with the three nullable fields as null.
	for _, want := range []string{
		`"decision":null`,
		`"decision_reason":null`,
		`"decided_at":null`,
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout missing %q; got %s", want, stdout)
		}
	}

	var got map[string]any
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("parse stdout %q: %v", stdout, err)
	}
	for _, key := range []string{"request_token", "request_id", "tool_name", "tool_input", "requested_at", "decision", "decision_reason", "decided_at"} {
		if _, present := got[key]; !present {
			t.Errorf("stdout missing key %q (got %s)", key, stdout)
		}
	}
	if got["request_token"] != testRequestToken {
		t.Errorf("request_token = %v; want %q", got["request_token"], testRequestToken)
	}
	if got["tool_name"] != toolName {
		t.Errorf("tool_name = %v; want %q", got["tool_name"], toolName)
	}
	if got["tool_input"] != toolInput {
		t.Errorf("tool_input = %v; want %q (byte-identical)", got["tool_input"], toolInput)
	}
	if got["decision"] != nil {
		t.Errorf("decision = %v; want JSON null for open row", got["decision"])
	}
	if got["decision_reason"] != nil {
		t.Errorf("decision_reason = %v; want JSON null for open row", got["decision_reason"])
	}
	if got["decided_at"] != nil {
		t.Errorf("decided_at = %v; want JSON null for open row", got["decided_at"])
	}
	if rid, _ := got["request_id"].(float64); rid == 0 {
		t.Errorf("request_id = %v; want non-zero", got["request_id"])
	}
}

// TestGetPermissionClosedAllow pins SR-9.2 case 2: an allow row carries
// decision="allow", decision_reason=null (SR-1.3), and decided_at non-null
// RFC3339-parseable.
func TestGetPermissionClosedAllow(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	home := t.TempDir()
	bootstrapDB(t, home)
	dbPath := filepath.Join(home, ".agent-director", "state.db")

	const id = "id-gpc-allow"
	seedSpawnRow(t, dbPath, id, "cd-gpc-allow", "check_permission", "on")
	seedOpenPermissionRequest(t, dbPath, id, testRequestToken, "Bash", `{"cmd":"echo"}`)
	closePermissionRequest(t, dbPath, id, testRequestToken, "allow", "")

	stdout, stderr, code := runSpawnCLI(t, home, fakeDir,
		"get-permission", "--request-token", testRequestToken)
	if code != 0 {
		t.Fatalf("exit = %d; stderr=%s", code, stderr)
	}

	var got map[string]any
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("parse stdout %q: %v", stdout, err)
	}
	if got["decision"] != "allow" {
		t.Errorf("decision = %v; want \"allow\"", got["decision"])
	}
	if got["decision_reason"] != nil {
		t.Errorf("decision_reason = %v; want null (SR-1.3 allow rows carry no reason)", got["decision_reason"])
	}
	decidedAt, ok := got["decided_at"].(string)
	if !ok {
		t.Fatalf("decided_at = %v (%T); want non-null string", got["decided_at"], got["decided_at"])
	}
	if _, err := time.Parse(time.RFC3339, decidedAt); err != nil {
		t.Errorf("decided_at %q not RFC3339-parseable: %v", decidedAt, err)
	}
}

// TestGetPermissionClosedDeny is the table-driven pin for SR-9.2 cases 3–5:
// each canonical decision_reason value (operator, timeout, find_missing)
// surfaces verbatim through the CLI envelope, with decision="deny" and
// decided_at non-null.
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
			fakeDir := buildFakeTmux(t)
			home := t.TempDir()
			bootstrapDB(t, home)
			dbPath := filepath.Join(home, ".agent-director", "state.db")

			id := "id-gpc-deny-" + tc.name
			seedSpawnRow(t, dbPath, id, "cd-gpc-"+tc.name, "check_permission", "on")
			seedOpenPermissionRequest(t, dbPath, id, testRequestToken, "Bash", `{"cmd":"rm -rf /"}`)
			closePermissionRequest(t, dbPath, id, testRequestToken, "deny", tc.reason)

			stdout, stderr, code := runSpawnCLI(t, home, fakeDir,
				"get-permission", "--request-token", testRequestToken)
			if code != 0 {
				t.Fatalf("exit = %d; stderr=%s", code, stderr)
			}

			var got map[string]any
			if err := json.Unmarshal([]byte(stdout), &got); err != nil {
				t.Fatalf("parse stdout %q: %v", stdout, err)
			}
			if got["decision"] != "deny" {
				t.Errorf("decision = %v; want \"deny\"", got["decision"])
			}
			if got["decision_reason"] != tc.reason {
				t.Errorf("decision_reason = %v; want %q", got["decision_reason"], tc.reason)
			}
			if _, ok := got["decided_at"].(string); !ok {
				t.Errorf("decided_at = %v; want non-null string", got["decided_at"])
			}
		})
	}
}

// TestGetPermissionMissingRow pins SR-9.2 case 6: an unwritten request_token
// surfaces ErrPermissionRequestNotFound on the stderr envelope, exit non-zero,
// stdout empty. An unrelated open row in the same store is unaffected.
func TestGetPermissionMissingRow(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	home := t.TempDir()
	bootstrapDB(t, home)
	dbPath := filepath.Join(home, ".agent-director", "state.db")

	// Seed an unrelated open row so we can prove the miss did not touch it.
	const id = "id-gpc-miss-survivor"
	seedSpawnRow(t, dbPath, id, "cd-gpc-miss", "check_permission", "on")
	seedOpenPermissionRequest(t, dbPath, id, testRequestToken, "Read", `{"file":"/etc/hosts"}`)

	// A valid UUIDv4 never written to the store.
	const missingToken = "deadbeef-dead-4dea-adea-deadbeefdead"
	stdout, stderr, code := runSpawnCLI(t, home, fakeDir,
		"get-permission", "--request-token", missingToken)
	if code == 0 {
		t.Fatalf("exit = 0; want non-zero (stdout=%s stderr=%s)", stdout, stderr)
	}
	if stdout != "" {
		t.Errorf("stdout = %q; want empty on error", stdout)
	}
	env := parseEnvelope(t, stderr)
	if env.ErrName != "ErrPermissionRequestNotFound" {
		t.Errorf("err_name = %q; want ErrPermissionRequestNotFound", env.ErrName)
	}

	// Cross-check: unrelated open row is still readable and undecided.
	survivorStdout, survivorStderr, survivorCode := runSpawnCLI(t, home, fakeDir,
		"get-permission", "--request-token", testRequestToken)
	if survivorCode != 0 {
		t.Fatalf("unrelated row should still be readable: exit=%d stderr=%s", survivorCode, survivorStderr)
	}
	var survivor map[string]any
	if err := json.Unmarshal([]byte(survivorStdout), &survivor); err != nil {
		t.Fatalf("parse survivor stdout %q: %v", survivorStdout, err)
	}
	if survivor["decision"] != nil {
		t.Errorf("unrelated row decision = %v; want null (miss must not mutate state)", survivor["decision"])
	}
}

// TestGetPermissionMissingFlag pins the CLI-layer gate: invoking
// `agent-director get-permission` with no --request-token returns
// ErrInvalidFlags before any store read.
func TestGetPermissionMissingFlag(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	home := t.TempDir()
	_, stderr, code := runSpawnCLI(t, home, fakeDir, "get-permission")
	if code == 0 {
		t.Fatalf("exit = 0; want non-zero (stderr=%s)", stderr)
	}
	env := parseEnvelope(t, stderr)
	if env.ErrName != "ErrInvalidFlags" {
		t.Errorf("err_name = %q; want ErrInvalidFlags", env.ErrName)
	}
}
