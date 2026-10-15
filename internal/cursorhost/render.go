package cursorhost

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

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

// Render actual descriptions as text, with the complete supplied envelope
// available below. Rendering does not classify screen text or change stored
// bytes; safeText prevents protocol content from injecting terminal controls.
func renderAuthorization(out io.Writer, a *authorization) {
	var params map[string]json.RawMessage
	if json.Unmarshal(a.frame.Params, &params) != nil {
		return
	}
	if a.plan {
		for _, key := range []string{"overview", "plan"} {
			var value string
			if json.Unmarshal(params[key], &value) == nil {
				fmt.Fprint(out, safeText(value)+"\r\n")
			}
		}
	} else {
		var tool struct {
			Content []struct {
				Content struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"content"`
		}
		if json.Unmarshal(params["toolCall"], &tool) == nil {
			for _, content := range tool.Content {
				if content.Content.Type == "text" {
					fmt.Fprint(out, safeText(content.Content.Text)+"\r\n")
				}
			}
		}
	}
	var formatted bytes.Buffer
	if json.Indent(&formatted, a.frame.Params, "", "  ") == nil {
		fmt.Fprint(out, safeText(formatted.String())+"\r\n")
	}
}
