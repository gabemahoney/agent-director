package codexfix

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// HookKind says what a registered handler is, by the shape of its command and
// matcher (not by who added it).
type HookKind string

const (
	// HookAD is `exec <quoted path> hook --agent <agent> [--timeout <n>]`.
	HookAD HookKind = "ad-hook"
	// HookHelp is `exec <quoted path> help`.
	HookHelp HookKind = "help"
	// HookDeny is a PreToolUse handler whose group matcher names
	// request_user_input_async (the Q1 deny group).
	HookDeny HookKind = "deny"
	// HookOther is any other handler.
	HookOther HookKind = "other"
)

// denyTool is the tool the Q1 deny group's matcher names.
const denyTool = "request_user_input_async"

// Hook is one handler of one `-c hooks.<Event>=[...]` value, with the
// positions Codex keys its trust state by (group and handler, 0-based).
type Hook struct {
	// Event is the event name as written in the key (SessionStart, ...).
	Event string
	// Group and Handler are the 0-based positions in the event's array and in
	// the group's hooks array.
	Group, Handler int
	// ArgIndex is the argv index of the "-c" element that carries the value;
	// AfterDashDash is whether it stands after the first "--".
	ArgIndex      int
	AfterDashDash bool

	// HasMatcher and Matcher are the group's matcher (an empty matcher is
	// still a matcher).
	HasMatcher bool
	Matcher    string
	// GroupKeys are the keys of the group table, sorted (matcher, hooks).
	GroupKeys []string

	Type    string
	Command string
	// HandlerKeys are the keys of the handler table, sorted (type, command,
	// timeout, async, additionalContextLimit, ...): assert on them to show
	// nothing stray was written.
	HandlerKeys []string
	// Timeout is the handler's timeout as given; HasTimeout is false when none
	// was given.
	Timeout    int
	HasTimeout bool
	// Limit is additionalContextLimit as given; HasLimit is false when none.
	Limit    int
	HasLimit bool

	Kind HookKind
	// Words is the command read back as a POSIX shell reads it. For HookAD
	// and HookHelp, Exec is the program path it names (the unquoted element
	// after `exec`).
	Words []string
	Exec  string
	// Agent is the `--agent` value, CmdTimeout the `--timeout` value (when
	// HasCmdTimeout), of a HookAD command.
	Agent         string
	CmdTimeout    int
	HasCmdTimeout bool
}

// decodeHooks decodes the value of `-c hooks.<event>=...` into its handlers.
func decodeHooks(event string, v any) ([]Hook, error) {
	groups, ok := asList(v)
	if !ok {
		return nil, fmt.Errorf("hooks.%s value is %T, want an array of group tables", event, v)
	}
	var out []Hook
	for gi, g := range groups {
		gm, ok := g.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("hooks.%s group %d is %T, want a table", event, gi, g)
		}
		matcher, hasMatcher := gm["matcher"].(string)
		handlers, ok := asList(gm["hooks"])
		if !ok {
			return nil, fmt.Errorf("hooks.%s group %d has no hooks array", event, gi)
		}
		for hi, h := range handlers {
			hm, ok := h.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("hooks.%s group %d handler %d is %T, want a table", event, gi, hi, h)
			}
			hk := Hook{
				Event: event, Group: gi, Handler: hi,
				HasMatcher: hasMatcher, Matcher: matcher, GroupKeys: sortedKeys(gm),
				HandlerKeys: sortedKeys(hm),
			}
			hk.Type, _ = hm["type"].(string)
			hk.Command, _ = hm["command"].(string)
			if n, ok := hm["timeout"].(int64); ok {
				hk.Timeout, hk.HasTimeout = int(n), true
			}
			if n, ok := hm["additionalContextLimit"].(int64); ok {
				hk.Limit, hk.HasLimit = int(n), true
			}
			hk.classify()
			out = append(out, hk)
		}
	}
	return out, nil
}

// classify sets Kind, Words and the command's own values.
func (h *Hook) classify() {
	h.Kind = HookOther
	words, err := ShellWords(h.Command)
	if err == nil {
		h.Words = words
	}
	switch {
	case err == nil && len(words) >= 3 && words[0] == "exec" && words[2] == "hook":
		h.Kind, h.Exec = HookAD, words[1]
		rest := words[3:]
		for i := 0; i < len(rest); i++ {
			switch w := rest[i]; {
			case w == "--agent" && i+1 < len(rest):
				i++
				h.Agent = rest[i]
			case strings.HasPrefix(w, "--agent="):
				h.Agent = strings.TrimPrefix(w, "--agent=")
			case w == "--timeout" && i+1 < len(rest):
				i++
				h.CmdTimeout, h.HasCmdTimeout = atoiOK(rest[i])
			case strings.HasPrefix(w, "--timeout="):
				h.CmdTimeout, h.HasCmdTimeout = atoiOK(strings.TrimPrefix(w, "--timeout="))
			}
		}
	case err == nil && len(words) == 3 && words[0] == "exec" && words[2] == "help":
		h.Kind, h.Exec = HookHelp, words[1]
	case h.Event == "PreToolUse" && h.HasMatcher && strings.Contains(h.Matcher, denyTool):
		h.Kind = HookDeny
	}
}

func atoiOK(s string) (int, bool) {
	n, err := strconv.Atoi(s)
	return n, err == nil
}

// asList reads a decoded TOML array (of tables or of anything).
func asList(v any) ([]any, bool) {
	switch x := v.(type) {
	case []any:
		return x, true
	case []map[string]any:
		out := make([]any, len(x))
		for i := range x {
			out[i] = x[i]
		}
		return out, true
	}
	return nil, false
}

func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
