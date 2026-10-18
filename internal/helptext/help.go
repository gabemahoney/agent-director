// Package helptext owns the pure serialization shared by CLI help and MCP
// instructions. It performs no configuration, store or process access.
package helptext

import (
	"encoding/json"

	api "github.com/gabemahoney/agent-director/pkg/api"
)

// Marshal returns the canonical JSON envelope without its stdout terminator.
// Codex's additionalContext retains this exact, unterminated representation.
func Marshal(verbs []api.VerbSummary) ([]byte, error) {
	return json.Marshal(struct {
		Verbs []api.VerbSummary `json:"verbs"`
	}{Verbs: verbs})
}

// Plain returns the complete plain CLI output, including its final newline.
func Plain() (string, error) {
	verbs, err := api.Help()
	if err != nil {
		return "", err
	}
	b, err := Marshal(verbs)
	if err != nil {
		return "", err
	}
	return string(b) + "\n", nil
}
