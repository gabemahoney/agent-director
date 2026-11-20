package spawn

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/layout"
)

// DroidForbiddenEnvKeys is the single list of credential, home and settings selectors
// refused in Droid requests and excluded from its ambient launch environment.
var DroidForbiddenEnvKeys = []string{
	"FACTORY_API_KEY", "AWS_BEARER_TOKEN_BEDROCK", "FACTORY_HOME_OVERRIDE",
	"FACTORY_RUNTIME_SETTINGS_PATH", "FACTORY_RUNTIME_SETTINGS_SOURCE",
	"FACTORY_RUNTIME_SETTINGS_SHA256", "FACTORY_RUNTIME_SETTINGS_ORIGIN",
	"FACTORY_ORG_MANAGED_SETTINGS_LOCAL_PATH", "FACTORY_ORG_MANAGED_SETTINGS_URL",
}

// DroidCustomModelPrefix selects Factory BYOK models rather than subscription use.
// Pinned-version acceptance must confirm this documented selector.
const DroidCustomModelPrefix = "custom:"

const droidSettingsFile = "droid-settings.json"

// LaunchFile describes an AD-generated file. Composition describes it without I/O;
// preparation creates it exclusively before the launch's first row write.
type LaunchFile struct {
	Path    string
	Content []byte
}

// LaunchDirectory derives the private token directory and refuses path traversal.
func LaunchDirectory(storeID, token string) (string, error) {
	for _, component := range []string{storeID, token} {
		if component == "" || component == "." || component == ".." || strings.ContainsAny(component, "/\\\x00") || filepath.Base(component) != component {
			return "", fmt.Errorf("invalid launch file identity")
		}
	}
	home, err := os.UserHomeDir()
	if err != nil || !filepath.IsAbs(home) {
		return "", fmt.Errorf("launch files require an absolute home directory")
	}
	return layout.Join(home, "launch", storeID, token), nil
}

// DroidModel accepts only the lane's split --model ID directive. No values or
// positional task text are included in diagnostics.
func DroidModel(args []string, required bool) (string, error) {
	model := ""
	for i := 0; i < len(args); i++ {
		option := args[i]
		if option != "--model" {
			if strings.HasPrefix(option, "--model=") {
				return "", fmt.Errorf("%w: droid requires the split --model ID form; supply --model and its ID as separate arguments", ErrSpawnDeniedFlag)
			}
			// An unrecognized argument may contain a value, even when it
			// starts with a dash. Identify its position, never echo its text.
			return "", fmt.Errorf("%w: droid refuses agent_args[%d]; remove this argument and any following value; supply only --model ID", ErrSpawnDeniedFlag, i)
		}
		if model != "" {
			return "", fmt.Errorf("%w: droid refuses duplicate --model; supply exactly one --model ID", ErrSpawnDeniedFlag)
		}
		if i+1 == len(args) || args[i+1] == "" || strings.HasPrefix(args[i+1], "-") || strings.IndexByte(args[i+1], 0) >= 0 || !utf8.ValidString(args[i+1]) {
			return "", fmt.Errorf("%w: droid --model requires a nonempty ID that is not an option; supply --model ID", ErrSpawnDeniedFlag)
		}
		i++
		model = args[i]
		if strings.HasPrefix(model, DroidCustomModelPrefix) {
			return "", fmt.Errorf("%w: droid --model refuses the custom-model selector; supply a subscription model ID", ErrSpawnDeniedFlag)
		}
	}
	if required && model == "" {
		return "", fmt.Errorf("%w: agent droid requires --model ID; supply exactly one explicit subscription model", ErrModelMissing)
	}
	return model, nil
}

func droidProgram(cfg config.Config) string {
	if cfg.Launch.DroidPath != "" {
		return cfg.Launch.DroidPath
	}
	return "droid"
}

// droidOverlay is the one owner of the Droid overlay's keys (b.s8e SR-3):
// model, optional checked permissionRules, then the required Guard hooks.
// This amends b.7tx SR-2.3 and b.i4t SR-3.1: no credentials, caller-controlled
// settings, autonomy, sandbox, other hooks or permission bypass.
type droidOverlay struct {
	Model string `json:"model"`
	// PermissionRules is Resolved.DroidRules as checked; encoding a compact
	// json.Marshal output again leaves its bytes unchanged.
	PermissionRules json.RawMessage `json:"permissionRules,omitempty"`
	Hooks           droidGuardHooks `json:"hooks"`
}

type droidGuardHooks struct {
	HooksDisabled bool                `json:"hooksDisabled"`
	PreToolUse    []droidGuardMatcher `json:"PreToolUse"`
}

type droidGuardMatcher struct {
	Matcher string           `json:"matcher"`
	Hooks   []droidGuardHook `json:"hooks"`
}

type droidGuardHook struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	Timeout int    `json:"timeout"`
}

// composeDroid describes the overlay and argv without I/O; the launch's
// ResolveDroidGuard step supplied the checked command, and ResolveDroidRules
// supplied any checked rules and the program that checked them.
func composeDroid(r Resolved, program string) ([]string, []LaunchFile, error) {
	if r.droidGuard == nil || len(r.droidGuard.entries) == 0 || r.droidGuard.command == "" {
		return nil, nil, droidGuardRefusal(0, guardUnchecked)
	}
	model, err := DroidModel(r.AgentArgs, true)
	if err != nil {
		return nil, nil, err
	}
	dir, err := LaunchDirectory(r.LaunchStoreID, r.LaunchToken)
	if err != nil {
		return nil, nil, err
	}
	content, err := json.Marshal(droidOverlay{
		Model:           model,
		PermissionRules: r.DroidRules,
		Hooks: droidGuardHooks{
			HooksDisabled: false,
			PreToolUse: []droidGuardMatcher{{
				Matcher: "*",
				Hooks: []droidGuardHook{{
					Type: "command", Command: r.droidGuard.command,
					Timeout: 60, // Droid's documented default; the Guard bounds its own work below it.
				}},
			}},
		},
	})
	if err != nil {
		return nil, nil, err
	}
	if r.DroidProgram != "" {
		// The launch runs exactly the executable that checked its rules.
		program = r.DroidProgram
	}
	path := filepath.Join(dir, droidSettingsFile)
	return []string{program, "--settings", path}, []LaunchFile{{Path: path, Content: content}}, nil
}
