package cursorhost

import "strings"

// Protocol text cannot inject terminal controls, OSC links or bracketed-paste
// delimiters into the human pane. No text is interpreted as runtime state.
func safeText(text string) string {
	var b strings.Builder
	for _, r := range text {
		if r == '\n' {
			b.WriteString("\r\n")
		} else if r == '\t' || (r >= ' ' && r != '\x7f' && !(r >= 0x80 && r <= 0x9f)) {
			b.WriteRune(r)
		}
	}
	return b.String()
}
