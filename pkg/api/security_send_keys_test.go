package api_test

// security_send_keys_test.go is send-keys' part of SR-15's per-verb table
// (security_test.go; Epic 11): send-keys on a live row, and on a pending row
// with allow_pending, meets the planted SECRET=xyz sessions on its Gone (a
// planted session or another store's session holding the row's name),
// Leftover, conflicting-labels and Ours paths. It types only into the
// agent's own pane, by pane id, and never into a planted session's pane.

import (
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// skSecText is the text the security rows send; it carries no forbidden value.
const skSecText = "status please"

// securitySendKeysCall returns send-keys' call on the subject, with allowPending.
func securitySendKeysCall(allowPending bool) func(*testing.T, *api.Client, *securityScene) (any, error) {
	return func(_ *testing.T, c *api.Client, s *securityScene) (any, error) {
		return c.SendKeys(api.SendKeysParams{ClaudeInstanceID: s.subject, Text: skSecText, AllowPending: allowPending})
	}
}

// skSecSends checks that no text or Enter call targeted a planted session's
// pane and that the call either typed skSecText into the agent's pane
// (delivered) or made only its lookup and sent nothing.
func skSecSends(delivered bool) func(*testing.T, *securityScene, any) {
	return func(t *testing.T, s *securityScene, _ any) {
		t.Helper()
		planted := map[string]bool{}
		for _, sess := range s.e.rec.Sessions(s.target.Socket) {
			if sess.ID == s.noIDSess || sess.ID == s.otherSess || sess.ID == s.extraSess.ID {
				for _, p := range sess.Panes {
					planted[p.ID] = true
				}
			}
		}
		for _, c := range s.e.rec.SocketCalls() {
			if (c.Call == tmux.CallSendText || c.Call == tmux.CallSendEnter) && planted[c.Target] {
				t.Errorf("%v targets planted pane %s", c.Call, c.Target)
			}
		}
		if delivered {
			s.e.assertDelivered(t, s.target.Socket, s.target.Spawn.Identity.PaneID, skSecText)
			return
		}
		s.e.assertNothingSent(t)
		s.e.assertPaneCalls(t, tmux.CallLookup)
	}
}

// skSecGone is send-keys' gone error for the target: nothing sent, its name quoted.
func skSecGone(s *securityScene) apitest.DescCase {
	return apitest.DescPaneGone(apitest.PaneGone{Verb: apitest.PaneSendKeys, InstanceID: s.target.ID, Name: s.target.Name})
}

// skSecLeftovers is the target's leftover session as the descriptions name it.
func skSecLeftovers(s *securityScene) []apitest.DescSession {
	return []apitest.DescSession{{Name: s.target.Session.Name, ID: s.target.Session.ID}}
}

// securitySendKeysCases meet the planted sessions on send-keys' Gone,
// Leftover, conflicting-labels and Ours paths (SR-7.2) for a row in state;
// ad.send_keys.called records the outcome, row_state and allow_pending.
func securitySendKeysCases(state string) []securityCase {
	pending := state == store.StatePending
	fields := func(outcome string) map[string]any {
		return map[string]any{"outcome": outcome, "row_state": state, "allow_pending": pending}
	}
	gone := func(name string, holder securityHolder, arrange func(*testing.T, *securityScene)) securityCase {
		return securityCase{name: name, target: killRowSpec{State: state, NoSession: true}, holder: holder,
			arrange: arrange, wantErr: api.ErrTmuxSendKeys, desc: skSecGone,
			fields: fields("ErrTmuxSendKeys"), check: skSecSends(false)}
	}
	leftover := securityCase{
		name:   "leftover",
		target: killRowSpec{State: state, NoSession: true},
		arrange: func(t *testing.T, s *securityScene) {
			s.e.seedSession(t, &s.target, tmuxfix.WithRowSessionLabel(s.target.old(), true))
		},
		wantErr: api.ErrTmuxSessionConflict,
		desc: func(s *securityScene) apitest.DescCase {
			return apitest.DescPaneLeftover(apitest.PaneLeftover{Verb: apitest.PaneSendKeys, InstanceID: s.target.ID,
				Sessions: skSecLeftovers(s)})
		},
		fields: fields("ErrTmuxSessionConflict"),
		check:  skSecSends(false),
	}
	if pending {
		leftover.wantErr, leftover.fields = api.ErrSpawnNotInteractive, fields("ErrSpawnNotInteractive")
		leftover.desc = func(s *securityScene) apitest.DescCase {
			return apitest.DescSendKeysPendingLeftover(s.target.ID, skSecLeftovers(s))
		}
	}
	return []securityCase{
		gone("gone, name held by the no-id session", securityHolderNoID, nil),
		gone("gone, name held by the other row's session", securityHolderOther, nil),
		gone("gone, another store's session with this launch's name, token and id", securityHolderNone,
			rpSecOtherStore(func(s *securityScene) string { return s.target.Name },
				func(s *securityScene) string { return s.target.Token })),
		leftover,
		{
			name:     "conflicting labels",
			target:   killRowSpec{State: state},
			arrange:  secDuplicateLabel,
			wantErr:  api.ErrTmuxSessionConflict,
			desc:     secConflictingLabels,
			disagree: true,
			fields:   fields("ErrTmuxSessionConflict"),
			check:    skSecSends(false),
		},
		{
			name:   "ours, keys typed into the agent's pane",
			target: killRowSpec{State: state},
			fields: fields("ok"),
			check:  skSecSends(true),
		},
	}
}
