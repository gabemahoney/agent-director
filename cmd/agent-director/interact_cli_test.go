package main_test

import (
	"database/sql"
	"os"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/faketmuxfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"

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

// The send-keys CLI over test/fake-tmux tables (SR-7.1 to SR-7.4): guards
// first, then one lookup, one pane listing, and the text and Enter by pane id.

// seedRelayRow seeds a relay-on check_permission row as seedKillRow does.
func seedRelayRow(t *testing.T) (home, id, socket string) {
	t.Helper()
	home = t.TempDir()
	socket = spawnSocket(t, home)
	id, err := apitest.SeedSpawn(stateDB(home), "", store.StateCheckPermission, "", "on", "", true, apitest.WithTmuxSocket(socket))
	if err != nil {
		t.Fatalf("SeedSpawn: %v", err)
	}
	return home, id, socket
}

// seedRequest seeds one open permission request for id and returns its token.
func seedRequest(t *testing.T, home, id string) string {
	t.Helper()
	req, err := apitest.SeedPermissionRequest(stateDB(home), id, "Bash")
	if err != nil {
		t.Fatalf("SeedPermissionRequest: %v", err)
	}
	return req.RequestToken
}

// ownSession is id's own labelled session "$3" with the agent's pane.
func ownSession(t *testing.T, home, id string) faketmuxfix.Session {
	t.Helper()
	token, _, storeID := launchIdentity(t, home, id)
	name, _ := rowColumns(t, home, id).TmuxSessionName.(string)
	return readPaneSession("$3", name, token, id, storeID, apitest.TestPaneID, apitest.TestPanePID, "")
}

// runSendKeys runs send-keys for id under home with the extra flags.
func runSendKeys(t *testing.T, fakeDir, home, id, text string, flags ...string) (stdout, stderr string, code int) {
	t.Helper()
	args := append([]string{"send-keys", "--claude-instance-id", id, "--text", text}, flags...)
	return runSpawnCLI(t, home, fakeDir, args...)
}

// assertDelivered checks exit 0, stdout {}, and exactly one lookup, one pane
// listing, then text and Enter to the agent's pane by id (never name:0.0).
func assertDelivered(t *testing.T, home, socket, text, stdout, stderr string, code int) {
	t.Helper()
	if code != 0 || stderr != "" || stdout != "{}\n" {
		t.Fatalf("send-keys exit = %d, stdout = %q, stderr = %q; want 0, {} and empty", code, stdout, stderr)
	}
	invs := assertInvocationKinds(t, home, "list-sessions", "list-panes", "send-keys", "send-keys")
	target := []string{"-u", "-S", socket, "send-keys", "-t", apitest.TestPaneID}
	if want := append(slices.Clone(target), "-l", "--", text); !slices.Equal(invs[2], want) {
		t.Errorf("text invocation = %q; want %q", invs[2], want)
	}
	if want := append(slices.Clone(target), "Enter"); !slices.Equal(invs[3], want) {
		t.Errorf("Enter invocation = %q; want %q", invs[3], want)
	}
}

// assertRefused checks exit 1, empty stdout and err_name want; it returns
// the error description.
func assertRefused(t *testing.T, stdout, stderr string, code int, want string) string {
	t.Helper()
	if code != 1 || stdout != "" {
		t.Fatalf("send-keys exit = %d, stdout = %q; want 1 and empty (stderr=%q)", code, stdout, stderr)
	}
	env := parseEnvelope(t, stderr)
	if env.ErrName != want {
		t.Errorf("err_name = %q; want %q (description %q)", env.ErrName, want, env.ErrDescription)
	}
	return env.ErrDescription
}

// assertSendKeysCalled checks the call's one ad.send_keys.called record has
// outcome and row_state (SR-7.4) and returns it.
func assertSendKeysCalled(t *testing.T, home, outcome, rowState string) map[string]any {
	t.Helper()
	sk := trailEvents(t, home, "ad.send_keys.called")
	if len(sk) != 1 {
		t.Fatalf("ad.send_keys.called count = %d; want 1", len(sk))
	}
	if sk[0]["outcome"] != outcome || sk[0]["row_state"] != rowState {
		t.Errorf("outcome = %v, row_state = %v; want %q, %q", sk[0]["outcome"], sk[0]["row_state"], outcome, rowState)
	}
	return sk[0]
}

// assertDisagree checks the call's ad.provenance.disagree records are exactly
// reasons, each from send-keys with action (SR-14).
func assertDisagree(t *testing.T, home, action string, reasons ...string) {
	t.Helper()
	var got []string
	for _, d := range trailEvents(t, home, "ad.provenance.disagree") {
		got = append(got, d["reason"].(string))
		if d["source"] != "ad_send_keys" || d["verb"] != "send-keys" || d["action"] != action {
			t.Errorf("disagree record source = %v, verb = %v, action = %v; want ad_send_keys, send-keys, %s",
				d["source"], d["verb"], d["action"], action)
		}
	}
	if !slices.Equal(got, reasons) {
		t.Errorf("ad.provenance.disagree reasons = %q; want %q", got, reasons)
	}
}

// TestSendKeysCLIDeliversByPaneID: a live row, and a pending row with
// --allow-pending, get the text and Enter in the agent's pane by id.
func TestSendKeysCLIDeliversByPaneID(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	cases := []struct {
		name  string
		state string
		flags []string
	}{
		{name: "live row", state: store.StateWaiting},
		{name: "pending row with allow pending", state: store.StatePending, flags: []string{"--allow-pending"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home, id, socket := seedKillRow(t, tc.state)
			faketmuxfix.Tables{}.Write(t, socket, killTable(ownSession(t, home, id)))

			stdout, stderr, code := runSendKeys(t, fakeDir, home, id, "hello world", tc.flags...)
			assertDelivered(t, home, socket, "hello world", stdout, stderr, code)
			row := assertSendKeysCalled(t, home, "ok", tc.state)
			if row["allow_pending"] != (tc.flags != nil) || row["guard_evaluation"] != "not-applicable" {
				t.Errorf("allow_pending = %v, guard_evaluation = %v; want %v, not-applicable",
					row["allow_pending"], row["guard_evaluation"], tc.flags != nil)
			}
			// The row records no server identity, so the lookup's is adopted once.
			assertDisagree(t, home, "keys_sent", "adopted")
			after := rowColumns(t, home, id)
			if after.State != tc.state || after.TmuxServerPID != int64(os.Getpid()) {
				t.Errorf("row state = %v, tmux_server_pid = %v after send-keys; want %q unchanged and %d adopted",
					after.State, after.TmuxServerPID, tc.state, os.Getpid())
			}
		})
	}
}

// TestSendKeysCLIEmptyTextPressesEnterOnly (b.9o4): --text "" is accepted on
// a live row and, with --allow-pending, a pending row: one lookup, one pane
// listing and one Enter to the agent's pane by id, nothing typed (an empty
// text call, if one is made, types "").
func TestSendKeysCLIEmptyTextPressesEnterOnly(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	for _, tc := range []struct {
		name  string
		state string
		flags []string
	}{
		{name: "live row", state: store.StateWaiting},
		{name: "pending row with allow pending", state: store.StatePending, flags: []string{"--allow-pending"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, id, socket := seedKillRow(t, tc.state)
			faketmuxfix.Tables{}.Write(t, socket, killTable(ownSession(t, home, id)))

			stdout, stderr, code := runSendKeys(t, fakeDir, home, id, "", tc.flags...)

			if code != 0 || stderr != "" || stdout != "{}\n" {
				t.Fatalf("send-keys --text \"\" exit = %d, stdout = %q, stderr = %q; want 0, {} and empty", code, stdout, stderr)
			}
			target := []string{"-u", "-S", socket, "send-keys", "-t", apitest.TestPaneID}
			var invs [][]string
			for _, argv := range fakeTmuxInvocations(t, home) {
				if !slices.Equal(argv, append(slices.Clone(target), "-l", "--", "")) {
					invs = append(invs, argv)
				}
			}
			if len(invs) != 3 || !slices.Contains(invs[0], "list-sessions") || !slices.Contains(invs[1], "list-panes") ||
				!slices.Equal(invs[2], append(slices.Clone(target), "Enter")) {
				t.Errorf("fake-tmux invocations, an empty text call aside = %q; want the lookup, the pane listing and %q",
					invs, append(slices.Clone(target), "Enter"))
			}
			assertSendKeysCalled(t, home, "ok", tc.state)
		})
	}
}

// TestSendKeysCLIErrSpawnNotInteractive: finished rows, and pending rows
// without --allow-pending or with no usable launch start or token, are
// refused before any tmux call (SR-7.1, SR-22.8).
func TestSendKeysCLIErrSpawnNotInteractive(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	allow := []string{"--allow-pending"}
	cases := []struct {
		name     string
		state    string
		flags    []string
		opts     []apitest.SpawnOption
		noLaunch bool // pending with allow_pending, launch start or token unusable
	}{
		{name: "ended row", state: store.StateEnded},
		{name: "ended row with allow pending", state: store.StateEnded, flags: allow},
		{name: "pending row without allow pending", state: store.StatePending},
		{name: "pending row with no launch token", state: store.StatePending, flags: allow,
			opts: []apitest.SpawnOption{apitest.WithNoLaunchToken()}, noLaunch: true},
		{name: "pending row with no launch start", state: store.StatePending, flags: allow,
			opts: []apitest.SpawnOption{apitest.WithNoLaunchStartedAt()}, noLaunch: true},
		{name: "pending row with an unreadable launch start", state: store.StatePending, flags: allow,
			opts: []apitest.SpawnOption{apitest.WithRawLaunchStartedAt("not-a-number")}, noLaunch: true},
		// One millisecond past 9999-12-31T23:59:59.999Z (SR-5.5).
		{name: "pending row with an out-of-range launch start", state: store.StatePending, flags: allow,
			opts: []apitest.SpawnOption{apitest.WithLaunchStartedAt(253402300800000)}, noLaunch: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home, id, _ := seedKillRow(t, tc.state, tc.opts...)
			before := rowColumns(t, home, id)

			stdout, stderr, code := runSendKeys(t, fakeDir, home, id, "hi", tc.flags...)
			desc := assertRefused(t, stdout, stderr, code, "ErrSpawnNotInteractive")
			forbid := []string{}
			if token, _ := before.LaunchToken.(string); token != "" {
				forbid = append(forbid, token)
			}
			c := apitest.DescCase{Name: "send-keys refused for its state"}
			if tc.noLaunch {
				c = apitest.DescSendKeysPendingNoLaunch(id)
			}
			apitest.AssertDescription(t, desc, c, forbid...)
			assertInvocationKinds(t, home)
			assertSendKeysCalled(t, home, "ErrSpawnNotInteractive", tc.state)
			if after := rowColumns(t, home, id); !reflect.DeepEqual(after, before) {
				t.Errorf("row after send-keys = %+v; want unchanged %+v", after, before)
			}
		})
	}
}

