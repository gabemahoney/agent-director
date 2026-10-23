package hook_test

// hook_codex_turn_end_test.go — b.x72: a Codex turn that ends with an error
// sends no Stop; its rollout's task_complete for the row's turn is the turn end
// a status, get or list read applies, and a late tool hook of an ended turn
// does not move the row back to working. Hooks go through hook.Handle with the
// Codex payload fixtures, the rollout is codexfix.ErroredTurn.

import (
	"os"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/codexfix"
	"github.com/gabemahoney/agent-director/pkg/api"
)

// cxTurnFire fires fixture at id's row as its own Codex agent, in
// codexfix.ErroredTurn's session with path as its transcript and, unless turn
// is "", turn as its turn_id.
func cxTurnFire(t *testing.T, st *store.Store, id, path, fixture, turn string) {
	t.Helper()
	extra := map[string]any{"session_id": codexfix.ErroredTurn.SessionID, "transcript_path": path}
	if turn != "" {
		extra["turn_id"] = turn
	}
	payload, _, _ := codexPayload(t, fixture, false, extra)
	cxFire(t, st, id, cxOwn(t, st, id), payload)
}

// cxTurnRow is a Codex row that reported in (SessionStart, recording the
// rollout at the returned path) and began codexfix.ErroredTurn's turn
// (UserPromptSubmit): raw working, the rollout still without its task_complete.
func cxTurnRow(t *testing.T, id string) (*store.Store, string) {
	t.Helper()
	return cxTurnRowAfter(t, id, "")
}

// cxTurnRowAfter is cxTurnRow with, unless priorTurn is "", a normal turn
// priorTurn begun (UserPromptSubmit) and ended by Stop before the errored
// turn's UserPromptSubmit, as in reported instance 5ace3681.
func cxTurnRowAfter(t *testing.T, id, priorTurn string) (*store.Store, string) {
	t.Helper()
	st, _ := cxSeed(t, id, store.StatePending)
	path := codexfix.TempRolloutPath(t, codexfix.ErroredTurn.SessionID, true, codexfix.ErroredTurn.Started())
	cxTurnFire(t, st, id, path, "session-start-startup.json", "")
	if priorTurn != "" {
		cxTurnFire(t, st, id, path, "user-prompt-submit.json", priorTurn)
		cxTurnFire(t, st, id, path, "stop.json", priorTurn)
		if got := mustGetSpawn(t, st, id).State; got != store.StateWaiting {
			t.Fatalf("state after the prior turn's Stop = %q; want waiting", got)
		}
	}
	cxTurnFire(t, st, id, path, "user-prompt-submit.json", codexfix.ErroredTurn.TurnID)
	if row := mustGetSpawn(t, st, id); row.State != store.StateWorking || row.JSONLPath != path {
		t.Fatalf("seeded state/jsonl_path = %q/%q; want working/%q", row.State, row.JSONLPath, path)
	}
	return st, path
}

// cxRowID is name as a row id: spaces, dots and slashes as dashes.
func cxRowID(name string) string {
	return strings.NewReplacer(" ", "-", ".", "-", "/", "-").Replace(name)
}

