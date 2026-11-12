package spawn

import (
	"fmt"
	"os"
	"slices"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/hostenv"
)

// paneOnlyPolicy supplies the shared lane's launch adapters. New pane-only
// agents extend config's capability table and this adapter table, not callers.
type paneOnlyPolicy struct {
	flag         string
	enabled      func(config.Experimental) bool
	program      func(config.Config) string
	readArgs     func([]string, bool) (string, error)
	forbiddenEnv []string
	unsetEnv     []string          // nil retains the forbiddenEnv session unsets
	forwardEnv   []string          // names only, resolved by tmux at create time
	sessionEnv   map[string]string // nonsecret AD-owned values, never saved caller env
	files        []string
	compose      func(Resolved, string) ([]string, []LaunchFile, error)
}

var paneOnlyAgents = map[string]paneOnlyPolicy{
	config.AgentDroid: {
		flag: "droid", enabled: func(e config.Experimental) bool { return e.Droid },
		program: droidProgram, readArgs: DroidModel, forbiddenEnv: DroidForbiddenEnvKeys,
		compose: composeDroid,
		files:   []string{droidSettingsFile},
	},
	config.AgentOpenCode: {
		flag: "opencode", enabled: func(e config.Experimental) bool { return e.OpenCode },
		program: opencodeProgram, readArgs: OpenCodeModel, forbiddenEnv: OpenCodeForbiddenEnvKeys,
		compose:    composeOpenCode,
		unsetEnv:   []string{"OPENCODE_CONFIG", "OPENCODE_CONFIG_DIR", "OPENCODE_AUTH_CONTENT", "OPENCODE_PERMISSION"},
		forwardEnv: []string{hostenv.OpenRouterAPIKey},
		sessionEnv: map[string]string{"OPENCODE_CONFIG_CONTENT": openCodeConfigContent, "OPENCODE_DISABLE_AUTOUPDATE": "1"},
	},
}

// IsPaneOnly identifies agents with the fixed, supervised pane-only contract.
func IsPaneOnly(agent string) bool {
	return config.IsPaneOnly(agent)
}

// CheckExperimental gates new launches and template creation, never reads or cleanup.
func CheckExperimental(agent string, experimental config.Experimental) error {
	if p, ok := paneOnlyAgents[agent]; ok && !p.enabled(experimental) {
		return fmt.Errorf("%w: agent %s requires [experimental] %s = true; enable that flag before creating its launch or template", ErrExperimentalAgentDisabled, agent, p.flag)
	}
	return nil
}

// PaneOnlyCapabilityError is the common refusal for unsupported lane operations.
func PaneOnlyCapabilityError(agent, capability string) error {
	return fmt.Errorf("%w: agent %s does not support %s in the pane-only lane; omit this capability and supervise the owned pane", ErrAgentCapabilityUnsupported, agent, capability)
}

// ValidatePaneOnlySettings checks the shared settings and the adapter's arguments.
// Templates may defer their model until spawn; launches must supply it.
func ValidatePaneOnlySettings(agent string, args []string, relay string, permissions *Permissions, requireModel bool) error {
	p, ok := paneOnlyAgents[agent]
	if !ok {
		return nil
	}
	if _, err := p.readArgs(args, requireModel); err != nil {
		return err
	}
	if relay == "on" {
		return PaneOnlyCapabilityError(agent, "relay_mode on")
	}
	if permissions != nil && len(permissions.Allow)+len(permissions.Deny)+len(permissions.Ask) > 0 {
		return fmt.Errorf("%w: agent %s takes no allow, deny or ask entries; remove those entries and handle native permission prompts through the pane", ErrPermissionsUnsupported, agent)
	}
	return nil
}

func validatePaneOnlyEnv(agent string, env map[string]string) error {
	p, ok := paneOnlyAgents[agent]
	if !ok {
		return nil
	}
	if key, found := firstEnvKey(env, func(key string) bool { return slices.Contains(p.forbiddenEnv, envVarName(key)) }); found {
		return fmt.Errorf("%w: agent %s refuses extra_env key %s; remove that key from extra_env", ErrReservedEnvKey, agent, envVarName(key))
	}
	return nil
}

// PaneOnlyExcludedEnv returns the lane's client exclusions. Session values are
// separately overridden or unset through the policy's session environment.
func PaneOnlyExcludedEnv(agent string) []string {
	return slices.Clone(paneOnlyAgents[agent].forbiddenEnv)
}

// validateHostCredentials checks presence only, after all caller-input checks.
// Templates do not call it: they launch nothing and may be saved without a key.
func validateHostCredentials(agent string) error {
	for _, key := range paneOnlyAgents[agent].forwardEnv {
		if os.Getenv(key) == "" {
			return fmt.Errorf("%w: agent %s requires nonempty %s in agent-director's own environment; set it there, never in extra_env, and restart the MCP server after changing it", ErrHostCredentialMissing, agent, key)
		}
	}
	return nil
}
