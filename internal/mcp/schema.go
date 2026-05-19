package mcp

import (
	"github.com/gabemahoney/claude-director/internal/api/manifest"
)

// buildInputSchema converts a manifest VerbDef into a JSON Schema
// object describing the tool's `arguments` shape. The schema is
// hand-rolled (no external JSON Schema lib) so the surface stays
// dependency-free and tight to MCP's needs.
//
// Each ParamDef.Type maps to a JSON Schema primitive per
// goTypeToJSONSchema below. Required params end up in the
// `required` array.
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
		"type":       "object",
		"properties": properties,
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
		// Carried as a string in the JSON form (e.g. "12h", "7d");
		// the verb's own parser handles the trailing-`d` extension.
		out["type"] = "string"
		out["description"] = appendDescription(out["description"],
			"Duration in Go's time.ParseDuration form (e.g. \"12h\") or a trailing-d days form (e.g. \"7d\").")
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

// appendDescription tacks on an extra sentence to an existing
// description field. Used to layer per-type help on top of the
// manifest's per-param description.
func appendDescription(existing any, extra string) string {
	if s, ok := existing.(string); ok && s != "" {
		return s + " " + extra
	}
	return extra
}
