// Package mcp implements a stdio Model Context Protocol server that
// exposes agent-director's CLI verbs as MCP tools (SRD §3.3, §4.1).
//
// The protocol is JSON-RPC 2.0 over line-delimited JSON on stdin/stdout.
// Three methods are wired: `initialize` (capability negotiation),
// `tools/list` (verb advertisement), `tools/call` (verb dispatch).
// Notifications (`notifications/initialized`) are accepted and dropped.
//
// The tool list is generated from `pkg/api/manifest.Verbs`, the
// same single source of truth that drives the CLI flags and the
// reference docs — adding a new verb to the manifest exposes it via
// MCP on next server start with no source changes here. The
// drift-by-construction invariant is pinned by a test that compares
// the tools/list output against `len(manifest.Verbs)`.
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"

	"github.com/gabemahoney/agent-director/pkg/api/errnames"
	"github.com/gabemahoney/agent-director/pkg/api/manifest"
)

// ProtocolVersion is the MCP protocol version this server advertises.
// 2024-11-05 is the stable schema Claude Code 2.x speaks; bumping
// requires a coordinated client/server upgrade.
const ProtocolVersion = "2024-11-05"

// ServerName + ServerVersion are returned in the initialize response.
// The version follows the binary's own version pin; for v1 we hard-
// code "0.1.0" — Epic 13 (release) will replace this with a build-
// time injected string.
const (
	ServerName    = "agent-director"
	ServerVersion = "0.1.0"
)

// Request is the JSON-RPC 2.0 request envelope. Notifications have
// no `id` field; the omitempty + pointer pattern lets us serialize
// both shapes from one struct.
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// Response is the JSON-RPC 2.0 response envelope. Exactly one of
// Result or Error is populated.
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *ResponseError  `json:"error,omitempty"`
}

// ResponseError is the JSON-RPC error object. Code values follow the
// JSON-RPC 2.0 spec: -32601 method not found, -32602 invalid params,
// -32603 internal error. Tool-execution failures use -32000 (server
// error) and carry the typed err_name + description in Data.
type ResponseError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// ToolErrorData is the structured payload attached to a tool-call
// error response. Carries the SRD §13.1 err_name + a human-readable
// description so MCP-aware clients can surface them programmatically.
type ToolErrorData struct {
	ErrName        string `json:"err_name"`
	ErrDescription string `json:"err_description"`
}

// Dispatcher is the seam the server uses to route tool calls to
// internal/api functions. Production wires a dispatcher backed by
// the live store + tmux + config; tests can swap in a fake to
// exercise routing without spinning up real infrastructure.
type Dispatcher interface {
	Call(ctx context.Context, name string, args json.RawMessage) (result any, err error)
}

// Server is the long-lived MCP server (SRD §3.3). One instance per
// process; config is loaded once at startup and not hot-reloaded.
type Server struct {
	dispatcher Dispatcher
	logger     *log.Logger
}

// New constructs a Server. The dispatcher carries the live wiring to
// internal/api; the logger receives operational diagnostics (NOT
// tool errors — those go in the JSON-RPC response).
func New(d Dispatcher, logger *log.Logger) *Server {
	if logger == nil {
		logger = log.New(io.Discard, "", 0)
	}
	return &Server{dispatcher: d, logger: logger}
}

// Serve runs the stdio loop. Each line from stdin is one JSON-RPC
// request; the response goes on its own line to stdout. The function
// exits cleanly on stdin EOF (Claude Code closes the pipe when the
// session ends) or on ctx.Done.
//
// A malformed line is responded to with a JSON-RPC parse error
// (-32700); subsequent lines are processed normally so a single bad
// message doesn't tear down the session.
func (s *Server) Serve(ctx context.Context, stdin io.Reader, stdout io.Writer) error {
	// The scanner's default buffer is 64KB — tool args can include
	// large JSON blobs (e.g. a spawn with many --extra-env entries),
	// so we bump the cap to 1MB which matches the hook handler's
	// MaxPayloadBytes.
	scanner := bufio.NewScanner(stdin)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	enc := json.NewEncoder(stdout)
	enc.SetEscapeHTML(false)

	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var req Request
		if err := json.Unmarshal(line, &req); err != nil {
			s.logger.Printf("mcp: parse error: %v (line=%q)", err, line)
			_ = enc.Encode(Response{
				JSONRPC: "2.0",
				Error: &ResponseError{
					Code:    -32700,
					Message: "parse error: " + err.Error(),
				},
			})
			continue
		}
		s.handle(ctx, &req, enc)
	}
	return scanner.Err()
}

// handle dispatches one parsed request. Notifications (no id) are
// processed for side effects only; the response is suppressed.
func (s *Server) handle(ctx context.Context, req *Request, enc *json.Encoder) {
	isNotification := len(req.ID) == 0

	respond := func(result any, errObj *ResponseError) {
		if isNotification {
			return
		}
		_ = enc.Encode(Response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  result,
			Error:   errObj,
		})
	}

	switch req.Method {
	case "initialize":
		respond(InitializeResult{
			ProtocolVersion: ProtocolVersion,
			Capabilities: ServerCapabilities{
				Tools: &ToolsCapability{ListChanged: false},
			},
			ServerInfo: ServerInfo{Name: ServerName, Version: ServerVersion},
		}, nil)

	case "notifications/initialized":
		// Standard MCP handshake — client tells us it's ready to
		// receive tool calls. No response.

	case "tools/list":
		respond(map[string]any{"tools": buildToolList()}, nil)

	case "tools/call":
		s.handleToolCall(ctx, req, respond)

	default:
		respond(nil, &ResponseError{
			Code:    -32601,
			Message: "method not found: " + req.Method,
		})
	}
}

