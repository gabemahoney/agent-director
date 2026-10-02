package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// projectLayerPath is where an agent working in cwd reads its project layer.
func projectLayerPath(cwd string) string {
	return filepath.Join(cwd, ".claude", "settings.json")
}

// prepareAgentDir makes one agent's fresh harness-owned working directory
// (sampleWorkDir) and puts its settings layers in it: a copy of the
// operator's staged project layer, then either the harness's generated
// layer (generated non-nil) or a copy of the staged local layer. The staged
// copies are only read. A generated layer beside a staged local layer is
// refused (lead decision 8; checkLocalLayer refuses it before a real run
// starts).
func (h *harness) prepareAgentDir(caseID string, index int, generated map[string]any) (string, error) {
	dir, err := h.sampleWorkDir(caseID, index)
	if err != nil {
		return "", err
	}
	if h.cfg.projectSettings != "" {
		if err := copyLayer(h.cfg.projectSettings, projectLayerPath(dir)); err != nil {
			return "", err
		}
	}
	switch {
	case generated != nil && h.cfg.localSettings != "":
		return "", fmt.Errorf("case %s: the generated layer cannot sit beside the deployment local layer %s", caseID, h.cfg.localSettings)
	case generated != nil:
		if err := writeGeneratedLayer(dir, generated); err != nil {
			return "", err
		}
	case h.cfg.localSettings != "":
		if err := copyLayer(h.cfg.localSettings, generatedLayerPath(dir)); err != nil {
			return "", err
		}
	}
	return dir, nil
}

// copyLayer copies a staged settings layer to dst (created exclusively,
// mode 0600, its directory 0700).
func copyLayer(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("staged layer: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// dryMCPConfig is the trivial MCP configuration a dry run attaches when the
// operator supplied none, so the MCP cases' path (--mcp-config, server
// names, the command check) runs end to end. Its one server's command is
// `true`, which the stub never starts.
const dryMCPConfig = `{"mcpServers": {"mx-dry-noop": {"command": "true", "args": []}}}`

// mcpForCases resolves the MCP configuration the MCP cases attach: the
// operator's staged copy, or in a dry run the trivial generated one. With
// neither (a real run without -mcp-config), notRun says why the MCP cases
// do not run, and they are reported as "not run" rows.
func (h *harness) mcpForCases() (path, notRun string, err error) {
	switch {
	case h.cfg.mcpConfig != "":
		return h.cfg.mcpConfig, "", nil
	case h.cfg.mode == modeDry:
		p := filepath.Join(h.iso.WorkDir, "dry-mcp.json")
		if _, statErr := os.Stat(p); statErr == nil {
			return p, "", nil
		}
		return p, "", os.WriteFile(p, []byte(dryMCPConfig+"\n"), 0o600)
	default:
		return "", "no MCP configuration supplied (-mcp-config): the deployment's MCP servers were not attached", nil
	}
}

// mcpMissingCommands lists, as notes, every stdio server of the MCP
// configuration whose command is not found in the container: such a server
// fails to start, which changes the measured exit (s5 review amendment).
// Server names and program base names only; never arguments or env.
func mcpMissingCommands(path, pathList string) ([]string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc struct {
		MCPServers map[string]struct {
			Command string `json:"command"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("%s: not valid MCP configuration JSON", path)
	}
	var notes []string
	for name, s := range doc.MCPServers {
		if s.Command == "" {
			continue // a remote (http/sse) server has no command
		}
		if _, err := lookPathIn(s.Command, pathList); err != nil || !programExists(s.Command) {
			notes = append(notes, fmt.Sprintf("MCP server %s: command %s not found in the container", name, programName(s.Command)))
		}
	}
	sort.Strings(notes)
	return notes, nil
}

// programExists reports whether an absolute or relative program path
// names an existing file; a bare name is left to lookPathIn.
func programExists(command string) bool {
	if !strings.Contains(command, "/") {
		return true
	}
	_, err := os.Stat(command)
	return err == nil
}

// hookProgramsMissing lists, as notes, every hook program of the given
// layers (any event) that is not found in the container: a failing hook,
// a SessionEnd one above all, changes the measured exit (gm note). It
// names the layer and the program's base name only.
func hookProgramsMissing(layers []settingsLayer, pathList string) []string {
	var notes []string
	seen := map[string]bool{}
	for _, l := range layers {
		doc, ok, err := readLayerAllEvents(l)
		if err != nil || !ok {
			continue
		}
		for event, groups := range doc.Hooks {
			for _, g := range groups {
				for _, hk := range g.Hooks {
					f := strings.Fields(hk.Command)
					if len(f) == 0 {
						continue
					}
					prog := strings.Trim(f[0], `"'`)
					if hk.Args != nil {
						prog = hk.Command // exec form: command is the whole program path
					}
					if _, err := lookPathIn(prog, pathList); err == nil && programExists(prog) {
						continue
					}
					note := fmt.Sprintf("%s layer: %s hook program %s not found in the container", l.kind, event, programName(prog))
					if !seen[note] {
						seen[note] = true
						notes = append(notes, note)
					}
				}
			}
		}
	}
	sort.Strings(notes)
	return notes
}

// allEventsDoc is a settings layer's hooks for every event.
type allEventsDoc struct {
	Hooks map[string][]struct {
		Hooks []struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"hooks"`
	} `json:"hooks"`
}

// readLayerAllEvents parses a layer's hooks; ok is false when its file
// does not exist.
func readLayerAllEvents(l settingsLayer) (allEventsDoc, bool, error) {
	var doc allEventsDoc
	if l.readErr != nil {
		return doc, false, l.readErr
	}
	data := l.inline
	if data == nil {
		b, err := os.ReadFile(l.path)
		if os.IsNotExist(err) {
			return doc, false, nil
		}
		if err != nil {
			return doc, false, err
		}
		data = b
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return doc, false, err
	}
	return doc, true, nil
}
