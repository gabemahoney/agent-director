package apitest

// settle.go — the schema-v8 settle columns (b.3ie) of an existing spawns row,
// written and read through a raw connection: settled_state (the last settled
// state) and state_changed_ms (when the raw state last changed, in Unix
// milliseconds). Writes go through the store's statements only in production;
// these helpers place a row at a chosen point of its settle time.

import (
	"fmt"

	"github.com/gabemahoney/agent-director/internal/store"
)

// SetSettle writes settled as id's settled_state and stampMs as its
// state_changed_ms, leaving every other column (state included) as it is. A
// missing row is an error wrapping store.ErrSpawnNotFound.
func SetSettle(dbPath, id, settled string, stampMs int64) error {
	raw, err := openRawStore(dbPath)
	if err != nil {
		return fmt.Errorf("SetSettle: %w", err)
	}
	defer raw.Close() //nolint:errcheck
	res, err := raw.Exec(`UPDATE spawns SET settled_state = ?, state_changed_ms = ? WHERE instance_id = ?`,
		settled, stampMs, id)
	if err != nil {
		return fmt.Errorf("SetSettle: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return fmt.Errorf("SetSettle: %w: %s (rows %d, %v)", store.ErrSpawnNotFound, id, n, err)
	}
	return nil
}

// ReadSettle returns id's settled_state and state_changed_ms as stored.
func ReadSettle(dbPath, id string) (settled string, stampMs int64, err error) {
	raw, err := openRawStore(dbPath)
	if err != nil {
		return "", 0, fmt.Errorf("ReadSettle: %w", err)
	}
	defer raw.Close() //nolint:errcheck
	if err := raw.QueryRow(`SELECT settled_state, state_changed_ms FROM spawns WHERE instance_id = ?`, id).
		Scan(&settled, &stampMs); err != nil {
		return "", 0, fmt.Errorf("ReadSettle %s: %w", id, err)
	}
	return settled, stampMs, nil
}
