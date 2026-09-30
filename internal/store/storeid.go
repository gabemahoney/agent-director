package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
)

// storeIDLen is the length of a well-formed store id: 64 random bits written
// as lowercase hexadecimal (SR-5.1; WD 2026-09-29 STORE).
const storeIDLen = 16

// insertStoreIDOnceSQL inserts store_meta's store_id row only when no
// store_id row exists, so it never replaces an existing value (SR-5.4). It is
// the only statement in the package that writes store_meta; createSchema and
// migrateV4toV5 run it inside their own transactions.
const insertStoreIDOnceSQL = `INSERT INTO store_meta(key, value)
SELECT 'store_id', ?
 WHERE NOT EXISTS (SELECT 1 FROM store_meta WHERE key = 'store_id')`

// newStoreID returns a fresh store id: 8 bytes from crypto/rand, hex-encoded
// lowercase (16 characters). A read failure is returned; there is no fallback
// to a weaker source.
func newStoreID() (string, error) {
	var b [storeIDLen / 2]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// insertStoreIDOnce generates a store id and inserts it on tx when store_meta
// holds no store_id row yet (SR-5.4). An existing value is kept. The caller
// owns tx and rolls it back on error; the error text never contains an id.
func insertStoreIDOnce(tx *sql.Tx) error {
	id, err := newStoreID()
	if err != nil {
		return fmt.Errorf("generate store id: %w", err)
	}
	if _, err := tx.Exec(insertStoreIDOnceSQL, id); err != nil {
		return fmt.Errorf("insert store id: %w", err)
	}
	return nil
}

// validStoreID reports whether v is exactly 16 lowercase hexadecimal
// characters.
func validStoreID(v string) bool {
	if len(v) != storeIDLen {
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

// readStoreID reads store_meta's store_id once, when the store opens
// (SR-5.1). It only reads. A missing table, a missing row, or a value that is
// not exactly 16 lowercase hex characters (including a non-TEXT value) is an
// error wrapping ErrSchemaMismatch. Treating a malformed value like a missing
// row keeps a made-up id out of labels (build-lead decision on WD 2026-09-29
// STORE). No error message ever contains the stored value (SR-15).
func readStoreID(db *sql.DB) (string, error) {
	var raw any
	err := db.QueryRow(`SELECT value FROM store_meta WHERE key = 'store_id'`).Scan(&raw)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return "", fmt.Errorf("%w: store has no valid store id (store_meta has no store_id row)",
			ErrSchemaMismatch)
	case err != nil:
		// Tell a store without the table (a schema error) from a read
		// failure (busy, I/O), which keeps its own error.
		var name string
		probeErr := db.QueryRow(
			`SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'store_meta'`,
		).Scan(&name)
		if errors.Is(probeErr, sql.ErrNoRows) {
			return "", fmt.Errorf("%w: store has no valid store id (no store_meta table)",
				ErrSchemaMismatch)
		}
		return "", fmt.Errorf("store: read store id: %w", err)
	}
	if v, ok := raw.(string); ok && validStoreID(v) {
		return v, nil
	}
	return "", fmt.Errorf("%w: store has no valid store id (store_meta's store_id is not 16 lowercase hexadecimal characters)",
		ErrSchemaMismatch)
}

// StoreID returns store_meta's store_id (SR-5.1; WD 2026-09-29 STORE), read
// once when the store opens: 16 lowercase hexadecimal characters, created once
// by a new store or by the v4→v5 migration and never changed by any verb. A
// store without a valid id fails the open with an error wrapping
// ErrSchemaMismatch, so a *Store always holds a well-formed id.
func (s *Store) StoreID() string {
	return s.storeID
}
