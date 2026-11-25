package main_test

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/faketmuxfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// TestSendKeysCLIDeliversByPaneID: send-keys' --text, --allow-pending,
// --no-enter and --key reach the verb: after the target lookup, a live row
// gets text and Enter in the agent's pane by id; a pending row
// with --allow-pending accepts Enter-only (b.9o4); --no-enter types text alone.
// --key sends one key, a named key by name or one character literally (b.146).
// The row is a pane-only Droid agent's, whose raw input busy send leaves
// alone (a Claude row's send is a Message, typed by no one: busy_send_cli_test.go).
// The guards, refusals and trail are pkg/api's sendkeys_*_test.go.
func TestSendKeysCLIDeliversByPaneID(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	typed := func(text string) []string { return []string{"-l", "--", text} }
	enter := []string{"Enter"}
	for _, tc := range []struct {
		name, state string
		args        []string   // send-keys' flags after --instance-id
		sends       [][]string // each send-keys call's arguments after its -t target
	}{
		{"live row", store.StateWaiting, []string{"--text", "hello world"}, [][]string{typed("hello world"), enter}},
		{"pending row with allow pending", store.StatePending, []string{"--text", "", "--allow-pending"}, [][]string{enter}},
		{"empty text presses Enter only", store.StateWaiting, []string{"--text", ""}, [][]string{enter}},
		{"no enter types the text alone", store.StateWaiting, []string{"--text", "hi", "--no-enter"}, [][]string{typed("hi")}},
		{"a named key alone", store.StateWaiting, []string{"--key", "Escape"}, [][]string{{"Escape"}}},
		{"one character alone", store.StateWaiting, []string{"--key", "y"}, [][]string{typed("y")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, id, socket := seedRowOnSocket(t, tc.state, apitest.WithAgent("droid"))
			session := ownSession(t, home, id, "")
			faketmuxfix.Tables{}.Write(t, socket, fakeTable(session))

			stdout, stderr, code := runSpawnCLI(t, home, fakeDir, append([]string{"send-keys", "--instance-id", id}, tc.args...)...)

			const outcome = "ok"
			if code != 0 || stderr != "" || stdout != "{}\n" {
				t.Fatalf("send-keys exit = %d, stdout = %q, stderr = %q; want 0, {} and empty", code, stdout, stderr)
			}
			target := []string{"-u", "-S", socket, "send-keys", "-t", apitest.TestPaneID}
			// The pane-only lane's one target lookup, then each send.
			kinds := []string{"list-sessions", "list-panes"}
			for range tc.sends {
				kinds = append(kinds, "send-keys")
			}
			invs := assertInvocationKinds(t, home, kinds...)
			var sends [][]string // the input calls
			for _, argv := range invs {
				if len(argv) < 3 || !slices.Equal(argv[:3], []string{"-u", "-S", socket}) {
					t.Fatalf("invocation = %q; want the row's socket %q", argv, socket)
				}
				if slices.Contains(argv, "send-keys") {
					sends = append(sends, argv)
				}
			}
			if len(invs) < 2 || !slices.Contains(invs[0], "list-sessions") || !slices.Contains(invs[1], "list-panes") {
				t.Fatalf("fake-tmux invocations = %q; want initial lookup and pane listing", invs)
			}
			var want [][]string
			for _, send := range tc.sends {
				want = append(want, append(slices.Clone(target), send...))
			}
			if !slices.EqualFunc(sends, want, slices.Equal) {
				t.Errorf("input calls = %q; want exactly %q", sends, want)
			}
			allowPending := slices.Contains(tc.args, "--allow-pending")
			sk := eventsOf(readTrailLines(t, home), "ad.send_keys.called")
			if len(sk) != 1 || sk[0]["outcome"] != outcome || sk[0]["row_state"] != tc.state || sk[0]["allow_pending"] != allowPending {
				t.Errorf("ad.send_keys.called = %v; want one, %s, row_state %s, allow_pending %v", sk, outcome, tc.state, allowPending)
			}
		})
	}
}

