package store

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"
)

// ListFilters captures every filter the list verb can apply. All
// non-zero fields AND together. State accepts a slice so a caller can
// request "waiting OR working" in one call. Labels match by exact
// JSON value via json_extract. Parent matches the column verbatim;
// "" means no parent filter (NOT "rows with NULL parent_id" —
// distinguishing that case is a future concern). Cwd matches the
// canonicalized cwd column. Limit caps the result count; 0 means no
// limit.
type ListFilters struct {
	State  []string
	Labels map[string]string
	Parent string
	Cwd    string
	Limit  int
}

// ListSpawns runs the filtered query and returns rows in unspecified
// order. SRD §4.2 explicitly accepts the linear scan + json_extract
// cost up to the low thousands of rows; no index is added for label
// filtering.
//
// SQL composition: each non-zero filter contributes one AND clause
// with positional `?` placeholders so the driver handles quoting. No
// caller string ever lands in the SQL text.
func (s *Store) ListSpawns(f ListFilters) ([]Spawn, error) {
	var where []string
	var args []any

	if len(f.State) > 0 {
		placeholders := make([]string, len(f.State))
		for i, st := range f.State {
			placeholders[i] = "?"
			args = append(args, st)
		}
		where = append(where, "state IN ("+strings.Join(placeholders, ",")+")")
	}

	// Label filters are sorted by key so the generated SQL is
	// deterministic across runs — easier on debug logs and any future
	// query-plan inspection.
	if len(f.Labels) > 0 {
		keys := make([]string, 0, len(f.Labels))
		for k := range f.Labels {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			where = append(where, "json_extract(labels, ?) = ?")
			args = append(args, jsonPathFlatKey(k), f.Labels[k])
		}
	}

	if f.Parent != "" {
		where = append(where, "parent_id = ?")
		args = append(args, f.Parent)
	}

	if f.Cwd != "" {
		where = append(where, "cwd = ?")
		args = append(args, f.Cwd)
	}

	q := `SELECT claude_instance_id, COALESCE(parent_id, ''), state, cwd,
	             tmux_session_name, claude_args, relay_mode,
	             COALESCE(jsonl_path, ''), COALESCE(claude_session_id, ''),
	             labels, started_at, last_seen_at, ended_at
	        FROM spawns`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	if f.Limit > 0 {
		q += " LIMIT ?"
		args = append(args, f.Limit)
	}

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list spawns query: %w", err)
	}
	defer rows.Close()

	var out []Spawn
	for rows.Next() {
		var (
			sp         Spawn
			argsJSON   string
			labelsJSON string
			endedAt    sql.NullTime
		)
		if err := rows.Scan(
			&sp.ClaudeInstanceID, &sp.ParentID, &sp.State, &sp.CWD,
			&sp.TmuxSessionName, &argsJSON, &sp.RelayMode,
			&sp.JSONLPath, &sp.ClaudeSessionID,
			&labelsJSON, &sp.StartedAt, &sp.LastSeenAt, &endedAt,
		); err != nil {
			return nil, fmt.Errorf("store: list spawns scan: %w", err)
		}
		if endedAt.Valid {
			t := endedAt.Time
			sp.EndedAt = &t
		}
		if sp.ClaudeArgs, err = decodeArgs(argsJSON); err != nil {
			return nil, fmt.Errorf("store: list spawns decode claude_args: %w", err)
		}
		if sp.Labels, err = decodeLabels(labelsJSON); err != nil {
			return nil, fmt.Errorf("store: list spawns decode labels: %w", err)
		}
		out = append(out, sp)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list spawns iterate: %w", err)
	}
	return out, nil
}

// jsonPathFlatKey returns the SQLite JSONPath expression that matches a
// FLAT (single-level) object key, even when the key contains JSONPath
// metacharacters like `.`, `[`, or `"`. The key is wrapped in double
// quotes with `\` and `"` escaped per JSON string syntax. Without this,
// a label key like `project.team` would be parsed as a nested
// `$.project.team` lookup and never match the flat-key labels blob
// encodeLabels writes.
func jsonPathFlatKey(key string) string {
	esc := strings.ReplaceAll(key, `\`, `\\`)
	esc = strings.ReplaceAll(esc, `"`, `\"`)
	return `$."` + esc + `"`
}
