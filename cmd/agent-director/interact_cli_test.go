package main_test

import (
	"slices"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/faketmuxfix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// TestSendKeysCLIDeliversByPaneID: send-keys' --text and --allow-pending
// reach the verb: a live row, and a pending row with --allow-pending, get the
// text and Enter in the agent's pane by id after one lookup and one pane
// listing, and --text "" presses Enter only (b.9o4). The guards, refusals and
// trail are pkg/api's sendkeys_*_test.go.
func TestSendKeysCLIDeliversByPaneID(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	for _, tc := range []struct {
		name, state, text string
		flags             []string
	}{
		{name: "live row", state: store.StateWaiting, text: "hello world"},
		{name: "pending row with allow pending", state: store.StatePending, text: "hello world", flags: []string{"--allow-pending"}},
		{name: "empty text presses Enter only", state: store.StateWaiting, text: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, id, socket := seedRowOnSocket(t, tc.state)
			faketmuxfix.Tables{}.Write(t, socket, fakeTable(ownSession(t, home, id, "")))

			stdout, stderr, code := runSpawnCLI(t, home, fakeDir,
				append([]string{"send-keys", "--claude-instance-id", id, "--text", tc.text}, tc.flags...)...)

			if code != 0 || stderr != "" || stdout != "{}\n" {
				t.Fatalf("send-keys exit = %d, stdout = %q, stderr = %q; want 0, {} and empty", code, stdout, stderr)
			}
			target := []string{"-u", "-S", socket, "send-keys", "-t", apitest.TestPaneID}
			typed := append(slices.Clone(target), "-l", "--", tc.text)
			var invs [][]string
			for _, argv := range fakeTmuxInvocations(t, home) {
				if tc.text != "" || !slices.Equal(argv, typed) { // an empty text call, if made, types ""
					invs = append(invs, argv)
				}
			}
			want := [][]string{typed, append(slices.Clone(target), "Enter")}
			if tc.text == "" {
				want = want[1:]
			}
			if len(invs) != 2+len(want) || !slices.Contains(invs[0], "list-sessions") || !slices.Contains(invs[1], "list-panes") ||
				!slices.EqualFunc(invs[2:], want, slices.Equal) {
				t.Errorf("fake-tmux invocations = %q; want the lookup, the pane listing and %q", invs, want)
			}
			sk := eventsOf(readTrailLines(t, home), "ad.send_keys.called")
			if len(sk) != 1 || sk[0]["outcome"] != "ok" || sk[0]["row_state"] != tc.state || sk[0]["allow_pending"] != (tc.flags != nil) {
				t.Errorf("ad.send_keys.called = %v; want one, ok, row_state %s, allow_pending %v", sk, tc.state, tc.flags != nil)
			}
		})
	}
}
