package tmux

import "regexp"

// ansiSequence matches the SGR / CSI escape sequences `tmux capture-pane -e`
// emits — `\x1b[<params><finalByte>` where params is any mix of digits and
// `;`, and the final byte is an ASCII letter (m for SGR, H for cursor home,
// etc.). It deliberately does not span non-ASCII bytes: the regex is byte-
// oriented and never consumes a UTF-8 continuation byte, which keeps the
// unicode TUI glyphs (`❯`, `⎿`, `🐝`) intact when the caller wants the
// rendered text.
//
// Reference: reference/pane-output-research.md ("Go library recommendation"
// — a 10-line regex covers the entire corpus we observed; no external
// dependency required for v1).
var ansiSequence = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

// StripANSI removes ANSI CSI escape sequences from s while preserving every
// other byte (including non-ASCII / multi-byte UTF-8 sequences). The TUI
// glyphs Claude Code uses (`❯`, `⎿`, `🐝`, box-drawing) are signal, not
// decoration; ASCII-mapping them would throw away state information the
// orchestrator relies on (per pane-output-research.md "State-signal value").
//
// Note: `tmux capture-pane -p` (the call this helper post-processes) already
// strips most escape sequences server-side. This helper exists for two
// reasons:
//
//  1. Some tmux versions / configurations leak through residual escapes —
//     pinning the strip at the verb layer makes the contract independent
//     of tmux quirks.
//  2. A future read-pane caller asking for raw `ansi=true` bytes might want
//     to selectively strip them later in the pipeline; the same function
//     covers both call sites.
func StripANSI(s string) string {
	return ansiSequence.ReplaceAllString(s, "")
}
