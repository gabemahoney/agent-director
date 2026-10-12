package spawn

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
)

// This file builds the per-launch settings a Codex launch carries on its
// command line (SR-5.2, SR-5.3, SR-9, SR-10.1, RN-2, RN-4, RN-5, RN-9, Q1,
// Q4): agent-director's hook entries, the trust of each of them, the typing
// setting, the stdin-approval feature, the folder trust and the question-tool
// switch. Nothing here reads or writes a file: Codex reads the settings from
// its own argv, never from a Codex folder.
//
// The builder is pure. It is given the running binary's path, the installed
// path, the effective relay window, the settings of the config in force and
// the pre-trust outcome, and returns the arguments in the order they stand in
// argv (CodexSettingsArgs).

// hookInterrupt is Codex's Interrupt event, which has no Claude counterpart
// (hookEvents).
const hookInterrupt hookEventName = "Interrupt"

// codexHookEvents enumerates the eight events agent-director registers on
// every Codex launch, in the order their -c values stand in argv. It is
// separate from hookEvents (Claude's eleven) and registers no SubagentStart
// or SubagentStop (Epic 5), no Notification and no compaction event (RN-3).
var codexHookEvents = []hookEventName{
	hookSessionStart, hookUserPromptSubmit, hookPreToolUse, hookPostToolUse,
	hookPermissionRequest, hookStop, hookInterrupt, hookSessionEnd,
}

// codexEventLabels maps an event to the snake_case label Codex uses in a hook
// key and in the identity it hashes (FORMAT.md section 1).
var codexEventLabels = map[hookEventName]string{
	hookSessionStart:      "session_start",
	hookUserPromptSubmit:  "user_prompt_submit",
	hookPreToolUse:        "pre_tool_use",
	hookPostToolUse:       "post_tool_use",
	hookPermissionRequest: "permission_request",
	hookStop:              "stop",
	hookInterrupt:         "interrupt",
	hookSessionEnd:        "session_end",
}

const (
	// codexHelpContextLimit is the additionalContextLimit of the help entry
	// (RN-2): Codex's default of 2500 (counted in roughly tokens, about
	// ceil(bytes/4)) is smaller than agent-director's
	// help text. It is in the entry and in its hash.
	codexHelpContextLimit = 4000
	// codexDefaultContextLimit is Codex's own default; a limit of exactly this
	// value is left out of the hash.
	codexDefaultContextLimit = 2500
	// codexDefaultTimeout is the timeout Codex hashes for an entry that gives
	// none, 1 for SessionEnd and Interrupt (codexDefaultEndTimeout).
	codexDefaultTimeout = 600
	// codexDefaultEndTimeout is that default for SessionEnd and Interrupt.
	codexDefaultEndTimeout = 1
	// codexEndTimeout is the timeout agent-director passes on SessionEnd and
	// Interrupt, the largest Codex honours there (it clamps those to 1..3).
	codexEndTimeout = 3
	// codexDenyTool is the tool the Q1 deny group's matcher names: Codex's
	// question tool.
	codexDenyTool = "request_user_input_async"
	// codexHookStateSource is the literal source part of a hook key: the
	// synthetic config layer the -c flags form, angle brackets included.
	codexHookStateSource = "/<session-flags>/config.toml"
	// codexQuestionOffSetting turns Codex's question tool off at its source;
	// it stands last in the block while disable_ask_user is on.
	codexQuestionOffSetting = "tools.experimental_request_user_input.enabled=false"
)

// codexDenyCommand is the command of the Q1 deny group: a plain shell command
// (no agent-director binary) that reads its stdin to the end, writes its
// reason to stderr only and exits 2, which Codex reads as a block of the tool
// with the reason. POSIX only, so it behaves the same under sh, bash -c and
// zsh -c, and stdout is never written.
const codexDenyCommand = `cat >/dev/null; echo "agent-director: the question tool is off for this agent (disable_ask_user); put the question in your reply instead" >&2; exit 2`

// codexHandler is one command handler of a group. Timeout and ContextLimit are
// the values the entry passes; 0 means the entry passes none (agent-director
// passes neither as 0).
type codexHandler struct {
	Command      string
	Timeout      int
	ContextLimit int
}

// codexGroup is one matcher group of an event: an optional matcher and its
// handlers, in order.
type codexGroup struct {
	Matcher    string
	HasMatcher bool
	Handlers   []codexHandler
}

