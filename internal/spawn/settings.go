package spawn

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/gabemahoney/agent-director/internal/config"
)

// hookEventName is the event-key string used in Claude Code's settings.
// Listed in stable order so the synthesized JSON is reproducible (Go's
// json.Marshal sorts keys alphabetically anyway; the slice exists so
// tests can iterate the canonical 8 events without hardcoding the list).
type hookEventName string

const (
	hookSessionStart      hookEventName = "SessionStart"
	hookUserPromptSubmit  hookEventName = "UserPromptSubmit"
	hookPreToolUse        hookEventName = "PreToolUse"
	hookPostToolUse       hookEventName = "PostToolUse"
	hookStop              hookEventName = "Stop"
	hookNotification      hookEventName = "Notification"
	hookSessionEnd        hookEventName = "SessionEnd"
	hookPermissionRequest hookEventName = "PermissionRequest"
)

// hookEvents enumerates the 8 events agent-director registers on every
// Spawn (SRD §6.1). Two of them (PreToolUse, PermissionRequest) carry a
// `"matcher": "*"` field; the matcherFields set names those.
var hookEvents = []hookEventName{
	hookSessionStart, hookUserPromptSubmit, hookPreToolUse,
	hookPostToolUse, hookStop, hookNotification, hookSessionEnd,
	hookPermissionRequest,
}

var matcherFields = map[hookEventName]bool{
	hookPreToolUse:        true,
	hookPermissionRequest: true,
}

// synthesizeSettings builds the inline JSON passed to `claude --settings`.
// Returns the JSON string and any error from os.Executable / json encoding.
//
// Shape (SRD §6.1):
//
//	{
//	  "hooks": {
//	    "<EventName>": [{"hooks":[{"type":"command","command":"<bin> hook"}]}],
//	    ... (and "PreToolUse"/"PermissionRequest" carry matcher "*")
//	  },
//	  "permissions": { "allow": [...], "deny": [...], "ask": [...] }
//	}
//
// `<bin>` is the absolute path to the currently-running agent-director
// binary (os.Executable, then filepath.Abs as belt-and-braces). The path
// is rendered through strconv.Quote-style escaping defensively even
// though tmux's direct-argv delivery does not require shell-escaping —
// SRD §4.3 calls for that belt-and-suspenders treatment.
//
// The `permissions` block is included whenever the caller supplied any
// non-empty allow/deny/ask array OR when cfg.Defaults.DisableAskUserQuestion
// is true (which prepends "AskUserQuestion" to deny). When omitted, Claude
// Code's tier merge runs against the user/project settings only.
func synthesizeSettings(r Resolved, cfg config.Config) (string, error) {
	exe, err := executablePath()
	if err != nil {
		return "", fmt.Errorf("resolve agent-director path: %w", err)
	}
	exe = quoteIfWhitespace(exe)
	cmd := exe + " hook"

	hooks := map[string]any{}
	for _, evt := range hookEvents {
		entry := map[string]any{
			"hooks": []any{
				map[string]any{"type": "command", "command": cmd},
			},
		}
		if matcherFields[evt] {
			entry["matcher"] = "*"
		}
		hooks[string(evt)] = []any{entry}
	}

	if cfg.Defaults.InjectHelpHook {
		bin, err := helpHookBinPath()
		if err != nil {
			return "", fmt.Errorf("resolve help-hook binary path: %w", err)
		}
		bin = quoteIfWhitespace(bin)
		helpEntry := map[string]any{
			"hooks": []any{
				map[string]any{"type": "command", "command": bin + " help"},
			},
		}
		existing, _ := hooks[string(hookSessionStart)].([]any)
		hooks[string(hookSessionStart)] = append(existing, helpEntry)
	}

	top := map[string]any{"hooks": hooks}

	allow, deny, ask := mergePermissions(r.Permissions, cfg.Defaults.DisableAskUserQuestion)
	if len(allow) > 0 || len(deny) > 0 || len(ask) > 0 {
		perm := map[string]any{}
		if len(allow) > 0 {
			perm["allow"] = allow
		}
		if len(deny) > 0 {
			perm["deny"] = deny
		}
		if len(ask) > 0 {
			perm["ask"] = ask
		}
		top["permissions"] = perm
	}

	out, err := json.Marshal(top)
	if err != nil {
		return "", fmt.Errorf("marshal settings: %w", err)
	}
	return string(out), nil
}

