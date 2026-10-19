package store

import (
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
// canonicalized cwd column. TmuxSessionName matches the
// tmux_session_name column exact-byte; "" means no filter (the
// filter is correlation across live/ended rows, not uniqueness
// enforcement). Limit caps the result count; 0 means no limit.
//
// Settle is the reader's settle time and clock (b.3ie): with a settle time,
// each row's ReportedState is its settled state and State filters on it; the
// zero value is no settle time, and ListSpawns runs exactly the query it ran
// before b.3ie, State filtering on the raw state.
type ListFilters struct {
	State           []string
	Labels          map[string]string
	Parent          string
	Cwd             string
	TmuxSessionName string
	Limit           int
	Settle          SettleRead
}

// ListSpawns runs the filtered query and returns rows in unspecified
// order. SRD §4.2 explicitly accepts the linear scan + json_extract
// cost up to the low thousands of rows; no index is added for label
// filtering.
//
// SQL composition: each non-zero filter contributes one AND clause
// with positional `?` placeholders so the driver handles quoting. No
// caller string ever lands in the SQL text.
//
// With f.Settle settling (b.3ie), the reported state (SettleRead.stateExpr) is
// selected after spawnColumns into ReportedState, and the state filter is on
// that same expression, in the statement, ahead of LIMIT, so a limited list
// is the first rows whose reported state matches. With no settle time the
// state filter is the bare `state IN (...)`, and ReportedState is State.
// FamilyState is selected from its own history, before these filters and
// limits. It never waits for a state to settle.
func (s *Store) ListSpawns(f ListFilters) ([]Spawn, error) {
	return s.listSpawns(f, nil)
}

func (s *Store) listSpawns(f ListFilters, proofs []CursorMissingProof) ([]Spawn, error) {
	var where []string
	var args []any

	stateExpr, stateArgs := f.Settle.stateExpr()
	stateExpr, stateArgs, err := cursorMissingState(stateExpr, stateArgs, proofs)
	if err != nil {
		return nil, err
	}
	selectCols, columnArgs, err := cursorMissingColumns(proofs)
	if err != nil {
		return nil, err
	}
	args = append(args, columnArgs...)
	if f.Settle.on() {
		// The select list comes first in the statement, so its arguments do.
		selectCols += `, ` + stateExpr
		args = append(args, stateArgs...)
		familyExpr, familyArgs := f.Settle.familyExpr()
		selectCols += `, ` + familyExpr
		args = append(args, familyArgs...)
	}

	if len(f.State) > 0 {
		placeholders := make([]string, len(f.State))
		args = append(args, stateArgs...)
		for i, st := range f.State {
			placeholders[i] = "?"
			args = append(args, st)
		}
		where = append(where, stateExpr+" IN ("+strings.Join(placeholders, ",")+")")
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

	if f.TmuxSessionName != "" {
		where = append(where, "tmux_session_name = ?")
		args = append(args, f.TmuxSessionName)
	}

	q := `SELECT ` + selectCols + ` FROM spawns`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	if f.Limit > 0 {
		q += " LIMIT ?"
		args = append(args, f.Limit)
	}

	rows, err := s.db.QueryContext(s.readContext(), q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list spawns query: %w", err)
	}
	defer rows.Close()

	var out []Spawn
	for rows.Next() {
		sp, err := scanSpawnRow(rows, listSpawnsErrs, f.Settle.on())
		if err != nil {
			return nil, err
		}
		if err := validateCursorMissingRead(sp, proofs); err != nil {
			return nil, err
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