// codexEventEntry is one event's whole value, its groups in order.
type codexEventEntry struct {
	Event  hookEventName
	Groups []codexGroup
}

// codexSettingsInput is what CodexSettingsArgs builds a launch's settings from.
type codexSettingsInput struct {
	// Exe is the running binary's path (executablePath) and HelpBin the
	// installed path (helpHookBinPath), used only with InjectHelp.
	Exe     string
	HelpBin string
	// Relay is the effective relay window in seconds
	// (config.Relay.EffectiveTimeoutSeconds).
	Relay int
	// InjectHelp is [defaults] inject_help_hook; QuestionOff is
	// [defaults] disable_ask_user.
	InjectHelp  bool
	QuestionOff bool
	// CWD is the canonical folder to trust, used only with PreTrustOK.
	CWD string
	// PreTrustOK is whether the launch's pre-trust outcome is ok: only then
	// does the block carry the folder trust and the bypass of Codex's hook
	// review.
	PreTrustOK bool
	// OmitNoDaemon, OmitBypass and OmitWriteStdin drop an argument the caller
	// already passes before its first `--`: Codex rejects a repeated
	// --no-daemon or --dangerously-bypass-hook-trust, and a second --enable
	// write_stdin_approval is only noise.
	OmitNoDaemon   bool
	OmitBypass     bool
	OmitWriteStdin bool
}

// codexQuotePath quotes a path for the shell text of a hook command (SR-5.3):
// the path inside single quotes, each single quote written as quote, backslash,
// quote, quote (close the quotes, an escaped quote, reopen them) and each
// backslash as quote, two backslashes, quote (outside the quotes, two
// backslashes are one). It is the rule the golden hashes cover, and the shell
// that runs the hook command reads the path back unchanged.
func codexQuotePath(p string) string {
	var b strings.Builder
	b.WriteByte('\'')
	for _, ch := range p {
		switch ch {
		case '\'':
			b.WriteString(`'\''`)
		case '\\':
			b.WriteString(`'\\'`)
		default:
			b.WriteRune(ch)
		}
	}
	b.WriteByte('\'')
	return b.String()
}

// codexHookCommand is the command of agent-director's hook entry for ev:
// `exec <quoted exe> hook --agent codex`, the PermissionRequest entry adding
// `--timeout <relay>` (the window its own entry gives Codex, as for Claude).
func codexHookCommand(exe string, ev hookEventName, relay int) string {
	c := "exec " + codexQuotePath(exe) + " hook --agent codex"
	if ev == hookPermissionRequest {
		c += " --timeout " + strconv.Itoa(relay)
	}
	return c
}

// codexHelpCommand is the command of the help entry: `exec <quoted installed
// path> help`.
func codexHelpCommand(helpBin string) string {
	return "exec " + codexQuotePath(helpBin) + " help"
}

// codexTimeoutPassed is the timeout agent-director's own entry for ev passes
// (FORMAT.md section 2); 0 for none.
func codexTimeoutPassed(ev hookEventName, relay int) int {
	switch ev {
	case hookSessionStart:
		return sessionStartHookTimeoutSeconds
	case hookPreToolUse, hookPermissionRequest:
		return relay
	case hookSessionEnd, hookInterrupt:
		return codexEndTimeout
	}
	return 0
}

// codexEntries builds the eight events' groups for in, in codexHookEvents
// order. Every agent-director entry stands in group 0 of its event, the help
// entry (InjectHelp) in group 1 of SessionStart, the deny group (QuestionOff)
// in group 1 of PreToolUse. A repeated -c hooks.<Event> replaces the whole
// event's value, so each event is composed once, with every group in it.
func codexEntries(in codexSettingsInput) []codexEventEntry {
	out := make([]codexEventEntry, 0, len(codexHookEvents))
	for _, ev := range codexHookEvents {
		groups := []codexGroup{{Handlers: []codexHandler{{
			Command: codexHookCommand(in.Exe, ev, in.Relay),
			Timeout: codexTimeoutPassed(ev, in.Relay),
		}}}}
		if ev == hookSessionStart && in.InjectHelp {
			groups = append(groups, codexGroup{Handlers: []codexHandler{{
				Command:      codexHelpCommand(in.HelpBin),
				ContextLimit: codexHelpContextLimit,
			}}})
		}
		if ev == hookPreToolUse && in.QuestionOff {
			groups = append(groups, codexGroup{
				Matcher:    codexDenyTool,
				HasMatcher: true,
				Handlers:   []codexHandler{{Command: codexDenyCommand}},
			})
		}
		out = append(out, codexEventEntry{Event: ev, Groups: groups})
	}
	return out
}

