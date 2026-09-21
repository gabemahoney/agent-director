package store

import (
	"context"
	"fmt"

	"github.com/gabemahoney/agent-director/internal/trail"
)

// SessionHistoryEntry is one archived (claude_session_id, jsonl_path) pair a
// spawn previously pointed at before a session rotation (b.v2c). jsonl_path is
// the empty string when the archived session never had a recorded transcript
// path (NULL in the column). Ordered newest-first by callers.
type SessionHistoryEntry struct {
	ClaudeSessionID string
	JSONLPath       string
	RecordedAt      string
}

// ListSessionHistory returns every archived prior session for the instance,
// newest recorded first. An absent instance yields an empty (non-nil) slice —
// there is nothing to distinguish "no history" from "no row" here; callers that
// need that distinction check the spawns row separately. Used by get/list to
// surface the "history exists under a different session id" case (AC6/AC8).
func (s *Store) ListSessionHistory(instanceID string) ([]SessionHistoryEntry, error) {
	const q = `SELECT claude_session_id, COALESCE(jsonl_path, ''), recorded_at
	             FROM session_history
	            WHERE claude_instance_id = ?
	         ORDER BY recorded_at DESC, history_id DESC`
	rows, err := s.db.Query(q, instanceID)
	if err != nil {
		return nil, fmt.Errorf("store: list session history: %w", err)
	}
	defer rows.Close()
	out := make([]SessionHistoryEntry, 0)
	for rows.Next() {
		var e SessionHistoryEntry
		if err := rows.Scan(&e.ClaudeSessionID, &e.JSONLPath, &e.RecordedAt); err != nil {
			return nil, fmt.Errorf("store: list session history scan: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list session history iterate: %w", err)
	}
	return out, nil
}

// HealJsonlPath records a now-present transcript path onto a row whose
// jsonl_path is currently NULL (b.v2c AC3, lazy healing). It writes only when
// jsonl_path IS NULL so it never clobbers an already-verified path, and only for
// the given claude_session_id (guarding against a race where the session rotated
// between the find-missing read and this write). Returns true when a row was
// updated. Emits an ad.session.jsonl_healed trail event on a successful write.
func (s *Store) HealJsonlPath(instanceID, sessionID, jsonlPath string) (bool, error) {
	const q = `UPDATE spawns
	              SET jsonl_path = ?
	            WHERE claude_instance_id = ?
	              AND claude_session_id = ?
	              AND jsonl_path IS NULL`
	res, err := s.db.Exec(q, jsonlPath, instanceID, sessionID)
	if err != nil {
		return false, fmt.Errorf("store: heal jsonl path: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("store: heal jsonl path rows affected: %w", err)
	}
	if n == 0 {
		return false, nil
	}
	_ = trail.Emit(context.Background(), "ad.session.jsonl_healed", map[string]any{
		"claude_instance_id": instanceID,
		"claude_session_id":  sessionID,
		"jsonl_path":         jsonlPath,
		"source":             "ad_spawn_store",
	})
	return true, nil
}

// ListProvisionalTranscripts returns the (instance, session id, cwd,
// config-dir) tuples for every live-state row that has a claude_session_id but
// a NULL jsonl_path — i.e. a session that started but whose transcript had not
// been written when SessionStart fired (b.v2c AC3). find-missing recomposes the
// path for each and stats it, healing rows whose transcript has since appeared.
func (s *Store) ListProvisionalTranscripts() ([]ProvisionalTranscript, error) {
	placeholders := make([]string, len(liveStates))
	args := make([]any, len(liveStates))
	for i, st := range liveStates {
		placeholders[i] = "?"
		args[i] = st
	}
	q := `SELECT claude_instance_id, claude_session_id, cwd, extra_env
	        FROM spawns
	       WHERE jsonl_path IS NULL
	         AND claude_session_id IS NOT NULL
	         AND claude_session_id != ''
	         AND state IN (` + joinPlaceholders(placeholders) + `)`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list provisional transcripts: %w", err)
	}
	defer rows.Close()
	out := make([]ProvisionalTranscript, 0)
	for rows.Next() {
		var (
			pt           ProvisionalTranscript
			extraEnvJSON string
		)
		if err := rows.Scan(&pt.ClaudeInstanceID, &pt.ClaudeSessionID, &pt.CWD, &extraEnvJSON); err != nil {
			return nil, fmt.Errorf("store: list provisional transcripts scan: %w", err)
		}
		env, derr := decodeExtraEnv(extraEnvJSON)
		if derr != nil {
			return nil, fmt.Errorf("store: list provisional transcripts decode extra_env: %w", derr)
		}
		pt.ConfigDir = env["CLAUDE_CONFIG_DIR"]
		out = append(out, pt)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list provisional transcripts iterate: %w", err)
	}
	return out, nil
}

// ProvisionalTranscript is one live row with a session id but no recorded
// transcript path yet (b.v2c AC3). ConfigDir is the row's
// ExtraEnv["CLAUDE_CONFIG_DIR"] (empty when the key is absent), so find-missing
// can recompose the transcript path under the correct config dir.
type ProvisionalTranscript struct {
	ClaudeInstanceID string
	ClaudeSessionID  string
	CWD              string
	ConfigDir        string
}

// joinPlaceholders joins pre-built "?" placeholder tokens with commas. Kept
// local to avoid importing strings for a one-liner in this file.
func joinPlaceholders(ph []string) string {
	out := ""
	for i, p := range ph {
		if i > 0 {
			out += ","
		}
		out += p
	}
	return out
}
