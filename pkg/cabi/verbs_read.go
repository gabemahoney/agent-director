package main

// #include <stdlib.h>
import "C"

import (
	"encoding/json"
	"fmt"

	pkgapi "github.com/gabemahoney/agent-director/pkg/api"
)

//export ad_spawn
// ad_spawn launches a tracked Claude Code instance inside a new tmux session.
// Fire-and-forget: returns the claude_instance_id immediately; state moves
// from pending to waiting on the first SessionStart hook.
//
// JSON params (all optional except handle and cwd):
//
//	{
//	  "handle":            "<token returned by ad_open>",
//	  "cwd":              "<absolute or ~/-prefixed path>",
//	  "template":         "<template name>",
//	  "claude_instance_id": "<explicit UUID>",
//	  "label":            ["KEY=VALUE", ...],
//	  "allow":            ["tool/pattern", ...],
//	  "deny":             ["tool/pattern", ...],
//	  "ask":              ["tool/pattern", ...],
//	  "relay_mode":       "on"|"off",
//	  "extra_env":        {"KEY": "VALUE", ...},
//	  "claude_args":      ["--flag", "value", ...],
//	  "no_pre_trust":     false,
//	  "tmux_session_name": "<explicit session name>"
//	}
//
// The returned *C.char must be released via ad_free_cstring.
func ad_spawn(params_json *C.char) *C.char {
	rawBytes := []byte(C.GoString(params_json))
	result := runVerb("ad_spawn", rawBytes, func(client *pkgapi.Client, params []byte) (any, error) {
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
			TmuxSessionName  string            `json:"tmux_session_name"`
		}
		if err := json.Unmarshal(params, &raw); err != nil {
			return nil, fmt.Errorf("ad_spawn: invalid params: %w", err)
		}
		labels := make(map[string]string, len(raw.Label))
		for _, kv := range raw.Label {
			k, v, ok := splitKV(kv)
			if !ok {
				return nil, fmt.Errorf("ad_spawn: invalid label %q (want key=value)", kv)
			}
			labels[k] = v
		}
		p := pkgapi.SpawnParams{
			CWD:                 raw.CWD,
			Template:            raw.Template,
			ClaudeInstanceID:    raw.ClaudeInstanceID,
			ExtraEnv:            raw.ExtraEnv,
			AgentDirectorLabels: labels,
			ClaudeArgs:          raw.ClaudeArgs,
			RelayMode:           raw.RelayMode,
			NoPreTrust:          raw.NoPreTrust,
			TmuxSessionName:     raw.TmuxSessionName,
		}
		if len(raw.Allow) > 0 || len(raw.Deny) > 0 || len(raw.Ask) > 0 {
			p.Permissions = &pkgapi.Permissions{
				Allow: raw.Allow,
				Deny:  raw.Deny,
				Ask:   raw.Ask,
			}
		}
		return client.Spawn(p)
	})
	return C.CString(string(result))
}

//export ad_status
// ad_status returns the current state of a tracked Spawn.
//
// JSON params: {"handle": "...", "claude_instance_id": "<id>"}
//
// The returned *C.char must be released via ad_free_cstring.
func ad_status(params_json *C.char) *C.char {
	rawBytes := []byte(C.GoString(params_json))
	result := runVerb("ad_status", rawBytes, func(client *pkgapi.Client, params []byte) (any, error) {
		var p struct {
			ClaudeInstanceID string `json:"claude_instance_id"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("ad_status: invalid params: %w", err)
		}
		return client.Status(p.ClaudeInstanceID)
	})
	return C.CString(string(result))
}

//export ad_get
// ad_get returns the full DB row for a tracked Spawn.
//
// JSON params: {"handle": "...", "claude_instance_id": "<id>"}
//
// The returned *C.char must be released via ad_free_cstring.
func ad_get(params_json *C.char) *C.char {
	rawBytes := []byte(C.GoString(params_json))
	result := runVerb("ad_get", rawBytes, func(client *pkgapi.Client, params []byte) (any, error) {
		var p struct {
			ClaudeInstanceID string `json:"claude_instance_id"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("ad_get: invalid params: %w", err)
		}
		return client.Get(p.ClaudeInstanceID)
	})
	return C.CString(string(result))
}

//export ad_list
// ad_list enumerates Spawn rows with optional filtering.
//
// JSON params:
//
//	{
//	  "handle":           "<token>",
//	  "state":            ["pending", "waiting", ...],
//	  "label":            ["KEY=VALUE", ...],
//	  "parent":           "<parent_id>",
//	  "cwd":              "<exact cwd match>",
//	  "tmux_session_name": "<exact session name match>",
//	  "limit":            0
//	}
//
// The returned *C.char must be released via ad_free_cstring.
func ad_list(params_json *C.char) *C.char {
	rawBytes := []byte(C.GoString(params_json))
	result := runVerb("ad_list", rawBytes, func(client *pkgapi.Client, params []byte) (any, error) {
		var raw struct {
			State           []string `json:"state"`
			Label           []string `json:"label"`
			Parent          string   `json:"parent"`
			Cwd             string   `json:"cwd"`
			TmuxSessionName string   `json:"tmux_session_name"`
			Limit           int      `json:"limit"`
		}
		if err := json.Unmarshal(params, &raw); err != nil {
			return nil, fmt.Errorf("ad_list: invalid params: %w", err)
		}
		return client.List(pkgapi.ListParams{
			State:           raw.State,
			Labels:          raw.Label,
			Parent:          raw.Parent,
			Cwd:             raw.Cwd,
			TmuxSessionName: raw.TmuxSessionName,
			Limit:           raw.Limit,
		})
	})
	return C.CString(string(result))
}

//export ad_send_keys
// ad_send_keys sends text into a tracked Spawn's tmux pane.
//
// JSON params:
//
//	{"handle": "...", "claude_instance_id": "<id>", "text": "<text>"}
//
// The returned *C.char must be released via ad_free_cstring.
func ad_send_keys(params_json *C.char) *C.char {
	rawBytes := []byte(C.GoString(params_json))
	result := runVerb("ad_send_keys", rawBytes, func(client *pkgapi.Client, params []byte) (any, error) {
		var p pkgapi.SendKeysParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("ad_send_keys: invalid params: %w", err)
		}
		return client.SendKeys(p)
	})
	return C.CString(string(result))
}

//export ad_read_pane
// ad_read_pane captures trailing lines from a tracked Spawn's tmux pane.
//
// JSON params:
//
//	{
//	  "handle":            "<token>",
//	  "claude_instance_id": "<id>",
//	  "n_lines":           25,
//	  "ansi":              false
//	}
//
// The returned *C.char must be released via ad_free_cstring.
func ad_read_pane(params_json *C.char) *C.char {
	rawBytes := []byte(C.GoString(params_json))
	result := runVerb("ad_read_pane", rawBytes, func(client *pkgapi.Client, params []byte) (any, error) {
		var p pkgapi.ReadPaneParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("ad_read_pane: invalid params: %w", err)
		}
		return client.ReadPane(p)
	})
	return C.CString(string(result))
}
