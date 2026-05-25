// global_flags.go — global CLI flags parsed BEFORE verb dispatch.
//
// Three flags live at the global scope (i.e. before the verb token):
//
//	--store-path <path>       overrides pkgapi.Options.StorePath
//	--home <path>             overrides HOME for this invocation
//	--tmux-command <path>     overrides pkgapi.Options.TmuxCommand
//
// These exist so the TS Client (pkg/ts-bun-client) can forward
// user-supplied values verbatim instead of mimicking CLI default-resolution.
// See bug b.32k for the rationale: prior to these flags the TS Client peeked
// at the parent dirname of storePath to derive a HOME override and prepended
// the tmux dirname to PATH, both of which encoded CLI-internal assumptions
// that would silently drift if the CLI changed its defaults.
//
// Parsing model: rather than route every per-verb FlagSet through a parent
// FlagSet, we pre-scan os.Args[1:] for the three flags, copy the remainder
// into a new argv slice, and let the per-verb dispatch operate on the
// stripped argv unchanged. Unknown args pass through untouched. Both
// `--flag value` and `--flag=value` forms are supported.
package main

import (
	"fmt"
	"strings"
)

// globalOptions holds the values parsed from the three global flags.
// A field is set iff its corresponding sentinel "<flag>Set" bool is true,
// so callers can distinguish "not provided" from "explicitly empty".
type globalOptions struct {
	storePath       string
	storePathSet    bool
	home            string
	homeSet         bool
	tmuxCommand     string
	tmuxCommandSet  bool
}

// parseGlobalFlags pre-scans argv for the three global flags and returns
// (parsed opts, stripped argv with those flag tokens removed, error).
//
// Both `--flag value` and `--flag=value` forms are accepted. Unknown args
// pass through unchanged so per-verb FlagSets still see what they expect.
//
// Errors are returned for malformed flag inputs (e.g. `--store-path` with
// no following value, or `--store-path=` with an empty value via the `=`
// form). Callers should write an ErrInvalidFlags envelope and exit non-zero.
//
// Caveat: the pre-scan recognizes flag tokens anywhere in argv and does NOT
// treat `--` as an end-of-options sentinel. If a caller intentionally passes
// our flag names through `--` (e.g. `spawn --claude-args -- --store-path /x`),
// the pre-scan will still consume them. The practical risk is low because
// the recognized names (`--store-path`, `--home`, `--tmux-command`) are
// unlikely to recur in legitimate passthrough argv, so threading
// `--`-awareness through the parser is not worth the added complexity.
// Revisit if a real collision is reported. See bug b.32k.
func parseGlobalFlags(argv []string) (globalOptions, []string, error) {
	var opts globalOptions
	out := make([]string, 0, len(argv))

	// recognized maps each global-flag name to a pointer-pair that captures
	// the parsed value and its "set" sentinel.
	type slot struct {
		val    *string
		setRef *bool
	}
	recognized := map[string]slot{
		"--store-path":   {&opts.storePath, &opts.storePathSet},
		"--home":         {&opts.home, &opts.homeSet},
		"--tmux-command": {&opts.tmuxCommand, &opts.tmuxCommandSet},
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
					return globalOptions{}, nil, fmt.Errorf("%s requires a value", name)
				}
				*s.val = val
				*s.setRef = true
				continue
			}
		}

		// `--flag value` form: consume the next argv element.
		if s, ok := recognized[tok]; ok {
			if i+1 >= len(argv) {
				return globalOptions{}, nil, fmt.Errorf("%s requires a value", tok)
			}
			*s.val = argv[i+1]
			*s.setRef = true
			i++ // skip the value
			continue
		}

		// Unknown token — pass through.
		out = append(out, tok)
	}

	return opts, out, nil
}
