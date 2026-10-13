package spawn

import (
	"fmt"
	"strings"
)

// This file is the one reader of a Codex pass-through argument list (SR-2.3,
// RN-13) and the check that refuses the arguments agent-director must own.
// Spawn's validation (CheckCodexArgs) and the Codex launch composition (the
// placement of agent-director's own settings and the duplicate checks,
// codexlaunch.go) both read arguments through ReadCodexArgs, so they agree on
// what is an option, a value, a prompt word and the first `--`.

// Codex's option names that agent-director names or compares. Long names are
// written with their dashes.
const (
	codexOptConfig                = "--config"
	codexOptEnable                = "--enable"
	codexOptDisable               = "--disable"
	codexOptRemote                = "--remote"
	codexOptRemoteAuthTokenEnv    = "--remote-auth-token-env"
	codexOptImage                 = "--image"
	codexOptNoDaemon              = "--no-daemon"
	codexOptBypassHookTrust       = "--dangerously-bypass-hook-trust"
	codexFeatureHooks             = "hooks"
	codexFeatureWriteStdin        = "write_stdin_approval"
	codexFeatureQuestionTool      = "default_mode_request_user_input"
	codexConfigKeyHooks           = "hooks"
	codexConfigKeyPasteBurst      = "tui.disable_paste_burst"
	codexConfigKeyFeaturesHooks   = "features.hooks"
	codexConfigKeyQuestionFeature = "features.default_mode_request_user_input"
	codexConfigKeyQuestionTool    = "tools.experimental_request_user_input"
)

// codexValueLong is the constant list of Codex's long options that take a
// value (SR-2.3, actual parser verified at 0.156.0 and 0.162.1), each with the long name it
// stands for. codexValueShort is the same for the short flags. AD reads no
// Codex help at run time; the pre-release check compares both with the parser
// of the newest and the minimum Codex (SR-16).
var codexValueLong = map[string]bool{
	"--config":                true,
	"--enable":                true,
	"--disable":               true,
	"--remote":                true,
	"--remote-auth-token-env": true,
	"--image":                 true,
	"--model":                 true,
	"--local-provider":        true,
	"--profile":               true,
	"--sandbox":               true,
	"--cd":                    true,
	"--add-dir":               true,
	"--ask-for-approval":      true,
}

var codexValueShort = map[byte]string{
	'c': "--config",
	'i': "--image",
	'm': "--model",
	'p': "--profile",
	's': "--sandbox",
	'C': "--cd",
	'a': "--ask-for-approval",
}

// Verified flattened TUI flags; these are valueless. Selector validity depends
// on command position, handled by CodexResumeArgs extraction.
var codexFlagLong = map[string]bool{
	"--help": true, "--version": true, "--oss": true, "--strict-config": true,
	"--approve-for-me": true, "--not-so-yolo": true,
	"--dangerously-bypass-approvals-and-sandbox": true, "--yolo": true,
	"--worktree": true, "--search": true,
	"--no-alt-screen": true, "--no-daemon": true,
	"--dangerously-bypass-hook-trust": true, "--last": true, "--all": true,
	"--include-non-interactive": true,
}

// codexSubcommands is the constant list of Codex's subcommands and their
// aliases (SR-2.3 as amended by RN-13: mcp-server is not one; tcp-tunnel,
// execpolicy, responses-api-proxy, stdio-to-uds, cloud-tasks and app are).
// Every one is refused in command position: each picks or resumes another
// session, runs non-interactively or attaches to a remote or shared service.
var codexSubcommands = map[string]bool{
	"agents": true, "exec": true, "e": true, "review": true, "login": true,
	"logout": true, "mcp": true, "plugin": true, "app-server": true,
	"remote-control": true, "completion": true, "update": true, "doctor": true,
	"sandbox": true, "debug": true, "apply": true, "a": true, "resume": true,
	"fork": true, "queue": true, "archive": true, "delete": true,
	"migrate-rollouts": true, "unarchive": true, "cloud": true,
	"cloud-tasks": true, "exec-server": true, "features": true, "help": true,
	"tcp-tunnel": true, "execpolicy": true, "responses-api-proxy": true,
	"stdio-to-uds": true, "app": true,
}

