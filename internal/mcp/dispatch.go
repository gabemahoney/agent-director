package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/gabemahoney/agent-director/internal/api"
	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/probe"
	"github.com/gabemahoney/agent-director/internal/spawn"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/tmux"
)

// LiveDispatcher routes MCP tool calls to internal/api functions
// using the live production wiring. One instance per MCP server
// process; the store + tmux client + config are opened once at
// startup per SRD §3.3.
type LiveDispatcher struct {
	store     *store.Store
	tmuxClient *tmux.Client
	cfg       config.Config
}

// NewLiveDispatcher constructs the dispatcher with the supplied
// wiring. The caller (cmd/-side) opens the store and config; this
// type is a thin facade that owns the tool-name → api function map.
func NewLiveDispatcher(st *store.Store, tmuxClient *tmux.Client, cfg config.Config) *LiveDispatcher {
	return &LiveDispatcher{store: st, tmuxClient: tmuxClient, cfg: cfg}
}

// Call routes one tool call. The tool name comes in as the MCP form
// (underscores); we convert to the verb form (hyphens) before
// dispatching to the api package.
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
		verbs, err := api.Help()
		if err != nil {
			return nil, err
		}
		return map[string]any{"verbs": verbs}, nil

	case "version":
		return api.Version()

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
		p := spawn.SpawnParams{
			CWD:                  raw.CWD,
			Template:             raw.Template,
			ClaudeInstanceID:     raw.ClaudeInstanceID,
			ExtraEnv:             raw.ExtraEnv,
			AgentDirectorLabels: labels,
			ClaudeArgs:           raw.ClaudeArgs,
			RelayMode:            raw.RelayMode,
		}
		if len(raw.Allow) > 0 || len(raw.Deny) > 0 || len(raw.Ask) > 0 {
			p.Permissions = &spawn.Permissions{
				Allow: raw.Allow,
				Deny:  raw.Deny,
				Ask:   raw.Ask,
			}
		}
		return api.Spawn(d.store, d.tmuxClient, d.cfg, p)

	case "status":
		var p struct {
			ClaudeInstanceID string `json:"claude_instance_id"`
		}
		if err := json.Unmarshal(args, &p); err != nil {
			return nil, fmt.Errorf("decode status params: %w", err)
		}
		return api.Status(d.store, p.ClaudeInstanceID)

	case "get":
		var p struct {
			ClaudeInstanceID string `json:"claude_instance_id"`
		}
		if err := json.Unmarshal(args, &p); err != nil {
			return nil, fmt.Errorf("decode get params: %w", err)
		}
		return api.Get(d.store, p.ClaudeInstanceID)

	case "send-keys":
		var p api.SendKeysParams
		if err := unmarshalSnake(args, &p); err != nil {
			return nil, err
		}
		return api.SendKeys(d.store, d.tmuxClient, p)

	case "read-pane":
		var p api.ReadPaneParams
		if err := unmarshalSnake(args, &p); err != nil {
			return nil, err
		}
		return api.ReadPane(d.store, d.tmuxClient, p)

	case "kill":
		var p api.KillParams
		if err := unmarshalSnake(args, &p); err != nil {
			return nil, err
		}
		// nil logger: the MCP-side caller sees errors via the API
		// envelope; the swallowed-tmux WARN is most useful to the
		// interactive CLI operator, not a long-lived MCP client.
		return api.Kill(d.store, d.tmuxClient, nil, p)

	case "pause":
		var p api.PauseParams
		if err := unmarshalSnake(args, &p); err != nil {
			return nil, err
		}
		return api.Pause(ctx, d.store, d.tmuxClient, d.cfg.Pause, p)

	case "resume":
		var p api.ResumeParams
		if err := unmarshalSnake(args, &p); err != nil {
			return nil, err
		}
		return api.Resume(d.store, d.tmuxClient, d.cfg, p)

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
		return api.List(d.store, api.ListParams{
			State:  raw.State,
			Labels: raw.Label,
			Parent: raw.Parent,
			Cwd:    raw.Cwd,
			Limit:  raw.Limit,
		})

	case "make-template":
		var raw struct {
			Name                 string                       `json:"name"`
			CWD                  string                       `json:"cwd"`
			RelayMode            string                       `json:"relay_mode"`
			ClaudeArgs           []string                     `json:"claude_args"`
			ExtraEnv             map[string]string            `json:"extra_env"`
			Label                []string                     `json:"label"`
			Allow                []string                     `json:"allow"`
			Deny                 []string                     `json:"deny"`
			Ask                  []string                     `json:"ask"`
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
			Name:                 raw.Name,
			CWD:                  raw.CWD,
			RelayMode:            raw.RelayMode,
			ClaudeArgs:           raw.ClaudeArgs,
			ExtraEnv:             raw.ExtraEnv,
			AgentDirectorLabels: labels,
		}
		if len(raw.Allow) > 0 || len(raw.Deny) > 0 || len(raw.Ask) > 0 {
			p.Permissions = &api.MakeTemplatePermissions{
				Allow: raw.Allow,
				Deny:  raw.Deny,
				Ask:   raw.Ask,
			}
		}
		return api.MakeTemplate(p)

	case "find-missing":
		return api.FindMissing(ctx, d.store, probe.New(), nil)

	case "expire":
		var raw struct {
			OlderThan string `json:"older_than"`
		}
		if err := json.Unmarshal(args, &raw); err != nil {
			return nil, fmt.Errorf("decode expire params: %w", err)
		}
		var older *time.Duration
		if raw.OlderThan != "" {
			d, err := parseDuration(raw.OlderThan)
			if err != nil {
				return nil, fmt.Errorf("expire older_than: %w", err)
			}
			older = &d
		}
		return api.Expire(d.store, d.cfg, older, nil)

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
		return api.Delete(d.store, raw.ClaudeInstanceID)

	case "decide":
		var p api.DecideParams
		if err := unmarshalSnake(args, &p); err != nil {
			return nil, err
		}
		return api.Decide(d.store, p)

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
