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
// stick.
//
// Pin H4: the MCP dispatcher uses a SEPARATE *pkgapi.Client constructed
// with Options.Logger: nil. This preserves the pre-refactor behavior where
// Kill/FindMissing/Expire swallowed their tmux WARN logs on the MCP path
// (those warnings are most useful to the interactive CLI operator, not a
// long-lived MCP client). The two Clients have distinct logger ownership.
//
// Pin H6: cfg is threaded in directly from run() via setupClient() so
// newMCPLogger can receive it without a Client.Config() accessor, which
// would leak internal/config.Config into pkg/api's public surface.
func serveHandlerWith(cfg config.Config, args []string) error {
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

	// Construct a SEPARATE Client for the MCP dispatcher (Pin H4).
	// Logger: nil so Kill/FindMissing/Expire WARN paths are silent for MCP.
	// CreateIfMissing: true so the MCP server can create the DB on first run.
	mcpClient, err := pkgapi.New(pkgapi.Options{
		ConfigPath:      configPath,
		CreateIfMissing: true,
		Logger:          nil, // intentional: MCP path discards WARN logs (Pin H4)
	})
	if err != nil {
		return writeApiErrorAndDispatch("ErrStoreOpen", err.Error())
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

