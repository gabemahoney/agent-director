package mcp

import (
	api "github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/manifest"
)

// buildInputSchema converts a manifest VerbDef into a JSON Schema
// object describing the tool's `arguments` shape. The schema is
// hand-rolled (no external JSON Schema lib) so the surface stays
// dependency-free and tight to MCP's needs.
//
// Each ParamDef.Type maps to a JSON Schema primitive per
// goTypeToJSONSchema below. Required params end up in the
// `required` array. additionalProperties is false: the dispatcher
// refuses any argument that is not one of the verb's params with
// ErrInvalidFlags (b.c4u), so the schema states what the server enforces.
//
// The schema is deliberately permissive — it ONLY pins the types,
// not the deeper validation (e.g. relay_mode ∈ {on, off}). The api
// layer's typed errors enforce the deeper rules; surfacing them as
// schema validation would just shift the same error to a different
// place in the response.
func buildInputSchema(v manifest.VerbDef) map[string]any {
	properties := make(map[string]any, len(v.Params))
	required := make([]string, 0, len(v.Params))

	for _, p := range v.Params {
		properties[p.Name] = goTypeToJSONSchema(p.Type, p.Description)
		if p.Required {
			required = append(required, p.Name)
		}
	}

	schema := map[string]any{
		"type":                 "object",
		"properties":           properties,
		"additionalProperties": false,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

// goTypeToJSONSchema maps the manifest's Go-style type strings into
// JSON Schema primitives. The mapping is conservative — anything
// unrecognized collapses to a permissive `{}` so a future
// manifest-side addition doesn't crash here.
func goTypeToJSONSchema(goType, description string) map[string]any {
	out := map[string]any{}
	if description != "" {
		out["description"] = description
	}

	switch goType {
	case "string":
		out["type"] = "string"
	case "bool":
		out["type"] = "boolean"
	case "int":
		out["type"] = "integer"
	case "[]string":
		out["type"] = "array"
		out["items"] = map[string]any{"type": "string"}
	case "map[string]string":
		out["type"] = "object"
		out["additionalProperties"] = map[string]any{"type": "string"}
	case "duration":
		// Carried as a string in the JSON form (e.g. "12h", "7d"). expire's
		// older_than, the one duration param, is parsed by
		// api.ParseOlderThan, so its accepted form is api.OlderThanForm.
		out["type"] = "string"
		out["description"] = appendDescription(out["description"],
			"Must be "+api.OlderThanForm+".")
	case "json":
		// Free-form JSON. Used by the hook verb's stdin param; not
		// MCP-exposed but kept for completeness.
		// no `type` constraint
	default:
		// Unknown type — emit no constraint. Better to let an MCP
		// client send whatever and surface the error from the verb
		// than to reject schema-side and lose the typed err_name.
	}
	return out
}

// expectedValue describes, for a refusal (paramDecodeError), the JSON value
// a param of manifest type goType takes, in the terms goTypeToJSONSchema
// declares for it; keep the two in step. It is "" for a type
// goTypeToJSONSchema leaves unconstrained.
func expectedValue(goType string) string {
	switch goType {
	case "string":
		return "a string"
	case "bool":
		return "a boolean"
	case "int":
		return "an integer"
	case "[]string":
		return "an array of strings"
	case "map[string]string":
		return "an object with string values"
	case "duration":
		return "a string holding " + api.OlderThanForm
	}
	return ""
}

// appendDescription tacks on an extra sentence to an existing
// description field. Used to layer per-type help on top of the
// manifest's per-param description.
func appendDescription(existing any, extra string) string {
	if s, ok := existing.(string); ok && s != "" {
		return s + " " + extra
	}
	return extra
}
