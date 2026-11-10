package inputjournal

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// ValidatePayload checks payload, phase and the exact retained provider record.
func ValidatePayload(a *Attempt) error {
	if a.PayloadSHA256 != Hash([]byte(a.Payload)) || a.BytesAttempted < 0 || a.BytesAttempted > len(a.Payload) {
		return fmt.Errorf("invalid delivery payload journal")
	}
	switch a.Phase {
	case "not_started", "attempt_started", "text_attempted", "enter_attempted", "manual_input", "submitted":
	default:
		return fmt.Errorf("unknown delivery phase")
	}
	if a.Phase == "submitted" {
		p := a.Proof
		if p == nil || p.RawSHA256 != Hash(p.Raw) || !a.EnterAttempted {
			return fmt.Errorf("invalid stored submission proof")
		}
		if _, match := MatchRecord(p.Raw, a, p.SessionID); !match {
			return fmt.Errorf("stored user record does not prove this payload")
		}
	}
	return nil
}

// MatchRecord parses one pinned provider record without reading external state.
// Live confirmation and persisted validation must use this same parser.
func MatchRecord(line []byte, a *Attempt, session string) (string, bool) {
	var r struct {
		Type, UUID, SessionID, CWD, Version string
		IsSidechain                         bool
		Message                             struct {
			Role    string
			Content json.RawMessage
		}
		Payload struct {
			Type, Role, ID string
			Content        []struct{ Type, Text string }
			Metadata       struct {
				Kinds []string `json:"content_item_kinds"`
			} `json:"internal_chat_message_metadata_passthrough"`
		}
	}
	if json.Unmarshal(line, &r) != nil {
		return "", false
	}
	if a.Identity.Agent == "claude" {
		if r.Type != "user" || r.Message.Role != "user" || r.IsSidechain || r.SessionID != session || r.CWD != a.Identity.CWD || r.Version != "2.1.280" {
			return "", false
		}
		if _, err := uuid.Parse(r.UUID); err != nil {
			return "", false
		}
		var text string
		if json.Unmarshal(r.Message.Content, &text) != nil {
			return "", false
		}
		return r.UUID, ClaudeExact(text, a.Payload)
	}
	p := r.Payload
	if r.Type != "response_item" || p.Type != "message" || p.Role != "user" {
		return "", false
	}
	userText := false
	for _, k := range p.Metadata.Kinds {
		if k == "user.text" {
			userText = true
		}
	}
	if !userText || len(p.Content) == 0 {
		return "", false
	}
	var text strings.Builder
	for _, c := range p.Content {
		if c.Type != "input_text" {
			return "", false
		}
		text.WriteString(c.Text)
	}
	return p.ID, text.String() == a.Payload
}

// ClaudeExact consumes either the literal payload, its whole pinned wrapper,
// or each known write segment literally/wrapped. Wrapper IDs must pair exactly;
// no trimming, substring matching, or joining different user records is allowed.
func ClaudeExact(text, payload string) bool {
	if text == payload {
		return true
	}
	if rest, ok := consumeWrapper(text, payload); ok && rest == "" {
		return true
	}
	parts, err := Chunks(payload)
	if err != nil {
		return false
	}
	for _, part := range parts {
		if strings.HasPrefix(text, part) {
			text = strings.TrimPrefix(text, part)
			continue
		}
		rest, ok := consumeWrapper(text, part)
		if !ok {
			return false
		}
		text = rest
	}
	return text == ""
}

func consumeWrapper(text, part string) (string, bool) {
	const opening = "\n\n<pasted_content id=\""
	if !strings.HasPrefix(text, opening) {
		return "", false
	}
	tail := strings.TrimPrefix(text, opening)
	id, tail, ok := strings.Cut(tail, "\">\n")
	if !ok || id == "" {
		return "", false
	}
	for _, c := range id {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return "", false
		}
	}
	end := part + "\n</pasted_content id=\"" + id + "\">\n"
	if !strings.HasPrefix(tail, end) {
		return "", false
	}
	return strings.TrimPrefix(tail, end), true
}
