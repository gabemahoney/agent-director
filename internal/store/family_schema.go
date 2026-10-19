package store

import (
	"database/sql"
	"fmt"
	"strings"
)

var familyColumns = []struct {
	name, kind, def string
}{
	{"family_state", "TEXT", "''"},
	{"family_settled_state", "TEXT", "''"},
	{"family_changed_ms", "INTEGER", "0"},
}

// migrateV11toV12 adds independent family history. Validate every existing
// column before mutation; one transaction contains DDL, initialization and
// the final version stamp. Compatible reentry preserves established history.
func migrateV11toV12(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	found, err := existingFamilyColumns(tx)
	if err != nil {
		return err
	}
	for _, col := range familyColumns {
		if !found[col.name] {
			if _, err := tx.Exec("ALTER TABLE spawns ADD COLUMN " + col.name + " " + col.kind + " NOT NULL DEFAULT " + col.def); err != nil {
				return err
			}
		}
	}
	var invalid bool
	if err := tx.QueryRow(`SELECT EXISTS (SELECT 1 FROM spawns
 WHERE family_state NOT IN ('', 'working', 'waiting')
 OR family_settled_state NOT IN ('', 'working', 'waiting')
 OR typeof(family_changed_ms) <> 'integer')`).Scan(&invalid); err != nil {
		return err
	}
	if invalid {
		return fmt.Errorf("%w: store: v11→v12 invalid family history", ErrSchemaMismatch)
	}
	if _, err := tx.Exec(familyAggregateSQL + `UPDATE spawns SET
 family_state = aggregate.value,
 family_settled_state = aggregate.value,
 family_changed_ms = 0 FROM aggregate
 WHERE spawns.instance_id = aggregate.root AND family_state = ''`); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE spawns SET family_settled_state = family_state WHERE family_settled_state = ''`); err != nil {
		return err
	}
	if _, err := tx.Exec(`PRAGMA user_version = 12`); err != nil {
		return err
	}
	return tx.Commit()
}

// existingFamilyColumns validates compatible reentry before any DDL runs.
func existingFamilyColumns(tx *sql.Tx) (map[string]bool, error) {
	found := make(map[string]bool)
	rows, err := tx.Query(`PRAGMA table_info(spawns)`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var cid, notnull, pk int
		var name, kind string
		var def sql.NullString
		if err := rows.Scan(&cid, &name, &kind, &notnull, &def, &pk); err != nil {
			rows.Close()
			return nil, err
		}
		for _, col := range familyColumns {
			if !strings.EqualFold(name, col.name) {
				continue
			}
			if name != col.name || kind != col.kind || notnull != 1 || pk != 0 || !def.Valid || def.String != col.def {
				rows.Close()
				return nil, fmt.Errorf("%w: store: v11→v12 incompatible %s column", ErrSchemaMismatch, col.name)
			}
			found[name] = true
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	return found, nil
}