// CodexSettingsArgs returns the arguments of a Codex launch's per-launch
// settings, in the order they stand in argv (SR-5.2):
//
//	--no-daemon
//	-c hooks.<Event>=[...]   one per event in codexHookEvents
//	-c hooks.state={"<key>"={trusted_hash="sha256:..."},...}
//	-c tui.disable_paste_burst=true
//	--enable write_stdin_approval
//	-c projects={"<cwd>"={trust_level="trusted"}}   only with PreTrustOK
//	--dangerously-bypass-hook-trust                 only with PreTrustOK
//	-c tools.experimental_request_user_input.enabled=false   only with QuestionOff, last
//
// less what in omits. The hooks.state value has one entry per handler of
// every event, at its real group and handler position, each hash as
// codexHandlerHash computes it: with those, Codex lists every entry trusted
// and the bypass is not needed for them; it is passed with the folder trust
// only on an ok pre-trust, as the launch's choice not to stop at Codex's
// review dialogs. The question-off setting stands last so nothing after it
// can turn the question tool back on. It does no I/O.
func CodexSettingsArgs(in codexSettingsInput) []string {
	var out []string
	if !in.OmitNoDaemon {
		out = append(out, codexOptNoDaemon)
	}
	var state []string
	entries := codexEntries(in)
	for _, e := range entries {
		out = append(out, "-c", "hooks."+string(e.Event)+"="+codexEventTOML(e))
		for gi, g := range e.Groups {
			for hi, h := range g.Handlers {
				key := codexEntryKey(e.Event, gi, hi)
				state = append(state, tomlQuote(key)+`={trusted_hash=`+tomlQuote(codexHandlerHash(e.Event, g, h))+`}`)
			}
		}
	}
	out = append(out, "-c", "hooks.state={"+strings.Join(state, ",")+"}")
	out = append(out, "-c", codexConfigKeyPasteBurst+"=true")
	if !in.OmitWriteStdin {
		out = append(out, codexOptEnable, codexFeatureWriteStdin)
	}
	if in.PreTrustOK {
		out = append(out, "-c", "projects={"+tomlQuote(in.CWD)+`={trust_level="trusted"}}`)
		if !in.OmitBypass {
			out = append(out, codexOptBypassHookTrust)
		}
	}
	if in.QuestionOff {
		out = append(out, "-c", codexQuestionOffSetting)
	}
	return out
}

// codexEventTOML is the TOML array value of one event: its groups, each a
// table with the matcher (when it has one) and its handlers.
func codexEventTOML(e codexEventEntry) string {
	groups := make([]string, 0, len(e.Groups))
	for _, g := range e.Groups {
		handlers := make([]string, 0, len(g.Handlers))
		for _, h := range g.Handlers {
			s := `{type="command",command=` + tomlQuote(h.Command)
			if h.Timeout != 0 {
				s += ",timeout=" + strconv.Itoa(h.Timeout)
			}
			if h.ContextLimit != 0 {
				s += ",additionalContextLimit=" + strconv.Itoa(h.ContextLimit)
			}
			handlers = append(handlers, s+"}")
		}
		t := "{"
		if g.HasMatcher {
			t += "matcher=" + tomlQuote(g.Matcher) + ","
		}
		groups = append(groups, t+"hooks=["+strings.Join(handlers, ",")+"]}")
	}
	return "[" + strings.Join(groups, ",") + "]"
}

// codexEntryKey is the key Codex lists an entry under and hooks.state names it
// by (FORMAT.md section 1): the literal source, the event's label, the group
// index and the handler index, both 0-based.
func codexEntryKey(ev hookEventName, group, handler int) string {
	return codexHookStateSource + ":" + codexEventLabels[ev] + ":" + strconv.Itoa(group) + ":" + strconv.Itoa(handler)
}

