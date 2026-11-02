package main_test

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"syscall"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/faketmuxfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// TestSendKeysCLIDeliversByPaneID: send-keys' --text, --allow-pending,
// --no-enter and --key reach the verb: after one lookup and one pane listing,
// a live row gets text and Enter in the agent's pane by id; a pending row
// with --allow-pending accepts Enter-only (b.9o4); --no-enter types text alone.
// --key sends one key, a named key by name or one character literally (b.146).
// b.qcf: pending Claude ordinary text waits for SessionStart; Enter-only drives
// an unreported row. An unreadable tty refuses before typing and exposes
// not_started on stderr.
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
		{"pending row unreadable terminal", store.StatePending, []string{"--text", "hello world", "--allow-pending"}, nil},
		{"empty text presses Enter only", store.StateWaiting, []string{"--text", ""}, [][]string{enter}},
		{"no enter types the text alone", store.StateWaiting, []string{"--text", "hi", "--no-enter"}, [][]string{typed("hi")}},
		{"a named key alone", store.StateWaiting, []string{"--key", "Escape"}, [][]string{{"Escape"}}},
		{"one character alone", store.StateWaiting, []string{"--key", "y"}, [][]string{typed("y")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, id, socket := seedRowOnSocket(t, tc.state)
			session := ownSession(t, home, id, "")
			faketmuxfix.Tables{}.Write(t, socket, fakeTable(session))

			stdout, stderr, code := runSpawnCLI(t, home, fakeDir, append([]string{"send-keys", "--instance-id", id}, tc.args...)...)

			outcome := "ok"
			if tc.state == store.StatePending && len(tc.sends) == 0 {
				outcome = "ErrSubmissionUnconfirmed"
				assertOnlyEnvelope(t, stdout, stderr, code, outcome)
				var got struct {
					Details map[string]any `json:"err_details"`
				}
				want := map[string]any{
					"outcome": "not_started", "phase": "pre_input", "bytes_attempted": float64(0),
					"enter_may_have_been_sent": false, "keys_sent": false, "submission_confirmed": false,
					"row_state": tc.state,
					"reason": "the agent's terminal mode could not be read: read the terminal mode of /dev/null: " + syscall.ENOTTY.Error() +
						"; nothing was typed, so the same send-keys can be retried later",
				}

				if err := json.Unmarshal([]byte(stderr), &got); err != nil || !reflect.DeepEqual(got.Details, want) {
					t.Errorf("err_details = %v (%v); want %v", got.Details, err, want)
				}
			} else if code != 0 || stderr != "" || stdout != "{}\n" {
				t.Fatalf("send-keys exit = %d, stdout = %q, stderr = %q; want 0, {} and empty", code, stdout, stderr)
			}
			target := []string{"-u", "-S", socket, "send-keys", "-t", apitest.TestPaneID}
			var invs [][]string
			for _, argv := range fakeTmuxInvocations(t, home) {
				if !slices.Equal(argv, append(slices.Clone(target), typed("")...)) { // an empty text call, if made, types ""
					invs = append(invs, argv)
				}
			}
			if len(invs) < 2 || !slices.Contains(invs[0], "list-sessions") || !slices.Contains(invs[1], "list-panes") {
				t.Fatalf("fake-tmux invocations = %q; want initial lookup and pane listing", invs)
			}
			var want [][]string
			if tc.state == store.StatePending && len(tc.sends) == 0 {
				want = append(want, []string{"-u", "-S", socket, "display-message", "-p", "-t", apitest.TestPaneID, "#{pane_tty}"})
			}
			for _, s := range tc.sends {
				want = append(want, append(slices.Clone(target), s...))
			}
			if len(invs) != 2+len(want) || !slices.Contains(invs[0], "list-sessions") || !slices.Contains(invs[1], "list-panes") ||
				!slices.EqualFunc(invs[2:], want, slices.Equal) {
				t.Errorf("fake-tmux invocations = %q; want the lookup, the pane listing and %q", invs, want)
			}
			allowPending := slices.Contains(tc.args, "--allow-pending")
			sk := eventsOf(readTrailLines(t, home), "ad.send_keys.called")
			if len(sk) != 1 || sk[0]["outcome"] != outcome || sk[0]["row_state"] != tc.state || sk[0]["allow_pending"] != allowPending {
				t.Errorf("ad.send_keys.called = %v; want one, %s, row_state %s, allow_pending %v", sk, outcome, tc.state, allowPending)
			}
		})
	}
}

// TestSendKeysCLIPaneAnswer (b.146 rule 8): a pane answer through the CLI
// (--request-token, --as, --key and --expect-pane-sha256 from read-pane's
// pane_sha256) on a request that fell back captures the agent's pane, then
// sends exactly its one key, no Enter. When its sent write fails after the
// key, it exits 1 with ErrInternal, err_details {key_sent: true,
// request_token}.
func TestSendKeysCLIPaneAnswer(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	for _, failSent := range []bool{false, true} {
		t.Run(fmt.Sprintf("its sent write fails %v", failSent), func(t *testing.T) {
			home, id, socket := seedRowOnSocket(t, store.StateCheckPermission, apitest.WithRelayMode("on"))
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
			if len(invs) != 4 || !slices.Contains(invs[0], "list-sessions") || !slices.Contains(invs[1], "list-panes") ||
				!slices.Contains(invs[2], "capture-pane") || !slices.Equal(invs[3], key) {
				t.Errorf("fake-tmux invocations = %q; want the lookup, the pane listing, the capture and %q alone", invs, key)
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
