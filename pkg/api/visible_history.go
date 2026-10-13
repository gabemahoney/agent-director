package api

// visibleHistory returns row's visible history (SR-8.7): of lifeEntries — the
// session-history entries of row's life, as returned by the life-taking
// ListSessionHistory read for row.LifeNumber — every entry except those whose
// session id equals row's current, non-empty SessionID. A row with no
// current session id drops nothing. The input order (newest first) is kept,
// lifeEntries is not modified, and the result is an empty non-nil slice when
// nothing remains.
//
// Session history belongs to a life. Same-agent reuse keeps it; a switch or
// switch back starts a new life. Get shows this visible history; resume uses
// it only to distinguish ErrJsonlNeverWritten from ErrJsonlMissing, never to
// select an older session. Resume continues only the latest session. The
// current session's entry never counts toward that distinction, appears in
// prior_sessions or makes transcript_status rotated. This filter works on
// the row the caller already read and never reads the store.
func visibleHistory(row Spawn, lifeEntries []SessionHistoryEntry) []SessionHistoryEntry {
	out := make([]SessionHistoryEntry, 0, len(lifeEntries))
	for _, e := range lifeEntries {
		if row.SessionID != "" && e.SessionID == row.SessionID {
			continue
		}
		out = append(out, e)
	}
	return out
}
