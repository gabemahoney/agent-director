// Package errnames is the canonical err_name catalog for agent-director.
// It is the single source of truth for the CLI's Classify function and the
// MCP server's tools/call err_name mapping.
//
//go:generate go run ./generate.go
package errnames
