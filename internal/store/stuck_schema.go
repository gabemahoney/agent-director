package store

import (
	"database/sql"
	"fmt"
	"strings"
)

// The permission epoch changes only on a true raw-state transition. Request
// ownership is independent of row_version: admitting another request or a held
// hook must not retire the other requests in the same interval.
var stuckColumns = []struct{ table, name, kind, def string }{
	{"spawns", "permission_epoch", "INTEGER", "0"},
	{"spawns", "stuck_version", "INTEGER", "0"},
	{"spawns", "stuck_observation", "TEXT", ""},
	{"spawns", "stuck_exit", "TEXT", ""},
	{"permission_requests", "owner_launch_token", "TEXT", ""},
	{"permission_requests", "owner_permission_epoch", "INTEGER", ""},
	{"permission_requests", "admitted_at", "INTEGER", ""},
	{"permission_requests", "fallback_at", "INTEGER", ""},
	{"permission_requests", "fallback_basis", "TEXT", ""},
	{"permission_requests", "superseded_at", "INTEGER", ""},
}

// migrateV13toV14 retains report-only history without inventing an earlier
// stuck episode or process-death observation. Recovery is by stopped-writer
// pre-upgrade backup, as for v13; no reverse migration is implemented.
func migrateV13toV14(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	found := make(map[string]bool)
	for _, table := range []string{"spawns", "permission_requests"} {
		rows, err := tx.Query("PRAGMA table_info(" + table + ")")
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
			for _, c := range stuckColumns {
				if c.table != table || !strings.EqualFold(name, c.name) {
					continue
				}
				wantNN := 0
				if c.def != "" {
					wantNN = 1
				}
				if name != c.name || kind != c.kind || pk != 0 || nn != wantNN || def.Valid != (c.def != "") || (def.Valid && def.String != c.def) {
					rows.Close()
					return fmt.Errorf("%w: store: v13→v14 incompatible %s.%s", ErrSchemaMismatch, table, c.name)
				}
				found[table+"."+name] = true
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
	}
	// Validate the existing partial shape before adding any missing column.
	for _, c := range stuckColumns {
		if !found[c.table+"."+c.name] {
			continue
		}
		predicate := "typeof(" + c.name + ") <> '" + strings.ToLower(c.kind) + "'"
		if c.kind == "INTEGER" {
			predicate += " OR " + c.name + " < 0"
		}
		if c.def == "" {
			predicate = c.name + " IS NOT NULL AND (" + predicate + ")"
		}
		var invalid bool
		if err := tx.QueryRow("SELECT EXISTS(SELECT 1 FROM " + c.table + " WHERE " + predicate + ")").Scan(&invalid); err != nil {
			return err
		}
		if invalid {
			return fmt.Errorf("%w: store: v13→v14 invalid %s.%s", ErrSchemaMismatch, c.table, c.name)
		}
	}
	for _, name := range []string{"stuck_observation", "stuck_exit"} {
		if !found["spawns."+name] {
			continue
		}
		rows, err := tx.Query("SELECT " + name + " FROM spawns WHERE " + name + " IS NOT NULL")
		if err != nil {
			return err
		}
		for rows.Next() {
			var raw string
			if err := rows.Scan(&raw); err != nil {
				rows.Close()
				return err
			}
			_, valid := decodeStuckExit(raw)
			if name == "stuck_observation" {
				_, valid = decodeStuckObservation(raw)
			}
			if !valid {
				rows.Close()
				return fmt.Errorf("%w: store: v13→v14 invalid %s", ErrSchemaMismatch, name)
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
	}
	if found["permission_requests.fallback_basis"] {
		var invalid bool
		if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM permission_requests WHERE fallback_basis IS NOT NULL AND fallback_basis NOT IN ('process','deadline','admission'))`).Scan(&invalid); err != nil {
			return err
		}
		if invalid {
			return fmt.Errorf("%w: store: v13→v14 invalid fallback basis", ErrSchemaMismatch)
		}
	}
	if found["permission_requests.fallback_at"] || found["permission_requests.fallback_basis"] {
		atExpr, basisExpr := "NULL", "NULL"
		if found["permission_requests.fallback_at"] {
			atExpr = "fallback_at"
		}
		if found["permission_requests.fallback_basis"] {
			basisExpr = "fallback_basis"
		}
		var invalid bool
		if err := tx.QueryRow("SELECT EXISTS(SELECT 1 FROM permission_requests WHERE (" + atExpr + " IS NULL) <> (" + basisExpr + " IS NULL))").Scan(&invalid); err != nil {
			return err
		}
		if invalid {
			return fmt.Errorf("%w: store: v13→v14 incomplete fallback clock", ErrSchemaMismatch)
		}
	}
	for _, c := range stuckColumns {
		if found[c.table+"."+c.name] {
			continue
		}
		ddl := "ALTER TABLE " + c.table + " ADD COLUMN " + c.name + " " + c.kind
		if c.def != "" {
			ddl += " NOT NULL DEFAULT " + c.def
		}
		if _, err := tx.Exec(ddl); err != nil {
			return err
		}
	}
	// Only requests that could belong to the currently continuous permission
	// interval acquire compatibility ownership. SQLite's historical created_at
	// has second precision: only a creation definitely at or after the interval
	// can establish ownership. Ambiguous same-second historical requests keep
	// NULL ownership. NULL ownership on an
	// explicitly retained partial column is conservative and stays untouched.
	if !found["permission_requests.owner_permission_epoch"] && !found["permission_requests.owner_launch_token"] {
		if _, err := tx.Exec(`UPDATE permission_requests SET
 owner_launch_token = (SELECT launch_token FROM spawns WHERE spawns.instance_id=permission_requests.instance_id),
 owner_permission_epoch = (SELECT permission_epoch FROM spawns WHERE spawns.instance_id=permission_requests.instance_id)
 WHERE EXISTS (SELECT 1 FROM spawns WHERE spawns.instance_id=permission_requests.instance_id
 AND state='check_permission' AND unixepoch(permission_requests.created_at) * 1000 >= state_changed_ms)`); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`PRAGMA user_version = 14`); err != nil {
		return err
	}
	return tx.Commit()
}
