package tmuxfix_test

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// adPanes maps each pane id listed on sock to its AdPane.
func adPanes(t *testing.T, r *tmuxfix.Recorder, sock string) map[string]string {
	t.Helper()
	panes, err := r.ListPanes(sock)
	if err != nil {
		t.Fatalf("ListPanes(%s): %v", sock, err)
	}
	out := map[string]string{}
	for _, p := range panes {
		out[p.ID] = p.AdPane
	}
	return out
}

// TestRecorder_CreatePaneLabel: a create labels its new pane with the token
// exactly when it labels the session; other panes stay unlabelled.
func TestRecorder_CreatePaneLabel(t *testing.T) {
	var byID tmuxfix.StoredName
	for _, n := range tmuxfix.StoredNames() {
		if n.LabelByID && byID.Raw == "" {
			byID = n
		}
	}
	cases := []struct {
		name, session, stored string
		script                *tmuxfix.Script
		want                  string
	}{
		{name: "chained", session: "work", stored: "work", want: tmuxfix.Token},
		{name: "applied-timeout", session: "work", stored: "work",
			script: &tmuxfix.Script{Failure: tmux.FailTimeout, Applied: true}, want: tmuxfix.Token},
		{name: "label-step-fails", session: "work", stored: "work", script: &tmuxfix.Script{Failure: tmux.FailLabel}},
		{name: "label-by-id-name", session: byID.Raw, stored: byID.Stored},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := seeded()
			if tc.script != nil {
				r.Script(sockA, *tc.script, tmux.CallCreate)
			}
			_, _ = r.NewSession(sockA, tc.session, "/w", nil, nil, tmuxfix.Token, agent, tmuxfix.StoreID)
			pane := findByName(t, r, tc.stored).Panes[0].ID
			want := map[string]string{"%0": "", pane: tc.want}
			if got := adPanes(t, r, sockA); !reflect.DeepEqual(got, want) {
				t.Errorf("AdPane = %q, want %q", got, want)
			}
		})
	}
}

