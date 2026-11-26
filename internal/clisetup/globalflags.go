package clisetup

import (
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
)

// GlobalFlags holds the values of the four global flags agent-director and
// agent-director-admin both take (b.32k, b.vqr, b.78b), so that both binaries
// reach the same store and tmux the same way:
//
//	--store-path <path>                overrides pkgapi.Options.StorePath
//	--home <path>                      overrides HOME for this invocation
//	--tmux-command <path>              overrides pkgapi.Options.TmuxCommand
//	--create-if-missing <true|false>   overrides pkgapi.Options.CreateIfMissing
//
// They exist so the TS Client (pkg/ts-bun-client) can forward user-supplied
// values verbatim instead of mimicking the CLI's default-resolution. Before
// them, the TS Client derived a HOME override from the parent dirname of its
// store path and prepended the tmux dirname to PATH, both of which encoded
// CLI-internal assumptions that would silently drift if the CLI changed its
// defaults (b.32k). --create-if-missing carries the TS Client's
// createIfMissing option, which until then reached no binary (b.78b).
//
// A value is set iff its "<flag>Set" field is true, so callers can tell "not
// provided" from "provided". A set value is never empty: ParseGlobalFlags
// rejects an empty value in both the `--flag value` and `--flag=value` forms
// (b.pu2), and a --create-if-missing value other than "true" or "false".
type GlobalFlags struct {
	// StorePath is --store-path's value, set when StorePathSet.
	StorePath    string
	StorePathSet bool
	// Home is --home's value, set when HomeSet.
	Home    string
	HomeSet bool
	// TmuxCommand is --tmux-command's value, set when TmuxCommandSet.
	TmuxCommand    string
	TmuxCommandSet bool
	// CreateIfMissing is --create-if-missing's value ("true" is true,
	// "false" false), set when CreateIfMissingSet.
	CreateIfMissing    bool
	CreateIfMissingSet bool
}

// createIfMissingFlag is the global flag that sets
// pkgapi.Options.CreateIfMissing; its value is "true" or "false".
const createIfMissingFlag = "--create-if-missing"

// slot captures a global flag's parsed value and its set sentinel.
type slot struct {
	val    *string
	setRef *bool
}

// globalFlagSlots is the parser's registry. create-if-missing is captured as
// text, validated by assign and converted to g.CreateIfMissing after the scan.
func globalFlagSlots(g *GlobalFlags, createIfMissing *string) map[string]slot {
	return map[string]slot{
		"--store-path":      {&g.StorePath, &g.StorePathSet},
		"--home":            {&g.Home, &g.HomeSet},
		"--tmux-command":    {&g.TmuxCommand, &g.TmuxCommandSet},
		createIfMissingFlag: {createIfMissing, &g.CreateIfMissingSet},
	}
}

