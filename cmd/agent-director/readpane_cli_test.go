package main_test

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/faketmuxfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// TestReadPaneCLIFlags: read-pane's --ansi, --n-lines and --allow-pending
// reach the capture of the agent's pane by id: -e only with --ansi (escapes
// stripped without it), -S -<n> with the default 25 (also for 0), --n-lines
// read as decimal (b.c4n), and stdout exactly {"pane":...} (SR-7.2, SR-7.5).
// The refusals are pkg/api's, int_flags_cli_test.go's and test/envelope-diff's
// read-pane rows.
func TestReadPaneCLIFlags(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	ansi := tmuxfix.Find(tmuxfix.Captures(), "capture/ansi").Stdout
	cases := []struct {
		name, state string
		flags       []string
		wantPane    string
		wantArgs    []string // the capture's argv after the socket
	}{
		{"default strips escapes", store.StateWaiting, nil, tmux.StripANSI(ansi),
			[]string{"capture-pane", "-p", "-t", apitest.TestPaneID, "-S", "-25"}},
		{"ansi keeps escapes", store.StateWaiting, []string{"--ansi"}, ansi,
			[]string{"capture-pane", "-p", "-e", "-t", apitest.TestPaneID, "-S", "-25"}},
		{"n-lines", store.StateWaiting, []string{"--n-lines", "100"}, tmux.StripANSI(ansi),
			[]string{"capture-pane", "-p", "-t", apitest.TestPaneID, "-S", "-100"}},
		// b.c4n: base 10 only, so a leading zero is not octal (010 is 10, not 8).
		{"n-lines with a leading zero", store.StateWaiting, []string{"--n-lines", "010"}, tmux.StripANSI(ansi),
			[]string{"capture-pane", "-p", "-t", apitest.TestPaneID, "-S", "-10"}},
		{"n-lines 0 is the default", store.StateWaiting, []string{"--n-lines", "0"}, tmux.StripANSI(ansi),
			[]string{"capture-pane", "-p", "-t", apitest.TestPaneID, "-S", "-25"}},
		{"allow-pending on a pending row", store.StatePending, []string{"--allow-pending"}, tmux.StripANSI(ansi),
			[]string{"capture-pane", "-p", "-t", apitest.TestPaneID, "-S", "-25"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home, id, socket := seedRowOnSocket(t, tc.state)
			faketmuxfix.Tables{}.Write(t, socket, fakeTable(ownSession(t, home, id, ansi)))

			stdout, stderr, code := runSpawnCLI(t, home, fakeDir,
				append([]string{"read-pane", "--claude-instance-id", id}, tc.flags...)...)

			var res map[string]string
			if code != 0 || stderr != "" || json.Unmarshal([]byte(stdout), &res) != nil || len(res) != 1 || res["pane"] != tc.wantPane {
				t.Fatalf("read-pane exit = %d, stdout = %q, stderr = %q; want 0, exactly {\"pane\":%q} and empty",
					code, stdout, stderr, tc.wantPane)
			}
			invs := assertInvocationKinds(t, home, "list-sessions", "list-panes", "capture-pane")
			if want := append([]string{"-u", "-S", socket}, tc.wantArgs...); !slices.Equal(invs[2], want) {
				t.Errorf("capture invocation = %q; want %q", invs[2], want)
			}
		})
	}
}
