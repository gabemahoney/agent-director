package api_test

// security_find_missing_test.go is find-missing's part of SR-15's per-verb
// table (security_test.go; Epic 14): a target row whose agent process cannot
// be checked meets the planted SECRET=xyz sessions on the lookup's
// tmux_name_held marks (SR-11.3, SR-14) and on each lookup disagree reason
// (SR-3.16).

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// securityFindMissingCall runs one find-missing sweep through the Client.
func securityFindMissingCall(_ *testing.T, c *api.Client, _ *securityScene) (any, error) {
	return c.FindMissing(context.Background())
}

// securityFindMissingRecord checks find-missing's ad.launch.name_held record
// as spawn's (securityNameHeldRecord); a mark the store refused must have
// been logged, so the client log check is not vacuous.
func securityFindMissingRecord(t *testing.T, s *securityScene, rec map[string]any, texts map[string]string) {
	t.Helper()
	securityNameHeldRecord(t, s, rec, texts)
	if rec["row_result"] == "still_pending" && !strings.Contains(texts["client log"], "MarkMissingIfSameLife") {
		t.Errorf("client log = %q; want the refused mark's line", texts["client log"])
	}
}

// fmSecHeld is the ad.launch.name_held fields of a find-missing mark with
// lookup outcome outcome and row result rowResult.
func fmSecHeld(outcome, rowResult string) map[string]any {
	return map[string]any{"source": "ad_find_missing", "launch": nil, "outcome": nil,
		"lookup_outcome": outcome, "row_result": rowResult}
}

// fmSecDisagree is the ad.provenance.disagree fields of find-missing's
// reason record with the lookup's server and verdict and the row's action.
func fmSecDisagree(reason, server, verdict, action string) map[string]any {
	return map[string]any{"verb": "find-missing", "source": "ad_find_missing", "reason": reason,
		"server": server, "verdict": verdict, "action": action}
}

// securityFindMissingCases meet the planted sessions on find-missing's
// tmux_name_held marks (a Gone holder of each kind, a Leftover, a mark the
// store refuses) and on each lookup disagree reason. The target's agent
// start time cannot be read, so its lookup decides.
var securityFindMissingCases = append(secHeldBy(securityCase{name: "gone, ",
	target: killRowSpec{Agent: agentUnreadable, NoSession: true}, fields: fmSecHeld("gone", "marked_missing")}, nil, nil),
	securityCase{
		name: "gone, name held by another store's session", target: killRowSpec{Agent: agentUnreadable, NoSession: true},
		arrange: secOtherStoreHolder, fields: fmSecHeld("gone", "marked_missing"),
	},
	securityCase{
		name: "leftover", target: killRowSpec{Agent: agentUnreadable, NoSession: true},
		arrange: func(t *testing.T, s *securityScene) {
			s.e.seedSession(t, &s.target, tmuxfix.WithRowSessionLabel(s.target.old(), true))
		},
		fields: fmSecHeld("leftover", "marked_missing"),
	},
	securityCase{
		name: "gone, name held by the no-id session, mark refused by the store", holder: securityHolderNoID,
		target: killPendSpec(store.StatePending, apitest.WithLaunchStartedAt(killClockStart.Add(-time.Hour).UnixMilli())),
		arrange: func(t *testing.T, s *securityScene) {
			storefix.InjectWriteFailure(t, s.e.dbPath, storefix.WriteFailReuseRestore, s.target.ID)
		},
		fields: fmSecHeld("gone", "still_pending"),
	},
	securityCase{
		name: "server_restarted", target: killRowSpec{Agent: agentUnreadable, NoSession: true}, event: "ad.provenance.disagree",
		server: func(e *killEnv, socket string) { e.rec.RestartServer(socket, tmuxfix.Server{}) },
		fields: fmSecDisagree("server_restarted", "restarted", "gone", "marked_missing"),
	},
	securityCase{
		name: "server_mismatch", target: killRowSpec{Agent: agentUnreadable, NoSession: true}, event: "ad.provenance.disagree",
		server: func(e *killEnv, socket string) { e.rec.RebindServer(socket, tmuxfix.Server{}) },
		fields: fmSecDisagree("server_mismatch", "differs", "different_server", "left_unverified"),
	},
	securityCase{
		name: "duplicate_label", target: killRowSpec{Agent: agentUnreadable}, arrange: secDuplicate,
		event: "ad.provenance.disagree", fields: fmSecDisagree("duplicate_label", "match", "provenance_conflict", "left_unverified"),
	},
	securityCase{
		name: "scope_value", target: killRowSpec{Agent: agentUnreadable},
		arrange: func(_ *testing.T, s *securityScene) {
			s.e.rec.SetScope(s.target.Socket, tmuxfix.ScopeGlobal,
				tmuxfix.ScopeValue{SessionID: s.target.Session.ID, Label: s.target.current()})
		},
		event: "ad.provenance.disagree", fields: fmSecDisagree("scope_value", "match", "provenance_conflict", "left_unverified"),
	},
	securityCase{
		name: "name_changed", target: killRowSpec{Agent: agentUnreadable, NoSession: true},
		arrange: func(t *testing.T, s *securityScene) {
			s.e.seedSession(t, &s.target, tmuxfix.WithRowSessionName("renamed-"+uuid.NewString()[:8]))
		},
		event: "ad.provenance.disagree", fields: fmSecDisagree("name_changed", "match", "ours", "left_unverified"),
	},
	securityCase{
		name: "adopted", target: killRowSpec{Agent: agentUnreadable, NoServerIdentity: true}, event: "ad.provenance.disagree",
		fields: fmSecDisagree("adopted", "unknown", "ours", "left_unverified"),
	},
)
