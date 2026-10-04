package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	api "github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/manifest"
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
// Every exposed tool first checks the top-level keys of its arguments
// against its verb's manifest params (checkParamNames): any other key is
// refused with ErrInvalidFlags before anything is decoded or run (b.c4u).
// Each tool then decodes the arguments into its typed params struct via a
// json.Unmarshal round-trip: every manifest param has a typed field
// tagged with its manifest name (b.7or), so a wrong-typed value is a decode
// error naming the param and nothing runs.
func (d *LiveDispatcher) Call(ctx context.Context, toolName string, args json.RawMessage) (any, error) {
	verbName := VerbNameFromTool(toolName)
	if args == nil || len(args) == 0 {
		args = json.RawMessage("{}")
	}
	if v, ok := manifest.Lookup(verbName); ok && ExposedVerb(verbName) {
		if err := checkParamNames(v, args); err != nil {
			return nil, err
		}
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
		// tmux_session_name is a pointer so a present key is supplied,
		// even when empty (ErrTmuxSessionNameEmpty), while an absent or
		// null one is not, as with the CLI's --tmux-session-name.
		var raw struct {
			CWD              string            `json:"cwd"`
			Template         string            `json:"template"`
			ClaudeInstanceID string            `json:"claude_instance_id"`
			Label            []string          `json:"label"`
			Allow            []string          `json:"allow"`
			Deny             []string          `json:"deny"`
			Ask              []string          `json:"ask"`
			RelayMode        string            `json:"relay_mode"`
			ExtraEnv         map[string]string `json:"extra_env"`
			ClaudeArgs       []string          `json:"claude_args"`
			NoPreTrust       bool              `json:"no_pre_trust"`
			TmuxSessionName  *string           `json:"tmux_session_name"`
			ReuseFinished    bool              `json:"reuse_finished"`
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
			NoPreTrust:          raw.NoPreTrust,
			ReuseFinished:       raw.ReuseFinished,
		}
		if raw.TmuxSessionName != nil {
			p.TmuxSessionName = *raw.TmuxSessionName
			p.TmuxSessionNameSupplied = true
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
		// The MCP caller gets kill's result or named error through the envelope; kill has no logger path (SR-6.3).
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
			State           []string `json:"state"`
			Label           []string `json:"label"`
			Parent          string   `json:"parent"`
			Cwd             string   `json:"cwd"`
			TmuxSessionName string   `json:"tmux_session_name"`
			Limit           int      `json:"limit"`
		}
		if err := json.Unmarshal(args, &raw); err != nil {
			return nil, fmt.Errorf("decode list params: %w", err)
		}
		return d.client.List(api.ListParams{
			State:           raw.State,
			Labels:          raw.Label,
			Parent:          raw.Parent,
			Cwd:             raw.Cwd,
			TmuxSessionName: raw.TmuxSessionName,
			Limit:           raw.Limit,
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
			Overwrite  bool              `json:"overwrite"`
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
			Overwrite:           raw.Overwrite,
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

	case "decide":
		var p api.DecideParams
		if err := unmarshalSnake(args, &p); err != nil {
			return nil, err
		}
		return d.client.Decide(p)

	case "get-permission":
		var p api.GetPermissionParams
		if err := unmarshalSnake(args, &p); err != nil {
			return nil, err
		}
		return d.client.GetPermission(p)

	default:
		return nil, fmt.Errorf("%w: %s", ErrUnknownTool, toolName)
	}
}

// checkParamNames refuses an arguments object with a top-level key that is
// not one of v's manifest param names (b.c4u). Keys match exactly, case
// included: Go's decoder would match a key case-insensitively, and MCP
// would otherwise silently ignore an unknown or misspelt key, such as an old
// dashed name. The refusal wraps api.ErrInvalidFlags (the CLI's error for a
// flag that is not defined), names every unknown key as the caller sent it,
// sorted, and lists v's valid param names in manifest order. It runs before
// any decode, so nothing runs on a refusal. Arguments that are not a JSON
// object are a decode error.
func checkParamNames(v manifest.VerbDef, args json.RawMessage) error {
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(args, &keys); err != nil {
		return fmt.Errorf("decode %s params: %w", v.Name, err)
	}
	valid := make([]string, 0, len(v.Params))
	known := make(map[string]bool, len(v.Params))
	for _, p := range v.Params {
		valid = append(valid, p.Name)
		known[p.Name] = true
	}
	var unknown []string
	for k := range keys {
		if !known[k] {
			unknown = append(unknown, strconv.Quote(k))
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	sort.Strings(unknown)
	noun := "parameter"
	if len(unknown) > 1 {
		noun = "parameters"
	}
	list := "none"
	if len(valid) > 0 {
		list = strings.Join(valid, ", ")
	}
	return fmt.Errorf("%w: %s: unknown %s %s; valid parameters: %s",
		api.ErrInvalidFlags, v.Name, noun, strings.Join(unknown, ", "), list)
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