// CodexOption is one option read from a list: its name as written, bare (`-c`,
// `--config`, `--no-daemon`), the long name it stands for (`-c` is `--config`)
// and its values (the attached one, or the words after it; none for a flag).
// Index is the position of the option's own token; a cluster of short flags
// (`-hV`) gives one option per flag with the same Index.
type CodexOption = ArgOption

// CodexArgs is the reading of one argument list.
type CodexArgs struct {
	// Options are the options in order of appearance, those after the first
	// `--` included (After).
	Options []CodexOption
	// DoubleDash is the index of the first `--`, or -1 when there is none.
	DoubleDash int
	// Command is the index of the argument in command position, the first
	// positional word before any `--` (neither an option nor an option's
	// value), or -1 when there is none. It is not necessarily a subcommand.
	Command int
	// Positionals are original operand positions, including every token after
	// the unowned delimiter regardless of its option-shaped spelling.
	Positionals []int
	// SessionID and Prompt are original positions in command-position
	// resume/fork, or -1. --last reinterprets the first operand as prompt.
	SessionID int
	Prompt    int
}

// ReadCodexArgs reads args as Codex's command line is read (RN-13): an option
// takes its value as the next word, after `=`, or attached to a short flag;
// `-i`/`--image` takes every word up to the next flag, but `--image=FILE` and
// `-iFILE` exactly one; the first `--` ends the positional and option
// handling of the words before it, and nothing after it is in command
// position. It does no I/O and never fails.
func ReadCodexArgs(args []string) CodexArgs {
	out := CodexArgs{DoubleDash: -1, Command: -1, SessionID: -1, Prompt: -1}
	after := false
	for i := 0; i < len(args); {
		a := args[i]
		switch {
		case a == "--" && !after:
			out.DoubleDash = i
			for p := i + 1; p < len(args); p++ {
				out.Positionals = append(out.Positionals, p)
			}
			after = true
			i++
		case strings.HasPrefix(a, "--") && len(a) > 2:
			i = out.readLong(args, i, after)
		case strings.HasPrefix(a, "-") && len(a) > 1:
			i = out.readShort(args, i, after)
		default:
			if !after {
				out.Positionals = append(out.Positionals, i)
			}
			if !after && out.Command < 0 {
				out.Command = i
			}
			i++
		}
	}
	out.readSessionPositions(args)
	return out
}

func (c *CodexArgs) readSessionPositions(args []string) {
	if c.Command < 0 || (args[c.Command] != "resume" && args[c.Command] != "fork") {
		return
	}
	last := c.Has("--last")
	for _, p := range c.Positionals {
		if p <= c.Command {
			continue
		}
		if !last && c.SessionID < 0 {
			c.SessionID = p
		} else if c.Prompt < 0 {
			c.Prompt = p
		}
	}
}

// readLong reads the long option at args[i] and returns the index after it
// and its value words.
func (c *CodexArgs) readLong(args []string, i int, after bool) int {
	name, val, inline := strings.Cut(args[i], "=")
	opt := CodexOption{Index: i, Name: name, Canonical: name, Spelling: args[i], After: after, Known: codexValueLong[name] || codexFlagLong[name]}
	next := i + 1
	if codexValueLong[name] {
		if inline {
			opt.Values = []string{val}
		} else {
			next = c.takeValues(args, next, name == codexOptImage, after, &opt)
		}
	}
	c.Options = append(c.Options, opt)
	return next
}

