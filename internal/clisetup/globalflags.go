package clisetup

import (
	"fmt"
	"os"
	"strings"
)

// GlobalFlags holds the values of the three global flags agent-director and
// agent-director-admin both take (b.32k, b.vqr), so that both binaries reach
// the same store and tmux the same way:
//
//	--store-path <path>       overrides pkgapi.Options.StorePath
//	--home <path>             overrides HOME for this invocation
//	--tmux-command <path>     overrides pkgapi.Options.TmuxCommand
//
// They exist so the TS Client (pkg/ts-bun-client) can forward user-supplied
// values verbatim instead of mimicking the CLI's default-resolution. Before
// them, the TS Client derived a HOME override from the parent dirname of its
// store path and prepended the tmux dirname to PATH, both of which encoded
// CLI-internal assumptions that would silently drift if the CLI changed its
// defaults (b.32k).
//
// A value is set iff its "<flag>Set" field is true, so callers can tell "not
// provided" from "explicitly empty".
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
}

// ParseGlobalFlags pre-scans argv for the three global flags and returns
// (parsed flags, argv with those flag tokens removed, error).
//
// Parsing model: rather than route every per-verb FlagSet through a parent
// FlagSet, the pre-scan copies every other token, in order, into the returned
// argv, so the per-verb dispatch operates on it unchanged. Both `--flag value`
// and `--flag=value` forms are accepted, anywhere in argv.
//
// Errors are returned for malformed flag inputs (e.g. `--store-path` with no
// following value, or `--store-path=` with an empty value via the `=` form).
// Callers should write an ErrInvalidFlags envelope and exit non-zero.
//
// Caveat: the pre-scan recognizes flag tokens anywhere in argv and does NOT
// treat `--` as an end-of-options sentinel. If a caller intentionally passes
// these flag names through `--` (e.g. `spawn --claude-args -- --store-path /x`),
// the pre-scan will still consume them. The practical risk is low because the
// recognized names (`--store-path`, `--home`, `--tmux-command`) are unlikely
// to recur in legitimate passthrough argv, so threading `--`-awareness through
// the parser is not worth the added complexity. Revisit if a real collision is
// reported. See bug b.32k.
func ParseGlobalFlags(argv []string) (GlobalFlags, []string, error) {
	var g GlobalFlags
	out := make([]string, 0, len(argv))

	// recognized maps each global-flag name to a pointer-pair that captures
	// the parsed value and its "set" sentinel.
	type slot struct {
		val    *string
		setRef *bool
	}
	recognized := map[string]slot{
		"--store-path":   {&g.StorePath, &g.StorePathSet},
		"--home":         {&g.Home, &g.HomeSet},
		"--tmux-command": {&g.TmuxCommand, &g.TmuxCommandSet},
	}

	for i := 0; i < len(argv); i++ {
		tok := argv[i]

		// `--flag=value` form: split on first '='.
		if eq := strings.IndexByte(tok, '='); eq > 0 {
			name := tok[:eq]
			if s, ok := recognized[name]; ok {
				val := tok[eq+1:]
				// Reject `--flag=` with empty value. Without this guard the
				// empty string would silently fall through downstream
				// resolution (e.g. resolveStorePath's `!= ""` check) and
				// behave identically to omitting the flag — a footgun where
				// the user thinks they set it but didn't. Mirror the
				// missing-value error from the two-token form below.
				if val == "" {
					return GlobalFlags{}, nil, fmt.Errorf("%s requires a value", name)
				}
				*s.val = val
				*s.setRef = true
				continue
			}
		}

		// `--flag value` form: consume the next argv element.
		if s, ok := recognized[tok]; ok {
			if i+1 >= len(argv) {
				return GlobalFlags{}, nil, fmt.Errorf("%s requires a value", tok)
			}
			*s.val = argv[i+1]
			*s.setRef = true
			i++ // skip the value
			continue
		}

		// Unknown token — pass through.
		out = append(out, tok)
	}

	return g, out, nil
}

// Apply applies g to this process and returns the Overrides Open takes.
//
// --home replaces the HOME environment variable (its value tilde-expanded
// against the current HOME) BEFORE anything loads the config: internal/config,
// pkg/api.expandTilde and internal/store.expandTilde all expand "~/" with
// os.UserHomeDir(), which reads HOME on POSIX, so the override covers every
// downstream "~/" store/config path expansion. (A spawn cwd's "~" is not one
// of them: SRD §7.2 resolves it with user.Current().HomeDir, which ignores
// HOME.) Safe because the binaries are short-lived and not multi-threaded at
// startup. b.32k, b.hvf.
//
// --store-path is passed as given (pkg/api.New tilde-expands it), and
// --tmux-command tilde-expanded, after --home is applied, so a `~/bin/tmux`
// argument works (pkg/api uses Options.TmuxCommand as given).
//
// The only error is a failure to set HOME.
func (g GlobalFlags) Apply() (Overrides, error) {
	if g.HomeSet {
		if err := os.Setenv("HOME", ExpandTilde(g.Home)); err != nil {
			return Overrides{}, fmt.Errorf("set HOME: %w", err)
		}
	}
	var o Overrides
	if g.StorePathSet {
		o.StorePath = g.StorePath
	}
	if g.TmuxCommandSet {
		o.TmuxCommand = ExpandTilde(g.TmuxCommand)
	}
	return o, nil
}

// ExpandTilde expands a leading "~/" against the current HOME (env, then
// os.UserHomeDir), returning p unchanged when it has no such prefix or no
// home is known. Mirrors pkg/api.expandTilde, which is unexported.
func ExpandTilde(p string) string {
	if !strings.HasPrefix(p, "~/") {
		return p
	}
	home := os.Getenv("HOME")
	if home == "" {
		var err error
		home, err = os.UserHomeDir()
		if err != nil || home == "" {
			return p
		}
	}
	return home + p[1:]
}
