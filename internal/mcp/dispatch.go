package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/gabemahoney/agent-director/internal/api/manifest"
	api "github.com/gabemahoney/agent-director/pkg/api"
)

// LiveDispatcher routes MCP tool calls to pkg/api.Client methods.
// One instance per MCP server process; the Client is opened once at
// startup per SRD §3.3.
type LiveDispatcher struct {
	client *api.Client
}

// NewLiveDispatcher constructs the dispatcher with the supplied Client.
// The caller (cmd/-side) opens the Client; this type is a thin facade
// that owns the tool-name → Client method map.
func NewLiveDispatcher(client *api.Client) *LiveDispatcher {
	return &LiveDispatcher{client: client}
}

// Call routes one tool call. The tool name comes in as the MCP form
// (underscores); we convert to the verb form (hyphens) before
// dispatching to the Client.
//
// Each tool decodes the MCP arguments map into its typed params
// struct via json.Unmarshal round-trip; the manifest's per-param
// types match the struct field tags so the decode is trivial.
func (d *LiveDispatcher) Call(ctx context.Context, toolName string, args json.RawMessage) (any, error) {
	verbName := VerbNameFromTool(toolName)
	if args == nil || len(args) == 0 {
		args = json.RawMessage("{}")
	}

	switch verbName {
	case "help":
		out := make([]api.VerbSummary, 0, len(manifest.Verbs))
		for _, v := range manifest.Verbs {
			out = append(out, api.VerbSummary{
				Name:        v.Name,
				Description: v.Description,
			})
		}
		return map[string]any{"verbs": out}, nil

	case "version":
		return d.client.Version()

	case "spawn":
		// Field names match what the manifest publishes via tools/list
		// (and what docs/mcp-reference.md advertises). Labels arrive as
		// a []string of "k=v" entries; permissions arrive as three
		// independent allow/deny/ask arrays, not a nested object.
		var raw struct {
			CWD              string            `json:"cwd"`
			Template         string            `json:"template"`
			ClaudeInstanceID string            `json:"claude_instance_id"`
			Label            []string          `json:"label"`
			Allow            []string          `json:"allow"`
			Deny             []string          `json:"deny"`
			Ask              []string          `json:"ask"`
			RelayMode        string            `json:"relay-mode"`
			ExtraEnv         map[string]string `json:"extra-env"`
			ClaudeArgs       []string          `json:"claude_args"`
		}
		if err := json.Unmarshal(args, &raw); err != nil {
			return nil, fmt.Errorf("decode spawn params: %w", err)
		}
		labels := make(map[string]string, len(raw.Label))
		for _, kv := range raw.Label {
			k, v, ok := splitKV(kv)
			if !ok {
				return nil, fmt.Errorf("invalid label %q (want key=value)", kv)
			}
			labels[k] = v
		}
		p := api.SpawnParams{
			CWD:                 raw.CWD,
			Template:            raw.Template,
			ClaudeInstanceID:    raw.ClaudeInstanceID,
			ExtraEnv:            raw.ExtraEnv,
			AgentDirectorLabels: labels,
			ClaudeArgs:          raw.ClaudeArgs,
			RelayMode:           raw.RelayMode,
		}
		if len(raw.Allow) > 0 || len(raw.Deny) > 0 || len(raw.Ask) > 0 {
			p.Permissions = &api.Permissions{
				Allow: raw.Allow,
				Deny:  raw.Deny,
				Ask:   raw.Ask,
			}
		}
		return d.client.Spawn(p)

	case "status":
		var p struct {
			ClaudeInstanceID string `json:"claude_instance_id"`
		}
		if err := json.Unmarshal(args, &p); err != nil {
			return nil, fmt.Errorf("decode status params: %w", err)
		}
		return d.client.Status(p.ClaudeInstanceID)

	case "get":
		var p struct {
			ClaudeInstanceID string `json:"claude_instance_id"`
		}
		if err := json.Unmarshal(args, &p); err != nil {
			return nil, fmt.Errorf("decode get params: %w", err)
		}
		return d.client.Get(p.ClaudeInstanceID)

	case "send-keys":
		var p api.SendKeysParams
		if err := unmarshalSnake(args, &p); err != nil {
			return nil, err
		}
		return d.client.SendKeys(p)

	case "read-pane":
		var p api.ReadPaneParams
		if err := unmarshalSnake(args, &p); err != nil {
			return nil, err
		}
		return d.client.ReadPane(p)

	case "kill":
		var p api.KillParams
		if err := unmarshalSnake(args, &p); err != nil {
			return nil, err
		}
		// nil logger: the MCP-side caller sees errors via the API
		// envelope; the swallowed-tmux WARN is most useful to the
		// interactive CLI operator, not a long-lived MCP client.
		// The MCP Client is constructed with Options.Logger: nil so
		// c.logger is already a discard logger — no explicit nil pass needed.
		return d.client.Kill(p)

	case "pause":
		var p api.PauseParams
		if err := unmarshalSnake(args, &p); err != nil {
			return nil, err
		}
		return d.client.Pause(ctx, p)

	case "resume":
		var p api.ResumeParams
		if err := unmarshalSnake(args, &p); err != nil {
			return nil, err
		}
		return d.client.Resume(p)

	case "list":
		var raw struct {
			State  []string `json:"state"`
			Label  []string `json:"label"`
			Parent string   `json:"parent"`
			Cwd    string   `json:"cwd"`
			Limit  int      `json:"limit"`
		}
		if err := json.Unmarshal(args, &raw); err != nil {
			return nil, fmt.Errorf("decode list params: %w", err)
		}
		return d.client.List(api.ListParams{
			State:  raw.State,
			Labels: raw.Label,
			Parent: raw.Parent,
			Cwd:    raw.Cwd,
			Limit:  raw.Limit,
		})

	case "make-template":
		var raw struct {
			Name       string            `json:"name"`
			CWD        string            `json:"cwd"`
			RelayMode  string            `json:"relay_mode"`
			ClaudeArgs []string          `json:"claude_args"`
			ExtraEnv   map[string]string `json:"extra_env"`
			Label      []string          `json:"label"`
			Allow      []string          `json:"allow"`
			Deny       []string          `json:"deny"`
			Ask        []string          `json:"ask"`
		}
		if err := json.Unmarshal(args, &raw); err != nil {
			return nil, fmt.Errorf("decode make-template params: %w", err)
		}
		labels := make(map[string]string, len(raw.Label))
		for _, kv := range raw.Label {
			k, v, ok := splitKV(kv)
			if !ok {
				return nil, fmt.Errorf("invalid label %q (want key=value)", kv)
			}
			labels[k] = v
		}
		p := api.MakeTemplateParams{
			Name:                raw.Name,
			CWD:                 raw.CWD,
			RelayMode:           raw.RelayMode,
			ClaudeArgs:          raw.ClaudeArgs,
			ExtraEnv:            raw.ExtraEnv,
			AgentDirectorLabels: labels,
		}
		if len(raw.Allow) > 0 || len(raw.Deny) > 0 || len(raw.Ask) > 0 {
			p.Permissions = &api.MakeTemplatePermissions{
				Allow: raw.Allow,
				Deny:  raw.Deny,
				Ask:   raw.Ask,
			}
		}
		return d.client.MakeTemplate(p)

	case "find-missing":
		return d.client.FindMissing(ctx)

	case "expire":
		var raw struct {
			OlderThan string `json:"older_than"`
		}
		if err := json.Unmarshal(args, &raw); err != nil {
			return nil, fmt.Errorf("decode expire params: %w", err)
		}
		var older *time.Duration
		if raw.OlderThan != "" {
			dur, err := parseDuration(raw.OlderThan)
			if err != nil {
				return nil, fmt.Errorf("expire older_than: %w", err)
			}
			older = &dur
		}
		return d.client.Expire(older)

	case "delete":
		var raw struct {
			ClaudeInstanceID []string `json:"claude_instance_id"`
		}
		if err := json.Unmarshal(args, &raw); err != nil {
			return nil, fmt.Errorf("decode delete params: %w", err)
		}
		if len(raw.ClaudeInstanceID) == 0 {
			return nil, fmt.Errorf("delete: claude_instance_id is required (≥1)")
		}
		return d.client.Delete(raw.ClaudeInstanceID)

	case "decide":
		var p api.DecideParams
		if err := unmarshalSnake(args, &p); err != nil {
			return nil, err
		}
		return d.client.Decide(p)

	default:
		return nil, fmt.Errorf("%w: %s", ErrUnknownTool, toolName)
	}
}

