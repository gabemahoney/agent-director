package main_test

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// seedSpawnRow inserts a row at the requested state and relay_mode so
// the interact-verb CLI tests can drive state-precondition cases without
// going through the full spawn pipeline.
func seedSpawnRow(t *testing.T, dbPath, instanceID, sessionName, state, relayMode string) {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()
	_, err = db.Exec(`
        INSERT INTO spawns (claude_instance_id, state, cwd, tmux_session_name, relay_mode)
        VALUES (?, ?, '/tmp', ?, ?)
    `, instanceID, state, sessionName, relayMode)
	if err != nil {
		t.Fatalf("seed row: %v", err)
	}
}

// testRequestToken is a canonical UUIDv4 token used by CLI integration tests
// that need to insert permission_requests rows directly via raw SQL. It matches
// storefix.TestRequestTokenA so test data is consistent across packages.
const testRequestToken = "aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa"

// seedOpenPermissionRequest inserts an open permission_requests row
// (decision/decision_reason left NULL) for the given Spawn so the
// get-verb CLI tests can drive the `state=check_permission` happy-path
// and the SR-8.5 absence cases. Tool name and tool_input are pure
// parameters per SR-8.2 — no hardcoded literals inside the helper.
// requestToken must be a valid UUIDv4 (use testRequestToken for the
// canonical single-row case). Caller is responsible for inserting the
// parent spawn row first; the FK on claude_instance_id would reject otherwise.
func seedOpenPermissionRequest(t *testing.T, dbPath, instanceID, requestToken, toolName, toolInput string) {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`
        INSERT INTO permission_requests (claude_instance_id, request_token, tool_name, tool_input)
        VALUES (?, ?, ?, ?)
    `, instanceID, requestToken, toolName, toolInput); err != nil {
		t.Fatalf("seed permission row: %v", err)
	}
}

// markPermissionRequestDecided flips decision (and optionally
// decision_reason) on the open row, simulating a prior-cycle decision.
// Used by the M1-gating CLI test that asserts api.Get ignores decided
// rows even while the spawn is back at check_permission.
func markPermissionRequestDecided(t *testing.T, dbPath, instanceID, decision string) {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()
	res, err := db.Exec(`
        UPDATE permission_requests SET decision = ? WHERE claude_instance_id = ?
    `, decision, instanceID)
	if err != nil {
		t.Fatalf("mark decided: %v", err)
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		t.Fatalf("mark decided affected %d rows; want 1 (row missing?)", n)
	}
}

// bootstrapDB runs `agent-director list` once so the schema is created
// before the test seeds a row directly via raw SQL. `list` opens the store
// (CreateIfMissing), whereas help/version are DB-free and create nothing.
func bootstrapDB(t *testing.T, home string) {
	t.Helper()
	if _, _, code := runCLIWithHome(t, home, "list"); code != 0 {
		t.Fatalf("list bootstrap exit = %d", code)
	}
}

func TestSendKeysCLIHappyPath(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	home := t.TempDir()
	bootstrapDB(t, home)
	dbPath := filepath.Join(home, ".agent-director", "state.db")
	seedSpawnRow(t, dbPath, "id-sk-1", "cd-sk-1", "waiting", "off")

	stdout, stderr, code := runSpawnCLI(t, home, fakeDir,
		"send-keys", "--claude-instance-id", "id-sk-1", "--text", "hello world")
	if code != 0 {
		t.Fatalf("send-keys exit = %d; stderr=%s", code, stderr)
	}
	// Body is currently an empty struct; the CLI prints "{}".
	if strings.TrimSpace(stdout) != "{}" {
		t.Errorf("stdout = %q; want \"{}\"", stdout)
	}

	logBytes, err := os.ReadFile(filepath.Join(home, "fake-tmux.log"))
	if err != nil {
		t.Fatalf("read fake-tmux log: %v", err)
	}
	log := string(logBytes)
	// Exactly two send-keys invocations: the text, then Enter.
	if got := strings.Count(log, "send-keys"); got != 2 {
		t.Errorf("send-keys invocation count = %d; want 2 (text + Enter)\nlog=%s", got, log)
	}
	if !strings.Contains(log, "hello world") {
		t.Errorf("fake-tmux log missing the text argv: %s", log)
	}
	if !strings.Contains(log, "\nEnter\n") {
		t.Errorf("fake-tmux log missing the Enter token: %s", log)
	}
	if !strings.Contains(log, "cd-sk-1:0.0") {
		t.Errorf("fake-tmux log missing the pane target: %s", log)
	}
}

func TestSendKeysCLIErrSpawnNotInteractive(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	home := t.TempDir()
	bootstrapDB(t, home)
	dbPath := filepath.Join(home, ".agent-director", "state.db")
	seedSpawnRow(t, dbPath, "id-sk-3", "cd-sk-3", "ended", "off")

	_, stderr, code := runSpawnCLI(t, home, fakeDir,
		"send-keys", "--claude-instance-id", "id-sk-3", "--text", "hi")
	if code == 0 {
		t.Fatalf("expected non-zero exit; got 0 (stderr=%s)", stderr)
	}
	env := parseEnvelope(t, stderr)
	if env.ErrName != "ErrSpawnNotInteractive" {
		t.Errorf("err_name = %q; want ErrSpawnNotInteractive", env.ErrName)
	}
}