// cxEndTurn writes the turn's task_complete (with its error) to the rollout,
// followed by any later records.
func cxEndTurn(t *testing.T, path string, later ...string) {
	t.Helper()
	data := codexfix.ErroredTurn.Ended()
	for _, l := range later {
		data = append(data, l+"\n"...)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// cxLateItem is the item_completed Codex writes when a background command of
// codexfix.ErroredTurn's turn finishes after its task_complete, shaped as
// session 01a1627e's (2026-10-22T08:50:17Z) with the command and output
// trimmed: a record of the turn that is no turn end.
var cxLateItem = `{"timestamp":"2026-10-23T01:38:20.000Z","ordinal":14,"type":"event_msg","payload":{"type":"item_completed","thread_id":"` +
	codexfix.ErroredTurn.SessionID + `","turn_id":"` + codexfix.ErroredTurn.TurnID +
	`","started_at_ms":1792719473000,"completed_at_ms":1792719500000,"item":{"type":"CommandExecution","id":"exec-fixture-1",` +
	`"command":["/bin/bash","-lc","sleep 30"],"cwd":"file:///work/demo","source":"unified_exec_startup","status":"completed","stdout":"","stderr":"","exit_code":0}}}`

// cxTurnReaders are the reads that report a row's state and family_state.
var cxTurnReaders = []struct {
	name string
	read func(t *testing.T, st *store.Store, id string) (state, family string)
}{
	{"status", func(t *testing.T, st *store.Store, id string) (string, string) {
		r, err := api.Status(st, api.RelayView{}, id)
		if err != nil {
			t.Fatalf("Status: %v", err)
		}
		return r.State, r.FamilyState
	}},
	{"get", func(t *testing.T, st *store.Store, id string) (string, string) {
		r, err := api.Get(st, api.RelayView{}, id)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		return r.State, r.FamilyState
	}},
	{"list", func(t *testing.T, st *store.Store, id string) (string, string) {
		r, err := api.List(st, api.RelayView{}, api.ListParams{})
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		for _, row := range r.Spawns {
			if row.InstanceID == id {
				return row.State, row.FamilyState
			}
		}
		t.Fatalf("List: no row %s in %+v", id, r.Spawns)
		return "", ""
	}},
}

// cxAssertReads fails unless every reader reports id's row and its family as
// want, and its stored raw state is want.
func cxAssertReads(t *testing.T, st *store.Store, id, want string) {
	t.Helper()
	for _, r := range cxTurnReaders {
		if state, family := r.read(t, st, id); state != want || family != want {
			t.Errorf("%s state/family_state = %q/%q; want %q/%q", r.name, state, family, want, want)
		}
	}
	if got := mustGetSpawn(t, st, id).State; got != want {
		t.Errorf("raw state = %q; want %q", got, want)
	}
}

// TestCodexErroredTurnReadsWaiting (b.x72): the rollout ends the row's turn
// with task_complete carrying a server_overloaded error and no Stop arrives;
// whichever of status, get and list reads first moves the row to waiting, with
// one ad.spawn.state_transition working to waiting, and every read after it
// reports waiting. It does so whether the errored turn is the session's first
// (instance 8780c415) or follows a normal turn ended by Stop, which recorded
// that turn as ended (instance 5ace3681).
func TestCodexErroredTurnReadsWaiting(t *testing.T) {
	const priorTurn = "019f0000-0000-7000-8000-000000000006" // stop.json's own turn_id
	variants := []struct{ name, prior string }{
		{"first turn", ""},
		{"after a stopped turn", priorTurn},
	}
	for _, v := range variants {
		for _, first := range cxTurnReaders {
			t.Run(v.name+"/"+first.name, func(t *testing.T) {
				id := cxRowID("cx-errored-turn-" + v.name + "-" + first.name)
				st, path := cxTurnRowAfter(t, id, v.prior)
				cxEndTurn(t, path)
				before := len(readTrailLines(t, trailFile()))

				if state, family := first.read(t, st, id); state != store.StateWaiting || family != store.StateWaiting {
					t.Errorf("first read (%s) state/family_state = %q/%q; want waiting/waiting", first.name, state, family)
				}
				cxAssertReads(t, st, id, store.StateWaiting)

				var moves int
				for _, l := range readTrailLines(t, trailFile())[before:] {
					if l["instance_id"] == id && l["event"] == "ad.spawn.state_transition" &&
						l["prior_state"] == store.StateWorking && l["new_state"] == store.StateWaiting {
						moves++
					}
				}
				if moves != 1 {
					t.Errorf("ad.spawn.state_transition working->waiting lines = %d; want 1", moves)
				}
			})
		}
	}
}

// TestCodexLateToolHookKeepsWaiting (b.x72): once the row's turn has ended,
// by the rollout's task_complete (read waiting), by Stop or by Interrupt, a
// PreToolUse or PostToolUse of that same turn (a background command finishing,
// as in session 01a1627e) leaves the row waiting. When no read comes between
// the task_complete and the late hook, as nothing shows one did in 01a1627e,
// the hook lands on the still-open turn, and the read after it ends the turn
// past the background command's item_completed.
func TestCodexLateToolHookKeepsWaiting(t *testing.T) {
	ends := []struct {
		name string
		end  func(t *testing.T, st *store.Store, id, path string)
	}{
		{"errored turn read waiting", func(t *testing.T, st *store.Store, id, path string) {
			cxEndTurn(t, path)
			if state, _ := cxTurnReaders[0].read(t, st, id); state != store.StateWaiting {
				t.Fatalf("status after the errored turn = %q; want waiting", state)
			}
		}},
		{"errored turn no read before", func(t *testing.T, st *store.Store, id, path string) {
			cxEndTurn(t, path, cxLateItem)
		}},
		{"stop", func(t *testing.T, st *store.Store, id, path string) {
			cxTurnFire(t, st, id, path, "stop.json", codexfix.ErroredTurn.TurnID)
		}},
		{"interrupt", func(t *testing.T, st *store.Store, id, path string) {
			cxTurnFire(t, st, id, path, "interrupt.json", codexfix.ErroredTurn.TurnID)
		}},
	}
	for _, end := range ends {
		for _, late := range []string{"post-tool-use-bash.json", "pre-tool-use-bash.json"} {
			t.Run(end.name+"/"+late, func(t *testing.T) {
				id := cxRowID("cx-late-tool-" + end.name + "-" + late)
				st, path := cxTurnRow(t, id)
				end.end(t, st, id, path)

				cxTurnFire(t, st, id, path, late, codexfix.ErroredTurn.TurnID)

				cxAssertReads(t, st, id, store.StateWaiting)
			})
		}
	}
}

// TestCodexSubagentToolHookAfterStop (b.x72): a subagent's PreToolUse (its
// agent_id marks it, b.x72's main-thread rule) records no turn and is no late
// hook, even carrying the main session, its rollout and the turn the Stop
// ended, so it moves the waiting row to working as before, and every read
// reports working.
func TestCodexSubagentToolHookAfterStop(t *testing.T) {
	const id = "cx-subagent-tool-after-stop"
	st, path := cxTurnRow(t, id)
	cxTurnFire(t, st, id, path, "stop.json", codexfix.ErroredTurn.TurnID)
	if got := mustGetSpawn(t, st, id).State; got != store.StateWaiting {
		t.Fatalf("state after Stop = %q; want waiting", got)
	}

	cxTurnFire(t, st, id, path, "pre-tool-use-subagent.json", codexfix.ErroredTurn.TurnID)

	cxAssertReads(t, st, id, store.StateWorking)
}

// TestCodexEarlierTurnEndKeepsWorking (b.x72 guard): the rollout's last
// task_complete is for an earlier turn than the row's current one (its
// UserPromptSubmit came after), so a read keeps the row working, whether or
// not a read applied the earlier turn's end first.
func TestCodexEarlierTurnEndKeepsWorking(t *testing.T) {
	const nextTurn = "019f0000-0000-7000-8000-000000000006" // user-prompt-submit.json's own turn_id
	for _, readBetween := range []bool{false, true} {
		name := "no read between turns"
		if readBetween {
			name = "read between turns"
		}
		t.Run(name, func(t *testing.T) {
			id := cxRowID("cx-earlier-turn-" + name)
			st, path := cxTurnRow(t, id)
			cxEndTurn(t, path)
			if readBetween {
				cxTurnReaders[0].read(t, st, id)
			}

			cxTurnFire(t, st, id, path, "user-prompt-submit.json", nextTurn)

			cxAssertReads(t, st, id, store.StateWorking)
		})
	}
}
