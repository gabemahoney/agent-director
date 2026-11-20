package api_test

import (
	"slices"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

func busyFacts(t *testing.T, path, id string) apitest.BusySendFacts {
	t.Helper()
	facts, err := apitest.ReadBusySendFacts(path, id)
	if err != nil {
		t.Fatal(err)
	}
	return facts
}

// exitEvidenceFor is id's exit scope as read, taken as positive exit
// evidence: the test judges every owned process in it gone.
func exitEvidenceFor(t *testing.T, st *store.Store, id string) *store.BusySendExitEvidence {
	t.Helper()
	scope, err := st.ReadBusySendExitScope(id)
	if err != nil {
		t.Fatal(err)
	}
	return &store.BusySendExitEvidence{Scope: scope}
}

// deleteCoveredRow deletes id's covered row as administrator deletion does
// once it established exit evidence (exitEvidenceFor).
func deleteCoveredRow(t *testing.T, st *store.Store, id string) {
	t.Helper()
	row, err := st.GetSpawn(id)
	if err != nil {
		t.Fatal(err)
	}
	if res, err := st.DeleteBusySendCovered(id, row.Snapshot, *exitEvidenceFor(t, st, id)); err != nil || res != store.CondApplied {
		t.Fatalf("DeleteBusySendCovered(%s) = %v, %v; want applied", id, res, err)
	}
}
func seedBusyCoveredConversation(t *testing.T, path, id string) apitest.BusySendFacts {
	t.Helper()
	if err := apitest.SeedBusySendCoverage(path, id, store.BusySendNotCovered, store.BusySendCovered); err != nil {
		t.Fatal(err)
	}
	return busyFacts(t, path, id)
}
func assertBusyLaunchPhase(t *testing.T, path, id, kind, phase string, want store.BusySendCoverage) {
	t.Helper()
	facts := busyFacts(t, path, id)
	n := 0
	for _, l := range facts.Launches {
		if l.Kind == kind && l.Phase == phase {
			n++
			if l.Coverage != string(want) {
				t.Errorf("%s %s coverage=%s; want %s", kind, phase, l.Coverage, want)
			}
			if phase == "current" && l.Conversation != facts.Binding {
				t.Error("current Launch differs from published binding")
			}
			if phase == "failed" && l.EndCause.String != "creation_failed" {
				t.Errorf("failed candidate end=%+v", l.EndCause)
			}
		}
	}
	if n != 1 {
		t.Fatalf("%s %s count=%d; facts=%+v", kind, phase, n, facts)
	}
}
func assertBusyProjection(t *testing.T, path, id string, old apitest.BusySendFacts) {
	t.Helper()
	facts := busyFacts(t, path, id)
	if facts.Binding != old.Binding || facts.Coverage != old.Coverage {
		t.Fatalf("projection changed: %+v; prior %+v", facts, old)
	}
	for _, prior := range old.Conversations {
		if prior.ID != old.Binding || prior.Publication != "published" {
			continue
		}
		for _, current := range facts.Conversations {
			if current.ID == prior.ID && current.Publication == "published" && current.Coverage == prior.Coverage {
				return // Candidate records and admitted sequence/revision changes are independent.
			}
		}
		t.Fatalf("published conversation coverage changed: %+v; prior %+v", facts.Conversations, prior)
	}
	t.Fatal("saved projection has no bound published conversation")
}

// TestBusySendPublicSpawnAllAgents (SR-9.1, SR-3.4, SR-5.6): one public
// creation and projection case per kind. A fresh spawn records this release's
// coverage, written out here: only Claude is covered, and only its Launch
// records the claude_hooks route; the launch types nothing into the pane.
func TestBusySendPublicSpawnAllAgents(t *testing.T) {
	// Serial: newSpawnEnv sets HOME, TMUX and TMUX_TMPDIR.
	for _, agent := range []string{"claude", "codex", "cursor", "droid", "opencode"} {
		t.Run(agent, func(t *testing.T) {
			e := newSpawnEnv(t)
			e.c = agentClientWithConfig(t, e, droidRulesConfig(t, "", "", "droid", "opencode"))
			params := api.SpawnParams{Agent: agent, CWD: t.TempDir(), NoPreTrust: true}
			if agent == "droid" || agent == "opencode" {
				params.AgentArgs = paneOnlyArgs(agent, "model")
			}
			if agent == "opencode" {
				t.Setenv("OPENROUTER_API_KEY", "synthetic-api-key")
			}
			result, err := e.c.Spawn(params)
			if err != nil {
				t.Fatal(err)
			}
			want, route := store.BusySendNotCovered, ""
			if agent == "claude" {
				want, route = store.BusySendCovered, string(store.BusySendRouteClaudeHooks)
			}
			facts := busyFacts(t, e.dbPath, result.InstanceID)
			if facts.Coverage != want || facts.Binding == "" || len(facts.Conversations) != 1 || len(facts.Launches) != 1 ||
				facts.Conversations[0].Provider != agent || facts.Launches[0].Provider != agent || facts.Launches[0].RouteKind.String != route {
				t.Fatalf("fresh core=%+v; want coverage %s, route %q", facts, want, route)
			}
			if n := len(e.rec.SocketCallsOf(tmux.CallSendText)) + len(e.rec.SocketCallsOf(tmux.CallSendKey)); n != 0 {
				t.Errorf("the launch typed %d times into the pane; want none", n)
			}
			// A spawn choosing none publishes the out-of-the-box Agent Defaults (SR-1.4).
			if d := facts.Conversations[0].Defaults; d != store.BusySendOutOfBoxDefaults {
				t.Errorf("published Agent Defaults = %+v; want wait_turn, false, false", d)
			}
			cols, err := apitest.ReadSpawnColumns(e.dbPath, result.InstanceID)
			if err != nil || cols.TurnStarts != int64(0) {
				t.Fatalf("counter=%v,%v", cols.TurnStarts, err)
			}
			out, err := e.c.Get(result.InstanceID)
			if err != nil {
				t.Fatal(err)
			}
			assertBusyPublic(t, out, want)
			status, err := e.c.Status(result.InstanceID)
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := jsonField(t, status, "busy_send"); ok {
				t.Error("status gained busy_send")
			}
			listed, err := e.c.List(api.ListParams{})
			if err != nil {
				t.Fatal(err)
			}
			for _, row := range listed.Spawns {
				for _, key := range []string{"busy_send", "busy_conversation_id", "turn_starts"} {
					if _, ok := jsonField(t, row, key); ok {
						t.Errorf("list gained %s", key)
					}
				}
			}

		})
	}
}

// The covered blocks of a row whose inbox is empty and whose Agent Defaults
// are out of the box (SR-5.6), with the one capability table's Claude limits:
// Wait Turn's, then Steer Turn's (SR-1.6).
const (
	busyDefaultsJSON     = `{"send_mode":"wait_turn","confirmed_delivery":false,"confirm_from_transcripts":false}`
	busyEmptyInboxJSON   = `{"held_count":0,"held_bytes":0,"first_wait":null,"handover_count":0,"startup_wait":null,"force":null,"awaiting_turn_start":0}`
	busyCapabilitiesJSON = `{"modes":[{"send_mode":"wait_turn","message_bytes":8000,"prompt_bytes":9600,"text_kind":"utf8_tab_lf"},` +
		`{"send_mode":"steer_turn","message_bytes":8000,"prompt_bytes":9600,"text_kind":"utf8_tab_lf"}],"held_count":1000,"held_bytes":8000000,` +
		`"force":{"admission":"prompt_sighting","limits":["duplicate_text","unrecoverable_escaping","user_hook_after_sighting"],"states":[` +
		`{"state":"idle","outcome":"handed_over","reason":"admitted"},{"state":"tool_running","outcome":"pending","reason":"turn_running"},` +
		`{"state":"between_tools","outcome":"pending","reason":"turn_running"},{"state":"permission_dialog","outcome":"pending","reason":"dialog"},` +
		`{"state":"plan_dialog","outcome":"pending","reason":"dialog"},{"state":"compacting","outcome":"pending","reason":"compaction"},` +
		`{"state":"not_started","outcome":"refused","reason":"agent_starting"},{"state":"ended","outcome":"refused","reason":"launch_ended"}]},` +
		`"confirm_from_transcripts_capability":"not_needed","confirm_from_transcripts_reason":"hook_sighting_first"}`
	// busyFallbackCapabilitiesJSON is a covered row of a provider this version
	// has no capability row for (a later release's covered Codex row): Wait
	// Turn's limits only, and no force route or transcript confirmation.
	busyFallbackCapabilitiesJSON = `{"modes":[{"send_mode":"wait_turn","message_bytes":8000,"prompt_bytes":9600,"text_kind":"utf8_tab_lf"}],"held_count":1000,"held_bytes":8000000,` +
		`"force":null,"confirm_from_transcripts_capability":"cannot_confirm","confirm_from_transcripts_reason":""}`
)

// assertBusyPublic checks get's busy-send projection of a row whose inbox is
// empty: busy_send, and for a covered row only, agent_defaults, delivery and
// delivery_capabilities; never the core's own columns.
func assertBusyPublic(t *testing.T, out api.SpawnRow, want store.BusySendCoverage) {
	t.Helper()
	if out.BusySend != want {
		t.Errorf("get coverage=%s; want %s", out.BusySend, want)
	}
	raw, ok := jsonField(t, out, "busy_send")
	if !ok || string(raw) != `"`+string(want)+`"` {
		t.Errorf("busy_send JSON=%s, present=%t", raw, ok)
	}
	assertBusyBlocks(t, out, out.InstanceID, want == store.BusySendCovered, "agent_defaults", "delivery", "delivery_capabilities")
	for _, key := range []string{"default_send_mode", "default_confirmed", "confirm_from_transcripts", "turn_starts", "busy_conversation_id"} {
		if raw, ok := jsonField(t, out, key); ok {
			t.Errorf("unexpected top-level %s=%s", key, raw)
		}
	}
}

// assertBusyBlocks fails unless out, id's get, status or list row, carries
// exactly the shown blocks of an empty inbox with out-of-the-box defaults when
// covered, and none of the three blocks otherwise.
func assertBusyBlocks(t *testing.T, out any, id string, covered bool, shown ...string) {
	t.Helper()
	capabilities := busyCapabilitiesJSON
	if raw, ok := jsonField(t, out, "agent"); ok && string(raw) != `"claude"` {
		capabilities = busyFallbackCapabilitiesJSON
	}
	for key, block := range map[string]string{"agent_defaults": busyDefaultsJSON, "delivery": busyEmptyInboxJSON, "delivery_capabilities": capabilities} {
		raw, ok := jsonField(t, out, key)
		if want := covered && slices.Contains(shown, key); ok != want || want && string(raw) != block {
			t.Errorf("%s %s=%s, present=%t; want present=%t (%s)", id, key, raw, ok, want, block)
		}
	}
}

// TestGetBusySendPersistedProjection (SR-5.6, SR-9.3): get shows a row's
// recorded coverage, and for a covered row its Agent Defaults, delivery facts
// and capabilities; status shows the defaults and delivery facts, list the
// defaults, neither busy_send. An uncovered row shows none of them.
func TestGetBusySendPersistedProjection(t *testing.T) {
	t.Parallel()
	for _, state := range []string{store.StatePending, store.StateEnded} {
		for _, coverage := range []store.BusySendCoverage{store.BusySendNotCovered, store.BusySendCovered} {
			t.Run(state+string(coverage), func(t *testing.T) {
				e := newKillEnv(t)
				// Codex rows, so each row's coverage is only what the case records
				// (a covered one as a later release would record it).
				e.rowAgent = "codex"
				r := e.seedReusable(t, agentGone, reuseRowSpec{State: store.StateEnded})
				// Pending is seeded independently rather than manufacturing a new launch.
				id := r.ID
				if state == store.StatePending {
					var err error
					id, err = apitest.SeedSpawn(e.dbPath, "", state, t.TempDir(), "off", "", false, apitest.WithAgent("codex"))
					if err != nil {
						t.Fatal(err)
					}
				}
				if err := apitest.SeedBusySendCoverage(e.dbPath, id, coverage, store.BusySendCovered); err != nil {
					t.Fatal(err)
				}
				out, err := api.Get(e.st, api.RelayView{}, id)
				if err != nil {
					t.Fatal(err)
				}
				assertBusyPublic(t, out, coverage)
				c, _ := e.client(t)
				status, err := c.Status(id)
				if err != nil {
					t.Fatal(err)
				}
				assertBusyBlocks(t, status, id, coverage == store.BusySendCovered, "agent_defaults", "delivery")
				listed, err := c.List(api.ListParams{})
				if err != nil {
					t.Fatal(err)
				}
				for _, row := range listed.Spawns {
					assertBusyBlocks(t, row, row.InstanceID, row.InstanceID == id && coverage == store.BusySendCovered, "agent_defaults")
				}
				for _, v := range []any{status, listed.Spawns[0]} {
					if raw, ok := jsonField(t, v, "busy_send"); ok {
						t.Errorf("status or list gained busy_send=%s", raw)
					}
				}
			})
		}
	}
}

// CR2: the same permitted Gone preflight has three different process facts.
// Process evidence on spawns remains distinct from the core's logical Launch End.
func TestBusySendCandidateReplacementProcessEvidence(t *testing.T) {
	t.Parallel()
	for _, state := range []string{"none", "unknown", "exited"} {
		t.Run(state, func(t *testing.T) {
			e := newKillEnv(t)
			r := e.seedResumableRow(t, e.resumableSpec(rlkSettled(e), agentNotRecorded))
			row, err := e.st.GetSpawn(r.ID)
			if err != nil {
				t.Fatal(err)
			}
			const token = "1122334455667788"
			res, _, err := e.st.MoveToPendingWithExit(r.ID, row.Snapshot, e.clock.Now().UnixMilli(), token, r.Socket, "", store.LaunchOwner{}, store.BusySendDefaultsChoice{}, storefix.ExitEvidence(t, e.st, r.ID))
			if err != nil || res != store.CondApplied {
				t.Fatalf("stage=%v,%v", res, err)
			}
			if err = e.st.MarkBusySendLaunchUncertain(r.ID, token); err != nil {
				t.Fatal(err)
			}
			exit := store.ProcessExit{}
			if state != "none" {
				const pid = 919191
				if err = apitest.SeedBusySendCandidateProcess(e.dbPath, r.ID, pid, apitest.LinuxProcStarttime); err != nil {
					t.Fatal(err)
				}
				process := procfix.Unreadable()
				if state == "exited" {
					process = procfix.Gone()
					exit = store.ProcessExit{PID: pid, Start: apitest.LinuxProcStarttime, AtMillis: e.clock.Now().UnixMilli()}
				}
				e.pc.Set(pid, process)
			}
			row, err = e.st.GetSpawn(r.ID)
			if err != nil {
				t.Fatal(err)
			}
			_, res, recorded, err := e.st.RecoveryWrites(row).MarkMissingIfSameLifeFrom(r.ID, row.Snapshot, store.EvidenceTmux, exit)
			if err != nil || res != store.CondApplied || recorded != (state == "exited") {
				t.Fatalf("mark missing=%v,recorded %t,%v", res, recorded, err)
			}
			row, err = e.st.GetSpawn(r.ID)
			if err != nil {
				t.Fatal(err)
			}
			wantProcess := store.ProcessUnknown
			wantConfirmation := store.UnknownConfirmation(store.ReasonNoEvidence)
			if state == "exited" {
				wantProcess = store.ProcessExited
				wantConfirmation = store.Confirmation{Status: store.ConfirmationConfirmed,
					ConfirmedAtMillis: exit.AtMillis, Source: store.EvidenceProcess, CheckedAtMillis: exit.AtMillis}
			}
			if value, confirmation := row.ProcessConfirmation(); value != wantProcess || confirmation != wantConfirmation {
				t.Fatalf("persisted process evidence=%s %+v; want %s %+v", value, confirmation, wantProcess, wantConfirmation)
			}
			for _, l := range busyFacts(t, e.dbPath, r.ID).Launches {
				if l.Token.String == token && (l.Phase != "uncertain" || l.EndCause.Valid || l.EndedAt.Valid) {
					t.Fatalf("spawns process evidence changed uncertain core Launch: %+v", l)
				}
			}
			if _, err = e.resume(r.ID); err != nil {
				t.Fatalf("resume %s=%v", state, err)
			}
			if evidence := storedProcess(t, e.st, r.ID); evidence != "" {
				t.Fatalf("replacement inherited prior launch's process evidence: %s", evidence)
			}
			facts := busyFacts(t, e.dbPath, r.ID)
			found := false
			for _, l := range facts.Launches {
				if l.Token.String == token {
					found = true
					if l.Phase != "superseded" || l.EndCause.String != "candidate_replaced" {
						t.Errorf("%s replacement=%+v; want superseded/candidate_replaced", state, l)
					}
				}
			}
			if !found {
				t.Fatal("replacement deleted uncertain Launch history")
			}
		})
	}
}