// TestSendKeysCLIRefusals: Gone, a leftover and another store's session send
// nothing after the one lookup (SR-7.2, SR-3.4).
func TestSendKeysCLIRefusals(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	allow := []string{"--allow-pending"}
	gone := func(id, name string, _ []apitest.DescSession) apitest.DescCase {
		return apitest.DescPaneGone(apitest.PaneGone{Verb: apitest.PaneSendKeys, InstanceID: id, Name: name})
	}
	leftover := func(id, _ string, s []apitest.DescSession) apitest.DescCase {
		return apitest.DescPaneLeftover(apitest.PaneLeftover{Verb: apitest.PaneSendKeys, InstanceID: id, Sessions: s})
	}
	cases := []struct {
		name       string
		state      string
		flags      []string
		leftover   bool // a session labelled with an earlier launch's token
		otherStore bool // a session labelled by another agent-director store
		wantErr    string
		desc       func(id, name string, sessions []apitest.DescSession) apitest.DescCase
	}{
		{name: "gone", state: store.StateWaiting, wantErr: "ErrTmuxSendKeys", desc: gone},
		{name: "leftover on a live row", state: store.StateWaiting, leftover: true,
			wantErr: "ErrTmuxSessionConflict", desc: leftover},
		{name: "leftover on a pending row", state: store.StatePending, flags: allow, leftover: true,
			wantErr: "ErrSpawnNotInteractive", desc: func(id, _ string, s []apitest.DescSession) apitest.DescCase {
				return apitest.DescSendKeysPendingLeftover(id, s)
			}},
		{name: "another store's session on a pending row", state: store.StatePending, flags: allow, otherStore: true,
			wantErr: "ErrTmuxSendKeys", desc: gone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home, id, socket := seedKillRow(t, tc.state)
			token, _, storeID := launchIdentity(t, home, id)
			name, _ := rowColumns(t, home, id).TmuxSessionName.(string)
			forbid := []string{token, storeID}
			var sessions []faketmuxfix.Session
			var named []apitest.DescSession
			if tc.leftover || tc.otherStore {
				tok, sid := token, storeID
				if tc.leftover {
					tok = tmuxfix.OtherToken
				}
				if tc.otherStore {
					sid = tmuxfix.OtherStoreID
				}
				s := readPaneSession("$4", "sk-other", tok, id, sid, "%4", apitest.TestPanePID+1, "")
				sessions, named = append(sessions, s), append(named, apitest.DescSession{Name: s.Name, ID: s.ID})
				forbid = append(forbid, tok, sid, s.Label)
			}
			faketmuxfix.Tables{}.Write(t, socket, killTable(sessions...))
			before := rowColumns(t, home, id)

			stdout, stderr, code := runSendKeys(t, fakeDir, home, id, "hi", tc.flags...)
			desc := assertRefused(t, stdout, stderr, code, tc.wantErr)
			apitest.AssertDescription(t, desc, tc.desc(id, name, named), forbid...)
			assertInvocationKinds(t, home, "list-sessions")
			assertSendKeysCalled(t, home, tc.wantErr, tc.state)
			assertDisagree(t, home, "nothing_sent")
			if after := rowColumns(t, home, id); !reflect.DeepEqual(after, before) {
				t.Errorf("row after send-keys = %+v; want unchanged %+v", after, before)
			}
		})
	}
}

