package main

// unknown_handle_test.go verifies that every handle-requiring callable verb
// returns an ErrUnknownHandle envelope when invoked with a handle that is not
// present in the registry.
//
// The matrix is driven by manifest.CallableVerbs() filtered through
// isHandleFree — no hand-coded verb list lives here.
//
// Special case: ad_close is NOT in manifest.CallableVerbs() (it is a lifecycle
// helper, not a verb), but its unknown-handle contract is explicitly pinned by
// TestAdCloseUnknownHandleIsNoopSuccess at the bottom of this file.
//
// NOTE: import "C" is NOT used here.

import (
	"fmt"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/pkg/api/errnames"
	"github.com/gabemahoney/agent-director/pkg/api/manifest"
)

// unknownHandle is a syntactically valid 32-char hex handle that is never
// registered in the test registry.
const unknownHandle = "deadbeefdeadbeefdeadbeefdeadbeef"

// verbWrappers maps manifest verb names (as they appear in manifest.CallableVerbs)
// to their corresponding string wrappers defined in verbs_test.go.
// The wrappers pass the params JSON through runVerb which performs handle
// resolution before calling the verb fn.
var verbWrappers = map[string]func(string) string{
	"spawn":         adSpawnStr,
	"status":        adStatusStr,
	"get":           adGetStr,
	"list":          adListStr,
	"send-keys":     adSendKeysStr,
	"read-pane":     adReadPaneStr,
	"kill":          adKillStr,
	"pause":         adPauseStr,
	"decide":        adDecideStr,
	"resume":        adResumeStr,
	"find-missing":  adFindMissingStr,
	"expire":        adExpireStr,
	"delete":        adDeleteStr,
	"make-template": adMakeTemplateStr,
	// "version" is handle-free; excluded below via isHandleFree check.
}

// TestAllHandleRequiringVerbsReturnErrUnknownHandle iterates every callable
// manifest verb for which isHandleFree returns false and verifies that calling
// it with an unregistered handle produces an ErrUnknownHandle envelope.
//
// No client is created: the handle is deliberately absent from the registry,
// so runVerb's resolveHandle short-circuits before any fn or store interaction.
func TestAllHandleRequiringVerbsReturnErrUnknownHandle(t *testing.T) {
	// Resolve the catalog name once; assertions compare against this string.
	wantErrName, _ := errnames.Classify(errnames.ErrUnknownHandle)

	for _, v := range manifest.CallableVerbs() {
		v := v // capture for t.Run closure
		cabiName := "ad_" + strings.ReplaceAll(v.Name, "-", "_")
		if isHandleFree(cabiName) {
			// Handle-free verbs (ad_version) bypass handle resolution.
			continue
		}

		t.Run(v.Name, func(t *testing.T) {
			wrapper, ok := verbWrappers[v.Name]
			if !ok {
				t.Fatalf("no string wrapper registered for verb %q — update verbWrappers", v.Name)
			}

			params := fmt.Sprintf(`{"handle":%q}`, unknownHandle)
			result := wrapper(params)
			m := unmarshalObj(t, []byte(result))

			if got := m["err_name"]; got != wantErrName {
				t.Errorf("err_name = %v; want %q", got, wantErrName)
			}
			if desc, _ := m["err_description"].(string); desc == "" {
				t.Errorf("err_description is empty; want non-empty for %q", wantErrName)
			}
		})
	}
}

// TestAdCloseUnknownHandleIsNoopSuccess explicitly pins the ad_close contract:
// an unknown handle must return a success envelope (no err_name), not
// ErrUnknownHandle. ad_close's post-condition ("no active session") is already
// met when the handle is unknown, so the operation is a no-op success.
//
// Coverage duplicates lifecycle_test.go#TestAdCloseUnknownHandle but is
// explicit here so the unknown-handle matrix is demonstrably complete.
func TestAdCloseUnknownHandleIsNoopSuccess(t *testing.T) {
	result := adCloseStr(fmt.Sprintf(`{"handle":%q}`, unknownHandle))
	m := unmarshalObj(t, []byte(result))
	if name, has := m["err_name"]; has {
		t.Fatalf("ad_close with unknown handle must succeed (no-op); got err_name=%v", name)
	}
}
