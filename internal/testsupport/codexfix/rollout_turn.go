package codexfix

import (
	"bytes"
	_ "embed"
)

// erroredTurn is testdata/rollout-errored-turn.jsonl: a Codex 0.159.2 rollout
// trimmed from a real errored turn (b.x72, session 01a16668, 2026-10-23) to its
// session_meta, task_started, turn_context and the task_complete that carries
// error.codex_error_info server_overloaded. Prompt-bearing records, the base
// instructions, account ids and git details were dropped and the cwd set to
// /work/demo; every other value is as Codex wrote it.
//
//go:embed testdata/rollout-errored-turn.jsonl
var erroredTurn []byte

// ErroredTurn is the captured rollout of one Codex turn that ended with an
// error and no Stop hook: SessionID is its session_meta id, TurnID the turn
// its task_started and task_complete name. Must use: a test that needs a
// Codex turn's end in a rollout builds it from ErroredTurn, editing a copy of
// its records for a variant (another turn, no error); never hand-write
// task_started or task_complete records.
var ErroredTurn = RolloutTurn{
	SessionID: "01a16668-480f-7080-adb4-b91464140a96",
	TurnID:    "01a16668-4862-7930-b160-2863caf991d1",
	data:      erroredTurn,
}

// RolloutTurn is a rollout whose last record is its turn's task_complete;
// Started and Ended give its content for RolloutPath. Must use: a test passes
// a captured turn's rollout through a RolloutTurn (ErroredTurn), never its own
// copy of the records.
type RolloutTurn struct {
	SessionID, TurnID string
	data              []byte
}

// Started is the rollout while the turn runs: every record but the last.
func (r RolloutTurn) Started() []byte {
	body := bytes.TrimSuffix(r.data, []byte("\n"))
	return append([]byte(nil), r.data[:bytes.LastIndexByte(body, '\n')+1]...)
}

// Ended is the whole rollout, ending with the turn's task_complete.
func (r RolloutTurn) Ended() []byte { return append([]byte(nil), r.data...) }
