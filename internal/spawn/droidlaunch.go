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

// DroidForbiddenEnvKeys is the single list of credential and home selectors
// refused in Droid requests and excluded from its ambient launch environment.
var DroidForbiddenEnvKeys = []string{"FACTORY_API_KEY", "AWS_BEARER_TOKEN_BEDROCK", "FACTORY_HOME_OVERRIDE"}

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

func composeDroid(r Resolved, program string) ([]string, []LaunchFile, error) {
	model, err := DroidModel(r.AgentArgs, true)
	if err != nil {
		return nil, nil, err
	}
	dir, err := LaunchDirectory(r.LaunchStoreID, r.LaunchToken)
	if err != nil {
		return nil, nil, err
	}
	content, err := json.Marshal(struct {
		Model string `json:"model"`
	}{model})
	if err != nil {
		return nil, nil, err
	}
	path := filepath.Join(dir, droidSettingsFile)
	return []string{program, "--settings", path}, []LaunchFile{{Path: path, Content: content}}, nil
}