// readShort reads the cluster of short flags at args[i]: each flag that takes
// no value is its own option, and the first that takes one ends the cluster,
// its value the rest of the token (a leading `=` dropped) or the next word.
func (c *CodexArgs) readShort(args []string, i int, after bool) int {
	tok := args[i]
	next := i + 1
	for p := 1; p < len(tok); p++ {
		ch := tok[p]
		opt := CodexOption{Index: i, Name: "-" + string(ch), Canonical: "-" + string(ch), Spelling: tok, After: after, ClusterStart: p, ClusterEnd: p + 1}
		long, valued := codexValueShort[ch]
		opt.Known = valued || ch == 'h' || ch == 'V'
		if !valued {
			c.Options = append(c.Options, opt)
			continue
		}
		opt.Canonical = long
		opt.ClusterEnd = len(tok)
		if rest := tok[p+1:]; rest != "" {
			opt.Values = []string{strings.TrimPrefix(rest, "=")}
		} else {
			next = c.takeValues(args, next, long == codexOptImage, after, &opt)
		}
		c.Options = append(c.Options, opt)
		break
	}
	return next
}

// takeValues takes the value words of an option whose value is not attached:
// the next word, or for `-i`/`--image` every word up to the next flag. Each is
// taken as values; the index after them is returned.
func (c *CodexArgs) takeValues(args []string, next int, many, after bool, opt *CodexOption) int {
	for next < len(args) {
		if many && strings.HasPrefix(args[next], "-") {
			break
		}
		opt.Values = append(opt.Values, args[next])
		opt.ValueIndices = append(opt.ValueIndices, next)
		next++
		if !many {
			break
		}
	}
	return next
}

// Has reports whether the options before the first `--` include name, a
// canonical option name (a copy after `--` does not count: Codex reads the
// words there as the prompt).
func (c CodexArgs) Has(name string) bool {
	for _, o := range c.Options {
		if !o.After && o.Canonical == name {
			return true
		}
	}
	return false
}

// HasEnable reports whether the options before the first `--` include
// `--enable feature` in any spelling.
func (c CodexArgs) HasEnable(feature string) bool {
	for _, o := range c.Options {
		if o.After || o.Canonical != codexOptEnable {
			continue
		}
		for _, v := range o.Values {
			if v == feature {
				return true
			}
		}
	}
	return false
}

// codexKeyPath splits the key of a `-c key=value` override as Codex does: the
// text before the first `=`, trimmed, split on `.`.
func codexKeyPath(value string) []string {
	key, _, _ := strings.Cut(value, "=")
	return strings.Split(strings.TrimSpace(key), ".")
}

// codexKeyOverlaps reports whether key path p equals, lies above or lies under
// protected path q (one is a segment-wise prefix of the other). A path that
// only shares a prefix string (`tui.theme` against `tui.disable_paste_burst`)
// does not overlap.
func codexKeyOverlaps(p, q []string) bool {
	n := len(p)
	if len(q) < n {
		n = len(q)
	}
	for i := 0; i < n; i++ {
		if p[i] != q[i] {
			return false
		}
	}
	return true
}

// codexConfigRefusals is the table of config key paths a Codex spawn's `-c`
// may not touch, each with why. The question-tool keys are refused only while
// the question-tool setting is on (disable_ask_user).
var codexConfigRefusals = []struct {
	key          string
	questionTool bool
	why          string
}{
	{codexConfigKeyHooks, false, "agent-director sets the hooks and their trust for every Codex launch"},
	{codexConfigKeyPasteBurst, false, "agent-director sets the typing setting for every Codex launch"},
	{codexConfigKeyFeaturesHooks, false, "agent-director needs Codex's hooks on for every launch"},
	{codexConfigKeyQuestionFeature, true, "disable_ask_user keeps Codex's question tool off"},
	{codexConfigKeyQuestionTool, true, "disable_ask_user keeps Codex's question tool off"},
}

// codexFeatureRefusals is the table of `--enable` and `--disable` features a
// Codex spawn may not pass: the features agent-director's settings must win.
// Codex applies every --enable/--disable after every -c and --disable beats
// --enable (RN-13), so a caller's flag cannot be out-placed and is refused.
// `--disable write_stdin_approval` is not here: it is the caller's to choose
// (Q4), and a caller's `--enable` of it is only not added twice.
var codexFeatureRefusals = []struct {
	feature      string
	enable       bool // --enable of it is refused
	disable      bool // --disable of it is refused
	questionTool bool // refused only while disable_ask_user is on
	why          string
}{
	{codexFeatureHooks, false, true, false, "agent-director needs Codex's hooks on for every launch"},
	{codexFeatureQuestionTool, true, false, true, "disable_ask_user keeps Codex's question tool off"},
}

