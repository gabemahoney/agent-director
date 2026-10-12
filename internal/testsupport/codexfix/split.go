package codexfix

import (
	"fmt"
	"strings"
	"testing"
)

// Kind says what one part of a recorded Codex launch command is. A part is
// classified by its shape, not by who put it there: a caller's own
// `--no-daemon` or `-c projects=...` is the same kind as AD's, and a test
// tells them apart by position, count or value.
type Kind string

const (
	// KindPassThrough is any argument the splitter does not recognise: the
	// caller's (or a resumed row's replayed) options, values and prompt words,
	// and every `-c key=value` it has no kind for (Item.Override is set).
	KindPassThrough Kind = "pass-through"
	// KindResume is `resume <session id>` at argv[1] (Item.SessionID).
	KindResume Kind = "resume"
	// KindDashDash is the first `--`. Later `--` words are pass-through.
	KindDashDash Kind = "dashdash"
	// KindNoDaemon is `--no-daemon`.
	KindNoDaemon Kind = "no-daemon"
	// KindBypass is `--dangerously-bypass-hook-trust`.
	KindBypass Kind = "bypass-hook-trust"
	// KindEnable and KindDisable are `--enable <feature>` and
	// `--disable <feature>` (Item.Feature), in either `--flag value` or
	// `--flag=value` spelling.
	KindEnable  Kind = "enable"
	KindDisable Kind = "disable"
	// KindHooks is `-c hooks.<Event>=[...]` (Item.Event, Item.Hooks): one
	// whole event value, whose handlers are the item's Hooks.
	KindHooks Kind = "hooks"
	// KindState is `-c hooks.state={...}` (Item.State: key -> trusted_hash).
	KindState Kind = "hooks-state"
	// KindPaste is `-c tui.disable_paste_burst=<bool>` (Item.Bool).
	KindPaste Kind = "paste"
	// KindQuestionTool is
	// `-c tools.experimental_request_user_input.enabled=<bool>` (Item.Bool).
	KindQuestionTool Kind = "question-tool"
	// KindProjects is `-c projects={"<cwd>"={trust_level="..."}}`, the whole
	// table form (Item.Projects: decoded cwd -> trust level).
	KindProjects Kind = "projects"
)

// Feature and key names the splitter keys on.
const (
	// FeatureWriteStdinApproval is the feature AD enables on every launch.
	FeatureWriteStdinApproval = "write_stdin_approval"
	// KeyPaste and KeyQuestionTool are the dotted -c key paths of the paste
	// and question-tool settings.
	KeyPaste        = "tui.disable_paste_burst"
	KeyQuestionTool = "tools.experimental_request_user_input.enabled"
)

// Item is one part of the launch: the argv elements it consumed, where they
// stand and what they decode to.
type Item struct {
	Kind Kind
	// Index is the argv index of Args[0] (the program is index 0).
	Index int
	// Args are the argv elements the part consumed, in order: one element, or
	// two for `-c key=value` and `--enable <feature>`.
	Args []string
	// AfterDashDash is true when the part stands after the first `--` (the
	// `--` itself is false).
	AfterDashDash bool

	// Override is the decoded value of a `-c` part of any kind; nil otherwise.
	Override *Override
	// Event and Hooks are set for KindHooks.
	Event string
	Hooks []Hook
	// State is set for KindState: key -> trusted_hash.
	State map[string]string
	// Bool is set for KindPaste and KindQuestionTool.
	Bool bool
	// Feature is set for KindEnable and KindDisable.
	Feature string
	// Projects is set for KindProjects: decoded cwd -> trust_level.
	Projects map[string]string
	// SessionID is set for KindResume.
	SessionID string
}

// Launch is a recorded Codex launch command split into parts. Every part
// keeps its argv position and whether it stands before or after the first
// `--`, so a test asserts presence, count, order and precedence on parts and
// never compares whole argv strings.
type Launch struct {
	// Argv is the command as recorded.
	Argv []string
	// Program is Argv[0]: the codex program, one unchanged element.
	Program string
	// Items are the parts after the program, in argv order. Their Args
	// concatenate to Argv[1:].
	Items []Item
	// DashDash is the argv index of the first `--`, or -1.
	DashDash int
}

