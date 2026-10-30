package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

const cursorInputsDDL = `CREATE TABLE IF NOT EXISTS cursor_inputs (
 store_id TEXT NOT NULL,
 connection_id TEXT NOT NULL,
 instance_id TEXT NOT NULL REFERENCES spawns(instance_id) ON DELETE CASCADE,
 submission_id TEXT NOT NULL,
 record TEXT NOT NULL,
 PRIMARY KEY(store_id,connection_id,submission_id)
);`
const cursorProofsDDL = `CREATE TABLE IF NOT EXISTS cursor_proofs (
 store_id TEXT NOT NULL,
 connection_id TEXT NOT NULL,
 instance_id TEXT NOT NULL REFERENCES spawns(instance_id) ON DELETE CASCADE,
 proof_id TEXT NOT NULL,
 record TEXT NOT NULL,
 raw BLOB NOT NULL,
 PRIMARY KEY(store_id,connection_id,proof_id)
);`
const cursorObligationsDDL = cursorInputsDDL + cursorProofsDDL

// The new hop follows the settled v17 permission provenance step. No backfill
// from transcripts or requests: neither history nor an unknown claim is work.
// Recovery is stopped-writer backup/restore only; dropping these records would
// discard uncertain input/proof evidence needed before an older runtime starts.
func migrateV17toV18(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var storeID string
	if err = tx.QueryRow(`SELECT value FROM store_meta WHERE key='store_id'`).Scan(&storeID); err != nil {
		return err
	}
	if !validStoreID(storeID) {
		return fmt.Errorf("%w: invalid Cursor obligation store", ErrSchemaMismatch)
	}
	for _, table := range []struct {
		name, id, ddl string
		proof         bool
	}{
		{"cursor_inputs", "submission_id", cursorInputsDDL, false},
		{"cursor_proofs", "proof_id", cursorProofsDDL, true},
	} {
		var name, kind, ddl string
		err = tx.QueryRow(`SELECT name,type,sql FROM sqlite_master WHERE lower(name)=?`, table.name).Scan(&name, &kind, &ddl)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		// Comparing the normalized declared SQL also checks constraints rather
		// than accepting matching columns with a different uniqueness contract.
		normalize := func(s string) string {
			return strings.Join(strings.Fields(strings.ReplaceAll(strings.TrimSuffix(strings.TrimSpace(s), ";"), "IF NOT EXISTS ", "")), " ")
		}
		if name != table.name || kind != "table" || normalize(ddl) != normalize(table.ddl) {
			return fmt.Errorf("%w: v17→v18 incompatible %s", ErrSchemaMismatch, table.name)
		}
		var invalid bool
		predicate := `typeof(store_id)<>'text' OR typeof(connection_id)<>'text' OR typeof(instance_id)<>'text' OR typeof(` + table.id + `)<>'text' OR typeof(record)<>'text'`
		if table.proof {
			predicate += ` OR typeof(raw)<>'blob'`
		}
		if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM ` + table.name + ` WHERE ` + predicate + `)`).Scan(&invalid); err != nil {
			return err
		}
		if invalid {
			return fmt.Errorf("%w: v17→v18 invalid %s types", ErrSchemaMismatch, table.name)
		}
		query := `SELECT store_id,connection_id,instance_id,` + table.id + `,record`
		if table.proof {
			query += `,raw`
		}
		rows, err := tx.Query(query + ` FROM ` + table.name)
		if err != nil {
			return err
		}
		for rows.Next() {
			var owner, connection, instance, id, raw string
			var evidence []byte
			args := []any{&owner, &connection, &instance, &id, &raw}
			if table.proof {
				args = append(args, &evidence)
			}
			if err = rows.Scan(args...); err == nil {
				if table.proof {
					var p CursorProof
					err = decodeCursorRecord([]byte(raw), &p)
					p.Raw = evidence
					if err == nil {
						err = p.validate()
					}
					if err == nil && (p.Owner.Runtime.InstanceID != instance || p.Owner.StoreID != owner || p.Owner.Runtime.Connection != connection || p.ID != id) {
						err = fmt.Errorf("proof identity mismatch")
					}
				} else {
					var v CursorInput
					err = decodeCursorRecord([]byte(raw), &v)
					if err == nil {
						err = v.validate()
					}
					if err == nil && (v.Owner.Runtime.InstanceID != instance || v.Owner.StoreID != owner || v.Owner.Runtime.Connection != connection || v.SubmissionID != id) {
						err = fmt.Errorf("input identity mismatch")
					}
				}
			}
			if err != nil || owner != storeID {
				rows.Close()
				return fmt.Errorf("%w: v17→v18 invalid %s evidence", ErrSchemaMismatch, table.name)
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
	}
	if _, err = tx.Exec(cursorObligationsDDL); err != nil {
		return err
	}
	if _, err = tx.Exec(`PRAGMA user_version = 18`); err != nil {
		return err
	}
	return tx.Commit()
}
