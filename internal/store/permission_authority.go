package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// Verdict provenance is independent of the public reason (an explicit allow
// initially has no reason). NULL is historical/unknown and grants no response
// authority.
// Observation includes outside-answer claims and inferred tool completion.
const (
	DecisionOriginCaller      = "caller"
	DecisionOriginTimeout     = "timeout"
	DecisionOriginPane        = "pane"
	DecisionOriginObservation = "observation"
)

// Response claims are irrevocable. A confirmed claim admits one enqueue; the
// Cursor writer consumes it before I/O. Neither stage proves transport or
// execution, and an uncertain commit must never be recovered by replay.
const (
	ResponseRelayClaimed  = "relay_claimed"
	ResponseNativeClaimed = "native_claimed"
	ResponseRelayIssued   = "relay_issued"
	ResponseNativeIssued  = "native_issued"
)

// permissionOwnerSQL matches the immutable admission to the current owner.
// The existing launch/epoch columns alone cannot distinguish a conversation
// or connection replaced within a launch. Historical unknown owners fail
// closed. The transaction's hook/independent Cursor evidence remains required.
// New verdicts and claims require response_state IS NULL, so they must still
// match the admission epoch. A committed Cursor claim survives a raw-state
// epoch change caused by another request's proof: acknowledgement/native claim
// already released its row hold, but its own response is still pending. Only
// that unconsumed claim may cross epochs, under the same life/session/connection
// and the caller's exact claim, closure, proof, generation and deadline checks.
// This fragment uses the table's full name, including in UPDATE statements.
const permissionOwnerSQL = `EXISTS (SELECT 1 FROM spawns AS owner
 WHERE owner.instance_id = permission_requests.instance_id
 AND owner.state NOT IN ('pending', 'ended', 'missing')
 AND owner.launch_token IS permission_requests.owner_launch_token
 AND (owner.permission_epoch = permission_requests.owner_permission_epoch
      OR (owner.agent = 'cursor' AND permission_requests.response_state IN ('relay_claimed', 'native_claimed')))
 AND owner.life_number = permission_requests.owner_life_number
 AND owner.session_id IS permission_requests.owner_session_id
 AND (owner.agent IS NOT 'cursor' OR EXISTS (SELECT 1 FROM cursor_runtimes AS runtime
     WHERE runtime.bound_instance_id = owner.instance_id
     AND runtime.bound_connection_id = permission_requests.owner_cursor_connection
     AND runtime.bound_session_id = owner.session_id AND runtime.bound_terminated = 0)))`

// Response eligibility is deliberately narrower than the report-only open
// predicate: proof, closure, old ownership and a prior claim all exclude a new
// response. Observation and a non-null decision never supply authority alone.
const permissionResponseEligibleSQL = `closed_at IS NULL AND proven_gone_at IS NULL AND ` + permissionOwnerSQL

const relayVerdictSQL = `decision IN ('allow', 'deny') AND decision_origin IN ('caller', 'timeout')`

// Verify the actual response transaction's lease. A claim made with weaker
// durability must never authorize a response, even if a prior lease was FULL.
func permissionResponseDurability(ctx context.Context, conn *sql.Conn) error {
	var mode string
	var synchronous int
	if err := conn.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&mode); err != nil {
		return err
	}
	if err := conn.QueryRowContext(ctx, `PRAGMA synchronous`).Scan(&synchronous); err != nil {
		return err
	}
	if mode != "wal" || synchronous != 2 {
		return fmt.Errorf("store: permission response requires WAL synchronous FULL")
	}
	return ctx.Err()
}

// v17 columns are nullable without defaults: migration must not infer an
// owner, origin or emission claim from historical decisions or pane records.
var v17PermissionColumns = []struct{ name, kind, invalid string }{
	{"decision_origin", "TEXT", `typeof(decision_origin) <> 'text' OR decision_origin NOT IN ('caller','timeout','pane','observation')`},
	{"response_state", "TEXT", `typeof(response_state) <> 'text' OR response_state NOT IN ('relay_claimed','native_claimed','relay_issued','native_issued')`},
	{"owner_life_number", "INTEGER", `typeof(owner_life_number) <> 'integer' OR owner_life_number < 0`},
	{"owner_session_id", "TEXT", `typeof(owner_session_id) <> 'text'`},
	{"owner_cursor_connection", "TEXT", `typeof(owner_cursor_connection) <> 'text' OR length(owner_cursor_connection) = 0`},
}

// migrateV16toV17 follows the settled input cutover, without modifying or
// reimporting its tables. Validate every existing column/value before any DDL;
// compatible reentry preserves facts, and the stamp commits with the additions.
// Recovery requires the stopped-writer pre-upgrade backup, as for v16.
func migrateV16toV17(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	found := make(map[string]bool)
	rows, err := tx.Query(`PRAGMA table_info(permission_requests)`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var cid, nn, pk int
		var name, kind string
		var def sql.NullString
		if err := rows.Scan(&cid, &name, &kind, &nn, &def, &pk); err != nil {
			rows.Close()
			return err
		}
		for _, c := range v17PermissionColumns {
			if !strings.EqualFold(name, c.name) {
				continue
			}
			if name != c.name || kind != c.kind || nn != 0 || pk != 0 || def.Valid {
				rows.Close()
				return fmt.Errorf("%w: store: v16→v17 incompatible permission_requests.%s", ErrSchemaMismatch, c.name)
			}
			found[c.name] = true
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, c := range v17PermissionColumns {
		if !found[c.name] {
			continue
		}
		var invalid bool
		// Names and predicates are the fixed schema specification above.
		if err := tx.QueryRow(`SELECT EXISTS (SELECT 1 FROM permission_requests WHERE ` + c.name + ` IS NOT NULL AND (` + c.invalid + `))`).Scan(&invalid); err != nil {
			return err
		}
		if invalid {
			return fmt.Errorf("%w: store: v16→v17 invalid permission_requests.%s", ErrSchemaMismatch, c.name)
		}
	}
	for _, c := range v17PermissionColumns {
		if !found[c.name] {
			if _, err := tx.Exec(`ALTER TABLE permission_requests ADD COLUMN ` + c.name + ` ` + c.kind); err != nil {
				return err
			}
		}
	}
	if _, err := tx.Exec(`PRAGMA user_version = 17`); err != nil {
		return err
	}
	return tx.Commit()
}
