package api_test

// codex_turn_test.go — b.x72: the turn end a status, get or list read applies
// from a Codex row's rollout (codex_turn.go). The rollout reader's backward
// chunked scan, limits and safety, a stale recorded turn, failing open, and a
// parent's family_state through a real family. The errored turn itself and
// late tool hooks are in internal/hook/hook_codex_turn_end_test.go.

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/codexfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// nextTurn is a turn after codexfix.ErroredTurn's.
const nextTurn = "019f0000-0000-7000-8000-000000000006"

// seedCodexTurn seeds id's Codex row in s (at dbPath), with path as its rollout,
// under parent when that is not "", and then starts codexfix.ErroredTurn's
// turn with an applied UserPromptSubmit (codexHook). The row is raw working on
// an open turn.
func seedCodexTurn(t *testing.T, s *store.Store, dbPath, id, path, parent string) {
	t.Helper()
	turn := codexfix.ErroredTurn
	if _, err := apitest.SeedSpawn(dbPath, id, store.StateWaiting, "", "off", turn.SessionID, false, codexRowOpts(apitest.WithJsonlPath(path))...); err != nil {
		t.Fatal(err)
	}
	if parent != "" {
		if err := apitest.SeedParentChild(dbPath, parent, id); err != nil {
			t.Fatal(err)
		}
	}
	codexHook(t, s, dbPath, id, "UserPromptSubmit", turn.TurnID, store.StateWorking)
	if state := mustGetSpawnState(t, s, id); state != store.StateWorking {
		t.Fatalf("seeded state = %q; want working", state)
	}
}

