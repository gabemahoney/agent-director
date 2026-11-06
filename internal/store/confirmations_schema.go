package store

import (
	"database/sql"
	"fmt"
	"strings"
)

// v19EvidenceColumns are the main-field evidence columns (confirmations.go):
// nullable TEXT without defaults. Migration backfills nothing: no stored
// timestamp establishes the exact value and owner a record names, so every
// existing row starts with no evidence (unknown) until an owned event,
// command or Active Check establishes a value. A present value must be a
// JSON object; anything else refuses the step rather than being accepted as
// a record a read would then ignore.
var v19EvidenceColumns = []string{"state_evidence", "settled_evidence", "process_evidence"}

// migrateV18toV19 follows the settled v18 Cursor obligation step without
// modifying its tables. It validates every existing evidence column and
// value before any DDL, so a compatible partial reentry keeps its facts, an
// incompatible column shape or value refuses with the previous schema and
// data intact, and the additions commit with the stamp. Recovery to an older
// binary requires the stopped-writer pre-upgrade backup, as for v16 to v18;
// rows written after the upgrade lose their evidence with it.
func migrateV18toV19(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	found := make(map[string]bool)
	rows, err := tx.Query(`PRAGMA table_info(spawns)`)
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
		for _, c := range v19EvidenceColumns {
			if !strings.EqualFold(name, c) {
				continue
			}
			if name != c || kind != "TEXT" || nn != 0 || pk != 0 || def.Valid {
				rows.Close()
				return fmt.Errorf("%w: store: v18→v19 incompatible spawns.%s", ErrSchemaMismatch, c)
			}
			found[c] = true
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, c := range v19EvidenceColumns {
		if !found[c] {
			continue
		}
		var invalid bool
		// Names are the fixed schema specification above. CASE keeps json_type
		// from ever reading malformed text.
		if err := tx.QueryRow(`SELECT EXISTS (SELECT 1 FROM spawns WHERE ` + c + ` IS NOT NULL AND CASE WHEN typeof(` + c + `) <> 'text' THEN 1
			WHEN NOT json_valid(` + c + `) THEN 1 WHEN json_type(` + c + `) <> 'object' THEN 1 ELSE 0 END)`).Scan(&invalid); err != nil {
			return err
		}
		if invalid {
			return fmt.Errorf("%w: store: v18→v19 invalid spawns.%s", ErrSchemaMismatch, c)
		}
	}
	for _, c := range v19EvidenceColumns {
		if !found[c] {
			if _, err := tx.Exec(`ALTER TABLE spawns ADD COLUMN ` + c + ` TEXT`); err != nil {
				return err
			}
		}
	}
	if _, err := tx.Exec(`PRAGMA user_version = 19`); err != nil {
		return err
	}
	return commitMigrationStep(tx)
}

// v20DisplayEvidenceColumn is the displayed_state evidence column
// (confirmations.go): nullable TEXT without a default, like the v19 columns.
// Migration backfills nothing: no stored display, since or pane baseline
// records the exact value, owner and attempt a record names, so every
// existing row reads its displayed_state unknown until a pane sample
// establishes it.
const v20DisplayEvidenceColumn = "display_evidence"

// migrateV19toV20 follows the v19 main-field evidence step without modifying
// its columns, with the same validate-first rule: an already-present column
// of the exact shape whose values are all JSON objects is kept with its
// values, any other shape or value refuses with the previous schema and data
// intact, and the addition commits with the stamp. Recovery to an older
// binary requires the stopped-writer pre-upgrade backup; display evidence
// written after the upgrade is lost with it.
func migrateV19toV20(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	found := false
	rows, err := tx.Query(`PRAGMA table_info(spawns)`)
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
		if !strings.EqualFold(name, v20DisplayEvidenceColumn) {
			continue
		}
		if name != v20DisplayEvidenceColumn || kind != "TEXT" || nn != 0 || pk != 0 || def.Valid {
			rows.Close()
			return fmt.Errorf("%w: store: v19→v20 incompatible spawns.%s", ErrSchemaMismatch, v20DisplayEvidenceColumn)
		}
		found = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if found {
		var invalid bool
		// CASE keeps json_type from ever reading malformed text.
		if err := tx.QueryRow(`SELECT EXISTS (SELECT 1 FROM spawns WHERE display_evidence IS NOT NULL AND CASE WHEN typeof(display_evidence) <> 'text' THEN 1
			WHEN NOT json_valid(display_evidence) THEN 1 WHEN json_type(display_evidence) <> 'object' THEN 1 ELSE 0 END)`).Scan(&invalid); err != nil {
			return err
		}
		if invalid {
			return fmt.Errorf("%w: store: v19→v20 invalid spawns.%s", ErrSchemaMismatch, v20DisplayEvidenceColumn)
		}
	} else if _, err := tx.Exec(`ALTER TABLE spawns ADD COLUMN ` + v20DisplayEvidenceColumn + ` TEXT`); err != nil {
		return err
	}
	if _, err := tx.Exec(`PRAGMA user_version = 20`); err != nil {
		return err
	}
	return commitMigrationStep(tx)
}