// Split splits a recorded Codex launch command (the create request's Command:
// program first). It fails the test when AD's own kinds do not decode: a
// hooks, hooks.state, paste, question-tool or projects `-c` whose value is
// not the TOML shape AD writes. Anything else it does not recognise stays a
// pass-through part, in order.
func Split(t testing.TB, argv []string) Launch {
	t.Helper()
	l, err := TrySplit(argv)
	if err != nil {
		t.Fatalf("codexfix.Split: %v\nargv: %q", err, argv)
	}
	return l
}

// TrySplit is Split returning the decode error instead of failing a test.
func TrySplit(argv []string) (Launch, error) {
	if len(argv) == 0 {
		return Launch{}, fmt.Errorf("empty command")
	}
	l := Launch{Argv: argv, Program: argv[0], DashDash: -1}
	i := 1
	if len(argv) > 1 && argv[1] == "resume" {
		it := Item{Kind: KindResume, Index: 1, Args: argv[1:2]}
		i = 2
		if len(argv) > 2 {
			it.Args = argv[1:3]
			it.SessionID = argv[2]
			i = 3
		}
		l.Items = append(l.Items, it)
	}
	for i < len(argv) {
		it, n, err := l.scan(argv, i)
		if err != nil {
			return Launch{}, err
		}
		if it.Kind == KindDashDash {
			l.DashDash = i
		}
		l.Items = append(l.Items, it)
		i += n
	}
	return l, nil
}

// scan classifies the part starting at argv[i] and returns it with the number
// of elements it consumed.
func (l *Launch) scan(argv []string, i int) (Item, int, error) {
	arg := argv[i]
	after := l.DashDash >= 0
	one := func(k Kind) (Item, int, error) {
		return Item{Kind: k, Index: i, Args: argv[i : i+1], AfterDashDash: after}, 1, nil
	}
	// withValue reads an option whose value is the next word or follows "=".
	withValue := func(long, short string) (value string, n int, ok bool) {
		switch {
		case arg == long || (short != "" && arg == short):
			if i+1 < len(argv) {
				return argv[i+1], 2, true
			}
		case strings.HasPrefix(arg, long+"="):
			return strings.TrimPrefix(arg, long+"="), 1, true
		case short != "" && strings.HasPrefix(arg, short+"="):
			return strings.TrimPrefix(arg, short+"="), 1, true
		case short != "" && strings.HasPrefix(arg, short) && !strings.HasPrefix(arg, "--") && len(arg) > len(short):
			return arg[len(short):], 1, true
		}
		return "", 0, false
	}

	switch {
	case arg == "--" && !after:
		return one(KindDashDash)
	case arg == "--no-daemon":
		return one(KindNoDaemon)
	case arg == "--dangerously-bypass-hook-trust":
		return one(KindBypass)
	}
	if v, n, ok := withValue("--enable", ""); ok {
		it := Item{Kind: KindEnable, Index: i, Args: argv[i : i+n], AfterDashDash: after, Feature: v}
		return it, n, nil
	}
	if v, n, ok := withValue("--disable", ""); ok {
		it := Item{Kind: KindDisable, Index: i, Args: argv[i : i+n], AfterDashDash: after, Feature: v}
		return it, n, nil
	}
	if v, n, ok := withValue("--config", "-c"); ok {
		it, err := configItem(i, argv[i:i+n], after, v)
		return it, n, err
	}
	return one(KindPassThrough)
}

