package spawn

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/installversion"
	"github.com/gabemahoney/agent-director/internal/layout"
)

// hookEventName is the event-key string used in Claude Code's settings.
// Listed in stable order so the synthesized JSON is reproducible (Go's
// json.Marshal sorts keys alphabetically anyway; the slice exists so
// tests can iterate the canonical eleven events without hardcoding the list).
type hookEventName string

const (
	hookSessionStart       hookEventName = "SessionStart"
	hookUserPromptSubmit   hookEventName = "UserPromptSubmit"
	hookPreToolUse         hookEventName = "PreToolUse"
	hookPostToolUse        hookEventName = "PostToolUse"
	hookPostToolUseFailure hookEventName = "PostToolUseFailure"
	hookStop               hookEventName = "Stop"
	hookNotification       hookEventName = "Notification"
	hookSessionEnd         hookEventName = "SessionEnd"
	hookPermissionRequest  hookEventName = "PermissionRequest"
	hookSubagentStart      hookEventName = "SubagentStart"
	hookSubagentStop       hookEventName = "SubagentStop"
)

// hookEvents enumerates the eleven events agent-director registers on every
// Spawn (SRD §6.1). Two of them (PreToolUse, PermissionRequest) carry a
// `"matcher": "*"` field; the matcherFields set names those.
// PostToolUseFailure (b.146 rule 13) is Claude Code's PostToolUse for a tool
// that ran and failed: with PostToolUse it carries the tool_use_id that
// closes a fallen-back permission request whose tool ran, and it moves the
// row as PostToolUse does. SubagentStart and SubagentStop (the last two) are
// Claude Code's subagent lifecycle events; the hook records each subagent of the
// row's agent from them.
var hookEvents = []hookEventName{
	hookSessionStart, hookUserPromptSubmit, hookPreToolUse,
	hookPostToolUse, hookPostToolUseFailure, hookStop, hookNotification,
	hookSessionEnd, hookPermissionRequest, hookSubagentStart, hookSubagentStop,
}

// hookFileChanged is busy send's owned wake (SR-3.4): registered, with
// asyncRewake, only for a covered Claude Launch (Resolved.BusySendCovered),
// beside the eleven state-tracking events, never as a twelfth state report.
// Claude Code runs it in the background when a path the SessionStart hook
// returned in watchPaths changes; its exit 2 hands its stderr to the agent as
// a wake prompt.
const hookFileChanged hookEventName = "FileChanged"

// hookPostToolBatch is busy send's owned steer (SR-3.4): registered, as a
// synchronous hook, only for a covered Claude Launch (Resolved.BusySendCovered),
// beside the owned wake and never as a state report. Claude Code runs it once
// a batch of tool calls has finished; a parent's stdout may return one Steer
// Turn prompt as additionalContext.
const hookPostToolBatch hookEventName = "PostToolBatch"

var matcherFields = map[hookEventName]bool{
	hookPreToolUse:        true,
	hookPermissionRequest: true,
}

// sessionStartHookTimeoutSeconds is the inner `timeout` (seconds) on the
// SessionStart `agent-director hook` entry (SR-22.9, WD 2026-09-30c). 600 is
// Claude Code's current default command-hook timeout, stated explicitly so a
// change to that default cannot move Claude Code's kill boundary under
// internal/hook's sessionStartWaitCap (540 s after the hook begins waiting).
// The cap < timeout relation is not checked in code (internal/spawn does not
// import internal/hook); each value is pinned in its own package's tests.
//
// The relation bounds the hook's wait for its launch's identity write, not
// the hook: it does not guarantee that SessionStart ends itself before Claude
// Code kills it. This timeout runs from the hook's start, and the hook's
// store writes before and after the wait (up to four
// RecordSessionStartIdentity writes, two on each side) each can wait up to
// [store] busy_timeout_ms on a contended store. busy_timeout_ms is accepted
// up to math.MaxInt32 ms and is not capped against the 60 s between the cap
// and this timeout (b.c7f, b.146), so a long store wait can still get
// SessionStart killed before it writes its result or its no_pane_recorded,
// with no trail record; so can any other death of the hook. The row is then
// left pending although its agent runs, and find-missing reports it (b.kdf):
// within pending_grace_seconds plus one find-missing period, provided
// find-missing is scheduled, it writes liveness note unreported and leaves
// the row pending, for a caller that reads the pane to decide what to type
// (see sessionStartWaitCap in internal/hook).
const sessionStartHookTimeoutSeconds = 600

