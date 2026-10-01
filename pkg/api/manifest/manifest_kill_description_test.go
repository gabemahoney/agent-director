package manifest_test

import (
	"testing"

	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// TestKillDescriptionStatesOutcomes pins SR-18.9, SR-6.1 and SR-1.7 on kill's
// manifest and surface.json descriptions, with no opt-in or session-ending command.
func TestKillDescriptionStatesOutcomes(t *testing.T) {
	_, surface := readSurfaceJSON(t)
	descs := verbDescriptions(t, surface, "kill")
	if len(descs) != 2 {
		t.Fatalf("kill description found in %d of manifest and surface.json", len(descs))
	}
	for source, desc := range descs {
		apitest.AssertAgentTextCase(t, source+": kill description", desc, apitest.DescKillManifest())
	}
}

// TestKillGoDocStatesInternalTrigger pins SR-1.7 on Client.Kill's Go doc prose
// outside "Errors:" (its CLI: line excepted): the unusable-name ErrInternal trigger.
func TestKillGoDocStatesInternalTrigger(t *testing.T) {
	apitest.AssertDescription(t, clientGoDocProse(t, "Kill"), apitest.DescKillInternalTrigger())
}

// TestKillGoDocStatesRepeatedKillLimitation pins decision-0930b Q5 (a repeated
// Kill as the tmux server exits) on Client.Kill's Go doc prose.
func TestKillGoDocStatesRepeatedKillLimitation(t *testing.T) {
	apitest.AssertDescription(t, clientGoDocProse(t, "Kill"), apitest.DescKillRepeatedAfterLastSession("Kill"))
}
