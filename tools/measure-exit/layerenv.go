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

// claudeCredentialsFile is Claude Code's credentials file (its login) in
// the .claude directory under HOME.
const claudeCredentialsFile = ".credentials.json"

// layerRefusedFileNames are Claude Code's state file and credentials file:
// a layer that is, or resolves to, a file of either name is refused. The
// host runner's refuse_credential_layer (run.sh) refuses the same names;
// keep the two in step.
var layerRefusedFileNames = []string{claudeStateFile, claudeCredentialsFile}

// layerCredentialKeyParts make a layer key credential-like: a key at any
// depth whose upper-cased name contains one is refused, because its value
// may be a credential (an env entry, an MCP server's env or headers, a
// helper such as apiKeyHelper). The host runner's refuse_credential_layer
// case pattern (run.sh, *KEY* | *TOKEN* | ...) refuses the same parts; keep
// the two in step.
var layerCredentialKeyParts = []string{
	"KEY", "TOKEN", "SECRET", "PASSWORD", "PASSWD", "CREDENTIAL", "OAUTH", "AUTHORIZATION", "COOKIE",
}

// layerCredentialSettings are Claude Code keys refused by their whole name
// (case ignored, at any depth), because none holds a layerCredentialKeyParts
// part. Each one's value is a command whose output Claude Code uses as a
// credential, as request headers or as settings, and no check here can see
// that output:
//   - awsAuthRefresh prints refreshed AWS (Bedrock) credentials;
//   - gcpAuthRefresh prints a Google Cloud access token;
//   - otelHeadersHelper prints the OpenTelemetry request headers, typically
//     an auth header;
//   - policyHelper prints the managed settings, which can hold any
//     credential or env setting these checks refuse;
//   - headersHelper, in an MCP server's entry, prints that server's request
//     headers, typically an Authorization header.
//
// The first four come from Claude Code's settings reference
// (code.claude.com/docs/en/settings-reference), headersHelper from its MCP
// reference (code.claude.com/docs/en/mcp). The settings reference's other
// credential commands, apiKeyHelper and awsCredentialExport, are refused by
// their KEY and CREDENTIAL parts. The host runner's refuse_credential_layer
// (run.sh LAYER_CREDENTIAL_SETTINGS) refuses the same names; keep the two in
// step.
var layerCredentialSettings = []string{
	"awsAuthRefresh", "gcpAuthRefresh", "otelHeadersHelper", "policyHelper", "headersHelper",
}

// managedMCPPath is where Claude Code reads its managed MCP file on Linux.
const managedMCPPath = "/etc/claude-code/managed-mcp.json"

// managedSettingsDropInPath is where Claude Code reads its managed settings
// drop-in directory on Linux, beside managedSettingsPath.
const managedSettingsDropInPath = "/etc/claude-code/managed-settings.d"

// refusedFile is a file (or, when dir is set, a directory) at a path Claude
// Code reads that real mode refuses whenever anything is there, without
// reading it: kind and path name it in the refusal, and why says what it
// would carry past the gateway.
type refusedFile struct {
	kind, path, why string
	dir             bool
}

// what names a refused file in a refusal: its kind, then "file", or
// "directory" for one Claude Code reads as a directory.
func (f refusedFile) what() string {
	if f.dir {
		return f.kind + " directory"
	}
	return f.kind + " file"
}

