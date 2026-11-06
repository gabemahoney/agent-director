package realtmux_test

import (
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/pkg/api"
)

// Capture length (b.7y8, GitHub #38): capture-pane -S -<n> prints n history
// rows and then the whole visible screen, so on an alternate screen (Claude
// Code's and Codex's TUIs: no history) every n gave the same full screen. The
// production capture and read-pane return exactly the last n rows.

// numberedRows is a shell loop printing "<prefix><i>" for i from 1 to count.
func numberedRows(prefix string, count int) string {
	return fmt.Sprintf(`i=1; while [ $i -le %d ]; do printf '%s%%s\n' $i; i=$((i+1)); done`, count, prefix)
}

// wantRows is "<prefix><i>\n" for i from from to to.
func wantRows(prefix string, from, to int) string {
	var b strings.Builder
	for i := from; i <= to; i++ {
		b.WriteString(prefix + strconv.Itoa(i) + "\n")
	}
	return b.String()
}

// TestCaptureReturnsLastRows runs, on an alternate-screen pane and on a normal
// pane with history, the production CapturePaneID for n below the pane's
// height (and, on the normal pane, above it) and checks it returns exactly
// the last n rows printed, while tmux's own capture of the same argv prints
// more.
func TestCaptureReturnsLastRows(t *testing.T) {
	rt := newRealTmux(t)
	cases := []struct {
		name, script, prefix string
		printed              int
		alternate            string // #{alternate_on}
		history              bool   // whether #{history_size} is above 0
		ns                   []int
	}{
		{"alternate screen", `printf '\033[?1049h'; ` + numberedRows("alt row ", 30), "alt row ", 30, "1", false, []int{2, 10}},
		{"normal screen with history", numberedRows("norm line ", 100), "norm line ", 100, "0", true, []int{2, 10, 30}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := rt.mustCreate(t, createSpec{Command: []string{"sh", "-c", tc.script + "; exec sleep 3600"}})
			pane := c.Reply.PaneID
			waitExeced(t, c.Reply.PanePID, "sleep")
			last := tc.prefix + strconv.Itoa(tc.printed)
			waitFor(t, "pane shows "+last, func() bool {
				return strings.Contains(rt.must(t, "capture-pane", "-p", "-t", pane), last+"\n")
			}, nil)
			height := rt.formatInt(t, pane, "#{pane_height}")
			if alt := rt.format(t, pane, "#{alternate_on}"); alt != tc.alternate {
				t.Fatalf("alternate_on = %s; want %s", alt, tc.alternate)
			}
			if history := rt.formatInt(t, pane, "#{history_size}"); (history > 0) != tc.history {
				t.Fatalf("history_size = %d; want above 0: %v", history, tc.history)
			}
			for _, n := range tc.ns {
				raw := rt.must(t, "capture-pane", "-p", "-t", pane, "-S", "-"+strconv.Itoa(n))
				if rows := strings.Count(raw, "\n"); rows <= n {
					t.Fatalf("n=%d: tmux's capture printed %d rows; want more than n (the scenario)", n, rows)
				}
				got, err := newClient().CapturePaneID(rt.Socket, pane, n, false)
				if err != nil {
					t.Fatalf("n=%d: CapturePaneID: %s", n, describe(err))
				}
				if want := wantRows(tc.prefix, tc.printed-n+1, tc.printed); got != want {
					t.Errorf("n=%d (pane height %d): CapturePaneID = %d rows %q; want exactly the last %d rows %q",
						n, height, strings.Count(got, "\n"), got, n, want)
				}
			}
		})
	}
}

// TestReadPaneReturnsLastRows is GitHub #38's scenario through read-pane: an
// agent row whose pane is an alternate screen gives exactly n rows for each
// n below the pane's height, each n's pane_sha256 differing.
func TestReadPaneReturnsLastRows(t *testing.T) {
	f := newKillFix(t)
	r := f.liveRow(t, killRowSpec{Command: []string{"sh", "-c",
		`printf '\033[?1049h'; ` + numberedRows("alt row ", 30) + "; exec sleep 3600"}})
	waitExeced(t, r.Reply.PanePID, "sleep")
	waitFor(t, "pane shows alt row 30", func() bool {
		return strings.Contains(r.RT.must(t, "capture-pane", "-p", "-t", r.Reply.PaneID), "alt row 30\n")
	}, nil)
	c := f.open(t, 0, "")
	t.Cleanup(func() { _ = c.Close() })
	hashes := map[string]int{}
	for _, n := range []int{2, 4, 10} {
		res, err := c.ReadPane(api.ReadPaneParams{InstanceID: r.InstanceID, NLines: n})
		if err != nil {
			t.Fatalf("n_lines %d: read-pane: %s", n, describe(err))
		}
		if want := wantRows("alt row ", 31-n, 30); res.Pane != want {
			t.Errorf("n_lines %d: pane = %d rows %q; want exactly the last %d rows %q", n, strings.Count(res.Pane, "\n"), res.Pane, n, want)
		}
		if prev, seen := hashes[res.PaneSHA256]; seen {
			t.Errorf("n_lines %d: pane_sha256 equals n_lines %d's", n, prev)
		}
		hashes[res.PaneSHA256] = n
	}
}