// unmarshalSnake is a small shim that decodes a JSON object into a
// struct whose field tags use snake_case keys. The api package's
// params structs already carry json: tags matching the SRD wire
// shape; this helper exists so future verbs that want to override
// per-field handling have one place to hook in.
func unmarshalSnake(args json.RawMessage, into any) error {
	if err := json.Unmarshal(args, into); err != nil {
		return fmt.Errorf("decode params: %w", err)
	}
	return nil
}

// splitKV parses a "key=value" string. Returns ok=false when there is
// no `=` separator.
func splitKV(kv string) (k, v string, ok bool) {
	i := strings.IndexByte(kv, '=')
	if i <= 0 {
		return "", "", false
	}
	return kv[:i], kv[i+1:], true
}

// parseDuration accepts Go's time.ParseDuration form plus a trailing-d
// days form. Mirrors the cmd/-side helper of the same intent so MCP
// callers can pass `7d` without knowing the underlying Go parser.
func parseDuration(s string) (time.Duration, error) {
	if n := len(s); n > 1 && s[n-1] == 'd' {
		var days int
		for _, c := range s[:n-1] {
			if c < '0' || c > '9' {
				return 0, fmt.Errorf("invalid duration: %s", s)
			}
			days = days*10 + int(c-'0')
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid duration: %s", s)
	}
	return d, nil
}