// handleToolCall walks the dispatcher with the requested tool name +
// arguments. Success → MCP content shape (`content: [{type:"text",text:...}]`).
// Failure → JSON-RPC error with typed err_name in Data.
func (s *Server) handleToolCall(ctx context.Context, req *Request, respond func(any, *ResponseError)) {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &p); err != nil {
		respond(nil, &ResponseError{
			Code:    -32602,
			Message: "invalid params: " + err.Error(),
		})
		return
	}
	if p.Name == "" {
		respond(nil, &ResponseError{Code: -32602, Message: "tools/call: name is required"})
		return
	}

	result, err := s.dispatcher.Call(ctx, p.Name, p.Arguments)
	if err != nil {
		errName, errDesc := classifyDispatchError(err)
		s.logger.Printf("mcp: tools/call %s → %s: %s", p.Name, errName, errDesc)
		respond(nil, &ResponseError{
			Code:    -32000,
			Message: errDesc,
			Data:    ToolErrorData{ErrName: errName, ErrDescription: errDesc},
		})
		return
	}
	// MCP tool result envelope: `content` is an array of typed parts;
	// we emit one text part carrying the JSON serialization of the
	// verb's typed result struct. This mirrors the CLI's stdout shape
	// so a script reading the CLI and an MCP client see the same JSON.
	body, mErr := json.Marshal(result)
	if mErr != nil {
		respond(nil, &ResponseError{
			Code:    -32603,
			Message: "result marshal: " + mErr.Error(),
		})
		return
	}
	respond(map[string]any{
		"content": []any{
			map[string]any{"type": "text", "text": string(body)},
		},
	}, nil)
}

// ErrUnknownTool is returned by the MCP dispatcher when the requested
// tool name is not in the registered verb list. Declared here (not in
// pkg/api/errnames) because it is a dispatch-level error, not a
// verb-surface error: it has no callable VerbDef.ErrorNames entry and
// must not appear in errnames.Catalog (doing so would violate the
// five-way coherence invariant). The import direction is
// internal/mcp → pkg/api/errnames; the sentinel lives at the call site.
var ErrUnknownTool = errors.New("ErrUnknownTool")

// classifyDispatchError extracts the typed err_name from a Dispatcher
// error. ErrUnknownTool is checked explicitly first (it is a dispatch-level
// sentinel not in errnames.Catalog); all other errors are classified via
// errnames.Classify, which walks the Catalog via errors.Is. Unrecognized
// errors collapse to "ErrInternal" — matching the CLI's Classify behavior.
func classifyDispatchError(err error) (name, description string) {
	if errors.Is(err, ErrUnknownTool) {
		return "ErrUnknownTool", err.Error()
	}
	return errnames.Classify(err)
}

// InitializeResult is the MCP initialize response payload.
type InitializeResult struct {
	ProtocolVersion string             `json:"protocolVersion"`
	Capabilities    ServerCapabilities `json:"capabilities"`
	ServerInfo      ServerInfo         `json:"serverInfo"`
}

// ServerCapabilities advertises which MCP capability groups the
// server supports. We expose only `tools`; resources/prompts/etc.
// are out of scope for v1.
type ServerCapabilities struct {
	Tools *ToolsCapability `json:"tools,omitempty"`
}

// ToolsCapability captures the tools-feature flags. ListChanged=false
// means we never proactively notify the client of tool-list changes
// (the manifest is fixed at startup per SRD §3.3).
type ToolsCapability struct {
	ListChanged bool `json:"listChanged"`
}

// ServerInfo carries the human-readable server identity.
type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// buildToolList produces the tools/list response from
// manifest.Verbs. Filters out the `hook` verb (internal, not for MCP)
// and the `serve` verb (would be self-referential). Other verbs map
// to MCP tools 1:1.
func buildToolList() []map[string]any {
	out := make([]map[string]any, 0, len(manifest.Verbs))
	for _, v := range manifest.Verbs {
		if !ExposedVerb(v.Name) {
			continue
		}
		out = append(out, map[string]any{
			"name":        ToolName(v.Name),
			"description": v.Description,
			"inputSchema": buildInputSchema(v),
		})
	}
	return out
}

// ExposedVerb reports whether a manifest verb should be exposed as
// an MCP tool. The `hook` verb is internal (only Claude Code's hook
// machinery calls it); `serve` would be self-referential.
func ExposedVerb(name string) bool {
	switch name {
	case "hook", "serve":
		return false
	}
	return true
}

// ToolName converts a manifest verb name to its MCP tool name. The
// MCP convention is no-hyphens-or-special-chars; verb names like
// `send-keys`, `read-pane`, `make-template`, `find-missing` get
// underscores. Round-tripping is done in the dispatcher via the
// inverse mapping.
func ToolName(verbName string) string {
	out := make([]byte, 0, len(verbName))
	for i := 0; i < len(verbName); i++ {
		c := verbName[i]
		if c == '-' {
			out = append(out, '_')
		} else {
			out = append(out, c)
		}
	}
	return string(out)
}

// VerbNameFromTool is the inverse of ToolName. Underscores become
// hyphens so `send_keys` → `send-keys` etc. The mapping is faithful
// because no current verb name carries an underscore.
func VerbNameFromTool(toolName string) string {
	out := make([]byte, 0, len(toolName))
	for i := 0; i < len(toolName); i++ {
		c := toolName[i]
		if c == '_' {
			out = append(out, '-')
		} else {
			out = append(out, c)
		}
	}
	return string(out)
}

