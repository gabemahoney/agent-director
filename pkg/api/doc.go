// Package api is the exported facade for agent-director.
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
//  1. No business logic in cmd/ — every verb is implemented in internal/api
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
