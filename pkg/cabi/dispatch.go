package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	pkgapi "github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/errnames"
)

// handleFreeVerbs is the single source of truth for the set of C-ABI verb
// names that can be dispatched without resolving a *pkgapi.Client handle.
// Currently only "ad_version" qualifies: it returns build-time metadata and
// requires no store, tmux, or config access.
//
// Future handle-free verbs are added here exclusively. No other file in
// pkg/cabi may hard-code a list of handle-free verbs. The pkg/api/manifest
// package exposes HandleFreeVerbs() for callers that need the manifest-level
// list; pkg/cabi maintains this internal table for its own dispatch until a
// cross-Epic pin promotes the field to manifest.VerbDef.HandleFree.
var handleFreeVerbs = map[string]struct{}{
	"ad_version": {},
}

// isHandleFree reports whether verbName can be dispatched without resolving a
// client handle from the registry. It reads from the package-level
// handleFreeVerbs map so additions require only one edit site.
func isHandleFree(verbName string) bool {
	_, ok := handleFreeVerbs[verbName]
	return ok
}

// resolveHandle extracts the "handle" field from paramsJSON, looks it up in
// the global registry, and returns the corresponding *pkgapi.Client.
//
// Return contract (dual-use second []byte):
//
//   - ok == true:  (client, strippedParams, true)  — strippedParams is
//     paramsJSON with the "handle" key deleted; pass this to the verb fn.
//   - ok == false: (nil, errEnvelope, false)        — errEnvelope is the
//     JSON-encoded ErrUnknownHandle error envelope; return it directly.
//
// Callers must check ok before using either return value. The bool flag is
// the canonical discriminant; the []byte meaning depends on it.
func resolveHandle(paramsJSON []byte) (*pkgapi.Client, []byte, bool) {
	// Extract the handle field.
	var h struct {
		Handle string `json:"handle"`
	}
	if err := json.Unmarshal(paramsJSON, &h); err != nil || h.Handle == "" {
		return nil, classifyAndEnvelope(errnames.ErrUnknownHandle), false
	}

	client, found := registry.lookup(h.Handle)
	if !found {
		return nil, classifyAndEnvelope(errnames.ErrUnknownHandle), false
	}

	// Strip "handle" from params so verb fns receive a clean JSON object
	// without the cabi routing field. json.Unmarshal ignores extra fields,
	// but stripping keeps the fn's raw bytes semantically accurate and
	// avoids surprises in fuzz corpora.
	var raw map[string]json.RawMessage
	stripped := paramsJSON
	if err := json.Unmarshal(paramsJSON, &raw); err == nil {
		delete(raw, "handle")
		if b, err := json.Marshal(raw); err == nil {
			stripped = b
		}
	}

	return client, stripped, true
}

// contextFromParams reads the optional "timeout_ms" integer field from
// paramsJSON and returns an appropriate context.Context.
//
// Behaviour:
//   - Absent or <= 0: context.Background() with a no-op CancelFunc.
//   - Positive: context.WithTimeout(Background, timeout_ms milliseconds).
//
// The caller is responsible for calling the returned CancelFunc (typically
// via defer cancel()) to release timer resources promptly.
func contextFromParams(paramsJSON []byte) (context.Context, context.CancelFunc) {
	var p struct {
		TimeoutMs int64 `json:"timeout_ms"`
	}
	if err := json.Unmarshal(paramsJSON, &p); err != nil || p.TimeoutMs <= 0 {
		return context.Background(), func() {}
	}
	return context.WithTimeout(context.Background(), time.Duration(p.TimeoutMs)*time.Millisecond)
}

// runVerb is the shared recover-wrapped dispatch helper used by every ad_*
// C export. It centralises:
//
//  1. The recover() site that catches panics and converts them to ErrInternal.
//  2. The panic-injection seam (no-op in production, active under cabi_panic_inject).
//  3. Handle resolution (skipped for handle-free verbs per isHandleFree).
//  4. Calling fn with the resolved client and stripped params.
//  5. Wrapping the result via successEnvelope or classifyAndEnvelope.
//
// fn receives:
//   - client: the resolved *pkgapi.Client (nil for handle-free verbs such as
//     ad_version; fn must not call client methods in that case).
//   - params: paramsJSON with the "handle" key stripped (handle-requiring
//     verbs), or the original paramsJSON unchanged (handle-free verbs).
//
// fn should return the verb result struct/map on success and a non-nil error
// on failure. runVerb calls successEnvelope(result) or classifyAndEnvelope(err)
// accordingly and always returns a non-empty JSON []byte.
func runVerb(
	verbName string,
	paramsJSON []byte,
	fn func(client *pkgapi.Client, params []byte) (any, error),
) []byte {
	var result []byte

	func() {
		defer func() {
			if r := recover(); r != nil {
				debugLog("runVerb: panic recovered", "verb", verbName, "panic", r)
				result = errorEnvelope("ErrInternal", "internal error")
			}
		}()

		// Panic-injection seam: no-op in production builds; fires when
		// cabi_panic_inject build tag is active and params carry
		// "_inject_panic": true. Placed early so it exercises the recover site.
		triggerInjectedPanicIfRequested(paramsJSON)

		var (
			client *pkgapi.Client
			params []byte
		)

		if isHandleFree(verbName) {
			// Handle-free verbs (e.g. ad_version) receive the full params JSON.
			// Any "handle" field present in the input is silently ignored.
			params = paramsJSON
		} else {
			c, secondReturn, ok := resolveHandle(paramsJSON)
			if !ok {
				// secondReturn is the ErrUnknownHandle error envelope.
				result = secondReturn
				return
			}
			// secondReturn is paramsJSON with the "handle" key stripped.
			client = c
			params = secondReturn
		}

		res, err := fn(client, params)
		if err != nil {
			result = classifyAndEnvelope(err)
			return
		}
		result = successEnvelope(res)
	}()

	return result
}

// splitKV parses a "key=value" string. The separator is the first '='.
// Returns ok=false when there is no '=' or the key portion is empty.
func splitKV(kv string) (k, v string, ok bool) {
	i := strings.IndexByte(kv, '=')
	if i <= 0 {
		return "", "", false
	}
	return kv[:i], kv[i+1:], true
}

// parseDuration accepts time.ParseDuration format plus a trailing-'d' days
// shorthand (e.g. "7d", "14d"). Mirrors the identical helper in
// internal/mcp/dispatch.go so the cabi layer does not depend on internal/*.
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
