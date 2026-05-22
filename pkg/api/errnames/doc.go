// Package errnames is the canonical err_name catalog for agent-director.
// It is the single source of truth for the CLI's Classify function, the
// MCP server's tools/call err_name mapping, and (later) pkg/cabi's JSON
// err_name field.
//
//go:generate go run ./generate.go
package errnames
