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
// opening the client or store under test.
func SeedStoreID(dbPath, id string) error {
	if !wellFormedStoreID(id) {
		return fmt.Errorf("SeedStoreID: id %q is not 16 lowercase hexadecimal characters", id)
	}
	raw, err := openRawStore(dbPath)
	if err != nil {
		return fmt.Errorf("SeedStoreID: %w", err)
	}
	defer raw.Close() //nolint:errcheck

	res, err := raw.Exec(`UPDATE store_meta SET value = ? WHERE key = 'store_id'`, id)
	if err != nil {
		if missing, perr := storeMetaMissing(raw); perr == nil && missing {
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
