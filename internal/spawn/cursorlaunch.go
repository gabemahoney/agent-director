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

// CursorArgs deliberately supports the small fresh-launch subset. Everything
// else is refused before writing a row, rather than silently changing policy.
func CursorArgs(args []string) (runtime []string, prompt string, err error) {
	for _, token := range args {
		if strings.IndexByte(token, 0) >= 0 || !utf8.ValidString(token) {
			return nil, "", fmt.Errorf("%w: invalid Cursor argument", ErrSpawnDeniedFlag)
		}
	}
	for i := 0; i < len(args); i++ {
		token := args[i]
		if strings.IndexByte(token, 0) >= 0 || !utf8.ValidString(token) {
			return nil, "", fmt.Errorf("%w: invalid Cursor argument", ErrSpawnDeniedFlag)
		}
		key, value, equals := strings.Cut(token, "=")
		switch key {
		case "--model", "-m":
			if !equals {
				i++
				if i >= len(args) {
					return nil, "", fmt.Errorf("%w: %s requires a value", ErrSpawnDeniedFlag, key)
				}
				value = args[i]
			}
			if value == "" || strings.HasPrefix(value, "-") {
				return nil, "", fmt.Errorf("%w: %s requires a model", ErrSpawnDeniedFlag, key)
			}
			runtime = append(runtime, "--model", value)
		default:
			// A single positional initial prompt is submitted through ACP exactly
			// once. It must not become a runtime subcommand or operating-mode flag.
			if key == "--" && !equals && i+1 < len(args) && i+2 == len(args) {
				prompt = args[i+1]
				i++
				continue
			}
			if !strings.HasPrefix(token, "-") && len(args)-i == 1 && prompt == "" && token != "acp" && token != "resume" && token != "chat" {
				prompt = token
				continue
			}
			return nil, "", fmt.Errorf("%w: %s (unsupported Cursor argument)", ErrSpawnDeniedFlag, key)
		}
	}
	return runtime, prompt, nil
}

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
	args, prompt, err := CursorArgs(r.AgentArgs)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(args)
	if err != nil {
		return nil, err
	}
	return []string{exe, "cursor-host", "--runtime", program, "--runtime-args", string(encoded), "--initial-prompt", prompt}, nil
}