// mergePermissions assembles the final allow/deny/ask slices. When
// disable_askuserquestion is on, "AskUserQuestion" is prepended to the
// deny list per SRD §11 (additive over user / project tiers, which Claude
// Code merges itself).
//
// The function preserves caller order within each slice; the prepend on
// deny is the only mutation. Callers passing nil for Permissions still
// get the auto-deny when the config flag is set.
func mergePermissions(p *Permissions, disableAUQ bool) (allow, deny, ask []string) {
	if p != nil {
		allow = append(allow, p.Allow...)
		deny = append(deny, p.Deny...)
		ask = append(ask, p.Ask...)
	}
	if disableAUQ {
		// Prepend so the auto-deny appears first in the final array — a
		// readability convention only; Claude Code's matcher doesn't care
		// about order within a single tier.
		deny = append([]string{"AskUserQuestion"}, deny...)
	}
	return allow, deny, ask
}

// executablePath returns the absolute, symlink-resolved path to the
// currently-running binary. The result is written verbatim into every
// hook command this spawn-side pipeline produces (SR-1.8, b.ue3).
//
// Mechanism (SR-1.8):
//
//	os.Executable()  → kernel-reported path (Linux's /proc/self/exe,
//	                   macOS's _NSGetExecutablePath); already absolute on
//	                   both supported platforms.
//	filepath.EvalSymlinks(...) → chases any symlinks in the path so the
//	                   hook command points at the real on-disk file.
//
// Stability assumption (load-bearing per SR-1.8): install.sh writes the
// AD CLI binary to ~/.agent-director/bin/agent-director and re-runs of
// install.sh overwrite the file at the same absolute path — the
// directory entry's identity is stable across re-installs even though
// the file's inode may change.  Hook commands captured into a spawned
// Claude session therefore continue to invoke the same path after an
// in-place upgrade.  If install.sh is ever changed to write to a
// per-version path (e.g. ~/.agent-director/bin/agent-director-0.7.0),
// the spawn-side hook contract breaks and this site plus the
// architecture doc need synchronized updates.
//
// Held as a var so tests can stub it without touching the actual filesystem.
var executablePath = func() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	abs, err := filepath.Abs(exe)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		// If the binary is gone between exec and resolve, fall back to
		// the absolute non-resolved path — better than failing the spawn.
		return abs, nil
	}
	return resolved, nil
}

// helpHookBinPath returns the canonical install-tree path of the
// agent-director binary used in the inject_help_hook SessionStart
// entry. The hook fires inside a Spawn whose PATH may not include
// ~/.local/bin, so the entry must embed the absolute install path
// (~/.agent-director/bin/agent-director after ~ expansion) rather
// than rely on PATH resolution.
//
// Held as a var so tests can stub it without touching $HOME.
var helpHookBinPath = func() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".agent-director", "bin", "agent-director"), nil
}

// quoteIfWhitespace defensively double-quotes a path that contains
// whitespace. The install skill rejects whitespace install destinations
// (SRD §4.3) so this branch is unreachable in production; the quoting
// is here so a hand-edited install of the binary under (e.g.)
// "/Users/some name/bin/agent-director" cannot trigger a split-on-space
// bug if the synthesized JSON ever flows through a shell-quoting
// downstream (which it currently doesn't — tmux delivers direct argv).
func quoteIfWhitespace(p string) string {
	if !strings.ContainsAny(p, " \t\n") {
		return p
	}
	// Wrap in double quotes and escape any internal quotes / backslashes.
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range p {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}
