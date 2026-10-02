package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// layerKind names a Claude Code settings layer the agents load.
type layerKind string

const (
	layerManaged layerKind = "managed"
	// layerAgentDirector is agent-director's own synthesized layer, passed
	// to claude as --settings <json>; it is not on disk.
	layerAgentDirector layerKind = "agent-director --settings"
	layerLocal         layerKind = "local"
	layerProject       layerKind = "project"
	layerUser          layerKind = "user"
	// layerGenerated is the harness's own .claude/settings.local.json in a
	// per-case working directory (the raised-budget hook, the RN-9
	// recorder). It occupies the local layer's file.
	layerGenerated layerKind = "generated (local)"
)

// managedSettingsPath is where Claude Code reads managed settings on Linux.
const managedSettingsPath = "/etc/claude-code/managed-settings.json"

// procRoot is the proc file system agent-director's layer is read from.
const procRoot = "/proc"

// settingsLayer is one layer to read: from a file (path) or, for
// agent-director's layer, from inline JSON. A missing file is no layer;
// readErr, when set, is why an inline layer could not be obtained.
type settingsLayer struct {
	kind    layerKind
	path    string
	inline  []byte
	readErr error
}

// agentLayers lists the layers an agent working in cwd loads, highest
// precedence first (Claude Code's order: managed, command-line --settings,
// local, project, user). ad is agent-director's layer (agentDirectorLayer).
// generated tells whether the local layer is the harness's own file.
func agentLayers(home, cwd string, generated bool, ad settingsLayer) []settingsLayer {
	local := layerLocal
	if generated {
		local = layerGenerated
	}
	return []settingsLayer{
		{kind: layerManaged, path: managedSettingsPath},
		ad,
		{kind: local, path: filepath.Join(cwd, ".claude", "settings.local.json")},
		{kind: layerProject, path: filepath.Join(cwd, ".claude", "settings.json")},
		{kind: layerUser, path: filepath.Join(home, ".claude", "settings.json")},
	}
}

// hookBudget is one SessionEnd hook in force: its layer and the program it
// runs (the first word of command, as a base name: arguments can carry
// secrets and are never reported), and its per-hook timeout in seconds, or
// "default" (Claude Code's own budget, which the harness never hard-codes).
type hookBudget struct {
	Layer   layerKind `json:"layer"`
	Path    string    `json:"path,omitempty"`
	Program string    `json:"program"`
	Timeout string    `json:"timeout"`
}

// envBudget is one place CLAUDE_CODE_SESSIONEND_HOOKS_TIMEOUT_MS is set.
type envBudget struct {
	Source string `json:"source"`
	Value  string `json:"value"`
}

// budgetReport is the SessionEnd budgets in force for one case.
type budgetReport struct {
	SessionEndHooks []hookBudget `json:"session_end_hooks"`
	// EnvSources lists every place the budget variable is set; Effective is
	// the value the agent sees under the precedence below, or "unset".
	EnvSources   []envBudget `json:"env_sources"`
	EnvEffective string      `json:"env_effective"`
	// EnvKeys lists, by layer, the key names (never values) of each layer's
	// env block.
	EnvKeys map[string][]string `json:"env_keys,omitempty"`
	// Warnings name layers that could not be read or parsed. A broken layer
	// is never reported as "default".
	Warnings []string `json:"warnings,omitempty"`
	// ClaudeCode is the version whose default budget "default" means.
	ClaudeCode string `json:"claude_code_version"`
}

// settingsDoc is the part of a settings layer the capture reads.
type settingsDoc struct {
	Env   map[string]any `json:"env"`
	Hooks map[string][]struct {
		Hooks []struct {
			Command string          `json:"command"`
			Timeout json.RawMessage `json:"timeout"`
		} `json:"hooks"`
	} `json:"hooks"`
}

