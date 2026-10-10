package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/gabemahoney/agent-director/internal/clisetup"
	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/mcp"
	pkgapi "github.com/gabemahoney/agent-director/pkg/api"
)

// serveHandlerWith implements `agent-director serve --stdio`.
// Without --stdio, prints usage. The verb is long-lived per SRD §3.3:
// once started, the process loops until stdin EOF (the MCP client
// has hung up) or a fatal stdio error.
//
// Config is loaded ONCE at startup; in-flight edits to
// ~/.agent-director/config.toml do not take effect until the next
// `serve` invocation. SRD §3.3 makes this explicit so operators
// don't have to wonder why a tweak to relay.timeout_seconds didn't
// stick. The same holds for the [tmux] table: the MCP dispatcher's Client
// is built through pkgapi.New at startup, which hands the query, action
// and create timeouts and the pipe-close wait to its tmux client then, so
// a changed value applies after a restart (SR-4.1).
//
// Pin H4: the MCP dispatcher uses a SEPARATE *pkgapi.Client constructed
// with Options.Logger: nil, so the verbs that still log WARN lines (Spawn,
// Resume, FindMissing, Expire) stay silent on the MCP path (those warnings
// are most useful to the interactive CLI operator, not a long-lived MCP
// client). Kill has no logger path at all (SR-6.3): its errors reach the MCP
// caller in the envelope and its audit is the ad.kill.called trail event.
// The two Clients have distinct logger ownership.
//
// o is the run's global --store-path and --tmux-command overrides, the same
// ones setupClient opened run()'s Client with (b.32k). The MCP Client is built
// from them too (mcpClientOptions), so the MCP tools use the same store and
// tmux as setupClient's Client: the store serve opened and checked at startup
// (b.wb7). With no overrides both fall back alike, to the config's db_path or
// the default store (Pin 2) and to the tmux on PATH. --home needs no
// threading: run() set HOME before either Client expands a "~/" path.
//
// newClient opens the MCP Client: pkg/api.New in production (handlers()); a
// test passes a stand-in to reach the failure path. A failed open is named
// by clisetup.NewOpenError, as setupClient's Open names its own: a schema
// refusal is ErrSchemaMismatch or ErrSchemaMigrationRequired, anything else
// ErrStoreOpen (b.uii).
//
// Pin H6: cfg is threaded in directly from run() via setupClient() so
// newMCPLogger can receive it without a Client.Config() accessor, which
// would leak internal/config.Config into pkg/api's public surface.
func serveHandlerWith(cfg config.Config, o clisetup.Overrides,
	newClient func(pkgapi.Options) (*pkgapi.Client, error), args []string) error {
	var stdioFlag bool
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&stdioFlag, "stdio", false, "enter the stdio MCP loop")
	if err := fs.Parse(args); err != nil {
		return writeApiErrorAndDispatch("ErrInvalidFlags", err.Error())
	}
	if !stdioFlag {
		fmt.Fprintln(os.Stderr, "usage: agent-director serve --stdio")
		fmt.Fprintln(os.Stderr, "  Run the stdio MCP server. Register with:")
		fmt.Fprintln(os.Stderr, "    claude mcp add agent-director <binary-path> serve --stdio")
		return nil
	}

	// Construct a SEPARATE Client for the MCP dispatcher (Pin H4), on the
	// store and tmux command o selects (b.wb7), and name a failed open as
	// clisetup.Open does (b.uii).
	mcpClient, err := newClient(mcpClientOptions(o))
	if err != nil {
		oe := clisetup.NewOpenError(err)
		return writeApiErrorAndDispatch(oe.Name, oe.Error())
	}
	defer mcpClient.Close()

	dispatcher := mcp.NewLiveDispatcher(mcpClient)
	// MCP server logs go to the configured error log (or stderr
	// fallback) — NOT stdout, which is reserved for the JSON-RPC
	// transport. cfg is passed directly (Pin H6).
	logger := newMCPLogger(cfg)
	server := mcp.New(dispatcher, logger)

	// Signal-aware ctx so SIGINT / SIGTERM (e.g. Kubernetes graceful
	// shutdown) flow as cancellation instead of killing the process
	// outright — defers (store close, WAL checkpoint) must run.
	ctx, stop := newSignalCtx()
	defer stop()

	// bufio.Scanner inside Serve blocks on stdin, so ctx alone won't
	// unblock it. Close stdin on cancellation so Serve can return.
	stdin := os.Stdin
	go func() {
		<-ctx.Done()
		_ = stdin.Close()
	}()

	return server.Serve(ctx, stdin, os.Stdout)
}

// mcpClientOptions returns the pkg/api.Options of the MCP dispatcher's Client:
// clisetup.APIOptions(o), the options setupClient's Client is built from, so
// the --store-path and --tmux-command overrides in o reach the MCP tools
// (b.wb7), and CreateIfMissing is true so serve can create the store on first
// run. Logger stays nil so the verbs that still log (Spawn, Resume,
// FindMissing, Expire) are silent for MCP; Kill has no logger path at all
// (SR-6.3).
func mcpClientOptions(o clisetup.Overrides) pkgapi.Options {
	opts := clisetup.APIOptions(o)
	opts.Logger = nil // intentional: logging verbs are silent on MCP; kill has no logger path (Pin H4)
	return opts
}

// newSignalCtx returns a context that is canceled when the process
// receives SIGINT or SIGTERM. Callers MUST defer the returned stop so
// the ctx is canceled (and signal handlers removed) on normal exit too.
//
// Extracted as a package-level helper so the signal wiring is unit-
// testable without spawning a subprocess.
func newSignalCtx() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

// newMCPLogger routes MCP operational diagnostics to the configured
// error log. Stdout is reserved for the JSON-RPC transport, so the
// log destination must NOT be stdout.
//
// cfg is passed directly from run() (Pin H6 — no Client.Config() accessor).
func newMCPLogger(cfg config.Config) *log.Logger {
	dest := io.Writer(os.Stderr)
	if cfg.Log.ErrorLogPath != "" {
		if f, err := os.OpenFile(cfg.Log.ErrorLogPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
			dest = f
		}
	}
	return log.New(dest, "agent-director-mcp ", log.LstdFlags)
}
