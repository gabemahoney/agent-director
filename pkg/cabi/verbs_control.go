package main

// #include <stdlib.h>
import "C"

import (
	"encoding/json"
	"fmt"

	pkgapi "github.com/gabemahoney/agent-director/pkg/api"
)

//export ad_kill
// ad_kill terminates a Spawn's tmux session. Idempotent on terminal states.
//
// JSON params: {"handle": "...", "claude_instance_id": "<id>"}
//
// The returned *C.char must be released via ad_free_cstring.
func ad_kill(params_json *C.char) *C.char {
	rawBytes := []byte(C.GoString(params_json))
	result := runVerb("ad_kill", rawBytes, func(client *pkgapi.Client, params []byte) (any, error) {
		var p pkgapi.KillParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("ad_kill: invalid params: %w", err)
		}
		return client.Kill(p)
	})
	return C.CString(string(result))
}

//export ad_pause
// ad_pause politely shuts down a waiting Spawn by sending `/exit` and waiting
// for the row to reach `ended`. One-shot — no caller-side polling.
//
// JSON params:
//
//	{
//	  "handle":            "<token>",
//	  "claude_instance_id": "<id>",
//	  "timeout_ms":        0
//	}
//
// timeout_ms > 0 bounds the total wait; omitted or <= 0 means background
// context (no deadline). The returned *C.char must be released via ad_free_cstring.
func ad_pause(params_json *C.char) *C.char {
	rawBytes := []byte(C.GoString(params_json))
	result := runVerb("ad_pause", rawBytes, func(client *pkgapi.Client, params []byte) (any, error) {
		ctx, cancel := contextFromParams(params)
		defer cancel()
		var p pkgapi.PauseParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("ad_pause: invalid params: %w", err)
		}
		return client.Pause(ctx, p)
	})
	return C.CString(string(result))
}

//export ad_decide
// ad_decide writes the orchestrator's allow/deny verdict on an open
// PermissionRequest. Race-free first-call-wins via a single-statement UPDATE.
//
// JSON params:
//
//	{
//	  "handle":            "<token>",
//	  "claude_instance_id": "<id>",
//	  "decision":          "allow"|"deny",
//	  "reason":            "<free text>"
//	}
//
// The returned *C.char must be released via ad_free_cstring.
func ad_decide(params_json *C.char) *C.char {
	rawBytes := []byte(C.GoString(params_json))
	result := runVerb("ad_decide", rawBytes, func(client *pkgapi.Client, params []byte) (any, error) {
		var p pkgapi.DecideParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("ad_decide: invalid params: %w", err)
		}
		return client.Decide(p)
	})
	return C.CString(string(result))
}

//export ad_resume
// ad_resume brings a terminated Spawn back to life via `claude --resume`.
//
// JSON params: {"handle": "...", "claude_instance_id": "<id>"}
//
// The returned *C.char must be released via ad_free_cstring.
func ad_resume(params_json *C.char) *C.char {
	rawBytes := []byte(C.GoString(params_json))
	result := runVerb("ad_resume", rawBytes, func(client *pkgapi.Client, params []byte) (any, error) {
		var p pkgapi.ResumeParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("ad_resume: invalid params: %w", err)
		}
		return client.Resume(p)
	})
	return C.CString(string(result))
}

//export ad_find_missing
// ad_find_missing reconciles DB state against live processes and transitions
// unprobeable rows to `missing`.
//
// JSON params:
//
//	{"handle": "...", "timeout_ms": 0}
//
// timeout_ms > 0 bounds the probe sweep; omitted or <= 0 means background
// context. The returned *C.char must be released via ad_free_cstring.
func ad_find_missing(params_json *C.char) *C.char {
	rawBytes := []byte(C.GoString(params_json))
	result := runVerb("ad_find_missing", rawBytes, func(client *pkgapi.Client, params []byte) (any, error) {
		ctx, cancel := contextFromParams(params)
		defer cancel()
		return client.FindMissing(ctx)
	})
	return C.CString(string(result))
}