// codexHook makes the gated hook write of event, moving id's row to newState,
// as Handle makes it for a main-thread Codex hook of turn: a UserPromptSubmit
// records turn as the row's turn, a Stop or Interrupt as its current and
// ended turn, a PreToolUse or PostToolUse carries it as its tool turn. A
// write the gate does not apply fails the test.
func codexHook(t *testing.T, s *store.Store, dbPath, id, event, turn, newState string) {
	t.Helper()
	if err := storefix.WithSeedPane(dbPath, id, func(gate store.HookGate) error {
		gate.Event, gate.CodexTurn = event, turn
		gate.CodexTurnStart = event == "UserPromptSubmit"
		gate.CodexTurnEnd = event == "Stop" || event == "Interrupt"
		gate.CodexToolTurn = event == "PreToolUse" || event == "PostToolUse"
		applied, err := s.ApplyHookTransition(id, gate, newState, false, event, "", false)
		if err == nil && !applied.Applied {
			err = errors.New(event + " not applied: " + applied.Reason)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

// mustGetSpawnState is id's raw state in s.
func mustGetSpawnState(t *testing.T, s *store.Store, id string) string {
	t.Helper()
	row, err := s.GetSpawn(id)
	if err != nil {
		t.Fatal(err)
	}
	return row.State
}

// turnEvent is codexfix.ErroredTurn's event_msg line of payload type kind
// (task_started or task_complete): as captured when edit is nil, otherwise
// re-encoded after edit.
func turnEvent(t *testing.T, kind string, edit func(record, payload map[string]any)) []byte {
	t.Helper()
	for _, line := range bytes.SplitAfter(codexfix.ErroredTurn.Ended(), []byte("\n")) {
		var record map[string]any
		if json.Unmarshal(line, &record) != nil {
			continue
		}
		payload, _ := record["payload"].(map[string]any)
		if record["type"] != "event_msg" || payload["type"] != kind {
			continue
		}
		if edit == nil {
			return append([]byte(nil), line...)
		}
		edit(record, payload)
		out, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		return append(out, '\n')
	}
	t.Fatalf("codexfix.ErroredTurn has no %s line", kind)
	return nil
}

// taskComplete is codexfix.ErroredTurn's task_complete line after edit.
func taskComplete(t *testing.T, edit func(record, payload map[string]any)) []byte {
	t.Helper()
	return turnEvent(t, "task_complete", edit)
}

// nextTaskStarted is nextTurn's task_started line.
func nextTaskStarted(t *testing.T) []byte {
	t.Helper()
	return turnEvent(t, "task_started", func(_, p map[string]any) { p["turn_id"], p["root_turn_id"] = nextTurn, nextTurn })
}

// rolloutPad is one well-formed rollout line of exactly n bytes that ends no turn.
func rolloutPad(n int) []byte {
	const head, tail = `{"type":"event_msg","payload":{"type":"token_count","pad":"`, "\"}}\n"
	return []byte(head + strings.Repeat("x", n-len(head)-len(tail)) + tail)
}

// cat joins rollout pieces.
func cat(parts ...[]byte) []byte { return bytes.Join(parts, nil) }

// codexTurnStore is the store a codexTurnRead reads.
type codexTurnStore interface {
	api.StatusStore
	api.GetStore
	api.ListStore
}

// codexTurnRead is one read that reports a row's state and family_state.
type codexTurnRead struct {
	name string
	read func(s codexTurnStore, id string) (state, family string, err error)
}

var codexTurnReads = []codexTurnRead{
	{"status", func(s codexTurnStore, id string) (string, string, error) {
		r, err := api.Status(s, api.RelayView{}, id)
		return r.State, r.FamilyState, err
	}},
	{"get", func(s codexTurnStore, id string) (string, string, error) {
		r, err := api.Get(s, api.RelayView{}, id)
		return r.State, r.FamilyState, err
	}},
	{"list", func(s codexTurnStore, id string) (string, string, error) {
		r, err := api.List(s, api.RelayView{}, api.ListParams{})
		for _, row := range r.Spawns {
			if row.InstanceID == id {
				return row.State, row.FamilyState, err
			}
		}
		return "", "", err
	}},
}

// turnEnds counts the trail's working-to-waiting moves of id since mark.
func turnEnds(t *testing.T, mark int, id string) int {
	t.Helper()
	n := 0
	for _, l := range readAPITrailLines(t)[mark:] {
		if l["instance_id"] == id && l["event"] == "ad.spawn.state_transition" &&
			l["prior_state"] == store.StateWorking && l["new_state"] == store.StateWaiting {
			n++
		}
	}
	return n
}

// TestCodexTurnRolloutReader (b.x72): a read ends the row's turn only from a
// regular file at an absolute path whose file name carries the session id,
// whose last event_msg task_started or task_complete, scanned backward from
// the end over a window of 64 KiB doubling up to 1 MiB, is a task_complete for
// the row's turn, errored or not. Each round reads only the bytes before the
// last round's, so a line cut by a window's start is read whole once a later
// round reaches its start, however many rounds it spans; one cut by the 1 MiB
// cap, a malformed line and any other record decide nothing. Every other
// rollout leaves the row working and the read succeeds.
func TestCodexTurnRolloutReader(t *testing.T) {
	t.Parallel()
	turn := codexfix.ErroredTurn
	started, ended := turn.Started(), turn.Ended()
	last := taskComplete(t, nil)
	truncated := cat(last[:bytes.Index(last, []byte(turn.TurnID))+len(turn.TurnID)], []byte("\n"))
	startedLine := turnEvent(t, "task_started", nil)
	long := taskComplete(t, func(_, p map[string]any) { p["last_agent_message"] = strings.Repeat("x", 300<<10) })
	rollout := func(content []byte) func(t *testing.T) string {
		return func(t *testing.T) string { return codexfix.TempRolloutPath(t, turn.SessionID, true, content) }
	}
	cases := []struct {
		name    string
		rollout func(t *testing.T) string
		want    string
	}{
		{"non-error task_complete", rollout(cat(started, taskComplete(t, func(_, p map[string]any) {
			delete(p, "error")
			p["last_agent_message"] = "done"
		}))), store.StateWaiting},
		{"task_complete followed by lines that decide nothing", rollout(cat(ended,
			cat(startedLine[:len(startedLine)-3], []byte("\n")),
			[]byte("not json\n"),
			turnEvent(t, "task_started", func(r, _ map[string]any) { r["type"] = "response_item" }),
			turnEvent(t, "task_complete", func(_, p map[string]any) { p["type"], p["message"] = "agent_message", "task_started" }),
			rolloutPad(512))), store.StateWaiting},
		{"task_complete across the first 64 KiB chunk's start", rollout(cat(ended, rolloutPad(64<<10-len(last)/2))), store.StateWaiting},
		{"task_complete across the 512 KiB chunk's start", rollout(cat(ended, rolloutPad(512<<10-len(last)/2))), store.StateWaiting},
		// long ends 100 KiB back and starts about 400 KiB back, so the 128,
		// 256 and 512 KiB rounds each read part of it.
		{"task_complete joined across three rounds", rollout(cat(rolloutPad(256<<10), startedLine, long, rolloutPad(100<<10))), store.StateWaiting},
		{"task_complete just inside the last 1 MiB", rollout(cat(ended, rolloutPad(1<<20-1-len(last)))), store.StateWaiting},
		{"task_complete just beyond the last 1 MiB", rollout(cat(ended, rolloutPad(1<<20-len(last)+1))), store.StateWorking},
		{"task_started of a later turn after the task_complete", rollout(cat(ended, nextTaskStarted(t))), store.StateWorking},
		{"truncated task_complete", rollout(cat(started, truncated)), store.StateWorking},
		{"task_complete of another turn", rollout(cat(started, taskComplete(t, func(_, p map[string]any) {
			p["turn_id"] = nextTurn
		}))), store.StateWorking},
		{"task_complete as another record type", rollout(cat(started, taskComplete(t, func(r, _ map[string]any) {
			r["type"] = "response_item"
		}))), store.StateWorking},
		{"another event naming task_complete", rollout(cat(started, taskComplete(t, func(_, p map[string]any) {
			p["type"], p["message"] = "agent_message", "task_complete"
		}))), store.StateWorking},
		{"symlink", func(t *testing.T) string {
			target := codexfix.TempRolloutPath(t, turn.SessionID, true, ended)
			link := filepath.Join(t.TempDir(), filepath.Base(target))
			if err := os.Symlink(target, link); err != nil {
				t.Fatal(err)
			}
			return link
		}, store.StateWorking},
		{"fifo", func(t *testing.T) string {
			path := codexfix.TempRolloutPath(t, turn.SessionID, false)
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := syscall.Mkfifo(path, 0o600); err != nil {
				t.Fatal(err)
			}
			return path
		}, store.StateWorking},
		{"missing", func(t *testing.T) string {
			return codexfix.TempRolloutPath(t, turn.SessionID, false)
		}, store.StateWorking},
		{"relative path", func(t *testing.T) string {
			cwd, err := os.Getwd()
			if err != nil {
				t.Fatal(err)
			}
			rel, err := filepath.Rel(cwd, codexfix.TempRolloutPath(t, turn.SessionID, true, ended))
			if err != nil || filepath.IsAbs(rel) {
				t.Fatalf("relative rollout path %q: %v", rel, err)
			}
			return rel
		}, store.StateWorking},
		{"file name without the session id", func(t *testing.T) string {
			dir := filepath.Join(t.TempDir(), turn.SessionID)
			path := filepath.Join(dir, "rollout.jsonl")
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, ended, 0o600); err != nil {
				t.Fatal(err)
			}
			return path
		}, store.StateWorking},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s, dbPath := storefix.OpenTempStore(t)
			id := uuid.NewString()
			seedCodexTurn(t, s, dbPath, id, tc.rollout(t), "")
			state, family, err := codexTurnReads[0].read(s, id)
			if err != nil || state != tc.want || family != tc.want {
				t.Errorf("status state/family_state = %q/%q, %v; want %q/%q", state, family, err, tc.want, tc.want)
			}
		})
	}
}

// failingTurnEnder is a store whose Codex turn end fails with an error other
// than a busy store: CodexOpenTurns when open is set, otherwise EndCodexTurn.
type failingTurnEnder struct {
	*store.Store
	open bool
}

func (f failingTurnEnder) CodexOpenTurns(instanceID string) ([]store.CodexOpenTurn, error) {
	if f.open {
		return nil, errors.New("injected codex open turns failure")
	}
	return f.Store.CodexOpenTurns(instanceID)
}

func (f failingTurnEnder) EndCodexTurn(store.CodexOpenTurn, time.Duration) (bool, error) {
	return false, errors.New("injected codex turn end failure")
}

// TestCodexTurnEndFailsOpen (b.x72): while the turn end cannot be written (a
// busy store: another process holds the write lock; or any other store
// error), a read whose rollout ends the row's turn returns at once, with no
// error and the state as read (working), and writes nothing; once the store
// works the next read ends the turn.
func TestCodexTurnEndFailsOpen(t *testing.T) {
	t.Parallel()
	failures := []struct {
		name string
		fail func(t *testing.T, s *store.Store, dbPath string) (codexTurnStore, func())
	}{
		{"busy store", func(t *testing.T, s *store.Store, dbPath string) (codexTurnStore, func()) {
			return s, apitest.HoldWriteLock(t, dbPath, time.Minute)
		}},
		{"open turns error", func(_ *testing.T, s *store.Store, _ string) (codexTurnStore, func()) {
			return failingTurnEnder{s, true}, func() {}
		}},
		{"turn end error", func(_ *testing.T, s *store.Store, _ string) (codexTurnStore, func()) {
			return failingTurnEnder{s, false}, func() {}
		}},
	}
	for _, f := range failures {
		for _, r := range codexTurnReads {
			t.Run(f.name+"/"+r.name, func(t *testing.T) {
				t.Parallel()
				s, dbPath := storefix.OpenTempStore(t)
				id := uuid.NewString()
				path := codexfix.TempRolloutPath(t, codexfix.ErroredTurn.SessionID, true, codexfix.ErroredTurn.Started())
				seedCodexTurn(t, s, dbPath, id, path, "")
				if err := os.WriteFile(path, codexfix.ErroredTurn.Ended(), 0o600); err != nil {
					t.Fatal(err)
				}
				mark := trailMark(t)

				failing, release := f.fail(t, s, dbPath)
				start := time.Now()
				state, family, err := r.read(failing, id)
				if elapsed := time.Since(start); elapsed >= store.DefaultBusyTimeoutMs*time.Millisecond/2 {
					t.Errorf("%s took %v; want no wait for the lock", r.name, elapsed)
				}
				if err != nil || state != store.StateWorking || family != store.StateWorking {
					t.Errorf("%s state/family_state = %q/%q, %v; want working/working, no error", r.name, state, family, err)
				}
				if n := turnEnds(t, mark, id); n != 0 {
					t.Errorf("working->waiting lines while failing = %d; want 0", n)
				}
				release()

				if state, _, err := r.read(s, id); err != nil || state != store.StateWaiting {
					t.Errorf("%s after the failure = %q, %v; want waiting", r.name, state, err)
				}
				if n := turnEnds(t, mark, id); n != 1 {
					t.Errorf("working->waiting lines = %d; want 1", n)
				}
			})
		}
	}
}

// TestCodexTurnStaleTurnKeepsWorking (b.x72): the rollout's last task_complete
// is codexfix.ErroredTurn's (no read ended it) while the agent runs nextTurn,
// so every read keeps the row working: when nextTurn's UserPromptSubmit was
// held by a permission request, which still records it as the row's turn, and
// when nextTurn began with no UserPromptSubmit, so the row still records the
// errored turn but nextTurn's task_started follows its task_complete.
func TestCodexTurnStaleTurnKeepsWorking(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		rollout func(t *testing.T) []byte
		next    func(t *testing.T, s *store.Store, dbPath, id string)
	}{
		{"held UserPromptSubmit", func(*testing.T) []byte { return codexfix.ErroredTurn.Ended() },
			func(t *testing.T, s *store.Store, dbPath, id string) {
				codexHook(t, s, dbPath, id, "PermissionRequest", "", store.StateCheckPermission)
				req, err := apitest.SeedPermissionRequest(dbPath, id, "Bash")
				if err != nil {
					t.Fatal(err)
				}
				codexHook(t, s, dbPath, id, "UserPromptSubmit", nextTurn, store.StateWorking)
				if state := mustGetSpawnState(t, s, id); state != store.StateCheckPermission {
					t.Fatalf("state after the held UserPromptSubmit = %q; want check_permission", state)
				}
				if err := apitest.SetPermissionFacts(dbPath, req.RequestID, apitest.PermissionProof(time.Now())); err != nil {
					t.Fatal(err)
				}
				codexHook(t, s, dbPath, id, "PreToolUse", nextTurn, store.StateWorking)
			}},
		{"no UserPromptSubmit", func(t *testing.T) []byte { return cat(codexfix.ErroredTurn.Ended(), nextTaskStarted(t)) },
			func(t *testing.T, s *store.Store, dbPath, id string) {
				codexHook(t, s, dbPath, id, "PreToolUse", nextTurn, store.StateWorking)
			}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s, dbPath := storefix.OpenTempStore(t)
			id := uuid.NewString()
			path := codexfix.TempRolloutPath(t, codexfix.ErroredTurn.SessionID, true, codexfix.ErroredTurn.Started())
			seedCodexTurn(t, s, dbPath, id, path, "")
			tc.next(t, s, dbPath, id)
			if state := mustGetSpawnState(t, s, id); state != store.StateWorking {
				t.Fatalf("state while nextTurn runs = %q; want working", state)
			}
			if err := os.WriteFile(path, tc.rollout(t), 0o600); err != nil {
				t.Fatal(err)
			}
			mark := trailMark(t)

			for _, r := range codexTurnReads {
				if state, family, err := r.read(s, id); err != nil || state != store.StateWorking || family != store.StateWorking {
					t.Errorf("%s state/family_state = %q/%q, %v; want working/working", r.name, state, family, err)
				}
			}
			if state := mustGetSpawnState(t, s, id); state != store.StateWorking {
				t.Errorf("raw state = %q; want working", state)
			}
			if n := turnEnds(t, mark, id); n != 0 {
				t.Errorf("working->waiting lines = %d; want 0", n)
			}
		})
	}
}

