package store

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/gabemahoney/agent-director/internal/trail"
)

// SessionHistoryEntry is one archived (claude_session_id, jsonl_path) pair a
// spawn previously pointed at before a session rotation (b.v2c). Session
// history belongs to a life: every entry belongs to the life (SR-5.9) of the
// id that was current when its session ran, and the life-taking read returns
// one life's entries only. jsonl_path is the empty string when the archived
// session never had a recorded transcript path (NULL in the column). Ordered
// newest-first by callers.
type SessionHistoryEntry struct {
	ClaudeSessionID string
	JSONLPath       string
	RecordedAt      string
}

// ListSessionHistory returns the instance's archived sessions of the given
// life (SR-5.9) only, newest recorded first; entries of the instance's other
// lives, and of every other instance, never appear. Session history belongs to
// a life: every entry belongs to the life of the id that was current when its
// session ran. Callers pass the life_number of the row they already read
// (Spawn.LifeNumber). No entries — including an absent instance — yields an
// empty (non-nil) slice; there is nothing to distinguish "no history" from
// "no row" here, so callers that need that distinction check the spawns row
// separately.
//
// The read keeps the entry for the row's current session id, if any; it does
// not apply the current-session rule. resume and get do: they show that life's
// history minus the entry for the row's current session id (SR-8.7), and use
// it to surface the "history exists under a different session id" case
// (AC6/AC8).
func (s *Store) ListSessionHistory(instanceID string, life int64) ([]SessionHistoryEntry, error) {
	const q = `SELECT claude_session_id, COALESCE(jsonl_path, ''), recorded_at
	             FROM session_history
	            WHERE claude_instance_id = ?
	              AND life_number = ?
	         ORDER BY recorded_at DESC, history_id DESC`
	rows, err := s.db.Query(q, instanceID, life)
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

// querier is the statement surface shared by *sql.DB, *sql.Tx and
// connQuerier, so a store-internal write can run on the pool or inside a
// caller's own transaction.
type querier interface {
	Exec(query string, args ...any) (sql.Result, error)
}

// connQuerier adapts a *sql.Conn, which has ExecContext but no Exec, to
// querier, so a helper taking a querier runs inside a transaction begun on
// that connection by hand (ResetForReuse's BEGIN IMMEDIATE).
type connQuerier struct {
	ctx  context.Context
	conn *sql.Conn
}

// Exec runs query on the adapted connection.
func (q connQuerier) Exec(query string, args ...any) (sql.Result, error) {
	return q.conn.ExecContext(q.ctx, query, args...)
}

// upsertSessionHistoryEntry archives (sessionID, jsonlPath) as an entry of
// instanceID's history in life (SR-5.9), on q — the pool, or a transaction
// the caller already holds; it runs exactly one statement and reads
// nothing, so the caller supplies values it has already read. An empty
// jsonlPath is stored as NULL. Entries stay keyed on (instance id, session id)
// and are never duplicated:
//   - no entry yet: a new entry in life, recorded_at now;
//   - an entry in the same life: the path is updated only when jsonlPath is
//     non-empty (an already-recorded path survives a NULL one), and
//     recorded_at is refreshed (b.5jm/4);
//   - an entry in another life: the entry moves to life and takes jsonlPath
//     exactly, NULL included — no path from the other life survives, since it
//     may point under that life's directories — and recorded_at is refreshed.
//
// It emits nothing, advances no row_version and returns the driver's error
// unwrapped; callers own their trail events, error wrapping and failure
// policy.
func upsertSessionHistoryEntry(q querier, instanceID, sessionID, jsonlPath string, life int64) error {
	var pathArg any
	if jsonlPath != "" {
		pathArg = jsonlPath
	}
	// In an upsert's DO UPDATE every right-hand side reads the existing
	// entry's values, so life_number in the CASE is the stored life even
	// though the same statement assigns it.
	_, err := q.Exec(
		`INSERT INTO session_history
		     (claude_instance_id, claude_session_id, jsonl_path, life_number)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(claude_instance_id, claude_session_id) DO UPDATE SET
		     jsonl_path  = CASE WHEN session_history.life_number = excluded.life_number
		                        THEN COALESCE(excluded.jsonl_path, session_history.jsonl_path)
		                        ELSE excluded.jsonl_path
		                   END,
		     life_number = excluded.life_number,
		     recorded_at = CURRENT_TIMESTAMP`,
		instanceID, sessionID, pathArg, life,
	)
	return err
}

// HealJsonlPath records a now-present transcript path onto a row whose
// jsonl_path is currently NULL (b.v2c AC3, lazy healing). It writes only when
// jsonl_path IS NULL so it never clobbers an already-verified path, and only for
// the given claude_session_id (guarding against a race where the session rotated
// between the find-missing read and this write). Returns true when a row was
// updated. Emits an ad.session.jsonl_healed trail event on a successful write.
//
// A write advances row_version by one and leaves launch_started_at
// unchanged; a guard miss advances nothing (SR-5.2).
func (s *Store) HealJsonlPath(instanceID, sessionID, jsonlPath string) (bool, error) {
	const q = `UPDATE spawns
	              SET jsonl_path = ?,
	                  ` + rowVersionAdvance + `
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
