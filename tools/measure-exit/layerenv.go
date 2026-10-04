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

// layerRefusedFileNames are Claude Code's state file and credentials file:
// a layer that is, or resolves to, a file of either name is refused. The
// host runner's refuse_credential_layer (run.sh) refuses the same names;
// keep the two in step.
var layerRefusedFileNames = []string{claudeStateFile, ".credentials.json"}

// layerCredentialKeyParts make a layer key credential-like: a key at any
// depth whose upper-cased name contains one is refused, because its value
// may be a credential (an env entry, an MCP server's env or headers, a
// helper such as apiKeyHelper). The host runner's refuse_credential_layer
// case pattern (run.sh, *KEY* | *TOKEN* | ...) refuses the same parts; keep
// the two in step.
var layerCredentialKeyParts = []string{
	"KEY", "TOKEN", "SECRET", "PASSWORD", "PASSWD", "CREDENTIAL", "OAUTH", "AUTHORIZATION", "COOKIE",
}

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

// checkLayerFiles refuses real mode when a layer file carries a credential
// or would take the agents off the gateway. It is the host runner's
// refuse_credential_layer, repeated here so a container started by hand is
// held to the same rule: checkLayerFile runs that function's checks, in the
// same order, on each file in turn, and the first refusal stops the run.
func checkLayerFiles(files []layerFile) error {
	for _, f := range files {
		if err := checkLayerFile(f); err != nil {
			return err
		}
	}
	return nil
}

// checkLayerFile refuses one layer file, in this order, when:
//   - its path as given is named after a layerRefusedFileNames file, even
//     when nothing is there yet: the driver seeds its own .claude.json
//     after the preflight;
//   - something is at its path but does not resolve: a dangling symlink
//     (or a chain ending in one). Its target cannot be checked, and may be
//     created later in the run (seedClaudeState creates .claude.json), so
//     any dangling link is refused, whatever its target's name. run.sh's
//     report_layers never stages a dangling source (it reports it MISSING),
//     so this refuses nothing a run started by run.sh would load;
//   - it does not resolve for any other reason (a symlink loop, a denied
//     directory), so it cannot be checked;
//   - the path it resolves to through symlinks is named after a
//     layerRefusedFileNames file;
//   - it cannot be read, or is not JSON, so it cannot be checked;
//   - any key at any depth is credential-like (findCredentialKey);
//   - an env object at any depth (the settings env, an MCP server's env)
//     sets a name layerEnvNameRefused refuses, or sets any name to a value
//     layerEnvValueRE matches (findLayerEnv).
//
// Otherwise a path with nothing at all there (os.Lstat finds nothing) is no
// layer. The refusal names the layer, its path and the key, never a value.
func checkLayerFile(f layerFile) error {
	if err := refuseLayerFileName(f, f.path); err != nil {
		return err
	}
	if _, err := os.Lstat(f.path); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	resolved, err := filepath.EvalSymlinks(f.path)
	if errors.Is(err, fs.ErrNotExist) {
		return refuse(ruleRealGatewayOnly,
			"the %s layer %s is a link that does not resolve, so its target cannot be checked for credentials or env settings that leave the gateway (it may be created later in the run); real mode never loads a dangling layer", f.kind, f.path)
	}
	if err != nil {
		return refuse(ruleRealGatewayOnly, "cannot resolve the %s layer %s, so it cannot be checked for credentials or env settings that leave the gateway: %v", f.kind, f.path, err)
	}
	if err := refuseLayerFileName(f, resolved); err != nil {
		return err
	}
	b, err := os.ReadFile(f.path)
	if err != nil {
		return refuse(ruleRealGatewayOnly, "cannot read the %s layer %s, so it cannot be checked for credentials or env settings that leave the gateway: %v", f.kind, f.path, err)
	}
	var doc any
	if err := json.Unmarshal(b, &doc); err != nil {
		return refuse(ruleRealGatewayOnly, "the %s layer %s is not a JSON document, so it cannot be checked for credentials or env settings that leave the gateway", f.kind, f.path)
	}
	if key, found := findCredentialKey(doc); found {
		return refuse(ruleRealGatewayOnly,
			"the %s layer %s holds the credential-like key %s (values are never printed); real mode carries only the gateway credential, from the environment", f.kind, f.path, key)
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
	return nil
}

// refuseLayerFileName refuses layer f when path (f's own path, or the path
// it resolves to) is named after a layerRefusedFileNames file.
func refuseLayerFileName(f layerFile, path string) error {
	base := filepath.Base(path)
	for _, n := range layerRefusedFileNames {
		if base == n {
			return refuse(ruleRealGatewayOnly,
				"the %s layer %s is (or links to) Claude Code's %s, which holds credentials or account state; real mode never loads it as a layer", f.kind, f.path, n)
		}
	}
	return nil
}

// credentialLikeKey reports whether a layer key's upper-cased name contains
// a layerCredentialKeyParts part.
func credentialLikeKey(key string) bool {
	upper := strings.ToUpper(key)
	for _, p := range layerCredentialKeyParts {
		if strings.Contains(upper, p) {
			return true
		}
	}
	return false
}

// findCredentialKey returns the first credential-like key, in sorted
// order, among the keys of every object in a decoded JSON document (at any
// depth, in arrays too): the key run.sh's check names.
func findCredentialKey(doc any) (key string, found bool) {
	keys := map[string]bool{}
	collectKeys(doc, keys)
	for k := range keys {
		if credentialLikeKey(k) && (!found || k < key) {
			key, found = k, true
		}
	}
	return key, found
}

// collectKeys adds the keys of every object in v, at any depth, to keys.
func collectKeys(v any, keys map[string]bool) {
	switch t := v.(type) {
	case map[string]any:
		for k, e := range t {
			keys[k] = true
			collectKeys(e, keys)
		}
	case []any:
		for _, e := range t {
			collectKeys(e, keys)
		}
	}
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
