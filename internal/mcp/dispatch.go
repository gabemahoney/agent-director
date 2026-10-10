package mcp

import (
	"context"
	"encoding/json"
	"errors"
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
// Each tool then decodes the arguments into its typed params struct
// (decodeParams): every manifest param has a typed field tagged with its
// manifest name (b.7or), so a wrong-typed value is refused with
// ErrInvalidFlags naming the param and its expected type, and nothing runs
// (b.ewa).
func (d *LiveDispatcher) Call(ctx context.Context, toolName string, args json.RawMessage) (any, error) {
	verbName := VerbNameFromTool(toolName)
	if args == nil || len(args) == 0 {
		args = json.RawMessage("{}")
	}
	v, inManifest := manifest.Lookup(verbName)
	if inManifest && ExposedVerb(verbName) {
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
		if err := decodeParams(v, args, &raw); err != nil {
			return nil, err
		}
		labels, err := labelMap(v, raw.Label)
		if err != nil {
			return nil, err
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
		if err := decodeParams(v, args, &p); err != nil {
			return nil, err
		}
		return d.client.Status(p.ClaudeInstanceID)

	case "get":
		var p struct {
			ClaudeInstanceID string `json:"claude_instance_id"`
		}
		if err := decodeParams(v, args, &p); err != nil {
			return nil, err
		}
		return d.client.Get(p.ClaudeInstanceID)

	case "send-keys":
		var p api.SendKeysParams
		if err := decodeParams(v, args, &p); err != nil {
			return nil, err
		}
		return d.client.SendKeys(p)

	case "read-pane":
		var p api.ReadPaneParams
		if err := decodeParams(v, args, &p); err != nil {
			return nil, err
		}
		return d.client.ReadPane(p)

	case "kill":
		var p api.KillParams
		if err := decodeParams(v, args, &p); err != nil {
			return nil, err
		}
		// The MCP caller gets kill's result or named error through the envelope; kill has no logger path (SR-6.3).
		return d.client.Kill(p)

	case "pause":
		var p api.PauseParams
		if err := decodeParams(v, args, &p); err != nil {
			return nil, err
		}
		return d.client.Pause(ctx, p)

	case "resume":
		var p api.ResumeParams
		if err := decodeParams(v, args, &p); err != nil {
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
		if err := decodeParams(v, args, &raw); err != nil {
			return nil, err
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
		if err := decodeParams(v, args, &raw); err != nil {
			return nil, err
		}
		labels, err := labelMap(v, raw.Label)
		if err != nil {
			return nil, err
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
		if err := decodeParams(v, args, &raw); err != nil {
			return nil, err
		}
		// older_than goes through api.ParseOlderThan, the parser the CLI's
		// --older-than shares, so a value it rejects (neither form,
		// a leading + or -, b.hxn, b.c4n, or a day count above
		// config.MaxExpireRetentionDays, 106751, b.sgw) is refused here
		// and Expire never runs.
		var older *time.Duration
		if raw.OlderThan != "" {
			dur, ok := api.ParseOlderThan(raw.OlderThan)
			if !ok {
				return nil, fmt.Errorf("%w: %s: parameter \"older_than\" value %q must be %s",
					api.ErrInvalidFlags, v.Name, raw.OlderThan, api.OlderThanForm)
			}
			older = &dur
		}
		return d.client.Expire(older)

	case "decide":
		var p api.DecideParams
		if err := decodeParams(v, args, &p); err != nil {
			return nil, err
		}
		return d.client.Decide(p)

	case "get-permission":
		var p api.GetPermissionParams
		if err := decodeParams(v, args, &p); err != nil {
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
// object are refused with ErrInvalidFlags too (paramDecodeError, b.ewa).
func checkParamNames(v manifest.VerbDef, args json.RawMessage) error {
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(args, &keys); err != nil {
		return paramDecodeError(v, err)
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

// decodeParams decodes args into into, verb v's typed params struct, whose
// json tags are v's manifest param names. A decode failure is refused per
// paramDecodeError.
func decodeParams(v manifest.VerbDef, args json.RawMessage, into any) error {
	if err := json.Unmarshal(args, into); err != nil {
		return paramDecodeError(v, err)
	}
	return nil
}

// paramDecodeError classifies a failure to decode verb v's arguments
// (b.ewa). A value of the wrong JSON type is the caller's error: it wraps
// api.ErrInvalidFlags, names the verb as checkParamNames does, names the
// param as sent (checkParamNames has refused any key that is not exactly a
// manifest param name) and states the param's expected type in the JSON
// Schema terms tools/list declares, never a Go type. Arguments that are not
// a JSON object (or not JSON) are ErrInvalidFlags too. Any other failure is
// agent-director's own fault and stays unclassified (ErrInternal).
func paramDecodeError(v manifest.VerbDef, err error) error {
	var typeErr *json.UnmarshalTypeError
	var syntaxErr *json.SyntaxError
	switch {
	case errors.As(err, &typeErr) && typeErr.Field != "":
		// Field is the dotted path to the bad value; its first segment is
		// the top-level key, which is the param.
		name, _, _ := strings.Cut(typeErr.Field, ".")
		if want := expectedParamValue(v, name); want != "" {
			return fmt.Errorf("%w: %s: parameter %q must be %s", api.ErrInvalidFlags, v.Name, name, want)
		}
		return fmt.Errorf("%w: %s: parameter %q has the wrong type", api.ErrInvalidFlags, v.Name, name)
	case errors.As(err, &typeErr), errors.As(err, &syntaxErr):
		// A type error with no field is the arguments value itself.
		return fmt.Errorf("%w: %s: arguments must be a JSON object", api.ErrInvalidFlags, v.Name)
	default:
		return fmt.Errorf("decode %s params: %w", v.Name, err)
	}
}

// expectedParamValue is expectedValue for v's param name, or "" when v has
// no such param or its type has no JSON Schema constraint.
func expectedParamValue(v manifest.VerbDef, name string) string {
	for _, p := range v.Params {
		if p.Name == name {
			return expectedValue(p.Type)
		}
	}
	return ""
}

// labelMap parses verb v's `label` entries, each "key=value", into a map.
// An entry with no "=" or an empty key is refused with api.ErrInvalidFlags
// naming the verb, the param and the entry, as the CLI refuses the same
// --label value (b.anw).
func labelMap(v manifest.VerbDef, entries []string) (map[string]string, error) {
	labels := make(map[string]string, len(entries))
	for _, kv := range entries {
		k, val, ok := splitKV(kv)
		if !ok {
			return nil, fmt.Errorf("%w: %s: parameter \"label\" entry %q must be key=value",
				api.ErrInvalidFlags, v.Name, kv)
		}
		labels[k] = val
	}
	return labels, nil
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
