package apitest

import (
	"database/sql"
	"errors"
	"fmt"
)

// ErrNoStoreID is returned (wrapped) by ReadStoreID and SeedStoreID when the
// store file has no store_meta table or no store_id row in it.
var ErrNoStoreID = errors.New("apitest: store has no store_id")

// storeIDFallback is OtherStoreID's result for an id that is not well-formed.
const storeIDFallback = "0000000000000000"

// ReadStoreID returns store_meta's store_id value from the store file at
// dbPath, read raw through a direct connection (SR-5.1, SR-20.3; WD
// 2026-09-29 STORE). It never creates a store file and does not check the
// value's form, so it also reads a hand-edited value. A missing store_meta
// table or store_id row returns an error wrapping ErrNoStoreID.
//
// Use it to assert against the file itself; a test that holds a *store.Store
// uses its StoreID(), which is the value read when that store opened.
func ReadStoreID(dbPath string) (string, error) {
	raw, err := openRawStore(dbPath)
	if err != nil {
		return "", fmt.Errorf("ReadStoreID: %w", err)
	}
	defer raw.Close() //nolint:errcheck

	var v sql.NullString
	err = raw.QueryRow(`SELECT value FROM store_meta WHERE key = 'store_id'`).Scan(&v)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return "", fmt.Errorf("ReadStoreID: %w: no store_id row", ErrNoStoreID)
	case err != nil:
		if missing, perr := storeMetaMissing(raw); perr == nil && missing {
			return "", fmt.Errorf("ReadStoreID: %w: no store_meta table", ErrNoStoreID)
		}
		return "", fmt.Errorf("ReadStoreID: %w", err)
	}
	return v.String, nil
}

