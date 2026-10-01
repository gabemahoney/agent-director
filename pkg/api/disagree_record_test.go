package api_test

// disagree_record_test.go holds the single-row verbs' shared
// ad.provenance.disagree record checks (SR-3.16, SR-14, SR-15), where kill,
// send-keys and pause give their verb and source, and the keys verbs'
// shared disagree table. It holds no tests.

import (
	"testing"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// disagreeWant is one expected ad.provenance.disagree record; current is the
// current_session_name (null when "") and ours says tmux_session_id is the
// row's session id (else null).
type disagreeWant struct {
	reason, server, verdict, action, current string
	ours                                     bool
}

// assertDisagreeRecord checks one ad.provenance.disagree record of r,
// written by verb with source, against want.
func assertDisagreeRecord(t *testing.T, rec map[string]any, r killRow, verb, source string, want disagreeWant) {
	t.Helper()
	var sessionID, current any
	if want.ours {
		sessionID = r.Session.ID
	}
	if want.current != "" {
		current = want.current
	}
	fields := map[string]any{
		"claude_instance_id": r.ID, "verb": verb, "source": source, "reason": want.reason,
		"tmux_socket": r.Socket, "tmux_session_name": r.Name, "tmux_session_id": sessionID,
		"current_session_name": current, "server": want.server, "verdict": want.verdict, "action": want.action,
	}
	for k, v := range fields {
		if got, ok := rec[k]; !ok || got != v {
			t.Errorf("%s: %s = %v (present %t); want %v", want.reason, k, got, ok, v)
		}
	}
	ktrAssertCaller(t, rec)
}

// adoptedRecords counts id's ad.provenance.disagree records with reason
// adopted written by verb.
func adoptedRecords(t *testing.T, verb, id string) int {
	t.Helper()
	n := 0
	for _, l := range pendTrail(t, "ad.provenance.disagree", id) {
		if l["verb"] == verb && l["reason"] == tmux.ReasonAdopted {
			n++
		}
	}
	return n
}

// keysDisagreeCase is one row of the keys verbs' disagree table: the row's
// spec, the setups run on it and the records one call writes, in order.
type keysDisagreeCase struct {
	name  string
	spec  killRowSpec
	setup []func(*testing.T, *killEnv, *killRow)
	want  []disagreeWant
}

// keysDisagreeCases is the table send-keys and pause share (one emit, source
// ad_send_keys): each reason once per call, the action what was typed.
func keysDisagreeCases() []keysDisagreeCase {
	type setups = []func(*testing.T, *killEnv, *killRow)
	restarted := func(action string) []disagreeWant {
		return []disagreeWant{{reason: "server_restarted", server: "restarted", verdict: "ours", action: action, ours: true}}
	}
	adopted := disagreeWant{reason: "adopted", server: "unknown", verdict: "ours", action: "keys_sent", ours: true}
	lostReply := killRowSpec{NoServerIdentity: true, NoPane: true}
	conflict := func(reason string) []disagreeWant {
		return []disagreeWant{{reason: reason, server: "match", verdict: "provenance_conflict", action: "nothing_sent"}}
	}
	return []keysDisagreeCase{
		{name: "normal ours writes none"},
		{name: "SessionStart pid differs from the pane pid writes none",
			spec: killRowSpec{Opts: []apitest.SpawnOption{apitest.WithPID(apitest.TestPanePID + 5)}}},
		{name: "server_restarted", setup: setups{ktrRestart}, want: restarted("keys_sent")},
		{name: "server_restarted, text call timed out",
			setup: setups{ktrRestart, ktrScript(tmux.FailTimeout, tmux.CallSendText)}, want: restarted("text_sent")},
		{name: "server_restarted, Enter call timed out",
			setup: setups{ktrRestart, ktrScript(tmux.FailTimeout, tmux.CallSendEnter)}, want: restarted("text_sent")},
		{name: "server_restarted, Enter call failed",
			setup: setups{ktrRestart, ktrScript(tmux.FailUnrecognized, tmux.CallSendEnter)}, want: restarted("text_sent")},
		{name: "server_restarted on the lookup and the follow-up, text call failed",
			setup: setups{ktrRestart, ktrScript(tmux.FailUnrecognized, tmux.CallSendText)}, want: restarted("nothing_sent")},
		{name: "server_mismatch", setup: setups{ktrRebind}, want: []disagreeWant{
			{reason: "server_mismatch", server: "differs", verdict: "different_server", action: "nothing_sent"}}},
		{name: "adopted", spec: lostReply, want: []disagreeWant{adopted}},
		{name: "adoption write not applied writes no adopted", spec: lostReply,
			setup: setups{func(t *testing.T, e *killEnv, r *killRow) { e.rowWriteAfter(t, tmux.CallLookup, *r) }}},
		{name: "duplicate_label", setup: setups{sktDuplicate}, want: conflict("duplicate_label")},
		{name: "scope_value",
			setup: setups{func(_ *testing.T, e *killEnv, r *killRow) {
				e.rec.SetScope(r.Socket, tmuxfix.ScopeGlobal, tmuxfix.ScopeValue{SessionID: r.Session.ID, Label: r.current()})
			}},
			want: conflict("scope_value")},
		{name: "name_changed", spec: killRowSpec{NoSession: true}, setup: setups{ktrRenamed},
			want: []disagreeWant{{reason: "name_changed", server: "match", verdict: "ours", action: "keys_sent",
				current: "renamed-kill", ours: true}}},
		{name: "adopted and name_changed", spec: killRowSpec{NoServerIdentity: true, NoPane: true, NoSession: true},
			setup: setups{ktrRenamed},
			want: []disagreeWant{adopted, {reason: "name_changed", server: "unknown", verdict: "ours", action: "keys_sent",
				current: "renamed-kill", ours: true}}},
	}
}

// seedKeysDisagreeCase seeds tc's row and another row's label on its server;
// it returns the row and that other row's id, which no record may hold.
func (e *killEnv) seedKeysDisagreeCase(t *testing.T, tc keysDisagreeCase) (killRow, string) {
	t.Helper()
	r := sktRow(tc.spec, tc.setup...)(t, e)
	other := "other-" + uuid.NewString()[:8]
	e.rec.SeedSessions(r.Socket, tmuxfix.SeedSession{Name: "foreign-" + uuid.NewString()[:8], Label: r.foreign(other)})
	return r, other
}

// assertKeysDisagrees fails unless recs are want, written by verb with source
// ad_send_keys, none holding r's token, the store id or other.
func assertKeysDisagrees(t *testing.T, e *killEnv, recs []map[string]any, r killRow, verb, other string, want []disagreeWant) {
	t.Helper()
	if len(recs) != len(want) {
		t.Fatalf("ad.provenance.disagree records = %d; want %d: %v", len(recs), len(want), recs)
	}
	for i, w := range want {
		assertDisagreeRecord(t, recs[i], r, verb, "ad_send_keys", w)
		ktrAssertNoForeignContent(t, recs[i], r.Token, e.storeID, other)
	}
}