// CheckCodexArgs is the Codex half of SR-1.3 step 7: it refuses with
// ErrSpawnDeniedFlag the first argument of args (in order) that is
//
//   - a Codex subcommand or alias in command position (every one, codexSubcommands);
//   - --remote, --remote-auth-token-env, or existing-session selectors;
//   - -c/--config whose key path (split on `.` as Codex does) equals, lies
//     above or lies under hooks, tui.disable_paste_burst or features.hooks;
//   - --disable hooks;
//   - while questionToolOff (disable_ask_user is on): --enable
//     default_mode_request_user_input, and -c/--config at, above or under
//     features.default_mode_request_user_input or
//     tools.experimental_request_user_input;
//
// Exact denied long names owned as option values are refused too. A refused
// option form after `--` is included (the words there are never in
// command position, so a subcommand name after `--` passes). Every spelling is
// recognised (value as the next word, after `=`, attached to a short flag).
// The description names the flag bare, as written without its value, plus the
// key path, the feature or the subcommand. Everything else passes, more than
// one word after `--` included. It does no I/O.
func CheckCodexArgs(args []string, questionToolOff bool) error {
	read := ReadCodexArgs(args)
	best, bestAt := error(nil), len(args)
	// Exact denied long tokens are refused even when a required option owns
	// them as a value. Merge those matches with parsed checks by original
	// position so the existing first-argument error precedence is retained.
	for i, token := range args {
		key, _, _ := strings.Cut(token, "=")
		if key == codexOptRemote || key == codexOptRemoteAuthTokenEnv ||
			strings.HasPrefix(key, "--") && CodexResumeArgs[key] {
			best, bestAt = fmt.Errorf("%w: %s", ErrSpawnDeniedFlag, key), i
			break
		}
	}

	if read.Command >= 0 && read.Command < bestAt && codexSubcommands[args[read.Command]] {
		best, bestAt = fmt.Errorf("%w: codex subcommand %s: a spawn launches the interactive agent, and this subcommand picks or resumes another session, runs non-interactively or attaches to a remote or shared service", ErrSpawnDeniedFlag, args[read.Command]), read.Command
	}
	for _, o := range read.Options {
		if o.Index > bestAt {
			break
		}
		if err := refuseCodexOption(o, questionToolOff); err != nil {
			best, bestAt = err, o.Index
			break
		}
	}
	return best
}

// refuseCodexOption applies the refusal tables to one option.
func refuseCodexOption(o CodexOption, questionToolOff bool) error {
	switch o.Canonical {
	case codexOptRemote, codexOptRemoteAuthTokenEnv:
		return fmt.Errorf("%w: %s: it attaches the agent to a remote or shared service", ErrSpawnDeniedFlag, o.Name)
	case codexOptConfig:
		for _, v := range o.Values {
			path := codexKeyPath(v)
			for _, r := range codexConfigRefusals {
				if (!r.questionTool || questionToolOff) && codexKeyOverlaps(path, strings.Split(r.key, ".")) {
					return fmt.Errorf("%w: %s %s: %s", ErrSpawnDeniedFlag, o.Name, strings.Join(path, "."), r.why)
				}
			}
		}
	case codexOptEnable, codexOptDisable:
		for _, v := range o.Values {
			for _, r := range codexFeatureRefusals {
				refused := (o.Canonical == codexOptEnable && r.enable) || (o.Canonical == codexOptDisable && r.disable)
				if refused && v == r.feature && (!r.questionTool || questionToolOff) {
					return fmt.Errorf("%w: %s %s: %s", ErrSpawnDeniedFlag, o.Name, v, r.why)
				}
			}
		}
	}
	return nil
}