// realModeRefusedFiles lists the files real mode refuses outright, not as
// layers (checkRefusedFiles), at the paths Claude Code reads them:
//   - $HOME/.claude/.credentials.json, Claude Code's login store on Linux.
//     Its whole content is a credential, so no check could pass it. The
//     agents read it under $HOME: the driver never passes CLAUDE_CONFIG_DIR
//     to a child (forbiddenEnvNames);
//   - /etc/claude-code/managed-mcp.json, the managed MCP file. It is refused
//     rather than checked as a layer because nothing legitimate puts it
//     there: run.sh has no flag to stage it and never mounts it, and the
//     deployment's MCP servers reach a run through -mcp-config, the mcp
//     layer. Claude Code's docs also say the file takes exclusive control of
//     MCP servers and that a session then refuses servers passed with
//     --mcp-config, so the MCP cases could not run as measured beside it;
//   - /etc/claude-code/managed-settings.d, the managed settings drop-in
//     directory. Claude Code merges every *.json file in it, after
//     managed-settings.json, into the managed settings, which override every
//     other settings level, so a drop-in's env (ANTHROPIC_BASE_URL, a
//     credential) or helper command (apiKeyHelper) would take the agents off
//     the gateway (code.claude.com/docs/en/managed-settings). It is refused
//     whatever it holds, even empty, for the managed MCP file's reason:
//     run.sh never mounts it and nothing legitimate puts it there, since the
//     deployment's managed settings reach a run as the managed layer
//     (managed-settings.json, which checkLayerFiles checks). The docs also
//     count a drop-in directory that exists but cannot be read as a present
//     managed source, so no content test could clear one.
func realModeRefusedFiles(home string) []refusedFile {
	return []refusedFile{
		{kind: "credentials", path: filepath.Join(home, ".claude", claudeCredentialsFile),
			why: "it holds a login that would carry the agents past the gateway"},
		{kind: "managed MCP", path: managedMCPPath,
			why: "it would take exclusive control of the agents' MCP servers (Claude Code then refuses the run's -mcp-config), and its servers' env, headers and headersHelper commands can carry credentials past the gateway; run.sh never mounts it"},
		{kind: "managed settings drop-in", path: managedSettingsDropInPath, dir: true,
			why: "Claude Code merges every *.json file in it into the managed settings, which override every other layer, so a drop-in's env (ANTHROPIC_BASE_URL, a credential) or helper command (apiKeyHelper) would carry the agents past the gateway unchecked; run.sh never mounts it"},
	}
}

// checkRefusedFiles refuses real mode when anything is at a refused file's
// path (os.Lstat finds it): a file, a directory or a link, dangling or not
// (a dangling link's target could be created later in the run). A path that
// cannot be checked (a directory it cannot search) is refused too. Nothing
// there is opened or listed, so the refusal names its path and never any
// content.
func checkRefusedFiles(files []refusedFile) error {
	for _, f := range files {
		_, err := os.Lstat(f.path)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			continue
		case err != nil:
			return refuse(ruleRealGatewayOnly,
				"cannot check for Claude Code's %s %s, so a credential there cannot be ruled out: %v", f.what(), f.path, err)
		}
		return refuse(ruleRealGatewayOnly,
			"Claude Code's %s %s is present (its content is never read or printed): %s; real mode carries only the gateway credential, from the environment, and refuses it outright", f.what(), f.path, f.why)
	}
	return nil
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
//   - any key at any depth is credential-like or a credential-producing
//     setting (findCredentialKey);
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
		if credentialSetting(key) {
			return refuse(ruleRealGatewayOnly,
				"the %s layer %s holds the credential-producing setting %s, a command whose output Claude Code would use as a credential, headers or settings that no check sees (values are never printed); real mode carries only the gateway credential, from the environment", f.kind, f.path, key)
		}
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

// credentialSetting reports whether a layer key is, case ignored, a
// layerCredentialSettings name.
func credentialSetting(key string) bool {
	upper := strings.ToUpper(key)
	for _, n := range layerCredentialSettings {
		if upper == strings.ToUpper(n) {
			return true
		}
	}
	return false
}

// findCredentialKey returns the first key, in sorted order, that is
// credential-like or a credential-producing setting, among the keys of
// every object in a decoded JSON document (at any depth, in arrays too):
// the key run.sh's check names.
func findCredentialKey(doc any) (key string, found bool) {
	keys := map[string]bool{}
	collectKeys(doc, keys)
	for k := range keys {
		if (credentialLikeKey(k) || credentialSetting(k)) && (!found || k < key) {
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
