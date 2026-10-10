package api_test

// advice_follow_count_test.go (b.c4n E10, F8): the shared layer's refusals of
// a negative read-pane n_lines and a negative list limit, each alternative the
// advice offers followed literally.

import (
	"testing"

	"github.com/gabemahoney/agent-director/pkg/api"
)

// TestAdviceFollow_E10_ReadPaneNegativeNLines: E10 "pass 0 (or omit it) for the
// default of 25 lines, or a positive number of lines"; read-pane re-issued each
// way captures that many lines of the agent's pane.
func TestAdviceFollow_E10_ReadPaneNegativeNLines(t *testing.T) {
	t.Parallel()
	const advice = "pass 0 (or omit it) for the default of 25 lines, or a positive number of lines"
	adviceAssertManifest(t, "read-pane", "n_lines", "Defaults to 25 when 0/omitted")
	for _, f := range []struct {
		name          string
		nLines, lines int
	}{
		{"pass 0 (or omit it)", 0, api.DefaultReadPaneLines},
		{"a positive number of lines", 3, 3},
	} {
		t.Run(f.name, func(t *testing.T) {
			e := newKillEnv(t)
			r := e.seedRow(t, killRowSpec{})
			e.setPaneTexts(r.Socket)
			_, err := e.readPaneClient(t, api.ReadPaneParams{ClaudeInstanceID: r.ID, NLines: -1})
			adviceAssertAdvice(t, err, api.ErrInvalidFlags, advice)
			e.assertNoTmuxCall(t)

			res, err := e.readPaneClient(t, api.ReadPaneParams{ClaudeInstanceID: r.ID, NLines: f.nLines})
			if err != nil {
				t.Fatalf("read-pane with n_lines %d: %v", f.nLines, err)
			}
			if want := paneText(r.Socket, r.Spawn.Identity.PaneID); res.Pane != want {
				t.Errorf("Pane = %q; want the agent's pane %q", res.Pane, want)
			}
			e.assertCaptured(t, r.Spawn.Identity.PaneID, f.lines, false)
		})
	}
}

// TestAdviceFollow_F8_ListNegativeLimit: F8 "pass 0 (or omit it) for no cap, or
// a positive number of rows"; list re-issued each way returns every row or that many.
func TestAdviceFollow_F8_ListNegativeLimit(t *testing.T) {
	t.Parallel()
	const advice = "pass 0 (or omit it) for no cap, or a positive number of rows"
	adviceAssertManifest(t, "list", "limit", "0 / omitted means no cap")
	e := newKillEnv(t)
	e.seedRow(t, killRowSpec{})
	e.seedRow(t, killRowSpec{})

	res, err := api.List(e.st, api.ListParams{Limit: -1})
	adviceAssertAdvice(t, err, api.ErrInvalidFlags, advice)
	if res.Spawns == nil || len(res.Spawns) != 0 {
		t.Errorf("refused list Spawns = %#v; want a non-nil empty slice", res.Spawns)
	}

	for _, f := range []struct{ limit, rows int }{{0, 2}, {1, 1}} {
		res, err := api.List(e.st, api.ListParams{Limit: f.limit})
		if err != nil || len(res.Spawns) != f.rows {
			t.Errorf("list with limit %d = %d rows, %v; want %d rows", f.limit, len(res.Spawns), err, f.rows)
		}
	}
}