// GlobalFlagNames returns exactly the names ParseGlobalFlags recognizes,
// sorted, as a fresh slice derived from the parser's own registry.
func GlobalFlagNames() []string {
	registry := globalFlagSlots(&GlobalFlags{}, new(string))
	names := make([]string, 0, len(registry))
	for name := range registry {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// ParseGlobalFlags parses only the global flags before the verb and returns
// (parsed flags, remaining argv, error). Both --flag value and --flag=value
// forms are accepted. Every global requires a value, even create-if-missing.
// The first word that is not a global flag ends this stage, so a verb's
// operands and flag values cannot redirect the invocation (b.t1b).
//
// Errors are returned for malformed flag inputs: `--store-path` with no
// following value, or an empty value in either form (`--store-path ""` or
// `--store-path=`). Both forms give the same "<flag> requires a value" error
// (b.pu2). A --create-if-missing value other than "true" or "false" is an
// error too (b.78b). A second copy of any global flag is refused, including
// a copy after the verb when the first was in the prefix. Callers should write an
// ErrInvalidFlags envelope and exit non-zero before any store, config or trail
// access. On a global repeat, g and the remaining argv are retained so
// callers can validate later global positions before reporting it.
func ParseGlobalFlags(argv []string) (GlobalFlags, []string, error) {
	return parseGlobalFlags(nil, GlobalFlags{}, argv, false, nil)
}

// ParseVerbGlobalFlags extracts globals only at option positions in argv,
// the words after a verb (or its routed sub-verb). fs must be built by the
// same builder the handler uses. This helper only reads fs.Lookup and each
// flag's IsBoolFlag method; it never parses or sets a verb flag. A nil fs
// allows only a leading sequence of globals, as before a sub-verb.
//
// A value-taking verb flag owns its next word even when it spells a global
// or --. A bare boolean owns no next word. A positional word, real --,
// unknown flag or malformed flag token ends extraction, leaving that suffix
// unchanged for the handler's parser. Operand-only routes such as get-many
// must not call this helper. g carries the already parsed prefix globals;
// any further occurrence is refused.
//
// A non-nil fs requires trialParse, which must silently parse the extracted
// argv with disposable, caller-owned values. Only verb repeats defer to a
// trial parse error, preserving the real handler's help and error handling.
// Global repeats are reported after every global copy has been validated,
// before any trial parse. A global-repeat error retains g and the extracted
// argv so callers can scan any later global positions; combine those results
// with GlobalFlagScanError before reporting the pending error.
//
// A nil fs requires no trial parser and scans globals only, as on help/version
// and before a sub-verb. The scanner itself never parses or sets fs. Both
// binaries must reuse these two stages rather than scan caller data (b.t1b).
func ParseVerbGlobalFlags(fs *flag.FlagSet, g GlobalFlags, argv []string, trialParse func([]string) error) (GlobalFlags, []string, error) {
	if fs != nil && trialParse == nil {
		return GlobalFlags{}, nil, fmt.Errorf("verb global scan requires a trial parser")
	}
	return parseGlobalFlags(fs, g, argv, true, trialParse)
}

func parseGlobalFlags(fs *flag.FlagSet, g GlobalFlags, argv []string, verbFlags bool, trialParse func([]string) error) (GlobalFlags, []string, error) {
	out := make([]string, 0, len(argv))
	seen := make(map[string]bool)
	var globalRepeatErr, verbRepeatErr error

	var createIfMissing string
	if g.CreateIfMissingSet {
		createIfMissing = fmt.Sprint(g.CreateIfMissing)
	}
	recognized := globalFlagSlots(&g, &createIfMissing)

	// assign stores one copy of flag name's value in s. It refuses a
	// --create-if-missing value other than "true" or "false" at the copy that
	// carries it, as the loop below refuses an empty value, so a repeated flag
	// cannot pass a bad earlier value by ending on a good one (b.78b).
	assign := func(name, val string, s slot) error {
		if name == createIfMissingFlag && val != "true" && val != "false" {
			return fmt.Errorf("%s must be true or false, got %q", createIfMissingFlag, val)
		}
		if *s.setRef {
			if globalRepeatErr == nil {
				globalRepeatErr = &flagRepeatError{name: strings.TrimPrefix(name, "--"), global: true}
			}
			return nil
		}
		*s.val = val
		*s.setRef = true
		return nil
	}

	for i := 0; i < len(argv); i++ {
		tok := argv[i]

		// `--flag=value` form: split on first '='.
		if eq := strings.IndexByte(tok, '='); eq > 0 {
			name := tok[:eq]
			if s, ok := recognized[name]; ok {
				val := tok[eq+1:]
				// Reject `--flag=` with an empty value, as the two-token form
				// below rejects `--flag ""`. Without these guards the empty
				// string would silently fall through downstream resolution
				// (e.g. resolveStorePath's `!= ""` check) and behave
				// identically to omitting the flag — a footgun where the user
				// thinks they set it but didn't (b.pu2: `--store-path ""`
				// opened the default store).
				if val == "" {
					return GlobalFlags{}, nil, fmt.Errorf("%s requires a value", name)
				}
				if err := assign(name, val, s); err != nil {
					return GlobalFlags{}, nil, err
				}
				continue
			}
		}

		// `--flag value` form: consume the next argv element. A missing or
		// empty value gets the same error as `--flag=` above (b.pu2).
		if s, ok := recognized[tok]; ok {
			if i+1 >= len(argv) || argv[i+1] == "" {
				return GlobalFlags{}, nil, fmt.Errorf("%s requires a value", tok)
			}
			if err := assign(tok, argv[i+1], s); err != nil {
				return GlobalFlags{}, nil, err
			}
			i++ // skip the value
			continue
		}

		// Follow flag.FlagSet's token grammar without consuming operands,
		// invoking Set, or treating a value's spelling as an option.
		name := strings.TrimPrefix(tok, "-")
		if strings.HasPrefix(name, "-") {
			name = name[1:]
		}
		if !verbFlags || len(tok) < 2 || tok[0] != '-' || name == "" || name[0] == '-' || name[0] == '=' {
			out = append(out, argv[i:]...)
			break
		}
		name, _, hasValue := strings.Cut(name, "=")
		var f *flag.Flag
		if fs != nil {
			f = fs.Lookup(name)
		}
		if f == nil {
			out = append(out, argv[i:]...)
			break
		}
		if !isRepeatableFlag(f.Value) {
			if seen[name] && verbRepeatErr == nil {
				verbRepeatErr = repeatedFlagError(name)
			}
			seen[name] = true
		}
		out = append(out, tok)
		boolFlag, isBool := f.Value.(interface{ IsBoolFlag() bool })
		if !hasValue && (!isBool || !boolFlag.IsBoolFlag()) && i+1 < len(argv) {
			i++
			out = append(out, argv[i])
		}
	}

	// assign let through at most one copy, and only "true" or "false".
	g.CreateIfMissing = createIfMissing == "true"

	if globalRepeatErr != nil {
		return g, out, globalRepeatErr
	}
	if verbRepeatErr != nil && trialParse(out) != nil {
		// Only the handler renders real parse failures (help included); an
		// otherwise valid repeat still fails before any client setup.
		verbRepeatErr = nil
	}
	return g, out, verbRepeatErr
}

// Apply applies g to this process and returns the Overrides Open takes.
//
// --home replaces the HOME environment variable (its value tilde-expanded
// against the current HOME by ExpandTilde) BEFORE anything loads the config:
// internal/config, pkg/api.expandTilde and internal/store.expandTilde all
// expand "~/" with os.UserHomeDir(), which reads HOME on POSIX, so the
// override covers every downstream "~/" store/config path expansion. (A spawn
// cwd's "~" is not one of them: SRD §7.2 resolves it with
// user.Current().HomeDir, which ignores HOME.) Safe because the binaries are
// short-lived and not multi-threaded at startup. b.32k, b.hvf.
//
// A --home of "~" or "~/…" while HOME is unset or empty is refused: there is
// no home to expand it against, and HOME is never set to an unexpanded "~"
// value nor resolved against the passwd home (b.38a, the rule b.4uz gave the
// store).
//
// --store-path is passed as given (pkg/api.New tilde-expands it), and
// --tmux-command tilde-expanded, after --home is applied, so a `~/bin/tmux`
// argument works (pkg/api uses Options.TmuxCommand as given). A
// --tmux-command ExpandTilde cannot expand is passed on unexpanded.
// --create-if-missing is passed on as given; APIOptions applies it (b.78b).
//
// The errors are that --home refusal and a failure to set HOME; both binaries
// print either as ErrInvalidFlags.
func (g GlobalFlags) Apply() (Overrides, error) {
	if g.HomeSet {
		home, ok := ExpandTilde(g.Home)
		if !ok {
			return Overrides{}, fmt.Errorf("--home %q: HOME is unset or empty, so there is no home directory to expand \"~\" against", g.Home)
		}
		if err := os.Setenv("HOME", home); err != nil {
			return Overrides{}, fmt.Errorf("set HOME: %w", err)
		}
	}
	var o Overrides
	if g.StorePathSet {
		o.StorePath = g.StorePath
	}
	if g.TmuxCommandSet {
		// Not ok means no HOME: the value goes on unexpanded, as it always has.
		o.TmuxCommand, _ = ExpandTilde(g.TmuxCommand)
	}
	if g.CreateIfMissingSet {
		o.CreateIfMissing, o.CreateIfMissingSet = g.CreateIfMissing, true
	}
	return o, nil
}

// ExpandTilde expands a bare "~" or a leading "~/" against the current HOME,
// read with os.UserHomeDir (on Unix, HOME and nothing else): "~" gives HOME
// and "~/x" gives HOME + "/x". Any other p, "~user/x" included, is returned
// unchanged with ok true.
//
// When p is "~" or "~/…" and HOME is unset or empty, ok is false and p is
// returned unchanged. It deliberately does not fall back to another home
// source such as the passwd entry, which holds the real AD directory (b.4uz,
// b.38a); internal/store and pkg/api refuse the same case.
func ExpandTilde(p string) (expanded string, ok bool) {
	if p != "~" && !strings.HasPrefix(p, "~/") {
		return p, true
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p, false
	}
	return home + p[1:], true
}
