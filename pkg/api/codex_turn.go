package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/gabemahoney/agent-director/internal/store"
)

// The turn-end read examines a rollout's end backward, first its last
// minTurnEndTail bytes, doubling up to maxTurnEndTail until a line decides.
// Each round reads only the bytes before those already read, so the whole
// read is at most maxTurnEndTail bytes.
// Codex writes a turn's task_started and task_complete as the turn starts and
// ends, so one of them is usually among the rollout's last records; a late
// background command's records after them are small beside the bound.
const (
	minTurnEndTail = 64 << 10
	maxTurnEndTail = 1 << 20
)

// codexRolloutEndsTurn reports whether the rollout at path, session's,
// records turn as complete: its last event_msg record of type task_started or
// task_complete, among its last maxTurnEndTail bytes, is a task_complete
// whose turn_id is turn, errored or not. A later task_started (a turn Codex
// began with no UserPromptSubmit) or another turn's task_complete reads
// false. It is opened as readCodexContext opens one: only an absolute path
// whose file name carries session, to a regular file (never a symlink or a
// FIFO, and the file opened is the file examined). A partial first line of
// the tail, and any line that does not parse, records nothing. Any failure
// reads false.
func codexRolloutEndsTurn(session, path, turn string) bool {
	if turn == "" {
		return false
	}
	last := codexRolloutLastTurn(session, path)
	return last != nil && last.kind == "task_complete" && last.turn == turn
}

// codexTurnObservation retains only structural task facts, never error contents.
type codexTurnObservation struct {
	kind    string
	turn    string
	errored bool
}

// codexLastTurnErrored is a live observation, independent of state settling.
// A new task_started clears it; sending input or answering a menu does not.
// Unsupported agents and unavailable evidence return unknown, not success.
func codexLastTurnErrored(row Spawn) *bool {
	if row.Agent != store.AgentCodex {
		return nil
	}
	last := codexRolloutLastTurn(row.SessionID, row.JSONLPath)
	if last == nil {
		return nil
	}
	return &last.errored
}

// codexRolloutLastTurn shares the lifecycle reader's bounded, safe file access.
// It observes the latest task boundary even when no turn hook was recorded.
// Future task-boundary observations must reuse this bounded reader instead of
// adding another rollout scanner.
func codexRolloutLastTurn(session, path string) *codexTurnObservation {
	if session == "" || !filepath.IsAbs(path) || !strings.Contains(filepath.Base(path), session) {
		return nil
	}
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() || before.Size() <= 0 {
		return nil
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(before, opened) {
		return nil
	}
	size := opened.Size()
	end := size
	var carry []byte
	for n := int64(minTurnEndTail); ; n = min(2*n, maxTurnEndTail) {
		start := max(size-n, 0)
		data := make([]byte, end-start, end-start+int64(len(carry)))
		m, err := f.ReadAt(data, start)
		if err != nil && !errors.Is(err, io.EOF) {
			return nil
		}
		if m < len(data) {
			// A short read before the carried line leaves a gap in the window.
			if end < size {
				return nil
			}
			data = data[:m]
		}
		data = append(data, carry...)
		end = start
		if start > 0 {
			if i := bytes.IndexByte(data, '\n'); i >= 0 {
				carry, data = data[:i], data[i+1:]
			} else {
				carry, data = data, nil
			}
		}
		if last := lastTurnEvent(data); last != nil {
			return last
		}
		if start == 0 || n == maxTurnEndTail {
			return nil
		}
	}
}

// lastTurnEvent scans data's lines from the last for the first event_msg
// record of type task_started or task_complete. A line that does not parse
// decides nothing. Error presence is an exact key lookup: null counts too.
func lastTurnEvent(data []byte) *codexTurnObservation {
	for len(data) > 0 {
		line := data
		if i := bytes.LastIndexByte(data, '\n'); i >= 0 {
			line, data = data[i+1:], data[:i]
		} else {
			data = nil
		}
		if !bytes.Contains(line, []byte("task_started")) && !bytes.Contains(line, []byte("task_complete")) {
			continue
		}
		var r struct {
			Type    string                     `json:"type"`
			Payload map[string]json.RawMessage `json:"payload"`
		}
		if json.Unmarshal(line, &r) != nil || r.Type != "event_msg" {
			continue
		}
		var kind, turn string
		if json.Unmarshal(r.Payload["type"], &kind) != nil {
			continue
		}
		if raw, ok := r.Payload["turn_id"]; ok && json.Unmarshal(raw, &turn) != nil {
			continue
		}
		switch kind {
		case "task_started":
			return &codexTurnObservation{kind: kind, turn: turn}
		case "task_complete":
			_, errored := r.Payload["error"]
			return &codexTurnObservation{kind: kind, turn: turn, errored: errored}
		}
	}
	return nil
}
