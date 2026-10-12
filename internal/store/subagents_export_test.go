package store

import "fmt"

// Export-for-test shim (b.xjn): runs the unexported gated cap evictions inside a
// transaction on s's connection, committing it, for the external store tests.
// The hook writes that call them in production are another Task's.

// evictAtCapInTx runs evict on a transaction of s, committing on success and
// rolling back on error.
func evictAtCapInTx(s *Store, instanceID string, evict func(querier, string) (bool, error)) (bool, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return false, fmt.Errorf("begin: %w", err)
	}
	evicted, err := evict(tx, instanceID)
	if err != nil {
		_ = tx.Rollback()
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit: %w", err)
	}
	return evicted, nil
}

// EvictSubagentEntryGatedForTest runs evictSubagentEntryGated for instanceID
// with gate in one transaction on s and commits it; it reports whether an entry
// was deleted.
func EvictSubagentEntryGatedForTest(s *Store, instanceID string, gate HookGate) (bool, error) {
	return evictAtCapInTx(s, instanceID, func(q querier, id string) (bool, error) {
		return evictSubagentEntryGated(q, id, gate)
	})
}

// EvictSubagentLaunchGatedForTest is EvictSubagentEntryGatedForTest for the
// claimable-launch cap (evictSubagentLaunchGated).
func EvictSubagentLaunchGatedForTest(s *Store, instanceID string, gate HookGate) (bool, error) {
	return evictAtCapInTx(s, instanceID, func(q querier, id string) (bool, error) {
		return evictSubagentLaunchGated(q, id, gate)
	})
}

// BackdateLastSeenForTest sets instanceID's last_seen_at to a fixed instant long
// before any test runs, so a write that bumps it is visible within one second.
func BackdateLastSeenForTest(s *Store, instanceID string) error {
	if _, err := s.db.Exec(`UPDATE spawns SET last_seen_at = '2000-01-01 00:00:00' WHERE instance_id = ?`, instanceID); err != nil {
		return fmt.Errorf("backdate last_seen_at: %w", err)
	}
	return nil
}
