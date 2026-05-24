package main

// #include <stdlib.h>
import "C"

import (
	"encoding/json"
	"fmt"
	"time"

	pkgapi "github.com/gabemahoney/agent-director/pkg/api"
)

//export ad_expire
// ad_expire removes terminal-state rows (ended/missing) older than the
// retention window.
//
// JSON params:
//
//	{"handle": "...", "older_than": "7d"}
//
// older_than accepts time.ParseDuration format ("2h", "30m") or a trailing-d
// days form ("7d", "14d"). Omitted or empty uses the config default.
//
// The returned *C.char must be released via ad_free_cstring.
func ad_expire(params_json *C.char) *C.char {
	rawBytes := []byte(C.GoString(params_json))
	result := runVerb("ad_expire", rawBytes, func(client *pkgapi.Client, params []byte) (any, error) {
		var raw struct {
			OlderThan string `json:"older_than"`
		}
		if err := json.Unmarshal(params, &raw); err != nil {
			return nil, fmt.Errorf("ad_expire: invalid params: %w", err)
		}
		var older *time.Duration
		if raw.OlderThan != "" {
			dur, err := parseDuration(raw.OlderThan)
			if err != nil {
				return nil, fmt.Errorf("ad_expire: older_than: %w", err)
			}
			older = &dur
		}
		return client.Expire(older)
	})
	return C.CString(string(result))
}

//export ad_delete
// ad_delete performs admin batch removal of rows by claude_instance_id.
// Per-row failures are recorded in the results map; the batch never aborts.
//
// JSON params:
//
//	{"handle": "...", "claude_instance_id": ["<id1>", "<id2>"]}
//
// The returned *C.char must be released via ad_free_cstring.
func ad_delete(params_json *C.char) *C.char {
	rawBytes := []byte(C.GoString(params_json))
	result := runVerb("ad_delete", rawBytes, func(client *pkgapi.Client, params []byte) (any, error) {
		var raw struct {
			ClaudeInstanceID []string `json:"claude_instance_id"`
		}
		if err := json.Unmarshal(params, &raw); err != nil {
			return nil, fmt.Errorf("ad_delete: invalid params: %w", err)
		}
		return client.Delete(raw.ClaudeInstanceID)
	})
	return C.CString(string(result))
}

//export ad_make_template
// ad_make_template saves a reusable spawn preset under
// ~/.agent-director/templates/<name>.toml.
//
// JSON params:
//
//	{
//	  "handle":      "<token>",
//	  "name":        "<template name>",
//	  "cwd":         "<default cwd>",
//	  "relay_mode":  "on"|"off",
//	  "claude_args": ["--flag", ...],
//	  "extra_env":   {"KEY": "VALUE", ...},
//	  "label":       ["KEY=VALUE", ...],
//	  "allow":       ["tool/pattern", ...],
//	  "deny":        ["tool/pattern", ...],
//	  "ask":         ["tool/pattern", ...]
//	}
//
// The returned *C.char must be released via ad_free_cstring.
func ad_make_template(params_json *C.char) *C.char {
	rawBytes := []byte(C.GoString(params_json))
	result := runVerb("ad_make_template", rawBytes, func(client *pkgapi.Client, params []byte) (any, error) {
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
		if err := json.Unmarshal(params, &raw); err != nil {
			return nil, fmt.Errorf("ad_make_template: invalid params: %w", err)
		}
		labels := make(map[string]string, len(raw.Label))
		for _, kv := range raw.Label {
			k, v, ok := splitKV(kv)
			if !ok {
				return nil, fmt.Errorf("ad_make_template: invalid label %q (want key=value)", kv)
			}
			labels[k] = v
		}
		p := pkgapi.MakeTemplateParams{
			Name:                raw.Name,
			CWD:                 raw.CWD,
			RelayMode:           raw.RelayMode,
			ClaudeArgs:          raw.ClaudeArgs,
			ExtraEnv:            raw.ExtraEnv,
			AgentDirectorLabels: labels,
		}
		if len(raw.Allow) > 0 || len(raw.Deny) > 0 || len(raw.Ask) > 0 {
			p.Permissions = &pkgapi.MakeTemplatePermissions{
				Allow: raw.Allow,
				Deny:  raw.Deny,
				Ask:   raw.Ask,
			}
		}
		return client.MakeTemplate(p)
	})
	return C.CString(string(result))
}

//export ad_version
// ad_version returns the binary's build-time version stamp as JSON.
// This is a handle-free verb: no Client handle is required or consulted.
// Any "handle" field present in the params JSON is silently ignored.
//
// JSON params: {} (empty object; all fields are ignored)
//
// The returned *C.char must be released via ad_free_cstring.
func ad_version(params_json *C.char) *C.char {
	rawBytes := []byte(C.GoString(params_json))
	// isHandleFree("ad_version") == true: runVerb skips handle resolution.
	result := runVerb("ad_version", rawBytes, func(_ *pkgapi.Client, _ []byte) (any, error) {
		// client is nil for handle-free verbs; call the package-level Version
		// function directly so no nil-pointer dereference can occur.
		return pkgapi.Version()
	})
	return C.CString(string(result))
}
