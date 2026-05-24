package api

import "log"

// Options configures a Client at construction time. All fields are optional;
// zero values are replaced with documented defaults inside New.
type Options struct {
	// StorePath is the filesystem path to the SQLite state database.
	//
	// Resolution order (three-tier precedence):
	//  1. Options.StorePath if non-empty (tilde-expanded by New).
	//  2. cfg.Store.DbPath loaded from the resolved ConfigPath.
	//  3. Hardcoded fallback: "~/.agent-director/state.db".
	//
	// This preserves the CLI's existing behavior: a user who set a custom
	// [store] db_path in their TOML continues to hit that path even when
	// neither CLI flags nor Options.StorePath override it.
	StorePath string

	// ConfigPath is the path to the TOML configuration file.
	// Defaults to "~/.agent-director/config.toml" when empty.
	// A leading "~/" is tilde-expanded by New before the file is loaded.
	ConfigPath string

	// TmuxCommand overrides the tmux binary used for session management.
	// When empty the tmux binary on PATH is used (standard behavior).
	TmuxCommand string

	// Logger receives operational log output. When nil, New substitutes
	// log.New(io.Discard, "", 0) so the caller is not required to supply
	// a logger. CLI callers pass a recovery logger; MCP callers pass nil
	// (intentional silence).
	Logger *log.Logger

	// CreateIfMissing controls whether New creates the store file and its
	// parent directory when they do not exist.
	//
	// false (default) — library contract: a missing store yields a typed
	// error wrapping store.ErrStoreNotInitialized. No files are created as
	// a side effect of constructing a Client.
	//
	// true — CLI contract: the store is created (parent dir + file + schema)
	// on first use, matching the pre-refactor behavior of the binary.
	// The CLI's setupClient sets this to true.
	CreateIfMissing bool

	// TmuxClient is an optional injection seam for tests. When non-nil it
	// is used as-is instead of constructing a production *tmux.Client from
	// TmuxCommand or the PATH default. Pass a *tmuxfix.Recorder here to
	// capture tmux calls without launching real tmux. Setting this field
	// does not change the behavior of any production code path — it is
	// only consulted by New; a nil value (the zero default) preserves the
	// original construction logic exactly.
	TmuxClient TmuxClient
}