func TestSendKeysCLIErrSpawnNotFound(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	home := t.TempDir()
	bootstrapDB(t, home)

	_, stderr, code := runSpawnCLI(t, home, fakeDir,
		"send-keys", "--claude-instance-id", "absent", "--text", "hi")
	if code == 0 {
		t.Fatalf("expected non-zero exit; got 0 (stderr=%s)", stderr)
	}
	env := parseEnvelope(t, stderr)
	if env.ErrName != "ErrSpawnNotFound" {
		t.Errorf("err_name = %q; want ErrSpawnNotFound", env.ErrName)
	}
}

// TestSendKeysCLIErrSendKeysWhileRelayed is the ZERO-ROWS pin: a relay-on
// check_permission Spawn with NO permission_requests rows must keep refusing
// send-keys with ErrSendKeysWhileRelayed. With no row there is no
// deliverability signal and no authority to release the guard (PM-pinned in
// evaluateRelayGuard: len(rows)==0 → held/refuse). The deliverable-row refusal
// path and the released-guard recovery path are exercised by the sibling tests
// below.
//
// This asserts BOTH observable contracts of the zero-rows refusal from one
// seed+invocation: the stderr error envelope (err_name) AND the audit trail
// event (guard_evaluation="held", outcome=ErrSendKeysWhileRelayed). The two
// were previously split across a separate ...ZeroRowsTrail test that ran the
// identical seed+command; merged here to avoid the duplicate run.
func TestSendKeysCLIErrSendKeysWhileRelayed(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	home := t.TempDir()
	bootstrapDB(t, home)
	dbPath := filepath.Join(home, ".agent-director", "state.db")
	seedSpawnRow(t, dbPath, "id-sk-4", "cd-sk-4", "check_permission", "on")
	// No permission_requests rows seeded → held/refuse (zero-rows pin).

	_, stderr, code := runSpawnCLI(t, home, fakeDir,
		"send-keys", "--claude-instance-id", "id-sk-4", "--text", "1")
	if code == 0 {
		t.Fatalf("expected non-zero exit; got 0 (stderr=%s)", stderr)
	}
	// (a) stderr error envelope.
	env := parseEnvelope(t, stderr)
	if env.ErrName != "ErrSendKeysWhileRelayed" {
		t.Errorf("err_name = %q; want ErrSendKeysWhileRelayed", env.ErrName)
	}
	// (b) audit trail event for the same refusal.
	sk := sendKeysCalledLines(readTrailLines(t, home))
	if len(sk) != 1 {
		t.Fatalf("ad.send_keys.called count = %d; want 1", len(sk))
	}
	row := sk[0]
	if row["outcome"] != "ErrSendKeysWhileRelayed" {
		t.Errorf("outcome = %v; want ErrSendKeysWhileRelayed", row["outcome"])
	}
	if row["guard_evaluation"] != "held" {
		t.Errorf("guard_evaluation = %v; want held", row["guard_evaluation"])
	}
}

// sendKeysCalledLines filters trail lines for ad.send_keys.called events,
// preserving order. Local to this file per the send-keys lane's file ownership;
// mirrors decideCalledLines / hookFiredLines.
func sendKeysCalledLines(lines []map[string]any) []map[string]any {
	var out []map[string]any
	for _, l := range lines {
		if l["event"] == "ad.send_keys.called" {
			out = append(out, l)
		}
	}
	return out
}

// TestSendKeysCLIErrSendKeysWhileRelayedDeliverableRow is the DELIVERABLE-ROW
// refusal variant: a relay-on check_permission Spawn with an open
// permission_requests row still inside its delivery window must refuse with
// ErrSendKeysWhileRelayed (guard held). The row is left at "now", so it is
// well within the CLI binary's default 86400s window — the poller could still
// deliver a decision, so the guard denies the racing keystroke. The trail
// event records outcome=ErrSendKeysWhileRelayed with guard_evaluation="held".
func TestSendKeysCLIErrSendKeysWhileRelayedDeliverableRow(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	home := t.TempDir()
	bootstrapDB(t, home)
	dbPath := filepath.Join(home, ".agent-director", "state.db")
	const id = "id-sk-relay-live-1"
	seedSpawnRow(t, dbPath, id, "cd-sk-relay-live-1", "check_permission", "on")
	// In-window row (created_at defaults to now) → guard held, refuse.
	seedOpenPermissionRequest(t, dbPath, id, testRequestToken, "Bash", `{"cmd":"ls"}`)

	_, stderr, code := runSpawnCLI(t, home, fakeDir,
		"send-keys", "--claude-instance-id", id, "--text", "1")
	if code == 0 {
		t.Fatalf("expected non-zero exit; got 0 (stderr=%s)", stderr)
	}
	env := parseEnvelope(t, stderr)
	if env.ErrName != "ErrSendKeysWhileRelayed" {
		t.Errorf("err_name = %q; want ErrSendKeysWhileRelayed", env.ErrName)
	}

	sk := sendKeysCalledLines(readTrailLines(t, home))
	if len(sk) != 1 {
		t.Fatalf("ad.send_keys.called count = %d; want 1", len(sk))
	}
	row := sk[0]
	if row["outcome"] != "ErrSendKeysWhileRelayed" {
		t.Errorf("outcome = %v; want ErrSendKeysWhileRelayed", row["outcome"])
	}
	if row["guard_evaluation"] != "held" {
		t.Errorf("guard_evaluation = %v; want held", row["guard_evaluation"])
	}
	if row["claude_instance_id"] != id {
		t.Errorf("claude_instance_id = %v; want %q", row["claude_instance_id"], id)
	}
}

