package spawn

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/hostenv"
)

// OpenCodeForbiddenEnvKeys names the caller controls owned by this adapter.
// Client exclusion uses the same list; session overrides and unsets are separate.
var OpenCodeForbiddenEnvKeys = []string{hostenv.OpenRouterAPIKey, "OPENCODE_CONFIG", "OPENCODE_CONFIG_DIR", "OPENCODE_CONFIG_CONTENT", "OPENCODE_AUTH_CONTENT", "OPENCODE_PERMISSION"}

// Native V1 config uses an environment reference, never an interpolated key.
// Field names follow https://docs.opencode.ai/docs/config/ on the 1.19.4 pin.
const openCodeConfigContent = `{"enabled_providers":["openrouter"],"permission":{"edit":"ask","bash":"ask"},"autoupdate":false,"provider":{"openrouter":{"options":{"apiKey":"{env:` + hostenv.OpenRouterAPIKey + `}"}}}}`

// OpenCodeModel accepts only one split --model openrouter/ID. Every refusal
// identifies the argument position without echoing its potentially private text.
func OpenCodeModel(args []string, required bool) (string, error) {
	model := ""
	for i := 0; i < len(args); i++ {
		if args[i] != "--model" || model != "" {
			return "", fmt.Errorf("%w: opencode refuses agent_args[%d]; supply only one split --model openrouter/ID", ErrSpawnDeniedFlag, i)
		}
		if i+1 == len(args) {
			return "", fmt.Errorf("%w: opencode agent_args[%d] requires a following openrouter/ID model value", ErrSpawnDeniedFlag, i)
		}
		i++
		value := args[i]
		id, ok := strings.CutPrefix(value, "openrouter/")
		if !ok || id == "" || strings.HasPrefix(value, "-") || strings.IndexByte(value, 0) >= 0 || !utf8.ValidString(value) {
			return "", fmt.Errorf("%w: opencode refuses agent_args[%d]; supply a nonempty openrouter/ID model value", ErrSpawnDeniedFlag, i)
		}
		model = value
	}
	if required && model == "" {
		return "", fmt.Errorf("%w: agent opencode requires --model openrouter/ID; supply one explicit model", ErrModelMissing)
	}
	return model, nil
}

func opencodeProgram(cfg config.Config) string {
	if cfg.Launch.OpenCodePath != "" {
		return cfg.Launch.OpenCodePath
	}
	return "opencode"
}

func composeOpenCode(r Resolved, program string) ([]string, []LaunchFile, error) {
	model, err := OpenCodeModel(r.AgentArgs, true)
	if err != nil {
		return nil, nil, err
	}
	return []string{program, "--model", model}, nil, nil
}
