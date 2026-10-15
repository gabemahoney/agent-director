package cursorhost

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
)

// These adapters describe Cursor 2026.10.12-d47ca18 / ACP 1. Plan outcomes
// are captured; question extension/fallback shapes are installed-source evidence.
// Raw params remain the ToolInput envelope: internal context is never added.
type authorization struct {
	frame              frame
	toolID, name, kind string
	options            []permissionOption
	plan               bool
}
type permissionOption struct {
	ID   string `json:"optionId"`
	Name string `json:"name"`
	Kind string `json:"kind"`
}

var questionToolID = regexp.MustCompile(`(?i)(ask[_-]?question|ask_question_tool_call|_q[0-9]+$|^ask_)`)

// uniqueJSON rejects duplicate keys at every depth, including otherwise valid
// options with conflicting meanings. encoding/json's last-key-wins is unsafe
// for authorization and for question classification.
func uniqueJSON(raw []byte) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var value func() error
	value = func() error {
		t, e := d.Token()
		if e != nil {
			return e
		}
		delim, ok := t.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				k, e := d.Token()
				if e != nil {
					return e
				}
				s, ok := k.(string)
				if !ok || seen[s] {
					return fmt.Errorf("duplicate JSON field")
				}
				seen[s] = true
				if e = value(); e != nil {
					return e
				}
			}
			t, e = d.Token()
			if e != nil || t != json.Delim('}') {
				return fmt.Errorf("invalid JSON object")
			}
		case '[':
			for d.More() {
				if e = value(); e != nil {
					return e
				}
			}
			t, e = d.Token()
			if e != nil || t != json.Delim(']') {
				return fmt.Errorf("invalid JSON array")
			}
		default:
			return fmt.Errorf("invalid JSON delimiter")
		}
		return nil
	}
	if e := value(); e != nil {
		return e
	}
	if _, e := d.Token(); e != io.EOF {
		return fmt.Errorf("trailing JSON")
	}
	return nil
}
func object(raw json.RawMessage) (map[string]json.RawMessage, error) {
	var m map[string]json.RawMessage
	if e := json.Unmarshal(raw, &m); e != nil || m == nil {
		return nil, fmt.Errorf("expected object")
	}
	return m, nil
}
func requiredString(m map[string]json.RawMessage, key string) (string, error) {
	var s string
	if json.Unmarshal(m[key], &s) != nil || s == "" {
		return "", fmt.Errorf("missing %s", key)
	}
	return s, nil
}
func onlyFields(m map[string]json.RawMessage, names ...string) bool {
	allowed := map[string]bool{}
	for _, n := range names {
		allowed[n] = true
	}
	for k := range m {
		if !allowed[k] {
			return false
		}
	}
	return true
}