// TestSendKeysCLIRelayRecoveryReleasesGuard is the recovery case (SR-5.2): a
// relay-on check_permission Spawn whose only permission_requests row is
// backdated far past the CLI binary's default effective window (86400s) plus
// RelayKillSafetyMargin is undeliverable — the delivering hook is dead — so the
// guard RELEASES and send-keys succeeds. The audited recovery is visible on the
// ad.send_keys.called event with guard_evaluation="released", outcome="ok", and
// the recovered Spawn's id. The CLI runs on the real clock, hence a 48h
// backdate rather than anything near the window (mirrors decide_cli_test.go's
// ErrRelayFallenBack case).
func TestSendKeysCLIRelayRecoveryReleasesGuard(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	home := t.TempDir()
	bootstrapDB(t, home)
	dbPath := filepath.Join(home, ".agent-director", "state.db")
	const id = "id-sk-relay-recover-1"
	seedSpawnRow(t, dbPath, id, "cd-sk-relay-recover-1", "check_permission", "on")
	seedOpenPermissionRequest(t, dbPath, id, testRequestToken, "Bash", `{"cmd":"ls"}`)
	// Push the only row's window far past default(86400s)+margin → undeliverable.
	backdatePermissionRequest(t, dbPath, id, testRequestToken, 48*time.Hour)

	stdout, stderr, code := runSpawnCLI(t, home, fakeDir,
		"send-keys", "--claude-instance-id", id, "--text", "recover me")
	if code != 0 {
		t.Fatalf("send-keys exit = %d; want 0 (guard should have released); stderr=%s", code, stderr)
	}
	if strings.TrimSpace(stdout) != "{}" {
		t.Errorf("stdout = %q; want \"{}\"", stdout)
	}

	// The keystrokes were actually delivered to the pane (text + Enter).
	logBytes, err := os.ReadFile(filepath.Join(home, "fake-tmux.log"))
	if err != nil {
		t.Fatalf("read fake-tmux log: %v", err)
	}
	if !strings.Contains(string(logBytes), "recover me") {
		t.Errorf("fake-tmux log missing delivered text: %s", string(logBytes))
	}

	sk := sendKeysCalledLines(readTrailLines(t, home))
	if len(sk) != 1 {
		t.Fatalf("ad.send_keys.called count = %d; want 1", len(sk))
	}
	row := sk[0]
	if row["guard_evaluation"] != "released" {
		t.Errorf("guard_evaluation = %v; want released", row["guard_evaluation"])
	}
	if row["outcome"] != "ok" {
		t.Errorf("outcome = %v; want ok", row["outcome"])
	}
	if row["claude_instance_id"] != id {
		t.Errorf("claude_instance_id = %v; want %q", row["claude_instance_id"], id)
	}
	if row["source"] != "ad_send_keys" {
		t.Errorf("source = %v; want ad_send_keys", row["source"])
	}
}

func TestSendKeysCLIAllowPendingFlag(t *testing.T) {
	// --allow-pending must be parsed and forwarded: a pending Spawn that
	// would normally return ErrSpawnNotInteractive succeeds with the flag.
	fakeDir := buildFakeTmux(t)
	home := t.TempDir()
	bootstrapDB(t, home)
	dbPath := filepath.Join(home, ".agent-director", "state.db")
	seedSpawnRow(t, dbPath, "id-sk-ap-1", "cd-sk-ap-1", "pending", "off")

	_, stderr, code := runSpawnCLI(t, home, fakeDir,
		"send-keys", "--allow-pending", "--claude-instance-id", "id-sk-ap-1", "--text", "hello")
	if code != 0 {
		t.Fatalf("send-keys --allow-pending exit = %d; stderr=%s", code, stderr)
	}
}

func TestSendKeysCLIMissingInstanceID(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	home := t.TempDir()
	bootstrapDB(t, home)

	_, stderr, code := runSpawnCLI(t, home, fakeDir,
		"send-keys", "--text", "hi")
	if code == 0 {
		t.Fatalf("expected non-zero exit; got 0 (stderr=%s)", stderr)
	}
	env := parseEnvelope(t, stderr)
	if env.ErrName != "ErrInvalidFlags" {
		t.Errorf("err_name = %q; want ErrInvalidFlags", env.ErrName)
	}
}
