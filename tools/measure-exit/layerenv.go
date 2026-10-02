package main

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// layerFile is one settings or MCP layer file a real run's agents load:
// kind names it in a refusal, path is where the driver reads it.
type layerFile struct {
	kind string
	path string
}

// layerEnvRefusedNames are names a layer's env object may not set in real
// mode, besides realModeRefusedEnv, the credentialEnv names and any
// layerEnvRefusedPrefix name: ANTHROPIC_CUSTOM_HEADERS would add headers to
// every gateway request (ANTHROPIC_BASE_URL is in credentialEnv). The host
// runner's refuse_credential_layer (run.sh LAYER_REFUSED_ENV) refuses the
// same names; keep the two in step.
var layerEnvRefusedNames = []string{"ANTHROPIC_CUSTOM_HEADERS"}

// layerEnvRefusedPrefix starts the Claude Code provider switches
// (CLAUDE_CODE_USE_BEDROCK, CLAUDE_CODE_USE_VERTEX, ...): a layer that sets
// one would move the bill off the gateway.
const layerEnvRefusedPrefix = "CLAUDE_CODE_USE_"

// layerEnvValueRE matches a layer env value that looks like a URL or an
// authorization header (any case), the same test run.sh runs in jq.
var layerEnvValueRE = regexp.MustCompile(`(?i)://|\bbearer\s|authorization\s*:`)

// realModeLayerFiles lists the layer files a real run's agents load, as the
// host runner mounts them: the managed and user layers at the paths Claude
// Code reads, then the staged project, local and MCP layers the flags name.
func realModeLayerFiles(c config, home string) []layerFile {
	files := []layerFile{
		{kind: string(layerManaged), path: managedSettingsPath},
		{kind: string(layerUser), path: filepath.Join(home, ".claude", "settings.json")},
	}
	for _, f := range []layerFile{
		{kind: string(layerProject), path: c.projectSettings},
		{kind: string(layerLocal), path: c.localSettings},
		{kind: "mcp", path: c.mcpConfig},
	} {
		if f.path != "" {
			files = append(files, f)
		}
	}
	return files
}

// layerEnvNameRefused reports whether a layer's env object may not set
// name in real mode. The comparison ignores case, as run.sh's does.
func layerEnvNameRefused(name string) bool {
	upper := strings.ToUpper(name)
	if strings.HasPrefix(upper, layerEnvRefusedPrefix) || realModeRefused(upper) || isCredentialName(upper) {
		return true
	}
	for _, n := range layerEnvRefusedNames {
		if upper == n {
			return true
		}
	}
	return false
}

// checkLayerFilesEnv refuses real mode when any env object in a layer file
// (at any depth: the settings env, an MCP server's env) sets a name
// layerEnvNameRefused refuses, or sets any name to a value layerEnvValueRE
// matches: either would take the agents off the gateway, or carry a
// credential into the run. It is the host runner's layer env check
// (refuse_credential_layer), repeated here so a container started by hand
// is covered too. A missing file is no layer; a file that cannot be read or
// is not JSON is refused, because it cannot be checked. The refusal names
// the layer, its path and the key, never a value.
func checkLayerFilesEnv(files []layerFile) error {
	for _, f := range files {
		b, err := os.ReadFile(f.path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return refuse(ruleRealGatewayOnly, "cannot read the %s layer %s, so it cannot be checked for env settings that leave the gateway: %v", f.kind, f.path, err)
		}
		var doc any
		if err := json.Unmarshal(b, &doc); err != nil {
			return refuse(ruleRealGatewayOnly, "the %s layer %s is not a JSON document, so it cannot be checked for env settings that leave the gateway", f.kind, f.path)
		}
		name, byValue, found := findLayerEnv(doc)
		switch {
		case !found:
		case byValue:
			return refuse(ruleRealGatewayOnly,
				"an env object in the %s layer %s sets %s to a value that looks like a URL or an authorization header (values are never printed); real mode bills to the gateway only", f.kind, f.path, name)
		default:
			return refuse(ruleRealGatewayOnly,
				"an env object in the %s layer %s sets %s, which would take the agents off the gateway (values are never printed); real mode bills to the gateway only", f.kind, f.path, name)
		}
	}
	return nil
}

// findLayerEnv walks a decoded JSON document, keys in sorted order, and
// returns the first entry of an env object whose name is refused (byValue
// false) or whose value looks like a URL or an authorization header
// (byValue true).
func findLayerEnv(v any) (name string, byValue, found bool) {
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		if env, ok := t["env"].(map[string]any); ok {
			envKeys := make([]string, 0, len(env))
			for k := range env {
				envKeys = append(envKeys, k)
			}
			sort.Strings(envKeys)
			for _, k := range envKeys {
				if layerEnvNameRefused(k) {
					return k, false, true
				}
				if layerEnvValueRE.MatchString(layerEnvValueText(env[k])) {
					return k, true, true
				}
			}
		}
		for _, k := range keys {
			if name, byValue, found = findLayerEnv(t[k]); found {
				return name, byValue, found
			}
		}
	case []any:
		for _, e := range t {
			if name, byValue, found = findLayerEnv(e); found {
				return name, byValue, found
			}
		}
	}
	return "", false, false
}

// layerEnvValueText is an env value as text: a string as it is, anything
// else as its JSON (jq's tostring).
func layerEnvValueText(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}