func TestSendKeysCLIErrSpawnNotFound(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	home := t.TempDir()
	bootstrapDB(t, home)

	stdout, stderr, code := runSendKeys(t, fakeDir, home, "absent", "hi")
	assertRefused(t, stdout, stderr, code, "ErrSpawnNotFound")
	assertInvocationKinds(t, home)
	assertSendKeysCalled(t, home, "ErrSpawnNotFound", "")
}

// TestSendKeysCLIErrSendKeysWhileRelayed: a relay-on check_permission row
// with no request, or one still inside its delivery window, keeps the guard
// held and makes no tmux call (SR-5.2).
func TestSendKeysCLIErrSendKeysWhileRelayed(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	cases := []struct {
		name    string
		request bool
	}{{name: "no request"}, {name: "request in window", request: true}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home, id, socket := seedRelayRow(t)
			if tc.request {
				seedRequest(t, home, id)
			}
			faketmuxfix.Tables{}.Write(t, socket, killTable(ownSession(t, home, id)))

			stdout, stderr, code := runSendKeys(t, fakeDir, home, id, "1")
			assertRefused(t, stdout, stderr, code, "ErrSendKeysWhileRelayed")
			assertInvocationKinds(t, home)
			row := assertSendKeysCalled(t, home, "ErrSendKeysWhileRelayed", store.StateCheckPermission)
			if row["guard_evaluation"] != "held" || row["claude_instance_id"] != id {
				t.Errorf("guard_evaluation = %v, claude_instance_id = %v; want held, %q",
					row["guard_evaluation"], row["claude_instance_id"], id)
			}
		})
	}
}

// TestSendKeysCLIRelayRecoveryReleasesGuard: a request backdated far past the
// default window plus margin releases the guard and the keys are delivered
// by pane id, audited as released (SR-5.2).
func TestSendKeysCLIRelayRecoveryReleasesGuard(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	home, id, socket := seedRelayRow(t)
	backdatePermissionRequest(t, stateDB(home), id, seedRequest(t, home, id), 48*time.Hour)
	faketmuxfix.Tables{}.Write(t, socket, killTable(ownSession(t, home, id)))

	stdout, stderr, code := runSendKeys(t, fakeDir, home, id, "recover me")
	assertDelivered(t, home, socket, "recover me", stdout, stderr, code)
	row := assertSendKeysCalled(t, home, "ok", store.StateCheckPermission)
	if row["guard_evaluation"] != "released" || row["claude_instance_id"] != id || row["source"] != "ad_send_keys" {
		t.Errorf("guard_evaluation = %v, claude_instance_id = %v, source = %v; want released, %q, ad_send_keys",
			row["guard_evaluation"], row["claude_instance_id"], row["source"], id)
	}
	assertDisagree(t, home, "keys_sent", "adopted")
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
