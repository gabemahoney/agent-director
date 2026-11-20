package inputdelivery

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/gabemahoney/agent-director/internal/hostenv"
	"github.com/gabemahoney/agent-director/internal/inputjournal"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
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
func Prepare(id Identity, command []string, env map[string]string, safe bool, persistence ...Persistence) ([]string, map[string]string, error) {
	if len(command) == 0 {
		return nil, nil, fmt.Errorf("missing agent command")
	}
	// Unsupported and resumed launches keep their original input contract and
	// do not allocate native state or require a native launch identity.
	if !safe || (id.Agent != "claude" && id.Agent != "codex") || !inputjournal.ValidID(id.StoreID) || !inputjournal.ValidID(id.Token) {
		return command, env, nil
	}
	program, err := ResolveProgram(command[0], env)
	if err != nil {
		return command, env, nil
	}
	version := probeVersion(id.Agent, program, env)
	if version == "" {
		return command, env, nil
	}
	if len(persistence) != 1 || persistence[0] == nil {
		return nil, nil, fmt.Errorf("native launch persistence is required")
	}
	// Life comes from the exact pending token, never from a previous reused
	// row, a timestamp, or the caller's zero value.
	prepareCtx, prepareCancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer prepareCancel()
	id, err = persistence[0].ReadInputPreparation(prepareCtx, id)
	if err != nil {
		return nil, nil, err
	}
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
	keepEvidence := false
	defer func() {
		if !keepEvidence {
			_ = os.Remove(dir)
		}
	}()
	lockPath := dir + ".lock"
	if info, e := os.Lstat(lockPath); e == nil && !info.Mode().IsRegular() || e != nil && !os.IsNotExist(e) {
		return nil, nil, ErrInvalidState
	}
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, nil, err
	}
	defer lock.Close()
	if err = inputjournal.LockWriter(lock); err != nil {
		return nil, nil, err
	}
	m := manifest{Agent: id.Agent, CWD: id.CWD, Socket: id.Socket, Token: id.Token, InstanceID: id.InstanceID, Created: time.Now(), Safe: true, Version: version}
	envs := make(map[string]string, len(env)+1)
	for k, v := range env {
		envs[k] = v
	}
	cmd := append([]string(nil), command...)
	// Probe and launch use the same executable, even if tmux has another PATH.
	cmd[0] = program
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
	parent, err := os.Open(filepath.Dir(dir))
	if err != nil {
		return nil, nil, err
	}
	defer parent.Close()
	if err = parent.Sync(); err != nil {
		return nil, nil, err
	}
	// The command cannot start unless the capability intent is confirmed FULL.
	// Keep evidence on an unknown commit; only committed SQLite decides authority.
	keepEvidence = true
	if err = persistence[0].CreateInputLaunch(prepareCtx, Launch{Identity: id, Capability: m, EvidenceDir: dir, Revision: 1}); err != nil {
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

// ResolveProgram finds the executable a launch of program would run, so a
// helper run and the launch use the same one even if tmux has another PATH: a
// path is made absolute; a bare name is looked up on env's PATH, or this
// process's when env has none, in absolute directories only.
// Use ResolveProgram for program resolution shared by helpers and launches.
func ResolveProgram(program string, env map[string]string) (string, error) {
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

// ProbeOutput keeps at most Limit bytes of a probe's output; a write past the
// limit fails, and with it the probe. The buffer is a field, not embedded, so
// io.Copy cannot bypass Write through bytes.Buffer's ReadFrom.
// Use ProbeOutput for bounded helper output.
type ProbeOutput struct {
	Limit int
	buf   bytes.Buffer
	err   error
}

func (b *ProbeOutput) Write(p []byte) (int, error) {
	if b.err != nil {
		return 0, b.err
	}
	if len(p) > b.Limit-b.buf.Len() {
		b.err = fmt.Errorf("probe output exceeds limit")
		return 0, b.err
	}
	return b.buf.Write(p)
}

// Err reports an output-bound failure even when exec.Cmd.Wait prioritizes a
// nonzero exit over its output-copy error. Read it after RunProbe returns.
func (b *ProbeOutput) Err() error { return b.err }

// String returns the output kept so far.
func (b *ProbeOutput) String() string { return b.buf.String() }

// probeWaitDelay bounds the wait for a probe's output pipes after it ends or
// its context is done, as a launcher's descendants may keep them open.
const probeWaitDelay = 200 * time.Millisecond

// ProbeCommand builds a helper run of program with args, bounded by ctx, for
// RunProbe. A helper run is not the launched pane: a wrapper must not run
// hooks as either the caller or the new launch, and must not receive a host
// credential. So its environment is this process's, then env's entries, each
// without agent-director's identity keys, hostenv's credentials and the keys
// in excluded.
// Use ProbeCommand for filtered helper environments.
func ProbeCommand(ctx context.Context, program string, args []string, env map[string]string, excluded []string) *exec.Cmd {
	command := exec.CommandContext(ctx, program, args...)
	command.WaitDelay = probeWaitDelay
	command.Env = make([]string, 0, len(env))
	keep := func(key string) bool {
		return !probeIdentityKey(key) && !hostenv.IsCredential(key) && !slices.Contains(excluded, key)
	}
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if keep(key) {
			command.Env = append(command.Env, entry)
		}
	}
	for k, v := range env {
		if keep(k) {
			command.Env = append(command.Env, k+"="+v)
		}
	}
	return command
}

func probeVersion(agent, program string, env map[string]string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	version := ProbeCommand(ctx, program, []string{"--version"}, env, nil)
	output := ProbeOutput{Limit: 4096}
	version.Stdout = &output
	if runErr, cleanupErr := RunProbe(version); runErr == nil && cleanupErr == nil {
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
