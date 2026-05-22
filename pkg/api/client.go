package api

import (
	"fmt"
	"io"
	"log"
	"os/user"
	"path/filepath"
	"strings"
	"sync"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/tmux"
)

// defaultConfigPath is the canonical TOML config location, matching the
// path the CLI uses. A leading "~/" is tilde-expanded at construction time.
const defaultConfigPath = "~/.agent-director/config.toml"

// defaultStorePath is the hardcoded last-resort fallback for the store path
// (tier 3 of the three-tier StorePath precedence).
const defaultStorePath = "~/.agent-director/state.db"

// Client is the opaque handle through which callers interact with
// agent-director. Obtain one via New; release resources with Close.
//
// Client is safe for concurrent use: the closed flag is mutex-guarded so
// concurrent Close calls and (Task 2+) verb method calls from multiple
// goroutines are race-free.
type Client struct {
	st          *store.Store
	tmuxClient  *tmux.Client
	cfg         config.Config
	logger      *log.Logger
	mu          sync.Mutex
	closed      bool
}

// New constructs a Client from opts, wiring config, store, and tmux.
//
// Startup sequence:
//  1. Apply defaults (ConfigPath, Logger).
//  2. Load config from the resolved ConfigPath.
//  3. Resolve StorePath via three-tier precedence.
//  4. Open (or init) the store according to opts.CreateIfMissing.
//  5. Construct the tmux client.
//
// On any error a nil *Client is returned together with a descriptive,
// errors.Is-matchable error. The constructor never leaves partially-
// opened resources behind.
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

	// Step 5 — construct tmux client.
	tc := tmux.New()
	if opts.TmuxCommand != "" {
		tc = tmux.NewWithBinary(opts.TmuxCommand)
	}

	return &Client{
		st:         st,
		tmuxClient: tc,
		cfg:        cfg,
		logger:     logger,
	}, nil
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
// caller holds no lock when it invokes the underlying internal/api function.
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

// BridgeStore returns the underlying *store.Store. This is a temporary bridge
// used exclusively by cmd/agent-director/serve_cmd.go to pass st to
// mcp.NewLiveDispatcher until Task 4 (the MCP refactor) updates the dispatcher
// to accept a *Client directly.
//
// TODO(Task4): remove once mcp.NewLiveDispatcher accepts *Client.
func (c *Client) BridgeStore() *store.Store { return c.st }

// BridgeConfig returns the loaded config.Config. This is a temporary bridge
// used exclusively by cmd/agent-director/serve_cmd.go to pass cfg to
// mcp.NewLiveDispatcher until Task 4 (the MCP refactor) updates the dispatcher
// to accept a *Client directly.
//
// TODO(Task4): remove once mcp.NewLiveDispatcher accepts *Client.
func (c *Client) BridgeConfig() config.Config { return c.cfg }

// BridgeTmuxClient returns the underlying *tmux.Client. This is a temporary
// bridge used exclusively by cmd/agent-director/serve_cmd.go to pass the tmux
// client to mcp.NewLiveDispatcher until Task 4.
//
// TODO(Task4): remove once mcp.NewLiveDispatcher accepts *Client.
func (c *Client) BridgeTmuxClient() *tmux.Client { return c.tmuxClient }

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

// expandTilde resolves a leading "~/" in path against the current user's
// home directory. Paths without a leading "~/" are returned unchanged.
func expandTilde(path string) (string, error) {
	if !strings.HasPrefix(path, "~/") {
		return path, nil
	}
	u, err := user.Current()
	if err != nil {
		return "", fmt.Errorf("expand tilde: %w", err)
	}
	return filepath.Join(u.HomeDir, strings.TrimPrefix(path, "~/")), nil
}
