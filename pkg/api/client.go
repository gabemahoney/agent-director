package api

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/probe"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/tmux"
)

// defaultConfigPath is the canonical TOML config location, matching the
// path the CLI uses. A leading "~/" is tilde-expanded at construction time.
const defaultConfigPath = "~/.agent-director/config.toml"

// defaultStorePath is the hardcoded last-resort fallback for the store path
// (tier 3 of the three-tier StorePath precedence).
const defaultStorePath = "~/.agent-director/state.db"

// TmuxClient is the client-level tmux injection point (Options.TmuxClient;
// SRD Appendix F.3). Every socket-taking method takes the socket (SR-3.3)
// and reports a failure as *TmuxCallError; an error of any other type from
// an injected implementation counts as TmuxFailUnrecognized for that call.
// HasSession, the one name-based method, stays (SR-2.1; SR-16.2 item 2) and
// matches by prefix: no verb uses it, and none may.
// *tmux.Client and tmuxfix.Recorder implement it.
type TmuxClient interface {
	// Lookup makes the one-call lookup on socket: sessions with labels and
	// the scope reads.
	Lookup(socket string) (TmuxLookupAnswer, error)
	// ListPanes lists every pane of the server at socket, each with its pane
	// label's token (TmuxPane.AdPane).
	ListPanes(socket string) ([]TmuxPane, error)
	// KillPane kills the pane paneID on socket.
	KillPane(socket, paneID string) error
	// KillSessionID kills the session sessionID on socket.
	KillSessionID(socket, sessionID string) error
	// SendKeysPane types text into the pane paneID on socket, then Enter when pressEnter is set.
	SendKeysPane(socket, paneID, text string, pressEnter bool) error
	// SendKeyPane sends one key, by its tmux key name and never typed
	// literally, to the pane paneID on socket (pause's C-u).
	SendKeyPane(socket, paneID, key string) error
	// CapturePaneID returns the last nLines lines of the pane paneID on socket.
	CapturePaneID(socket, paneID string, nLines int, ansi bool) (string, error)
	// NewSession creates the session name on socket with its chained labels,
	// the session label "ad1 <token> <session id> <instance id> <store id>",
	// storeID being this store's (*store.Store).StoreID(), and the pane label
	// "<token> <pane id>" on its pane, and returns the create reply.
	NewSession(socket, name, cwd string, envs map[string]string, command []string, token, instanceID, storeID string) (TmuxCreateReply, error)
	// SetLabel labels, in one call, the session sessionID on socket by its id
	// with "ad1 <token> <session id> <instance id> <store id>" and its pane
	// paneID by its id with "<token> <pane id>".
	SetLabel(socket, sessionID, paneID, token, instanceID, storeID string) error
	// HasSession reports whether a session whose name begins with name
	// exists (prefix match; stays per SR-2.1). No verb uses it, and none
	// may: a verb finds a session by its label (Lookup).
	HasSession(name string) (bool, error)
}

// The production client satisfies TmuxClient (tmuxfix.Recorder's assertion
// is in a test file: pkg/api must not import tmuxfix).
var _ TmuxClient = (*tmux.Client)(nil)

// Client is the opaque handle through which callers interact with
// agent-director. Obtain one via New; release resources with Close.
//
// Client is safe for concurrent use: the closed flag is mutex-guarded so
// concurrent Close calls and (Task 2+) verb method calls from multiple
// goroutines are race-free.
type Client struct {
	st         *store.Store
	tmuxClient TmuxClient
	cfg        config.Config
	logger     *log.Logger
	// now is the Client's clock, time.Now in production (Appendix F.5); the
	// spawn's launch start, kill's process wait and find-missing's pending
	// grace period read it. Tests replace it per Client.
	now func() time.Time
	// procChecker is the start-time reader (SR-3.8), probe.NewProcChecker in
	// production; the spawn's identity write reads the server's and the
	// pane's start times through it, kill judges processes with it, and
	// read-pane's lookup checks the server with it.
	// Tests replace it per Client.
	procChecker ProcChecker
	// sleep pauses kill's process wait between two readings (SR-6.1),
	// time.Sleep in production. Tests replace it per Client, so the shared
	// test clock advances in virtual time (Appendix F.5).
	sleep  func(time.Duration)
	mu     sync.Mutex
	closed bool
}

