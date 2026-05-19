package spawn

import (
	"fmt"
	"os"
	"path/filepath"
)

// JsonlPath returns the absolute on-disk path of the JSONL transcript
// Claude Code maintains for a session. Per SRD §8.2 the layout is:
//
//	~/.claude/projects/<slug(cwd)>/<session_id>.jsonl
//
// where `slug(cwd)` replaces every non-[A-Za-z0-9-] rune with `-`.
//
// This slug rule MUST stay byte-for-byte compatible with Claude Code's
// own algorithm. A "future cleanup" that unifies this with the tmux
// session-name sanitizer (SanitizeSessionName, which preserves `_`)
// would break resume — the on-disk JSONL path is determined by Claude
// Code itself, not by claude-director, so any divergence makes the
// resume pre-flight Stat fail. The asymmetry is intentional.
//
// JsonlPath does NOT touch the filesystem; it only composes the path.
// Resume's caller-side Stat is the I/O that verifies the file exists.
func JsonlPath(cwd, sessionID string) (string, error) {
	if sessionID == "" {
		return "", fmt.Errorf("JsonlPath: sessionID is empty")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("JsonlPath: home dir: %w", err)
	}
	return filepath.Join(home, ".claude", "projects",
		slugifyCwd(cwd), sessionID+".jsonl"), nil
}

// slugifyCwd implements the Claude Code slug rule: every rune outside
// [A-Za-z0-9-] becomes '-'. Note this differs from
// SanitizeSessionName, which preserves '_' as well — keep them
// distinct on purpose (see JsonlPath docs).
func slugifyCwd(cwd string) string {
	out := make([]byte, 0, len(cwd))
	for _, r := range cwd {
		switch {
		case r >= 'A' && r <= 'Z',
			r >= 'a' && r <= 'z',
			r >= '0' && r <= '9',
			r == '-':
			out = append(out, byte(r))
		default:
			// Single-byte replacement for every other rune — including
			// '/', '.', '_', spaces, dots, and any multi-byte UTF-8.
			// Multi-byte runes collapse to ONE '-', not one per byte,
			// because the for-range over a string iterates by rune.
			out = append(out, '-')
		}
	}
	return string(out)
}