// SeedStoreID overwrites the store_id of the store file at dbPath with id, so
// a test can build this store's labels deterministically (SR-5.1, SR-20.3;
// WD 2026-09-29 STORE). id must be well-formed, 16 lowercase hexadecimal
// characters; anything else is an error and nothing is written. The store
// must already hold a store_id row (any store the store package created
// does); otherwise the error wraps ErrNoStoreID.
//
// Test-only. No production path ever changes a store's id (SR-5.1). A store
// opened before the call keeps the id it read when it opened, so seed before
// opening the client or store under test. A fresh SQLite input-authority marker
// changes atomically with the id; any existing native records refuse reseeding.
// So does a schema-v21 busy-send core identity (busy-send-core-v1:<id>), which
// every open checks against the id; any row of a table with a store_id column
// (the core's records name the store) refuses reseeding.
func SeedStoreID(dbPath, id string) error {
	if !wellFormedStoreID(id) {
		return fmt.Errorf("SeedStoreID: id %q is not 16 lowercase hexadecimal characters", id)
	}
	raw, err := openRawStore(dbPath)
	if err != nil {
		return fmt.Errorf("SeedStoreID: %w", err)
	}
	defer raw.Close() //nolint:errcheck

	tx, err := raw.Begin()
	if err != nil {
		return fmt.Errorf("SeedStoreID: begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck
	var oldID, authority string
	err = tx.QueryRow(`SELECT value FROM store_meta WHERE key = 'store_id'`).Scan(&oldID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return fmt.Errorf("SeedStoreID: %w: no store_id row", ErrNoStoreID)
	case err != nil:
		var name string
		if perr := tx.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'store_meta'`).Scan(&name); errors.Is(perr, sql.ErrNoRows) {
			return fmt.Errorf("SeedStoreID: %w: no store_meta table", ErrNoStoreID)
		}
		return fmt.Errorf("SeedStoreID: read store_id: %w", err)
	}
	err = tx.QueryRow(`SELECT value FROM store_meta WHERE key = 'native_input_authority'`).Scan(&authority)
	switch {
	case errors.Is(err, sql.ErrNoRows): // historical schemas have no input authority
	case err != nil:
		return fmt.Errorf("SeedStoreID: read input authority: %w", err)
	default:
		if authority != "sqlite-v1:"+oldID {
			return fmt.Errorf("SeedStoreID: input authority does not match the original store")
		}
		var records int
		if err := tx.QueryRow(`SELECT (SELECT count(*) FROM input_launches) +
			(SELECT count(*) FROM input_attempts) + (SELECT count(*) FROM input_imports)`).Scan(&records); err != nil {
			return fmt.Errorf("SeedStoreID: inspect native records: %w", err)
		}
		if records != 0 {
			return fmt.Errorf("SeedStoreID: cannot change the identity of native records")
		}
		if _, err := tx.Exec(`UPDATE store_meta SET value = ? WHERE key = 'native_input_authority'`, "sqlite-v1:"+id); err != nil {
			return fmt.Errorf("SeedStoreID: update input authority: %w", err)
		}
	}
	if err := reseedBusySendCore(tx, oldID, id); err != nil {
		return err
	}
	res, err := tx.Exec(`UPDATE store_meta SET value = ? WHERE key = 'store_id'`, id)
	if err != nil {
		var name string
		if perr := tx.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'store_meta'`).Scan(&name); errors.Is(perr, sql.ErrNoRows) {
			return fmt.Errorf("SeedStoreID: %w: no store_meta table", ErrNoStoreID)
		}
		return fmt.Errorf("SeedStoreID: update: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("SeedStoreID: rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("SeedStoreID: %w: no store_id row", ErrNoStoreID)
	}
	return tx.Commit()
}

// busySendCoreIdentity is the schema-v21 store_meta busy_send_core value of
// the store whose id is storeID.
func busySendCoreIdentity(storeID string) string { return "busy-send-core-v1:" + storeID }

// reseedBusySendCore moves the busy-send core identity from oldID to id on tx,
// SeedStoreID's transaction. A store from before schema v21 has none. An
// identity of another store, or any row naming the store (a row of a table
// with a store_id column), refuses the reseed with nothing written.
func reseedBusySendCore(tx *sql.Tx, oldID, id string) error {
	var core string
	err := tx.QueryRow(`SELECT value FROM store_meta WHERE key = 'busy_send_core'`).Scan(&core)
	switch {
	case errors.Is(err, sql.ErrNoRows): // historical schemas have no busy-send core
		return nil
	case err != nil:
		return fmt.Errorf("SeedStoreID: read busy-send core identity: %w", err)
	case core != busySendCoreIdentity(oldID):
		return fmt.Errorf("SeedStoreID: busy-send core identity does not match the original store")
	}
	rows, err := tx.Query(`SELECT m.name FROM sqlite_master m, pragma_table_info(m.name) p
	                        WHERE m.type = 'table' AND p.name = 'store_id'`)
	if err != nil {
		return fmt.Errorf("SeedStoreID: list tables naming the store: %w", err)
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close() //nolint:errcheck
			return fmt.Errorf("SeedStoreID: list tables naming the store: %w", err)
		}
		tables = append(tables, name)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return fmt.Errorf("SeedStoreID: list tables naming the store: %w", err)
	}
	for _, table := range tables {
		var named bool
		if err := tx.QueryRow(`SELECT EXISTS (SELECT 1 FROM "` + table + `")`).Scan(&named); err != nil {
			return fmt.Errorf("SeedStoreID: inspect %s: %w", table, err)
		}
		if named {
			return fmt.Errorf("SeedStoreID: cannot change the identity of %s records", table)
		}
	}
	if _, err := tx.Exec(`UPDATE store_meta SET value = ? WHERE key = 'busy_send_core'`, busySendCoreIdentity(id)); err != nil {
		return fmt.Errorf("SeedStoreID: update busy-send core identity: %w", err)
	}
	return nil
}

// OtherStoreID returns a well-formed store id (16 lowercase hexadecimal
// characters) that is guaranteed to differ from id, for building another
// agent-director store's labels (SR-5.1, SR-20.3; WD 2026-09-29 STORE). It is
// deterministic: for a well-formed id it changes only the last hex digit, to
// the next one (0→1, …, 9→a, …, f→0). For an id that is not well-formed it
// returns "0000000000000000", which cannot equal it.
func OtherStoreID(id string) string {
	if !wellFormedStoreID(id) {
		return storeIDFallback
	}
	const digits = "0123456789abcdef"
	last := id[len(id)-1]
	var next byte
	for i := 0; i < len(digits); i++ {
		if digits[i] == last {
			next = digits[(i+1)%len(digits)]
			break
		}
	}
	return id[:len(id)-1] + string(next)
}

// wellFormedStoreID reports whether v is exactly 16 lowercase hexadecimal
// characters, the store id form of SR-5.1.
func wellFormedStoreID(v string) bool {
	if len(v) != 16 {
		return false
	}
	for i := 0; i < len(v); i++ {
		c := v[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// storeMetaMissing reports whether the store has no store_meta table.
func storeMetaMissing(raw *sql.DB) (bool, error) {
	var name string
	err := raw.QueryRow(
		`SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'store_meta'`,
	).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	return false, err
}
