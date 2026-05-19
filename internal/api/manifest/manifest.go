// Package manifest is the single source of truth for the claude-director
// CLI/MCP verb surface.
//
// Each VerbDef entry records a verb's name, description, parameters, result
// fields, and the set of error names it may emit. The CLI dispatch table, the
// MCP tool schema (Epic 11), and the generated reference docs
// (docs/cli-reference.md, docs/mcp-reference.md — Task 6 of Epic 1) all
// derive from Verbs. Adding a verb in any other way drifts from the manifest
// and is caught by the CI doc-drift gate.
//
// This package is deliberately minimal: types, a Verbs slice, and a Lookup
// helper. It does not import internal/store, internal/config, or cmd/ —
// dependencies flow downward toward internal/store, never sideways or up.
package manifest

//go:generate go run github.com/gabemahoney/claude-director/tools/gen-docs

// VerbDef describes one CLI/MCP verb exposed by claude-director.
type VerbDef struct {
	Name         string
	Description  string
	Params       []ParamDef
	ResultFields []FieldDef
	ErrorNames   []string
}

// ParamDef describes one input parameter of a verb.
type ParamDef struct {
	Name        string
	Type        string
	Description string
	Required    bool
}

// FieldDef describes one field of a verb's result object.
type FieldDef struct {
	Name        string
	Type        string
	Description string
}

// Verbs is the canonical, ordered list of verbs implemented by this binary.
// Epic 2+ workers append entries here as they implement new verbs.
var Verbs = []VerbDef{
	{
		Name:        "help",
		Description: "Print the manifest-derived list of verbs as JSON; intended for SessionStart / SessionEnd reason=compact hooks.",
		Params:      []ParamDef{},
		ResultFields: []FieldDef{
			{
				Name:        "verbs",
				Type:        "[]VerbSummary",
				Description: "Array of {name, description} for every verb in the manifest.",
			},
		},
		// Empty (not nil) so JSON marshalling renders [] consistently and
		// SRD §12.4 "help has no error conditions" is reflected in shape.
		ErrorNames: []string{},
	},
}

// Lookup returns the VerbDef registered under name. The second return is
// false when no verb with that name exists.
func Lookup(name string) (VerbDef, bool) {
	for _, v := range Verbs {
		if v.Name == name {
			return v, true
		}
	}
	return VerbDef{}, false
}