// New constructs a Client from opts, wiring config, store, and tmux.
//
// Startup sequence:
//  1. Apply defaults (ConfigPath, Logger).
//  2. Load config from the resolved ConfigPath.
//  3. Resolve StorePath via three-tier precedence.
//  4. Open (or init) the store according to opts.CreateIfMissing.
//  5. Construct the tmux client: an injected Options.TmuxClient as given,
//     otherwise the production client for Options.TmuxCommand with the
//     query, action and create timeouts and the pipe-close wait taken from
//     the loaded config's [tmux] table at construction (SR-2.4, SR-4.1), so
//     a changed value applies to the next Client built.
//  6. Set the Client's clock (time.Now), its sleep (time.Sleep) and the
//     production start-time reader (probe.NewProcChecker). This store's id is read once by the
//     store's open (Store.StoreID) and used from there.
//
// On any error a nil *Client is returned together with a descriptive,
// errors.Is-matchable error. The constructor never leaves partially-
// opened resources behind.
//
// # Construction errors
//
// The two most common typed construction errors are:
//
//   - [ErrStoreNotInitialized]: returned when opts.CreateIfMissing is false
//     (the library default) and the database file does not exist. Either set
//     CreateIfMissing: true or initialize the store out-of-band before calling
//     New.
//
//   - [ErrSchemaMismatch]: returned when this binary cannot use the store.
//     For a store newer than the binary, install the matching binary. For a
//     store at the current version without a valid store id (only a hand
//     edit causes this), restore the copy of state.db taken before the
//     install. Never delete state.db. See docs/architecture.md
//     "ErrSchemaMismatch recovery".
//
//   - [ErrSchemaMigrationRequired]: returned when the database is older than
//     the schema version this binary understands and no valid administrator
//     authorization was presented. The store never auto-migrates on open; the
//     upgrade must be performed by an administrator via the agent-director
//     install process.
//
// All are detectable via errors.Is:
//
//	client, err := api.New(api.Options{})
//	if errors.Is(err, api.ErrStoreNotInitialized)     { /* ... */ }
//	if errors.Is(err, api.ErrSchemaMismatch)          { /* ... */ }
//	if errors.Is(err, api.ErrSchemaMigrationRequired) { /* ... */ }
//
// # Lifecycle guarantee
//
// [Client.Close] is safe to call exactly once after New succeeds. It releases
// the store connection and any tmux-client resources. The idempotent close
// contract means a deferred Close is always safe even if the caller also calls
// Close explicitly on an early return path.
func New(opts Options) (*Client, error) {
	// Step 1 — resolve ConfigPath default.
	cfgPath := opts.ConfigPath
	if cfgPath == "" {
		cfgPath = defaultConfigPath
	}
	cfgPath, err := expandTilde(cfgPath)
	if err != nil {
		return nil, fmt.Errorf("api: expand config path: %w", err)
	}

	// Step 1b — resolve Logger default.
	logger := opts.Logger
	if logger == nil {
		logger = log.New(io.Discard, "", 0)
	}

	// Step 2 — load config.
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return nil, fmt.Errorf("api: load config: %w", err)
	}

	// Step 3 — resolve StorePath via three-tier precedence.
	storePath, err := resolveStorePath(opts.StorePath, cfg)
	if err != nil {
		return nil, fmt.Errorf("api: resolve store path: %w", err)
	}

	// Step 4 — open the store.
	var st *store.Store
	if opts.CreateIfMissing {
		st, err = store.OpenOrInit(storePath)
	} else {
		st, err = store.Open(storePath)
	}
	if err != nil {
		// Wrap unconditionally; errors.Is(err, store.ErrStoreNotInitialized)
		// and errors.Is(err, store.ErrSchemaMismatch) still work on callers
		// because %w preserves the chain.
		return nil, fmt.Errorf("api: open store: %w", err)
	}

	// Step 5 — resolve tmux client.
	// opts.TmuxClient is an opt-in injection seam for tests (e.g. a
	// *tmuxfix.Recorder), used exactly as given. When nil (the production
	// default), a real *tmux.Client is constructed from TmuxCommand or the
	// PATH default, with the [tmux] timeouts and pipe-close wait from cfg.
	var tc TmuxClient
	if opts.TmuxClient != nil {
		tc = opts.TmuxClient
	} else {
		tc = tmux.New(opts.TmuxCommand, tmuxTimeouts(cfg.Tmux))
	}

	return &Client{
		st:          st,
		tmuxClient:  tc,
		cfg:         cfg,
		logger:      logger,
		now:         time.Now,
		procChecker: probe.NewProcChecker(),
		sleep:       time.Sleep,
	}, nil
}

// tmuxTimeouts returns the production tmux client's per-class timeouts and
// pipe-close wait from the [tmux] table, through config.Tmux's accessors only
// (missing or 0 gives the default there; SR-2.4, SR-4.1). internal/tmux
// defines no default and never imports internal/config, so this is the one
// place the values are handed over.
func tmuxTimeouts(t config.Tmux) tmux.Timeouts {
	return tmux.Timeouts{
		Query:     t.EffectiveQueryTimeout(),
		Action:    t.EffectiveActionTimeout(),
		Create:    t.EffectiveCreateTimeout(),
		WaitDelay: t.EffectivePipeCloseWait(),
	}
}

// Close releases the resources held by the Client. It is idempotent: a
// second call returns nil without double-closing the underlying store.
//
// After Close returns, any subsequent verb method calls on the Client will
// return ErrClientClosed.
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return nil
	}
	c.closed = true
	return c.st.Close()
}

// checkClosed is a mutex-guarded helper used by every verb method. It acquires
// the Client's mutex, checks the closed flag, and returns ErrClientClosed if
// the Client has been closed. The lock is released before returning so the
// caller holds no lock when it invokes the underlying handler function.
//
// Correct usage:
//
//	if err := c.checkClosed(); err != nil {
//	    return ZeroResult{}, err
//	}
func (c *Client) checkClosed() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return ErrClientClosed
	}
	return nil
}

// resolveStorePath applies the three-tier StorePath precedence rule:
//  1. opts.StorePath if non-empty (tilde-expanded).
//  2. cfg.Store.DbPath if non-empty (already expanded by config.Load).
//  3. defaultStorePath (tilde-expanded).
func resolveStorePath(optsStorePath string, cfg config.Config) (string, error) {
	if optsStorePath != "" {
		return expandTilde(optsStorePath)
	}
	if cfg.Store.DbPath != "" {
		return cfg.Store.DbPath, nil
	}
	return expandTilde(defaultStorePath)
}

// expandTilde resolves a leading "~/" using os.UserHomeDir (honours $HOME,
// consistent with internal/config and shell convention).
func expandTilde(path string) (string, error) {
	if !strings.HasPrefix(path, "~/") {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("expand tilde: %w", err)
	}
	return filepath.Join(home, path[2:]), nil
}