// TestCodexTurnEndParentFamilyState (b.x72): a waiting parent whose Codex
// child's errored turn ended reads family_state waiting through status, get
// and list once a read applies the turn end, whichever read comes first: the
// parent's own or the child's.
func TestCodexTurnEndParentFamilyState(t *testing.T) {
	t.Parallel()
	for _, first := range []struct {
		name  string
		read  codexTurnRead
		child bool
	}{
		{"child status", codexTurnReads[0], true},
		{"list", codexTurnReads[2], false},
		{"parent status", codexTurnReads[0], false},
		{"parent get", codexTurnReads[1], false},
	} {
		t.Run(first.name, func(t *testing.T) {
			t.Parallel()
			s, dbPath := storefix.OpenTempStore(t)
			parent, child := uuid.NewString(), uuid.NewString()
			if _, err := apitest.SeedSpawn(dbPath, parent, store.StateWaiting, "", "off", "", false); err != nil {
				t.Fatal(err)
			}
			path := codexfix.TempRolloutPath(t, codexfix.ErroredTurn.SessionID, true, codexfix.ErroredTurn.Started())
			seedCodexTurn(t, s, dbPath, child, path, parent)
			if _, family, err := codexTurnReads[0].read(s, parent); err != nil || family != store.StateWorking {
				t.Fatalf("parent family_state during the turn = %q, %v; want working", family, err)
			}
			if err := os.WriteFile(path, codexfix.ErroredTurn.Ended(), 0o600); err != nil {
				t.Fatal(err)
			}

			id := parent
			if first.child {
				id = child
			}
			if _, family, err := first.read.read(s, id); err != nil || family != store.StateWaiting {
				t.Errorf("first read (%s) family_state = %q, %v; want waiting", first.name, family, err)
			}
			for _, r := range codexTurnReads {
				if state, family, err := r.read(s, parent); err != nil || state != store.StateWaiting || family != store.StateWaiting {
					t.Errorf("parent %s state/family_state = %q/%q, %v; want waiting/waiting", r.name, state, family, err)
				}
			}
			if row, err := s.GetSpawn(child); err != nil || row.State != store.StateWaiting {
				t.Errorf("child raw state = %q, %v; want waiting", row.State, err)
			}
		})
	}
}
