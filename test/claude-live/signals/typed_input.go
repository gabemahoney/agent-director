package signals

import (
	"fmt"
	"strings"
)

// TypedInput preserves the measured Claude 2.1.311 paste representation.
// Only typed-input callers may use Text; AD proofs require their original bytes.
type TypedInput struct {
	Raw     string `json:"raw"`
	Text    string `json:"text"`
	PasteID string `json:"paste_id,omitempty"`
}

func ParseTypedInput(raw string) TypedInput {
	in := TypedInput{Raw: raw, Text: raw}
	const prefix = "<pasted_content id=\""
	if !strings.HasPrefix(raw, prefix) {
		return in
	}
	id, rest, ok := strings.Cut(strings.TrimPrefix(raw, prefix), "\">\n")
	if !ok || id == "" {
		return in
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return in
		}
	}
	suffix := "\n</pasted_content id=\"" + id + "\">"
	if !strings.HasSuffix(rest, suffix) {
		return in
	}
	in.Text, in.PasteID = strings.TrimSuffix(rest, suffix), id
	return in
}

func typedForward(prompt string) string {
	return "The user sent a new message while you were working:\n" + prompt +
		"\n\nThis is how Claude Code surfaces messages the user sends mid-turn — within the running turn, often alongside the next tool result, rather than as a separate conversation turn. Address the message above as you continue this turn."
}

// NextTypedInput keeps normalization out of NextInput/NextBodyInput and AD
// admission/order checks. A queued forward must match the entire recorded form
// reconstructed from independent human attachment facts, never a substring.
func (t Transcript) NextTypedInput(after int64, want string) (Intake, error) {
	for i, request := range t.Requests {
		if request.Time.UnixMilli() < after {
			continue
		}
		in := Intake{Request: i, Line: request.Line, Time: request.Time}
		var queued []TypedInput
		for _, row := range t.Rows {
			a := row.Attachment
			if row.Type != "attachment" || row.Sidechain || row.Timestamp.UnixMilli() < after || row.Timestamp.After(request.Time) || a.Type != "queued_command" || a.CommandMode != "prompt" || a.Origin.Kind != "human" || !a.HumanTurn {
				continue
			}
			parsed := ParseTypedInput(a.Prompt)
			if parsed.Text == want {
				queued = append(queued, parsed)
			}
		}
		for j, message := range request.Messages {
			if message.Role != "user" {
				continue
			}
			texts, err := Texts(message)
			if err != nil {
				return in, err
			}
			for k, text := range texts {
				var matches []TypedInput
				parsed := ParseTypedInput(text)
				if parsed.Text == want {
					matches = append(matches, parsed)
				}
				for _, human := range queued {
					if text == typedForward(human.Raw) {
						matches = append(matches, human)
					}
				}
				for _, match := range matches {
					in.Count++
					in.Message, in.Block = j, k
					if match.PasteID != "" {
						copy := match
						in.Typed = &copy
					}
				}
			}
		}
		if in.Count != 1 {
			return in, fmt.Errorf("next eligible request contains %d whole inputs; expected exactly one", in.Count)
		}
		return in, nil
	}
	return Intake{}, fmt.Errorf("no next eligible model request")
}