// captureBudgets reports the SessionEnd budgets one agent runs under: every
// SessionEnd hook of every layer, and every source of
// CLAUDE_CODE_SESSIONEND_HOOKS_TIMEOUT_MS. layers come highest precedence
// first; processEnv is the agents' process environment as the driver passes
// it (container environment plus the spawn's extra environment, whose own
// entries are spawnEnv).
//
// Precedence of the variable, highest first: a settings layer's env block
// (in layer order), then the spawn's extra environment, then the container
// environment.
func captureBudgets(layers []settingsLayer, containerEnv, spawnEnv map[string]string, claudeCode string) budgetReport {
	r := budgetReport{ClaudeCode: claudeCode, SessionEndHooks: []hookBudget{}, EnvSources: []envBudget{}}
	for _, l := range layers {
		doc, ok, err := readLayer(l)
		if err != nil {
			r.Warnings = append(r.Warnings, fmt.Sprintf("%s layer %s: %v", l.kind, l.path, err))
			continue
		}
		if !ok {
			continue
		}
		r.addLayer(l, doc)
	}
	if v, ok := spawnEnv[sessionEndBudgetEnv]; ok {
		r.EnvSources = append(r.EnvSources, envBudget{Source: "spawn --extra-env", Value: v})
	}
	if v, ok := containerEnv[sessionEndBudgetEnv]; ok {
		r.EnvSources = append(r.EnvSources, envBudget{Source: "container environment", Value: v})
	}
	r.EnvEffective = "unset"
	if len(r.EnvSources) > 0 {
		r.EnvEffective = r.EnvSources[0].Value + " (" + r.EnvSources[0].Source + ")"
	}
	return r
}

// addLayer records one parsed layer's SessionEnd hooks and env block.
func (r *budgetReport) addLayer(l settingsLayer, doc settingsDoc) {
	for _, group := range doc.Hooks["SessionEnd"] {
		for _, hk := range group.Hooks {
			r.SessionEndHooks = append(r.SessionEndHooks, hookBudget{
				Layer: l.kind, Path: l.path, Program: programName(hk.Command), Timeout: timeoutText(hk.Timeout),
			})
		}
	}
	if len(doc.Env) == 0 {
		return
	}
	keys := make([]string, 0, len(doc.Env))
	for k := range doc.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if r.EnvKeys == nil {
		r.EnvKeys = map[string][]string{}
	}
	r.EnvKeys[string(l.kind)] = keys
	if v, ok := doc.Env[sessionEndBudgetEnv]; ok {
		r.EnvSources = append(r.EnvSources, envBudget{Source: string(l.kind) + " layer env", Value: fmt.Sprint(v)})
	}
}

// readLayer parses one layer; ok is false when its file does not exist.
func readLayer(l settingsLayer) (settingsDoc, bool, error) {
	var doc settingsDoc
	if l.readErr != nil {
		return doc, false, l.readErr
	}
	data := l.inline
	if data == nil {
		b, err := os.ReadFile(l.path)
		if errors.Is(err, os.ErrNotExist) {
			return doc, false, nil
		}
		if err != nil {
			return doc, false, err
		}
		data = b
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return doc, false, fmt.Errorf("not valid settings JSON: %w", err)
	}
	return doc, true, nil
}

// timeoutText renders a hook's timeout: "default" when absent, else the
// number of seconds as written.
func timeoutText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return "default"
	}
	if _, err := strconv.ParseFloat(string(raw), 64); err != nil {
		return "invalid (" + string(raw) + ")"
	}
	return string(raw) + " s"
}

// programName is the base name of a hook command's first word.
func programName(command string) string {
	f := strings.Fields(command)
	if len(f) == 0 {
		return ""
	}
	return filepath.Base(strings.Trim(f[0], `"'`))
}

// settingsFromCmdline reads agent-director's synthesized layer from the
// agent process's argv (/proc/<pid>/cmdline): the value after --settings, or
// of --settings=. It is the layer agent-director passes on every launch.
func settingsFromCmdline(procRoot string, pid int) ([]byte, error) {
	b, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "cmdline"))
	if err != nil {
		return nil, err
	}
	args := bytes.Split(bytes.TrimRight(b, "\x00"), []byte{0})
	for i, a := range args {
		if string(a) == "--settings" && i+1 < len(args) {
			return args[i+1], nil
		}
		if v, ok := bytes.CutPrefix(a, []byte("--settings=")); ok {
			return v, nil
		}
	}
	return nil, fmt.Errorf("pid %d has no --settings argument", pid)
}

// agentDirectorLayer is agent-director's layer for the agent process pid,
// read from its argv. When it cannot be read, captureBudgets reports the
// layer as a warning, never as "default". Read it while the agent runs,
// before the measured action.
func agentDirectorLayer(procRoot string, pid int) settingsLayer {
	b, err := settingsFromCmdline(procRoot, pid)
	return settingsLayer{kind: layerAgentDirector, path: "pid " + strconv.Itoa(pid), inline: b, readErr: err}
}

// mcpServerNames lists the server names of an MCP configuration, never its
// content (commands, arguments and env blocks can carry secrets).
func mcpServerNames(path string) ([]string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("%s: not valid MCP configuration JSON", path)
	}
	names := make([]string, 0, len(doc.MCPServers))
	for n := range doc.MCPServers {
		names = append(names, n)
	}
	sort.Strings(names)
	return names, nil
}
