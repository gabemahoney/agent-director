package toolguard

import (
	"encoding/json"
	"io"
)

// payload contains the envelope and already validated input. Only checked tools
// have fields; unchecked tools never acquire input validation. Missing or
// non-string cwd is retained as empty so only a later relative-path rule needs
// to refuse it. Metadata (including transcript_path) is never followed.
type payload struct {
	tool   string
	cwd    string
	fields map[string]any
}

// text and strings read fields whose types decodePayload has checked. The bool
// distinguishes absence from an empty value; later rules must not decode again.
func (p payload) text(name string) (string, bool) {
	s, ok := p.fields[name].(string)
	return s, ok
}

func (p payload) strings(name string) ([]string, bool) {
	s, ok := p.fields[name].([]string)
	return s, ok
}

type fieldType uint8

const (
	textField fieldType = iota
	numberField
	boolField
	stringsField
	textOrStringsField
)

type toolSchema struct {
	required string
	fields   map[string]fieldType
}

// checkedTools combines SR-6.1's allowlist/required policy with the JSON type
// families from Droid 1.5.0. In particular the Glob tool's own schema accepts
// string | string[] for both pattern fields (also observed in G1). Enum,
// range and content restrictions belong to the tool, not this boundary.
var checkedTools = map[string]toolSchema{
	"Read": {"file_path", map[string]fieldType{
		"file_path": textField, "image_quality": textField, "offset": numberField, "limit": numberField,
	}},
	"Create": {"file_path", map[string]fieldType{
		"file_path": textField, "content": textField,
	}},
	"Edit": {"file_path", map[string]fieldType{
		"file_path": textField, "old_str": textField, "new_str": textField, "change_all": boolField,
	}},
	"ApplyPatch": {"input", map[string]fieldType{"input": textField}},
	"LS": {"", map[string]fieldType{
		"directory_path": textField, "ignorePatterns": stringsField,
	}},
	"Grep": {"", map[string]fieldType{
		"pattern": textField, "path": textField, "glob_pattern": textField, "output_mode": textField,
		"case_insensitive": boolField, "line_numbers": boolField, "context": numberField,
		"context_before": numberField, "context_after": numberField, "head_limit": numberField,
	}},
	"Glob": {"", map[string]fieldType{
		"folder": textField, "patterns": textOrStringsField, "excludePatterns": textOrStringsField,
	}},
	"Execute": {"command", map[string]fieldType{
		"command": textField, "summary": textField, "riskLevel": textField, "riskLevelReason": textField,
		"timeout": numberField, "fireAndForget": boolField,
	}},
}

// decodePayload reads exactly one object, including EOF, inside the caller's
// whole-decision budget. UseNumber preserves every valid JSON number without
// adding float64 overflow/range restrictions. Checked fields are decoded once
// and normalized here; carried text is preserved without scanning for paths.
func decodePayload(stdin io.Reader) (payload, bool) {
	r := &io.LimitedReader{R: stdin, N: maxPayloadBytes + 1}
	d := json.NewDecoder(r)
	d.UseNumber()
	var envelope map[string]any
	if err := d.Decode(&envelope); err != nil || envelope == nil {
		return payload{}, false
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF || r.N <= 0 {
		return payload{}, false
	}
	event, ok := envelope["hook_event_name"].(string)
	if !ok || event != "PreToolUse" {
		return payload{}, false
	}
	tool, ok := envelope["tool_name"].(string)
	if !ok {
		return payload{}, false
	}
	cwd, _ := envelope["cwd"].(string)
	p := payload{tool: tool, cwd: cwd}
	schema, checked := checkedTools[tool]
	if !checked {
		return p, true
	}
	fields, ok := envelope["tool_input"].(map[string]any)
	if !ok {
		return payload{}, false
	}
	if schema.required != "" {
		if _, present := fields[schema.required]; !present {
			return payload{}, false
		}
	}
	for name, value := range fields {
		kind, known := schema.fields[name]
		if !known {
			return payload{}, false
		}
		normalized, valid := fieldValue(value, kind)
		if !valid {
			return payload{}, false
		}
		fields[name] = normalized
	}
	p.fields = fields
	return p, true
}

func fieldValue(value any, kind fieldType) (any, bool) {
	switch kind {
	case textField:
		v, ok := value.(string)
		return v, ok
	case numberField:
		v, ok := value.(json.Number)
		return v, ok
	case boolField:
		v, ok := value.(bool)
		return v, ok
	case textOrStringsField:
		if v, ok := value.(string); ok {
			return []string{v}, true
		}
		fallthrough
	case stringsField:
		values, ok := value.([]any)
		if !ok {
			return nil, false
		}
		strings := make([]string, len(values))
		for i, v := range values {
			s, ok := v.(string)
			if !ok {
				return nil, false
			}
			strings[i] = s
		}
		return strings, true
	default:
		return nil, false
	}
}