// codexHandlerHash is the hash Codex computes for one handler (RN-5,
// FORMAT.md section 1): "sha256:" and the lowercase hex SHA-256 of the
// handler's identity, serialised by codexIdentityJSON. The identity is
// normalised as Codex does:
//
//   - timeout is always present: the value the entry passes, else 600 (1 for
//     SessionEnd and Interrupt); SessionEnd and Interrupt are clamped into 1..3.
//   - matcher is hashed when the group has one and the event takes one; it is
//     dropped on UserPromptSubmit, Stop and Interrupt.
//   - additionalContextLimit is hashed when the entry passes one that is not
//     2500 and the event honours it (SessionStart, UserPromptSubmit,
//     PreToolUse, PostToolUse, SubagentStart).
//
// The hash does not depend on the group and handler indices, which are in the
// key only.
func codexHandlerHash(ev hookEventName, g codexGroup, h codexHandler) string {
	sum := sha256.Sum256(codexIdentityJSON(ev, g, h))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// codexIdentityJSON serialises the identity of one handler as Codex does:
// compact JSON with the keys sorted at every level, non-ASCII text as itself
// and no HTML escaping (Go's encoding/json escapes <, > and &, so the text is
// written by hand). Keys: event_name, hooks, matcher at the top; additionalContextLimit,
// async, command, timeout, type in the handler.
func codexIdentityJSON(ev hookEventName, g codexGroup, h codexHandler) []byte {
	var b strings.Builder
	b.WriteString(`{"event_name":`)
	jsonQuote(&b, codexEventLabels[ev])
	b.WriteString(`,"hooks":[{`)
	if h.ContextLimit != 0 && h.ContextLimit != codexDefaultContextLimit && codexHonoursContextLimit(ev) {
		b.WriteString(`"additionalContextLimit":` + strconv.Itoa(h.ContextLimit) + ",")
	}
	b.WriteString(`"async":false,"command":`)
	jsonQuote(&b, h.Command)
	b.WriteString(`,"timeout":` + strconv.Itoa(codexHashTimeout(ev, h.Timeout)) + `,"type":"command"}]`)
	if g.HasMatcher && codexTakesMatcher(ev) {
		b.WriteString(`,"matcher":`)
		jsonQuote(&b, g.Matcher)
	}
	b.WriteString("}")
	return []byte(b.String())
}

// codexHashTimeout is the timeout Codex hashes for an entry that passes
// passed (0 for none).
func codexHashTimeout(ev hookEventName, passed int) int {
	end := ev == hookSessionEnd || ev == hookInterrupt
	t := passed
	if t <= 0 {
		t = codexDefaultTimeout
		if end {
			t = codexDefaultEndTimeout
		}
	}
	if end {
		t = min(max(t, 1), codexEndTimeout)
	}
	return t
}

// codexTakesMatcher reports whether Codex hashes a matcher on ev: not on
// UserPromptSubmit, Stop and Interrupt, which ignore one.
func codexTakesMatcher(ev hookEventName) bool {
	switch ev {
	case hookUserPromptSubmit, hookStop, hookInterrupt:
		return false
	}
	return true
}

// codexHonoursContextLimit reports whether Codex honours (and so hashes) an
// additionalContextLimit on ev: SessionStart, UserPromptSubmit, PreToolUse,
// PostToolUse and SubagentStart.
func codexHonoursContextLimit(ev hookEventName) bool {
	switch ev {
	case hookSessionStart, hookUserPromptSubmit, hookPreToolUse, hookPostToolUse, hookSubagentStart:
		return true
	}
	return false
}

// jsonQuote writes s as a JSON string the way serde_json and Python's
// ensure_ascii=False do: quote, backslash, \b \f \n \r \t, other control
// characters below 0x20 as \u00xx, and every other character as itself.
func jsonQuote(b *strings.Builder, s string) {
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"':
			b.WriteString(`\"`)
		case r == '\\':
			b.WriteString(`\\`)
		case r == '\b':
			b.WriteString(`\b`)
		case r == '\f':
			b.WriteString(`\f`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r < 0x20:
			fmt.Fprintf(b, `\u%04x`, r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
}

// tomlQuote returns s as a TOML basic string that decodes back to exactly s:
// quote and backslash escaped, \b \t \n \f \r, every other control character
// (U+0000..U+001F and U+007F) as \u00XX, and every other character as itself.
func tomlQuote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"':
			b.WriteString(`\"`)
		case r == '\\':
			b.WriteString(`\\`)
		case r == '\b':
			b.WriteString(`\b`)
		case r == '\f':
			b.WriteString(`\f`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, `\u%04X`, r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}
