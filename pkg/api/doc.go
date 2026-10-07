// Package api is the public Go module surface for agent-director.
//
// Import path: github.com/gabemahoney/agent-director/pkg/api
//
// # Overview
//
// agent-director spawns, tracks, and drives Claude Code instances inside tmux
// sessions. pkg/api is the single entry point for Go library callers: it
// exposes an opaque [Client] handle with one method per CLI verb. The CLI
// binary (cmd/agent-director) and the stdio MCP server (internal/mcp) both
// dispatch through this same Client — no business logic is duplicated.
//
// # Client lifecycle
//
// Construct a Client with [New], call verb methods, then release resources with
// [Client.Close]:
//
//	client, err := api.New(api.Options{})
//	if err != nil {
//	    // handle construction error
//	}
//	defer client.Close()
//
//	result, err := client.Spawn(api.SpawnParams{CWD: "/path/to/project"})
//
// [Client.Close] is idempotent — calling it more than once is safe and returns
// nil each time. After Close returns, any verb method call on the same Client
// returns [ErrClientClosed].
//
// # Error handling
//
// All errors returned by Client methods are matched via [errors.Is]. A verb
// method's "Errors:" block in its own godoc lists its verb's manifest error
// names: the sentinels catalogued for that verb on every surface (CLI, MCP,
// TypeScript client and Go); "Errors: none" means the verb has none.
// Sentinels common to all methods, such as [ErrClientClosed], are not
// repeated there, and a refusal only a Go caller can reach is stated in the
// method's prose instead. Sentinels exported directly from this package
// (pkg/api) are named api.ErrXxx; some sentinels originate in internal
// packages but are re-exported here for caller convenience (see
// [ErrSpawnNotFound], [ErrTmuxSessionCreate], etc.).
//
// Construction errors from [New] can be detected as follows:
//
//	client, err := api.New(api.Options{CreateIfMissing: false})
//	if errors.Is(err, api.ErrStoreNotInitialized) {
//	    // store does not exist; run with CreateIfMissing:true or init first
//	}
//	if errors.Is(err, api.ErrSchemaMismatch) {
//	    // store unusable by this binary; see ErrSchemaMismatch (install the
//	    // matching binary, or restore the pre-install copy of state.db)
//	}
//	if errors.Is(err, api.ErrSchemaMigrationRequired) {
//	    // DB is older than this binary; migration must be performed by an
//	    // administrator via the agent-director install process
//	}
//
// # Verb reference
//
// For per-verb CLI usage see docs/cli-reference.md (generated from the
// manifest). For the rendered Go API reference see
// https://pkg.go.dev/github.com/gabemahoney/agent-director/pkg/api.
// The [pkg/api/manifest] package is the single source of truth for verb
// names, parameters, result fields, and error names; it is consumed by the
// CLI, MCP server, and the doc-drift CI gate.
//
// # Design contract
//
// This package is the stable public surface through which library callers,
// the CLI entrypoint, and the MCP server interact with agent-director. All
// business logic lives in the internal packages; this package only composes
// them and exposes a clean, versioned API.
//
// Key invariants kept here verbatim so code review can grep for them:
//
//  1. No business logic in cmd/ — every verb is implemented in pkg/api
//     or lower; cmd/ only marshals results to JSON and calls New().
//
//  2. No schema-init side effects in the constructor — New() with
//     Options.CreateIfMissing == false (the library default) will NOT create
//     the database file or its parent directory, and will NOT run any DDL. A
//     missing store yields a typed ErrStoreNotInitialized-wrapping error that
//     callers can detect via errors.Is.
//
//  3. CreateIfMissing opt-in — CLI callers set Options.CreateIfMissing = true
//     to preserve the first-run UX (store created automatically on first
//     invocation). Library callers default to false and must initialize the
//     store out-of-band.
package api