// synthesizeSettings builds the inline JSON passed to `claude --settings`.
// Returns the JSON string and any executable-capture or JSON-encoding error.
//
// Shape (SRD §6.1; SR-22.9):
//
//	{
//	  "hooks": {
//	    "<EventName>": [{"hooks":[{"type":"command","command":"<bin>","args":["hook"]}]}],
//	    ... (and "PreToolUse"/"PermissionRequest" carry matcher "*" on the
//	        outer entry AND an inner "timeout": <effective relay timeout>
//	        on the command object, sibling of "type"/"command"/"args";
//	        "SessionStart"'s agent-director hook entry carries an inner
//	        "timeout": 600 on its command object, never on the outer entry;
//	        "PermissionRequest"'s args are ["hook","--timeout","<effective
//	        relay timeout>"])
//	  },
//	  "permissions": { "allow": [...], "deny": [...], "ask": [...] }
//	}
//
// Every agent-director hook is registered in EXEC FORM (SR-22.9, "Exec-form
// hooks"): `command` is the program path and `args` its argument list, so
// Claude Code starts `<bin> hook` directly with no `sh` between them. The
// PermissionRequest entry's args are `["hook", "--timeout", "<N>"]`, N being
// the same effective relay timeout as its inner `timeout`: the relay hook
// counts its kill instant from its own start plus N (b.146 rule 4), so it
// never acks a verdict too late to write it. A
// shell-form entry ("command":"<bin> hook") would run under `sh -c`, and
// dash does not exec its last command, so the hook's parent would be that
// shell. In exec form the hook's getppid() is the Claude process itself —
// the row's recorded pane process — which is what the hook gate compares
// (SR-22.9). The README states the minimum Claude Code version (RN-9). A
// version that ignores `args` never runs `<bin> hook`: it runs `command`
// through /bin/sh with no verb (Claude Code 2.1.120 does), so no hook applies
// and the row stays pending until kill or find-missing (SR-18.12). Such a
// no-verb run with a hook payload on stdin prints no help text (none reaches
// the agent's context), exits 0 and writes ad.hook.ignored with reason
// no_exec_form (SR-22.9, SR-14; cmd/agent-director noVerbHookIgnored), so the
// trail says why.
//
// The inner `timeout` (seconds) is emitted on three agent-director hook
// entries only. The two relay entries (PermissionRequest, PreToolUse) carry
// the effective relay window via cfg.Relay.EffectiveTimeoutSeconds() — the
// same single source of truth the poll loop's deadline uses — so Claude
// Code's per-hook kill boundary and the poll loop's fail-closed deny move in
// lockstep (SR-1.2 / SR-1.3). A missing or 0 `relay.timeout_seconds` emits
// 86400 (never 0 or an omitted key); config.Load refuses a negative value and
// one above config.MaxRelayTimeoutSeconds, the largest per-hook `timeout`
// Claude Code honours (b.8q2). The SessionStart agent-director
// hook entry carries sessionStartHookTimeoutSeconds (600), which does not
// move with the relay settings: it keeps Claude Code's kill boundary above
// the internal/hook SessionStart wait cap (SR-22.9). The other eight events
// and the inject_help_hook SessionStart entry carry no timeout.
//
// `<bin>` is the absolute path to the currently-running agent-director
// binary (executablePath), written VERBATIM: in exec form `command` is a
// program path, not shell text, so it is never quoted (a quoted path would
// name a file that does not exist).
//
// The inject_help_hook SessionStart entry is unchanged and stays in shell
// form ("command":"<install path> help", through quoteIfWhitespace): it
// writes nothing, so its parent process does not matter.
//
// A covered Claude Launch (r.BusySendCovered; SR-3.4) also gets the owned
// FileChanged entry, in exec form with "asyncRewake": true and no timeout
// (hookFileChanged), and the owned PostToolBatch entry, in exec form with no
// asyncRewake, matcher or timeout (hookPostToolBatch); an uncovered Launch's
// settings are exactly as before.
// Other people's hooks and settings are never read, reordered or replaced.
//
// `statusLine` and `subagentStatusLine` carry the context-fullness callbacks
// composeContextStatuslines writes, unless cfg.Defaults.DisableContextFullness
// is true: then neither key is written and no Claude settings file is read for
// them, so Claude's own status lines stand and context stays unknown (b.kh7).
//
// The `permissions` block is included whenever the caller supplied any
// non-empty allow/deny/ask array OR when cfg.Defaults.DisableAskUser
// is true (which prepends "AskUserQuestion" to deny). When omitted, Claude
// Code's tier merge runs against the user/project settings only.
func synthesizeSettings(r Resolved, cfg config.Config) (string, error) {
	exe, err := executablePath()
	if err != nil {
		return "", fmt.Errorf("resolve agent-director path: %w", err)
	}

	// The two relay hook entries (PermissionRequest, PreToolUse) carry the
	// effective relay timeout on their inner command object so Claude Code's
	// per-hook kill boundary matches the poll loop's deadline (SR-1.2/1.3).
	// matcherFields names exactly those two events.
	relayTimeout := cfg.Relay.EffectiveTimeoutSeconds()

	hooks := map[string]any{}
	for _, evt := range hookEvents {
		// Exec form (SR-22.9): the path verbatim as the program, "hook" as
		// its first argument. The relay hook (PermissionRequest) is also
		// told its own timeout, the one this entry gives Claude Code, so it
		// knows its kill instant (b.146 rule 4).
		args := []string{"hook"}
		if evt == hookPermissionRequest {
			args = append(args, "--timeout", strconv.Itoa(relayTimeout))
		}
		command := map[string]any{"type": "command", "command": exe, "args": args}
		if matcherFields[evt] {
			command["timeout"] = relayTimeout
		}
		if evt == hookSessionStart {
			// Inner command object only; the help entry appended below
			// carries none (SR-22.9, WD 2026-09-30c).
			command["timeout"] = sessionStartHookTimeoutSeconds
		}
		entry := map[string]any{
			"hooks": []any{command},
		}
		if matcherFields[evt] {
			entry["matcher"] = "*"
		}
		hooks[string(evt)] = []any{entry}
	}
	if r.BusySendCovered {
		// SR-3.4: the owned wake, in exec form like every agent-director
		// hook, so its parent is the agent process the gate compares. It
		// carries no matcher and no timeout of its own.
		hooks[string(hookFileChanged)] = []any{map[string]any{"hooks": []any{
			map[string]any{"type": "command", "command": exe, "args": []string{"hook"}, "asyncRewake": true},
		}}}
		// The owned steer, synchronous so its stdout reaches the running
		// turn: exec form, no matcher and no timeout of its own.
		hooks[string(hookPostToolBatch)] = []any{map[string]any{"hooks": []any{
			map[string]any{"type": "command", "command": exe, "args": []string{"hook"}},
		}}}
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
	if !cfg.Defaults.DisableContextFullness {
		composeContextStatuslines(r, exe, top)
	}

	allow, deny, ask := mergePermissions(r.Permissions, cfg.Defaults.DisableAskUser)
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
// disable_ask_user is on, "AskUserQuestion" is prepended to the
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

// executablePath shares the startup helper's own-image capture rule: a ready
// immutable directory named for this binary's compiled stamp, or the resolved
// build path outside a layout. An unsafe in-layout lookup refuses generation;
// it never falls back to current, the permanent entry or an unresolved path.
// Each settings block passes this single captured value to all delivery and
// context hooks. Optional help deliberately keeps the permanent entry.
// Held as a var so tests can stub it without touching the actual filesystem.
var executablePath = installversion.CaptureExecutable

// helpHookBinPath returns the canonical install-tree path of the
// agent-director binary used in the inject_help_hook SessionStart
// entry. The hook fires inside a Spawn whose PATH may not include
// ~/.local/bin, so the entry must embed the absolute install path
// (~/.config/agent-director/bin/agent-director after ~ expansion) rather
// than rely on PATH resolution.
//
// Held as a var so tests can stub it without touching $HOME.
var helpHookBinPath = func() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return layout.Join(home, layout.BinDir, layout.BinaryName), nil
}

// quoteIfWhitespace defensively double-quotes a path that contains
// whitespace. It is used ONLY for the shell-form inject_help_hook entry,
// whose `command` is shell text; the exec-form agent-director hooks carry
// the path verbatim and are never quoted (SR-22.9). The install skill
// rejects whitespace install destinations (SRD §4.3) so this branch is
// unreachable in production; the quoting is here so a hand-edited install
// of the binary under (e.g.) "/Users/some name/bin/agent-director" cannot
// trigger a split-on-space bug in the shell Claude Code runs that entry
// under.
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
