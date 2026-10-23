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
	"time"

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

// codexTurnEnder is the optional store surface of the Codex turn end (b.x72).
// *store.Store satisfies it, and *store.CursorMissingReads through it; a
// store without it ends no turn.
type codexTurnEnder interface {
	CodexOpenTurns(instanceID string) ([]store.CodexOpenTurn, error)
	EndCodexTurn(t store.CodexOpenTurn, maxWait time.Duration) (bool, error)
}

// endCodexTurns is the Codex turn end of a read that reports state (status,
// get, list, and so family_state; b.x72). Codex runs no Stop hook for a turn
// that ends in an error, but its rollout records the turn's task_complete. For
// instanceID's row and its descendants, or every row when instanceID is "",
// that is raw working on a Codex turn not recorded as ended
// (store.CodexOpenTurns), it reads the rollout's end (codexRolloutEndsTurn)
// and, when that records the row's turn as complete, moves the row to waiting
// (store EndCodexTurn), so the read that follows reports it and the family it
// counts in. It runs before the read, takes no settle time and samples no
// pane. Like the reading verbs' other writes it never waits for the write
// lock and fails open: a busy store or any failure leaves the row working for
// this read, which goes ahead, and a later read ends the turn.
func endCodexTurns(s any, instanceID string) {
	e, ok := s.(codexTurnEnder)
	if !ok {
		return
	}
	turns, err := e.CodexOpenTurns(instanceID)
	if err != nil {
		return
	}
	for _, t := range turns {
		if codexRolloutEndsTurn(t.SessionID, t.JSONLPath, t.Turn) {
			_, _ = e.EndCodexTurn(t, readerLockWait)
		}
	}
}

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
	if session == "" || turn == "" || !filepath.IsAbs(path) || !strings.Contains(filepath.Base(path), session) {
		return false
	}
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() || before.Size() <= 0 {
		return false
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return false
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(before, opened) {
		return false
	}
	size := opened.Size()
	end := size
	var carry []byte
	for n := int64(minTurnEndTail); ; n = min(2*n, maxTurnEndTail) {
		start := max(size-n, 0)
		data := make([]byte, end-start, end-start+int64(len(carry)))
		m, err := f.ReadAt(data, start)
		if err != nil && !errors.Is(err, io.EOF) {
			return false
		}
		if m < len(data) {
			// A short read before the carried line leaves a gap in the window.
			if end < size {
				return false
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
		if ended, decided := lastTurnEvent(data, turn); decided {
			return ended
		}
		if start == 0 || n == maxTurnEndTail {
			return false
		}
	}
}

// lastTurnEvent scans data's lines from the last for the first event_msg
// record of type task_started or task_complete. decided is whether it found
// one, and ended whether that is turn's task_complete. A line that does not
// parse decides nothing.
func lastTurnEvent(data []byte, turn string) (ended, decided bool) {
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
			Type    string `json:"type"`
			Payload struct {
				Type   string `json:"type"`
				TurnID string `json:"turn_id"`
			} `json:"payload"`
		}
		if json.Unmarshal(line, &r) != nil || r.Type != "event_msg" {
			continue
		}
		switch r.Payload.Type {
		case "task_started":
			return false, true
		case "task_complete":
			return r.Payload.TurnID == turn, true
		}
	}
	return false, false
}

// Compile-time assertion that *store.Store and *store.CursorMissingReads
// (the Client's reads) end Codex turns.
var (
	_ codexTurnEnder = (*store.Store)(nil)
	_ codexTurnEnder = (*store.CursorMissingReads)(nil)
)
