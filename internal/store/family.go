package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// familyAggregateSQL carries working membership up stored parent links,
// including terminal intermediates. Each working ancestor is visited once;
// enumerating every ancestor/descendant pair would add quadratic work to
// bounded lifecycle transactions.
var familyAggregateSQL = familyAggregateForSQL(familyMemberSQL)

// familyAggregateForSQL also serves the migration before display columns exist.
// member is a source-owned SQL expression, never caller input.
func familyAggregateForSQL(member string) string {
	return `WITH RECURSIVE working(id) AS (
 SELECT instance_id FROM spawns WHERE ` + member + `
 UNION
 SELECT child.parent_id FROM working
 JOIN spawns AS child ON child.instance_id = working.id
 WHERE child.parent_id IS NOT NULL
), aggregate AS MATERIALIZED (
 SELECT instance_id AS root,
 CASE WHEN instance_id IN (SELECT id FROM working) THEN 'working' ELSE 'waiting' END AS value
 FROM spawns
) `
}

// A retained Displayed State keeps a waiting member working until its exit
// commits. The shared decoder excludes malformed and obsolete observations
// and observations on terminal rows, exactly as CommittedDisplay does.
const familyMemberSQL = `state NOT IN ('ended', 'missing', 'waiting') OR ` + committedDisplayStateSQL + ` IS NOT NULL`

// refreshFamilies runs under the lifecycle writer's lock, before commit. The
// previous aggregate, rather than any member's displayed state, supplies the
// settle base. Unchanged aggregates keep their original clock and history.
// Bounded writers call it only after a contribution or ancestry mutation;
// permission delivery and authority-only transactions skip family work.
// Join the materialized aggregate once: correlated SET subqueries otherwise
// scan it again for each changed row in the supported SQLite version.
func (s *Store) refreshFamilies(q querier) error {
	_, err := q.Exec(familyAggregateSQL+`UPDATE spawns SET
 family_settled_state = CASE
   WHEN family_state = '' THEN aggregate.value
   WHEN ? = 0 OR `+nowMsSQL+` - family_changed_ms >= ? THEN family_state
   ELSE COALESCE(NULLIF(family_settled_state, ''), family_state) END,
 family_changed_ms = CASE WHEN family_state = '' THEN 0 ELSE `+nowMsSQL+` END,
 family_state = aggregate.value
 FROM aggregate
 WHERE spawns.instance_id = aggregate.root AND family_state IS NOT aggregate.value`, s.settleMs, s.settleMs)
	if err != nil {
		return fmt.Errorf("store: refresh families: %w", err)
	}
	return nil
}

// refreshFamiliesAfterStateChange skips aggregate work when a state-only write
// preserves the member's contribution. The caller reads prior under the same
// write lock and supplies any committed display exits drained by this write;
// those can remove a waiting member's retained working contribution. A hook
// that retains a display needs no extra refresh when both raw states agree
// on membership. Parent links and all other members remain unchanged.
func (s *Store) refreshFamiliesAfterStateChange(q querier, prior, next string, exits ...displayTransition) error {
	contributes := func(state string) bool {
		return state != StateWaiting && state != StateEnded && state != StateMissing
	}
	if (contributes(prior) || len(exits) > 0) == contributes(next) {
		return nil
	}
	return s.refreshFamilies(q)
}

// errFamilyWriteNotApplied rolls back a guarded write that matched no row,
// preserving family histories when the lifecycle write did not apply.
var errFamilyWriteNotApplied = errors.New("store: family lifecycle write not applied")

// execFamily keeps an individual lifecycle statement and all affected family
// histories atomic. Preserve its result, including RowsAffected, for guards.
// instanceID names the one row query writes.
func (s *Store) execFamily(instanceID, query string, args ...any) (sql.Result, error) {
	var result sql.Result
	err := s.inImmediateTx(instanceID, func(ctx context.Context, conn *sql.Conn) error {
		var err error
		result, err = conn.ExecContext(ctx, query, args...)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err == nil && n == 0 {
			return errFamilyWriteNotApplied
		}
		return err
	})
	if errors.Is(err, errFamilyWriteNotApplied) {
		err = nil
	}
	return result, err
}

func (r SettleRead) familyExpr() (string, []any) {
	if !r.on() {
		return "family_state", nil
	}
	return "CASE WHEN ? - family_changed_ms >= ? THEN family_state ELSE COALESCE(NULLIF(family_settled_state, ''), family_state) END",
		[]any{r.NowMs, r.SettleMs}
}
