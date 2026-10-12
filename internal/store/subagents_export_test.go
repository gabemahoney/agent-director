package store

import "fmt"

// Export-for-test shim (b.xjn): runs the unexported subagent cap helpers inside
// a transaction on s's connection, committing it, for the external
// subagents_test.go. The hook writes that call them in production are another
// Task's.

// EvictSubagentEntryAtCapForTest runs evictSubagentEntryAtCap for instanceID in
// one transaction on s and commits it; it reports whether an entry was deleted.
func EvictSubagentEntryAtCapForTest(s *Store, instanceID string) (bool, error) {
	return evictAtCapInTx(s, instanceID, evictSubagentEntryAtCap)
}

// EvictSubagentLaunchAtCapForTest runs evictSubagentLaunchAtCap for instanceID
// in one transaction on s and commits it; it reports whether a claimable launch
// was deleted.
func EvictSubagentLaunchAtCapForTest(s *Store, instanceID string) (bool, error) {
	return evictAtCapInTx(s, instanceID, evictSubagentLaunchAtCap)
}

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
