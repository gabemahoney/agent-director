package api

// visibleHistory returns row's visible history (SR-8.7): of lifeEntries — the
// session-history entries of row's life, as returned by the life-taking
// ListSessionHistory read for row.LifeNumber — every entry except those whose
// session id equals row's current, non-empty ClaudeSessionID. A row with no
// current session id drops nothing. The input order (newest first) is kept,
// lifeEntries is not modified, and the result is an empty non-nil slice when
// nothing remains.
//
// Session history belongs to a life: each entry is a session that ran while
// that life's id was current. resume and get use only the visible history, so
// the entry for the session the row currently points at is never a history
// candidate, never counts as history in the ErrJsonlNeverWritten /
// ErrJsonlMissing decision, and never appears in prior_sessions or makes
// transcript_status rotated. This is the only place that rule is applied; it
// works on the row the caller already read and never reads the store.
func visibleHistory(row Spawn, lifeEntries []SessionHistoryEntry) []SessionHistoryEntry {
	out := make([]SessionHistoryEntry, 0, len(lifeEntries))
	for _, e := range lifeEntries {
		if row.ClaudeSessionID != "" && e.ClaudeSessionID == row.ClaudeSessionID {
			continue
		}
		out = append(out, e)
	}
	return out
}