// TestRecorder_SetLabelPaneLabel: SetLabel sets the session label, then the
// pane label of the pane id anywhere on the server, and records the pane id;
// an unknown pane leaves the session labelled, an unknown session changes nothing.
func TestRecorder_SetLabelPaneLabel(t *testing.T) {
	valid := tmuxfix.Valid(tmuxfix.Token, agent, tmuxfix.StoreID)
	cases := []struct {
		name, session, pane string
		wantFail            bool
		wantLabels          map[string]tmux.Label
		wantPanes           map[string]string
	}{
		{name: "both-steps", session: "$1", pane: "%1", wantLabels: map[string]tmux.Label{"$0": {}, "$1": valid},
			wantPanes: map[string]string{"%0": "", "%1": tmuxfix.Token, "%2": tmuxfix.OtherToken}},
		{name: "overwrites-old-token", session: "$1", pane: "%2", wantLabels: map[string]tmux.Label{"$0": {}, "$1": valid},
			wantPanes: map[string]string{"%0": "", "%1": "", "%2": tmuxfix.Token}},
		{name: "pane-of-another-session", session: "$0", pane: "%2", wantLabels: map[string]tmux.Label{"$0": valid, "$1": {}},
			wantPanes: map[string]string{"%0": "", "%1": "", "%2": tmuxfix.Token}},
		{name: "unknown-pane", session: "$1", pane: "%9", wantFail: true, wantLabels: map[string]tmux.Label{"$0": {}, "$1": valid},
			wantPanes: map[string]string{"%0": "", "%1": "", "%2": tmuxfix.OtherToken}},
		{name: "unknown-session", session: "$9", pane: "%1", wantFail: true, wantLabels: map[string]tmux.Label{"$0": {}, "$1": {}},
			wantPanes: map[string]string{"%0": "", "%1": "", "%2": tmuxfix.OtherToken}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := tmuxfix.SeedSession{ID: "$1", Name: "b", Panes: []tmuxfix.SeedPane{{ID: "%1"}, {Index: 1, ID: "%2", AdPane: tmuxfix.OtherToken}}}
			r := tmuxfix.NewRecorder().
				SeedSessions(sockA, sess("$0", "a", tmux.Label{}, false, "%0"), b).
				SeedSessions(sockB, tmuxfix.SeedSession{ID: "$1", Name: "b", Panes: []tmuxfix.SeedPane{{ID: "%1"}}})
			err := r.SetLabel(sockA, tc.session, tc.pane, tmuxfix.Token, agent, tmuxfix.StoreID)
			if tc.wantFail {
				if ce := callErr(t, err); ce.Failure != tmux.FailUnrecognized || ce.Call != tmux.CallSetLabel {
					t.Errorf("CallError = %+v, want FailUnrecognized on the label", ce)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			labels := map[string]tmux.Label{}
			for _, s := range lookup(t, r, sockA).Sessions {
				labels[s.ID] = s.Label
			}
			if !reflect.DeepEqual(labels, tc.wantLabels) {
				t.Errorf("session labels = %+v, want %+v", labels, tc.wantLabels)
			}
			if got := adPanes(t, r, sockA); !reflect.DeepEqual(got, tc.wantPanes) {
				t.Errorf("AdPane = %q, want %q", got, tc.wantPanes)
			}
			if got := adPanes(t, r, sockB); !reflect.DeepEqual(got, map[string]string{"%1": ""}) {
				t.Errorf("other socket's AdPane = %q, want unchanged", got)
			}
			want := tmuxfix.SocketCall{Call: tmux.CallSetLabel, Socket: sockA, Target: tc.session, PaneID: tc.pane,
				Token: tmuxfix.Token, InstanceID: agent, StoreID: tmuxfix.StoreID}
			if got := r.SocketCallsOf(tmux.CallSetLabel); len(got) != 1 || !reflect.DeepEqual(got[0], want) {
				t.Errorf("recorded %+v, want %+v", got, want)
			}
		})
	}
}

// TestRecorder_SeedRowSessionPaneLabel: the row's pane carries the seeded
// session label's token when that label is valid, else no pane label.
func TestRecorder_SeedRowSessionPaneLabel(t *testing.T) {
	const id = "agent-row"
	cases := []struct {
		name string
		opts func(token, storeID string) []tmuxfix.RowSessionOption
		want func(token string) string
	}{
		{"row-label", func(string, string) []tmuxfix.RowSessionOption { return nil },
			func(token string) string { return token }},
		{"old-label", func(_, storeID string) []tmuxfix.RowSessionOption {
			return []tmuxfix.RowSessionOption{tmuxfix.WithRowSessionLabel(tmuxfix.Valid(tmuxfix.OtherToken, id, storeID), true)}
		}, func(string) string { return tmuxfix.OtherToken }},
		{"malformed-label", func(string, string) []tmuxfix.RowSessionOption {
			return []tmuxfix.RowSessionOption{tmuxfix.WithRowSessionLabel(tmux.Label{}, true)}
		}, func(string) string { return "" }},
		{"no-label", func(string, string) []tmuxfix.RowSessionOption {
			return []tmuxfix.RowSessionOption{tmuxfix.WithRowSessionLabel(tmux.Label{}, false)}
		}, func(string) string { return "" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dbPath := filepath.Join(t.TempDir(), "state.db")
			if _, err := apitest.SeedSpawn(dbPath, id, "waiting", "/tmp", "off", "", true); err != nil {
				t.Fatalf("SeedSpawn: %v", err)
			}
			cols, err := apitest.ReadSpawnColumns(dbPath, id)
			if err != nil {
				t.Fatalf("ReadSpawnColumns: %v", err)
			}
			storeID, err := apitest.ReadStoreID(dbPath)
			if err != nil {
				t.Fatalf("ReadStoreID: %v", err)
			}
			token, _ := cols.LaunchToken.(string)
			if token == "" || token == tmuxfix.OtherToken {
				t.Fatalf("row token = %q, want a well-formed token other than OtherToken", token)
			}
			r := tmuxfix.NewRecorder()
			r.SeedRowSession(t, dbPath, id, tc.opts(token, storeID)...)
			want := map[string]string{apitest.TestPaneID: tc.want(token)}
			if got := adPanes(t, r, apitest.TestSocket); !reflect.DeepEqual(got, want) {
				t.Errorf("AdPane = %q, want %q", got, want)
			}
		})
	}
}
