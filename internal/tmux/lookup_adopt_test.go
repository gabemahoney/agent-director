package tmux_test

import (
	"testing"

	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
)

// The pane-by-token selector of SR-3.6, SR-3.7 and SR-11.3: panes are
// counted by distinct pane id, and only a pane whose AdPane names the token
// matches. tmuxfix.Token is the row's token, tmuxfix.OtherToken a leftover's.

// adPane builds a listing entry for pane id under session sid, tagged ad.
func adPane(sid, id string, pid int, ad string) tmux.Pane {
	return tmux.Pane{SessionID: sid, ID: id, PID: pid, AdPane: ad}
}

// TestPaneByToken_Outcomes: one, none or more than one distinct pane, and
// the Pane returned for each.
func TestPaneByToken_Outcomes(t *testing.T) {
	own := adPane("$1", "%1", 101, tmuxfix.Token)
	leftover := adPane("$2", "%2", 202, tmuxfix.OtherToken)
	shared := adPane("$3", "%1", 101, tmuxfix.Token) // %1 listed again under a grouped session
	teammate := adPane("$1", "%3", 303, tmuxfix.Token)
	borrowed := adPane("$1", "%4", 404, "") // a borrowed or mismatched value

	cases := []struct {
		name      string
		panes     []tmux.Pane
		token     string
		wantPane  tmux.Pane
		wantMatch tmux.PaneMatch
	}{
		{"one pane", []tmux.Pane{borrowed, own}, tmuxfix.Token, own, tmux.PaneOne},
		{"none", []tmux.Pane{own, borrowed}, tmuxfix.OtherToken, tmux.Pane{}, tmux.PaneNone},
		{"empty listing", nil, tmuxfix.Token, tmux.Pane{}, tmux.PaneNone},
		{"teammate pane gives more than one", []tmux.Pane{own, teammate}, tmuxfix.Token, tmux.Pane{}, tmux.PaneMany},
		{"same pane listed twice counts once", []tmux.Pane{own, shared}, tmuxfix.Token, own, tmux.PaneOne},
		{"shared pane then teammate gives more than one", []tmux.Pane{own, shared, teammate}, tmuxfix.Token, tmux.Pane{}, tmux.PaneMany},
		{"empty AdPane never matches", []tmux.Pane{borrowed, adPane("$1", "%5", 505, "")}, tmuxfix.Token, tmux.Pane{}, tmux.PaneNone},
		{"empty token matches nothing", []tmux.Pane{borrowed, own}, "", tmux.Pane{}, tmux.PaneNone},
		{"leftover's token selects the leftover's pane", []tmux.Pane{own, leftover, borrowed}, tmuxfix.OtherToken, leftover, tmux.PaneOne},
		{"row token beside a leftover selects the row's pane", []tmux.Pane{leftover, own}, tmuxfix.Token, own, tmux.PaneOne},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, match := tmux.PaneByToken(c.panes, c.token)
			if match != c.wantMatch || got != c.wantPane {
				t.Errorf("PaneByToken(%q) = %+v, %v; want %+v, %v", c.token, got, match, c.wantPane, c.wantMatch)
			}
		})
	}
}
