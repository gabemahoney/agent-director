package inputdelivery

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

// SafeArgs accepts only options whose normal, empty composer contract is known.
// Resumes, positional prompts, custom bindings and alternate modes fail closed.
func SafeArgs(args []string) bool {
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--model", "-m", "--permission-mode", "--effort":
			i++
			if i >= len(args) || strings.HasPrefix(args[i], "-") {
				return false
			}
		case "--no-alt-screen", "--dangerously-skip-permissions":
		default:
			return false
		}
	}
	return true
}

// Prepare provisions fresh native diagnostics for the exact launch. Unsupported
// binaries still launch and retain the established pane input contract.
func Prepare(id Identity, command []string, env map[string]string, safe bool) ([]string, map[string]string, error) {
	dir, err := launchDir(id.StoreID, id.Token)
	if err != nil {
		return nil, nil, err
	}
	if err = os.MkdirAll(filepath.Dir(dir), 0700); err != nil {
		return nil, nil, err
	}
	if err = os.Mkdir(dir, 0700); err != nil {
		return nil, nil, fmt.Errorf("allocate fresh input launch: %w", err)
	}
	m := manifest{Agent: id.Agent, CWD: id.CWD, Socket: id.Socket, Token: id.Token, InstanceID: id.InstanceID, Created: time.Now(), Safe: safe}
	envs := make(map[string]string, len(env)+1)
	for k, v := range env {
		envs[k] = v
	}
	cmd := append([]string(nil), command...)
	if len(cmd) == 0 {
		return nil, nil, fmt.Errorf("missing agent command")
	}
	if m.Safe {
		program, err := resolveProgram(cmd[0], envs)
		if err == nil {
			// The queried executable is also the one tmux will launch, even
			// when its server inherited a different PATH.
			cmd[0] = program
			m.Version = probeVersion(id.Agent, program, envs)
		}
	}
	if m.Safe && m.Version != "" {
		if id.Agent == "codex" {
			logDir, _ := json.Marshal(dir)
			cmd = append(cmd, "-c", "log_dir="+string(logDir), "-c", codexDefaultKeymap, "-c", "tui.vim_mode_default=false")
			m.Safe = codexManagedComposerSafe()
			m.KeymapPinned = true
			envs["RUST_LOG"] = "off,codex_tui=info"
		} else {
			m.SessionID = uuid.NewString()
			home, e := effectiveHome(envs)
			if e != nil {
				return nil, nil, e
			}
			configDir := envs["CLAUDE_CONFIG_DIR"]
			if _, explicit := envs["CLAUDE_CONFIG_DIR"]; !explicit {
				configDir = os.Getenv("CLAUDE_CONFIG_DIR")
			}
			if configDir == "" {
				configDir = filepath.Join(home, ".claude")
			}
			// tmux's server can have a different environment from this caller.
			// Its launch must use exactly the home and directory inspected below.
			envs["HOME"] = home
			envs["CLAUDE_CONFIG_DIR"] = configDir
			if !filepath.IsAbs(configDir) {
				m.Safe = false
			}
			if _, e := os.Stat(filepath.Join(configDir, "keybindings.json")); e == nil || !os.IsNotExist(e) {
				m.Safe = false
			}
			project := regexp.MustCompile(`[^A-Za-z0-9-]`).ReplaceAllString(id.CWD, "-")
			m.Transcript = filepath.Join(configDir, "projects", project, m.SessionID+".jsonl")
			cmd = append(cmd, "--session-id", m.SessionID, "--debug-file", filepath.Join(dir, "claude-debug.log"), "-d", "hooks")
		}
	}
	if err = writeJSON(dir, "launch.json", m); err != nil {
		return nil, nil, err
	}
	parent, err := os.Open(filepath.Dir(dir))
	if err != nil {
		return nil, nil, err
	}
	defer parent.Close()
	if err = parent.Sync(); err != nil {
		return nil, nil, err
	}
	return cmd, envs, nil
}

func effectiveHome(env map[string]string) (string, error) {
	if home, ok := env["HOME"]; ok {
		if !filepath.IsAbs(home) {
			return "", fmt.Errorf("launch HOME must be absolute")
		}
		return home, nil
	}
	return os.UserHomeDir()
}

func resolveProgram(program string, env map[string]string) (string, error) {
	if strings.ContainsRune(program, filepath.Separator) {
		return filepath.Abs(program)
	}
	path := os.Getenv("PATH")
	if p, ok := env["PATH"]; ok {
		path = p
	}
	for _, dir := range filepath.SplitList(path) {
		if !filepath.IsAbs(dir) {
			continue
		}
		candidate := filepath.Join(dir, program)
		st, err := os.Stat(candidate)
		if err == nil && st.Mode().IsRegular() && st.Mode().Perm()&0111 != 0 {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("agent executable cannot be resolved")
}

type versionOutput struct{ bytes.Buffer }

func (b *versionOutput) Write(p []byte) (int, error) {
	if len(p) > 4096-b.Len() {
		return 0, fmt.Errorf("version output exceeds limit")
	}
	return b.Buffer.Write(p)
}

func probeVersion(agent, program string, env map[string]string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	version := exec.CommandContext(ctx, program, "--version")
	version.WaitDelay = 200 * time.Millisecond
	// This is a capability probe, not the launched pane. A wrapper that ignores
	// --version must not run hooks as either the caller or the new launch.
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if !probeIdentityKey(key) {
			version.Env = append(version.Env, entry)
		}
	}
	for k, v := range env {
		if !probeIdentityKey(k) {
			version.Env = append(version.Env, k+"="+v)
		}
	}
	var output versionOutput
	version.Stdout = &output
	if version.Run() == nil {
		switch {
		case agent == "codex" && strings.TrimSpace(output.String()) == "codex-cli 0.159.2":
			return "0.159.2"
		case agent == "claude" && strings.TrimSpace(output.String()) == "2.1.280 (Claude Code)":
			return "2.1.280"
		}
	}
	return ""
}

func probeIdentityKey(key string) bool {
	return strings.HasPrefix(key, "AGENT_DIRECTOR_") || key == "TMUX" || key == "TMUX_PANE"
}

// Supports distinguishes launches with a native composer contract from panes
// that retain ordinary tmux input. It does not claim that the agent is ready.
func (s *nativeSession) Supports(id Identity) bool {
	m := s.manifest
	if !m.Safe || m.Version == "" || m.Token != id.Token || m.Socket != id.Socket || m.CWD != id.CWD || m.Agent != id.Agent || m.InstanceID != id.InstanceID {
		return false
	}
	if id.Agent == "codex" && (!m.KeymapPinned || !codexManagedComposerSafe()) {
		return false
	}
	return nativeTerminalSupported
}

func (s *nativeSession) Ready(id Identity) (bool, error) {
	if !s.Supports(id) {
		return false, fmt.Errorf("unsupported or non-fresh ordinary composer launch")
	}
	name, marker := "claude-debug.log", "Terminal capabilities:"
	if id.Agent == "codex" {
		name, marker = "codex-tui.log", "tui startup initial frame scheduled"
	}
	data, err := readBounded(filepath.Join(s.dir, name), 8<<20)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return strings.Contains(string(data), marker), nil
}
