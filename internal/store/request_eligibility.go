package store

import (
	"database/sql"
	"fmt"
	"strings"
)

// migrateV10toV11 separates main-turn proof eligibility from child identity.
// Only a newly added column is backfilled: compatible reentry keeps every
// valid explicit value, including conservative false on a legacy main request.
// Eligible known-child records contradict the proof contract and are refused.
func migrateV10toV11(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.Query(`PRAGMA table_info(permission_requests)`)
	if err != nil {
		return err
	}
	found := false
	for rows.Next() {
		var cid, notnull, pk int
		var name, kind string
		var def sql.NullString
		if err = rows.Scan(&cid, &name, &kind, &notnull, &def, &pk); err != nil {
			rows.Close()
			return err
		}
		if !strings.EqualFold(name, "main_turn_end_eligible") {
			continue
		}
		if name != "main_turn_end_eligible" || kind != "INTEGER" || notnull != 1 || pk != 0 || !def.Valid || def.String != "0" {
			rows.Close()
			return fmt.Errorf("%w: store: v10→v11 incompatible main-turn eligibility column", ErrSchemaMismatch)
		}
		found = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if !found {
		if _, err = tx.Exec(`ALTER TABLE permission_requests ADD COLUMN main_turn_end_eligible INTEGER NOT NULL DEFAULT 0`); err != nil {
			return err
		}
		// Historical non-Cursor main/child semantics, including NULL stored
		// agents, remain unchanged. Cursor NULL child identity supplies no
		// independently proven main origin; missing owning spawns stay false.
		if _, err = tx.Exec(`UPDATE permission_requests SET main_turn_end_eligible = 1
		 WHERE agent_id IS NULL AND EXISTS (SELECT 1 FROM spawns
		 WHERE spawns.instance_id = permission_requests.instance_id AND agent IS NOT 'cursor')`); err != nil {
			return err
		}
	}
	var invalid bool
	if err = tx.QueryRow(`SELECT EXISTS (SELECT 1 FROM permission_requests
	 WHERE typeof(main_turn_end_eligible) <> 'integer' OR main_turn_end_eligible NOT IN (0, 1)
	 OR (main_turn_end_eligible = 1 AND agent_id IS NOT NULL))`).Scan(&invalid); err != nil {
		return err
	}
	if invalid {
		return fmt.Errorf("%w: store: v10→v11 invalid main-turn eligibility value", ErrSchemaMismatch)
	}
	if _, err = tx.Exec(`PRAGMA user_version = 11`); err != nil {
		return err
	}
	return tx.Commit()
}
