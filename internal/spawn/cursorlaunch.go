package spawn

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"unicode/utf8"

	"github.com/gabemahoney/agent-director/internal/config"
)

// CursorArgs validates the same interpretation used by the private gateway.
func CursorArgs(args []string) (runtime []string, prompt string, err error) {
	runtime, prompt, _, err = CursorLaunchArgs(args)
	return
}

// CursorLaunchArgs extracts startup behavior from runtime argv. Explicit mode
// is configured through ACP, because the verified runtime ignores mode flags
// before acp. Model identifiers pass through without a catalog or substitution.
func CursorLaunchArgs(args []string) (runtime []string, prompt, mode string, err error) {
	for _, token := range args {
		if strings.IndexByte(token, 0) >= 0 || !utf8.ValidString(token) {
			return nil, "", "", fmt.Errorf("%w: invalid Cursor argument", ErrSpawnDeniedFlag)
		}
	}
	read := ReadCursorArgs(args)
	for _, o := range read.Options {
		fail := func(reason string) ([]string, string, string, error) {
			advice := "remove this argument and retry"
			if len(o.ValueIndices) > 0 {
				advice = "remove this option and its following value, then retry"
			}
			return nil, "", "", fmt.Errorf("%w: %s (%s); %s", ErrSpawnDeniedFlag, o.Name, reason, advice)
		}
		switch o.Canonical {
		case "--model", "--mode":
			if len(o.Values) != 1 || o.Values[0] == "" || strings.HasPrefix(o.Values[0], "-") {
				return fail("requires a value")
			}
			if o.Canonical == "--model" {
				runtime = append(runtime, "--model", o.Values[0])
				continue
			}
			if !CursorMode(o.Values[0]) {
				return fail("unsupported Cursor mode")
			}
			if mode != "" && mode != o.Values[0] {
				return fail("conflicting Cursor modes")
			}
			mode = o.Values[0]
		case "--plan":
			if len(o.Values) != 0 {
				return fail("does not take a value")
			}
			if mode != "" && mode != "plan" {
				return fail("conflicting Cursor modes")
			}
			mode = "plan"
		default:
			return fail("unsupported Cursor argument")
		}
	}
	if len(read.Positionals) > 1 || (read.DoubleDash >= 0 && (read.DoubleDash != len(args)-2 || len(read.Positionals) != 1 || read.Positionals[0] != read.DoubleDash+1)) {
		return nil, "", "", fmt.Errorf("%w: Cursor requires one initial prompt; supply it as one argument after --", ErrSpawnDeniedFlag)
	}
	if len(read.Positionals) == 1 {
		p := read.Positionals[0]
		if read.DoubleDash < 0 && (p != len(args)-1 || args[p] == "acp" || args[p] == "chat" || args[p] == "resume") {
			return nil, "", "", fmt.Errorf("%w: unsupported Cursor operand; use -- before a literal initial prompt", ErrSpawnDeniedFlag)
		}
		prompt = args[p]
	}
	return runtime, prompt, mode, nil
}

// CursorMode names the modes observed on the verified ACP candidate. Runtime
// availability is checked again against each new or loaded session's modes.
func CursorMode(mode string) bool { return mode == "agent" || mode == "plan" || mode == "ask" }

func RefuseCursorPermissions(allow, deny, ask []string) error {
	if len(allow)+len(deny)+len(ask) == 0 {
		return nil
	}
	return fmt.Errorf("%w: Cursor takes no allow, deny or ask entries; use Cursor's own permission rules", ErrPermissionsUnsupported)
}

// Library callers are not the gateway executable. Only the CLI can re-exec
// itself; other callers use the same canonical installed AD path as help hooks.
func installedGatewayBinary() (string, error) {
	if info, ok := debug.ReadBuildInfo(); ok && info.Path == "github.com/gabemahoney/agent-director/cmd/agent-director" {
		return executablePath()
	}
	path, err := helpHookBinPath()
	if err != nil {
		return "", err
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("installed Cursor gateway unavailable: %w", err)
	}
	st, err := os.Stat(path)
	if err != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0111 == 0 {
		return "", fmt.Errorf("installed Cursor gateway is not executable")
	}
	return path, nil
}

func composeCursorCommand(r Resolved, cfg config.Config) ([]string, error) {
	exe, err := installedGatewayBinary()
	if err != nil {
		return nil, fmt.Errorf("resolve agent-director path: %w", err)
	}
	program := cfg.Launch.CursorPath
	if program == "" {
		program = "agent"
	}
	args, prompt, mode, err := CursorLaunchArgs(r.AgentArgs)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(args)
	if err != nil {
		return nil, err
	}
	command := []string{exe, "cursor-host", "--runtime", program, "--runtime-args", string(encoded), "--initial-prompt", prompt}
	if mode != "" {
		command = append(command, "--requested-mode", mode)
	}
	return command, nil
}