// configItem classifies one `-c` part by its key path.
func configItem(i int, args []string, after bool, text string) (Item, error) {
	o, err := ParseOverride(text)
	it := Item{Kind: KindPassThrough, Index: i, Args: args, AfterDashDash: after}
	if err != nil {
		// Codex rejects this override; to the splitter it is an
		// unrecognised argument.
		return it, nil
	}
	it.Override = &o
	p := o.Path
	// ours reports the AD-form kinds, whose value must decode as written.
	ours := func(k Kind) error {
		it.Kind = k
		if o.ParseErr != nil {
			return fmt.Errorf("-c %s: value is not TOML (%v): %s", o.Key, o.ParseErr, o.RawValue)
		}
		return nil
	}
	switch {
	case len(p) == 2 && p[0] == "hooks" && p[1] == "state":
		if err := ours(KindState); err != nil {
			return it, err
		}
		m, ok := o.Value.(map[string]any)
		if !ok {
			return it, fmt.Errorf("-c hooks.state: value is %T, want a table", o.Value)
		}
		it.State = map[string]string{}
		for k, v := range m {
			vm, ok := v.(map[string]any)
			h, hok := vm["trusted_hash"].(string)
			if !ok || !hok {
				return it, fmt.Errorf("-c hooks.state[%q] is %v, want {trusted_hash=\"...\"}", k, v)
			}
			it.State[k] = h
		}
	case len(p) == 2 && p[0] == "hooks":
		if err := ours(KindHooks); err != nil {
			return it, err
		}
		hooks, err := decodeHooks(p[1], o.Value)
		if err != nil {
			return it, err
		}
		it.Event = p[1]
		for k := range hooks {
			hooks[k].ArgIndex = i
			hooks[k].AfterDashDash = after
		}
		it.Hooks = hooks
	case o.PathIs(KeyPaste):
		b, err := boolValue(o, KindPaste, ours)
		if err != nil {
			return it, err
		}
		it.Bool = b
	case o.PathIs(KeyQuestionTool):
		b, err := boolValue(o, KindQuestionTool, ours)
		if err != nil {
			return it, err
		}
		it.Bool = b
	case len(p) == 1 && p[0] == "projects":
		if err := ours(KindProjects); err != nil {
			return it, err
		}
		m, ok := o.Value.(map[string]any)
		if !ok {
			return it, fmt.Errorf("-c projects: value is %T, want a table", o.Value)
		}
		it.Projects = map[string]string{}
		for cwd, v := range m {
			vm, ok := v.(map[string]any)
			if !ok {
				return it, fmt.Errorf("-c projects[%q] is %T, want a table", cwd, v)
			}
			level, _ := vm["trust_level"].(string)
			it.Projects[cwd] = level
		}
	}
	return it, nil
}

func boolValue(o Override, k Kind, ours func(Kind) error) (bool, error) {
	if err := ours(k); err != nil {
		return false, err
	}
	b, ok := o.Value.(bool)
	if !ok {
		return false, fmt.Errorf("-c %s: value is %T, want a bool", o.Key, o.Value)
	}
	return b, nil
}

// Of returns the parts of kind k, in argv order, before and after `--`.
func (l Launch) Of(k Kind) []Item {
	var out []Item
	for _, it := range l.Items {
		if it.Kind == k {
			out = append(out, it)
		}
	}
	return out
}

// Before returns the parts of kind k that stand before the first `--` (all of
// them when there is none).
func (l Launch) Before(k Kind) []Item {
	var out []Item
	for _, it := range l.Of(k) {
		if !it.AfterDashDash {
			out = append(out, it)
		}
	}
	return out
}

// After returns the parts of kind k that stand after the first `--`.
func (l Launch) After(k Kind) []Item {
	var out []Item
	for _, it := range l.Of(k) {
		if it.AfterDashDash {
			out = append(out, it)
		}
	}
	return out
}

// HasDashDash reports whether the launch has a `--`.
func (l Launch) HasDashDash() bool { return l.DashDash >= 0 }

// Resume returns the `resume <id>` part, if the launch has one.
func (l Launch) Resume() (Item, bool) {
	r := l.Of(KindResume)
	if len(r) == 0 {
		return Item{}, false
	}
	return r[0], true
}

