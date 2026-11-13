package api_test

import (
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
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

// One public creation/projection case per kind; transport tests add only the
// field forwarding contract. No fixed-list mutation is needed in E1.
func TestBusySendPublicSpawnAllAgents(t *testing.T) {
	// Serial: newSpawnEnv sets HOME, TMUX and TMUX_TMPDIR.
	for _, agent := range []string{"claude", "codex", "cursor", "droid", "opencode"} {
		t.Run(agent, func(t *testing.T) {
			e := newSpawnEnv(t)
			e.c = agentClientWithConfig(t, e, "[experimental]\ndroid = true\nopencode = true\n")
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
			facts := busyFacts(t, e.dbPath, result.InstanceID)
			if facts.Coverage != store.BusySendNotCovered || facts.Binding == "" || len(facts.Conversations) != 1 || len(facts.Launches) != 1 || facts.Conversations[0].Provider != agent || facts.Launches[0].Provider != agent {
				t.Fatalf("fresh core=%+v", facts)
			}
			cols, err := apitest.ReadSpawnColumns(e.dbPath, result.InstanceID)
			if err != nil || cols.TurnStarts != int64(0) {
				t.Fatalf("counter=%v,%v", cols.TurnStarts, err)
			}
			out, err := e.c.Get(result.InstanceID)
			if err != nil {
				t.Fatal(err)
			}
			assertBusyPublic(t, out, store.BusySendNotCovered)
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

func assertBusyPublic(t *testing.T, out api.SpawnRow, want store.BusySendCoverage) {
	t.Helper()
	if out.BusySend != want {
		t.Errorf("get coverage=%s; want %s", out.BusySend, want)
	}
	raw, ok := jsonField(t, out, "busy_send")
	if !ok || string(raw) != `"`+string(want)+`"` {
		t.Errorf("busy_send JSON=%s, present=%t", raw, ok)
	}
	for _, key := range []string{"delivery", "delivery_capabilities", "agent_defaults", "default_send_mode", "default_confirmed", "confirm_from_transcripts", "turn_starts", "busy_conversation_id"} {
		if raw, ok := jsonField(t, out, key); ok {
			t.Errorf("unexpected top-level %s=%s", key, raw)
		}
	}
}

func TestGetBusySendPersistedProjection(t *testing.T) {
	t.Parallel()
	for _, state := range []string{store.StatePending, store.StateEnded} {
		for _, coverage := range []store.BusySendCoverage{store.BusySendNotCovered, store.BusySendCovered} {
			t.Run(state+string(coverage), func(t *testing.T) {
				e := newKillEnv(t)
				r := e.seedReusable(t, agentGone, reuseRowSpec{State: store.StateEnded})
				// Pending is seeded independently rather than manufacturing a new launch.
				id := r.ID
				if state == store.StatePending {
					var err error
					id, err = apitest.SeedSpawn(e.dbPath, "", state, t.TempDir(), "off", "", false)
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
			res, _, err := e.st.MoveToPending(r.ID, row.Snapshot, e.clock.Now().UnixMilli(), token, r.Socket, "", store.LaunchOwner{})
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
