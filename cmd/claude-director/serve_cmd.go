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

	"github.com/gabemahoney/claude-director/internal/api"
	"github.com/gabemahoney/claude-director/internal/config"
	"github.com/gabemahoney/claude-director/internal/mcp"
	"github.com/gabemahoney/claude-director/internal/probe"
	"github.com/gabemahoney/claude-director/internal/spawn"
	"github.com/gabemahoney/claude-director/internal/store"
	"github.com/gabemahoney/claude-director/internal/tmux"
)

// serveHandlerWith implements `claude-director serve --stdio`.
// Without --stdio, prints usage. The verb is long-lived per SRD §3.3:
// once started, the process loops until stdin EOF (the MCP client
// has hung up) or a fatal stdio error.
//
// Config is loaded ONCE at startup; in-flight edits to
// ~/.claude-director/config.toml do not take effect until the next
// `serve` invocation. SRD §3.3 makes this explicit so operators
// don't have to wonder why a tweak to relay.timeout_seconds didn't
// stick.
func serveHandlerWith(st *store.Store, cfg config.Config, args []string) error {
	var stdioFlag bool
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&stdioFlag, "stdio", false, "enter the stdio MCP loop")
	if err := fs.Parse(args); err != nil {
		return writeApiErrorAndDispatch("ErrInvalidFlags", err.Error())
	}
	if !stdioFlag {
		fmt.Fprintln(os.Stderr, "usage: claude-director serve --stdio")
		fmt.Fprintln(os.Stderr, "  Run the stdio MCP server. Register with:")
		fmt.Fprintln(os.Stderr, "    claude mcp add claude-director <binary-path> serve --stdio")
		return nil
	}

	registerMCPErrors()

	dispatcher := mcp.NewLiveDispatcher(st, tmuxClient, cfg)
	// MCP server logs go to the configured error log (or stderr
	// fallback) — NOT stdout, which is reserved for the JSON-RPC
	// transport.
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
func newMCPLogger(cfg config.Config) *log.Logger {
	dest := io.Writer(os.Stderr)
	if cfg.Log.ErrorLogPath != "" {
		if f, err := os.OpenFile(cfg.Log.ErrorLogPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
			dest = f
		}
	}
	return log.New(dest, "claude-director-mcp ", log.LstdFlags)
}

// registerMCPErrors populates the MCP server's err-name probe table
// from the CLI's errCatalog. This keeps the two views synchronized:
// the CLI's classifyError and the MCP server's classifyDispatchError
// surface the same canonical names for the same wrapped errors.
//
// Called once per `serve --stdio` invocation; idempotent across
// multiple calls in the same process.
func registerMCPErrors() {
	for _, ec := range errCatalog {
		mcp.RegisterError(ec.name, ec.err)
	}
	// Also register internal/mcp's own sentinel so a tools/call for
	// an unrecognized verb surfaces a stable err_name.
	mcp.RegisterError("ErrUnknownTool", mcp.ErrUnknownTool)
}

// Compile-time references so the imports stay live when the
// dispatcher's full surface is consumed by tests.
var (
	_ = api.Spawn
	_ = spawn.Permissions{}
	_ = probe.New
	_ = tmux.New
)