// b.t1b: a global-looking text value reaches the existing pane exactly,
// including its leading dashes, without selecting the decoy HOME.
func TestSendKeysCLIPreservesGlobalLookingText(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	home, id, socket := seedRowOnSocket(t, store.StateWaiting, apitest.WithAgent("droid"))
	faketmuxfix.Tables{}.Write(t, socket, fakeTable(ownSession(t, home, id, "")))
	decoy := t.TempDir()
	text := "--home=" + filepath.Join(decoy, "home")
	stdout, stderr, code := runSpawnCLI(t, home, fakeDir, "send-keys", "--instance-id", id, "--text", text)
	assertHomeTree(t, decoy)
	if code != 0 || stdout != "{}\n" || stderr != "" {
		t.Fatalf("send-keys exit=%d stdout=%q stderr=%q; want 0, {} and empty", code, stdout, stderr)
	}
	invs := assertInvocationKinds(t, home, "list-sessions", "list-panes", "send-keys", "send-keys")
	target := []string{"-u", "-S", socket, "send-keys", "-t", apitest.TestPaneID}
	want := [][]string{append(slices.Clone(target), "-l", "--", text), append(slices.Clone(target), "Enter")}
	if got := invs[2:]; !slices.EqualFunc(got, want, slices.Equal) {
		t.Errorf("input calls=%q; want exactly %q", got, want)
	}
}

// TestSendKeysCLIPaneAnswer (b.146 rule 8): a pane answer through the CLI
// (--request-token, --as, --key and --expect-pane-sha256 from read-pane's
// pane_sha256) on a Codex request that fell back captures the agent's pane,
// then sends exactly its one key, no Enter. When its sent write fails after
// the key, it exits 1 with ErrInternal, err_details {key_sent: true,
// request_token}. (A Claude row takes no keys through send-keys.)
func TestSendKeysCLIPaneAnswer(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	for _, failSent := range []bool{false, true} {
		t.Run(fmt.Sprintf("its sent write fails %v", failSent), func(t *testing.T) {
			home, id, socket := seedRowOnSocket(t, store.StateCheckPermission, apitest.WithRelayMode("on"), apitest.WithAgent("codex"))
			req, err := apitest.SeedPermissionRequest(stateDB(home), id, "Bash")
			if err == nil { // past the relay window: fallen back
				err = apitest.AgePermissionRequest(stateDB(home), req.RequestID, 48*time.Hour, 0)
			}
			if err != nil {
				t.Fatalf("seed a fallen-back request: %v", err)
			}
			faketmuxfix.Tables{}.Write(t, socket, fakeTable(ownSession(t, home, id, "Allow Bash? 1 yes, 2 no\n")))
			stdout, stderr, code := runSpawnCLI(t, home, fakeDir, "read-pane", "--instance-id", id)
			var read struct {
				PaneSHA256 string `json:"pane_sha256"`
			}
			if code != 0 || json.Unmarshal([]byte(stdout), &read) != nil || read.PaneSHA256 == "" {
				t.Fatalf("read-pane exit = %d, stdout = %q, stderr = %q; want its pane_sha256", code, stdout, stderr)
			}
			if failSent {
				storefix.InjectWriteFailure(t, stateDB(home), storefix.WriteFailPermissionDecision, id)
			}
			mark := len(fakeTmuxInvocations(t, home))

			stdout, stderr, code = runSpawnCLI(t, home, fakeDir, "send-keys", "--instance-id", id, "--request-token", req.RequestToken,
				"--as", "allow", "--key", "1", "--expect-pane-sha256", read.PaneSHA256)

			invs := fakeTmuxInvocations(t, home)[mark:]
			key := []string{"-u", "-S", socket, "send-keys", "-t", apitest.TestPaneID, "-l", "--", "1"}
			kinds := []string{"list-sessions", "list-panes", "capture-pane", "list-sessions", "list-panes", "capture-pane", "send-keys"}
			if len(invs) != len(kinds) {
				t.Fatalf("fake-tmux invocations = %q; want %v", invs, kinds)
			}
			for i, kind := range kinds {
				if !slices.Contains(invs[i], kind) || len(invs[i]) < 3 || !slices.Equal(invs[i][:3], []string{"-u", "-S", socket}) {
					t.Errorf("invocation %d = %q; want %s on socket %q", i, invs[i], kind, socket)
				}
			}
			if !slices.Equal(invs[len(invs)-1], key) {
				t.Errorf("input call = %q; want %q alone", invs[len(invs)-1], key)
			}
			if !failSent {
				if code != 0 || stderr != "" || stdout != "{}\n" {
					t.Errorf("send-keys exit = %d, stdout = %q, stderr = %q; want 0, {} and empty", code, stdout, stderr)
				}
				return
			}
			env := assertOnlyEnvelope(t, stdout, stderr, code, "ErrInternal")
			var got struct {
				Details map[string]any `json:"err_details"`
			}
			want := map[string]any{"key_sent": true, "request_token": req.RequestToken}
			if json.Unmarshal([]byte(stderr), &got) != nil || fmt.Sprint(got.Details) != fmt.Sprint(want) {
				t.Errorf("err_details = %v (%s); want %v", got.Details, env.ErrDescription, want)
			}
		})
	}
}
