package tmux

import "strings"

// LastPaneLines returns the last n rows of text, a capture-pane output, as
// CapturePaneID gives them (b.7y8). The blank rows below the last row with
// content are dropped first; blank rows between content rows count. A row
// is blank when it is whitespace only once ANSI escape sequences are
// stripped, so an -e capture and the same capture stripped give the same
// rows. Each row keeps its newline as tmux printed it, and escape sequences
// are kept. An all-blank text, or an n of 0 or less, gives "".
func LastPaneLines(text string, n int) string {
	if n <= 0 {
		return ""
	}
	rows := strings.SplitAfter(text, "\n")
	end := len(rows)
	for end > 0 && strings.TrimSpace(StripANSI(rows[end-1])) == "" {
		end--
	}
	return strings.Join(rows[max(0, end-n):end], "")
}
