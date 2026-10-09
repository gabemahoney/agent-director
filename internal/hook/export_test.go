package hook

// ExtractSessionID exposes extractSessionID to the external test package.
var ExtractSessionID = extractSessionID

// SessionStartWaitCap exposes sessionStartWaitCap, the longest SessionStart
// waits for its launch's identity write (WD 2026-09-30c), to the external
// test package.
const SessionStartWaitCap = sessionStartWaitCap