// parseAuthorization is the pre-admission policy, shared by all relay modes
// and suppression settings. Unknown shapes remain unanswerable. In particular
// kind=other alone cannot establish non-question provenance.
func parseAuthorization(f frame, session string) (*authorization, error) {
	if e := uniqueJSON(f.Params); e != nil {
		return nil, e
	}
	m, e := object(f.Params)
	if e != nil {
		return nil, e
	}
	a := &authorization{frame: f}
	switch f.Method {
	case "cursor/create_plan":
		if !onlyFields(m, "toolCallId", "name", "overview", "plan", "todos", "isProject", "phases") {
			return nil, fmt.Errorf("unsupported plan fields")
		}
		if a.toolID, e = requiredString(m, "toolCallId"); e != nil {
			return nil, e
		}
		if a.name, e = requiredString(m, "name"); e != nil {
			return nil, e
		}
		if _, e = requiredString(m, "overview"); e != nil {
			return nil, e
		}
		if _, e = requiredString(m, "plan"); e != nil {
			return nil, e
		}
		// Optional plan fields retain absent versus explicit null. Non-null values
		// must have the captured outer types; their full content is retained.
		for _, k := range []string{"todos", "phases"} {
			if r, ok := m[k]; ok && !bytes.Equal(bytes.TrimSpace(r), []byte("null")) {
				var v []json.RawMessage
				if json.Unmarshal(r, &v) != nil || v == nil {
					return nil, fmt.Errorf("invalid plan %s", k)
				}
			}
		}
		if r, ok := m["isProject"]; ok && !bytes.Equal(bytes.TrimSpace(r), []byte("null")) {
			var v bool
			if json.Unmarshal(r, &v) != nil {
				return nil, fmt.Errorf("invalid isProject")
			}
		}
		if questionToolID.MatchString(a.toolID) {
			return nil, fmt.Errorf("question denied")
		}
		a.plan = true
		return a, nil
	case "session/request_permission":
		if !onlyFields(m, "sessionId", "toolCall", "options") {
			return nil, fmt.Errorf("unsupported permission fields")
		}
		sid, e := requiredString(m, "sessionId")
		if e != nil || sid != session || session == "" {
			return nil, fmt.Errorf("permission session mismatch")
		}
		tool, e := object(m["toolCall"])
		if e != nil {
			return nil, e
		}
		if !onlyFields(tool, "toolCallId", "title", "kind", "status", "content", "locations", "rawInput", "rawOutput") {
			return nil, fmt.Errorf("ambiguous permission tool")
		}
		if a.toolID, e = requiredString(tool, "toolCallId"); e != nil {
			return nil, e
		}
		if a.name, e = requiredString(tool, "title"); e != nil {
			return nil, e
		}
		if a.kind, e = requiredString(tool, "kind"); e != nil {
			return nil, e
		}
		status, e := requiredString(tool, "status")
		if e != nil || status != "pending" {
			return nil, fmt.Errorf("permission is not pending")
		}
		if questionToolID.MatchString(a.toolID) {
			return nil, fmt.Errorf("question denied")
		}
		switch a.kind {
		case "execute", "read", "edit", "delete", "search", "fetch":
		case "other":
			// Captured MCP requests are explicitly titled with their MCP identity.
			// Generic 'other' is the source-derived question fallback's kind.
			if !strings.Contains(a.name, ": ") {
				return nil, fmt.Errorf("ambiguous authorization denied")
			}
		default:
			return nil, fmt.Errorf("unsupported authorization kind")
		}
		if json.Unmarshal(m["options"], &a.options) != nil || len(a.options) == 0 || len(a.options) > 8 {
			return nil, fmt.Errorf("invalid permission options")
		}
		var rawOptions []json.RawMessage
		if json.Unmarshal(m["options"], &rawOptions) != nil {
			return nil, fmt.Errorf("invalid option schema")
		}
		for _, raw := range rawOptions {
			v, e := object(raw)
			if e != nil || !onlyFields(v, "optionId", "name", "kind") {
				return nil, fmt.Errorf("unsupported option schema")
			}
		}
		seen := map[string]bool{}
		kinds := map[string]bool{}
		for _, o := range a.options {
			if o.ID == "__ask_question_skip__" {
				return nil, fmt.Errorf("question denied")
			}
			if o.ID == "" || o.Name == "" || seen[o.ID] || kinds[o.Kind] {
				return nil, fmt.Errorf("ambiguous permission options")
			}
			seen[o.ID] = true
			kinds[o.Kind] = true
			switch o.Kind {
			case "allow_once", "allow_always", "reject_once", "reject_always":
			default:
				return nil, fmt.Errorf("unsupported permission option")
			}
		}
		if a.kind == "other" {
			for _, o := range a.options {
				canonical := map[string]string{"allow_once": "allow-once", "allow_always": "allow-always", "reject_once": "reject-once", "reject_always": "reject-always"}
				if o.ID != canonical[o.Kind] {
					return nil, fmt.Errorf("ambiguous MCP authorization options")
				}
			}
		}
		if !kinds["allow_once"] || !kinds["reject_once"] {
			return nil, fmt.Errorf("permission needs once allow and reject")
		}
		return a, nil
	case "cursor/ask_question":
		return nil, fmt.Errorf("question denied")
	default:
		return nil, fmt.Errorf("unsupported authorization method")
	}
}
func (a *authorization) result(decision, nativeOption string) (any, error) {
	if a.plan {
		out := "rejected"
		if decision == "allow" {
			out = "accepted"
		}
		return map[string]any{"outcome": map[string]any{"outcome": out}}, nil
	}
	kind := "reject_once"
	if decision == "allow" {
		kind = "allow_once"
	}
	for _, o := range a.options {
		if (nativeOption == "" && o.Kind == kind) || (nativeOption != "" && o.ID == nativeOption) {
			return map[string]any{"outcome": map[string]any{"outcome": "selected", "optionId": o.ID}}, nil
		}
	}
	return nil, fmt.Errorf("unsupported choice")
}