// PassThrough returns the argv elements of the pass-through parts that stand
// before the first `--`, in order (the caller's or replayed options, values
// and prompt words, and `-c` values with no kind of their own).
func (l Launch) PassThrough() []string { return passArgs(l.Before(KindPassThrough)) }

// PassThroughAfter returns the pass-through elements after the first `--`.
func (l Launch) PassThroughAfter() []string { return passArgs(l.After(KindPassThrough)) }

// AfterDashDashArgs returns every element after the first `--` of any kind,
// in order, as recorded (what the caller passed after `--`, unchanged).
func (l Launch) AfterDashDashArgs() []string {
	if l.DashDash < 0 {
		return nil
	}
	return l.Argv[l.DashDash+1:]
}

func passArgs(items []Item) []string {
	var out []string
	for _, it := range items {
		out = append(out, it.Args...)
	}
	return out
}

// HookItems returns the `-c hooks.<event>=...` parts for event, before and
// after `--`. A repeated -c replaces the whole event value in Codex, so a
// launch should carry exactly one per event.
func (l Launch) HookItems(event string) []Item {
	var out []Item
	for _, it := range l.Of(KindHooks) {
		if it.Event == event {
			out = append(out, it)
		}
	}
	return out
}

// Hooks returns every handler of every hooks part, in argv then group then
// handler order.
func (l Launch) Hooks() []Hook {
	var out []Hook
	for _, it := range l.Of(KindHooks) {
		out = append(out, it.Hooks...)
	}
	return out
}

// HooksFor returns the handlers registered for event.
func (l Launch) HooksFor(event string) []Hook {
	var out []Hook
	for _, h := range l.Hooks() {
		if h.Event == event {
			out = append(out, h)
		}
	}
	return out
}

// HooksOfKind returns the handlers of kind k (HookAD, HookHelp, HookDeny) over
// all events, in order.
func (l Launch) HooksOfKind(k HookKind) []Hook {
	var out []Hook
	for _, h := range l.Hooks() {
		if h.Kind == k {
			out = append(out, h)
		}
	}
	return out
}

// Events returns the events that have a hooks part, in argv order, each once.
func (l Launch) Events() []string {
	var out []string
	seen := map[string]bool{}
	for _, it := range l.Of(KindHooks) {
		if !seen[it.Event] {
			seen[it.Event] = true
			out = append(out, it.Event)
		}
	}
	return out
}

// State returns the hooks.state map of the last hooks.state part and whether
// there is one (a repeated hooks.state replaces, so the last is the one in
// force).
func (l Launch) State() (map[string]string, bool) {
	s := l.Of(KindState)
	if len(s) == 0 {
		return nil, false
	}
	return s[len(s)-1].State, true
}

// Enabled returns the `--enable <feature>` parts for feature, before and after
// `--`; Disabled likewise for `--disable`.
func (l Launch) Enabled(feature string) []Item { return l.byFeature(KindEnable, feature) }

// Disabled returns the `--disable <feature>` parts for feature.
func (l Launch) Disabled(feature string) []Item { return l.byFeature(KindDisable, feature) }

func (l Launch) byFeature(k Kind, feature string) []Item {
	var out []Item
	for _, it := range l.Of(k) {
		if it.Feature == feature {
			out = append(out, it)
		}
	}
	return out
}

// Last returns the last part before the first `--` (or the last part when
// there is none) and whether there is one. The resume part and a lone `--`
// count: callers compare Kind.
func (l Launch) Last() (Item, bool) {
	var last Item
	found := false
	for _, it := range l.Items {
		if it.Kind == KindDashDash || it.AfterDashDash {
			break
		}
		last, found = it, true
	}
	return last, found
}

// LastPassThroughIndex returns the argv index of the last pass-through
// element before the first `--`, or 0 when there is none (the program's
// index, so "every AD setting is after it" still reads true).
func (l Launch) LastPassThroughIndex() int {
	idx := 0
	for _, it := range l.Before(KindPassThrough) {
		idx = it.Index + len(it.Args) - 1
	}
	return idx
}
