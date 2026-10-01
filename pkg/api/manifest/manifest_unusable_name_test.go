package manifest_test

// manifest_unusable_name_test.go pins SR-1.7's unusable recorded-name
// ErrInternal trigger (SR-3.2; Epic 19) on the verbs that gained the refusal:
// the pointer in each manifest and surface.json Description, and the full
// sentence in each Client method's Go doc prose. Kill's are
// manifest_kill_description_test.go's; ErrorNames is TestNoVerbListsErrInternal's.

import (
	"testing"

	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// unusableNameVerbs maps each verb that gained the refusal to its Client method.
var unusableNameVerbs = []struct{ verb, method string }{
	{"read-pane", "ReadPane"},
	{"send-keys", "SendKeys"},
	{"pause", "Pause"},
	{"resume", "Resume"},
	{"spawn", "Spawn"},
}

// TestUnusableNamePointerInDescriptions: each verb's manifest and surface.json Description
// carries the pointer, under agent-text rules (no opt-in, no session-ending command).
func TestUnusableNamePointerInDescriptions(t *testing.T) {
	for _, v := range unusableNameVerbs {
		t.Run(v.verb, func(t *testing.T) {
			for source, desc := range verbDescriptionsBoth(t, v.verb) {
				apitest.AssertAgentTextCase(t, source+": "+v.verb+" description", desc, apitest.DescUnusableNamePointer())
			}
		})
	}
}

// TestUnusableNameTriggerInGoDoc: each verb's Client method Go doc prose, outside
// "Errors:", states the full trigger sentence.
func TestUnusableNameTriggerInGoDoc(t *testing.T) {
	for _, v := range unusableNameVerbs {
		t.Run(v.method, func(t *testing.T) {
			apitest.AssertDescription(t, clientGoDocProse(t, v.method), apitest.DescUnusableNameTrigger())
		})
	}
}
