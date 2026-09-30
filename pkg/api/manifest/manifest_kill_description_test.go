package manifest_test

import (
	"strings"
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
	apitest.AssertDescription(t, killGoDocProse(t), apitest.DescKillInternalTrigger())
}

// TestKillGoDocStatesRepeatedKillLimitation pins decision-0930b Q5 (a repeated
// Kill as the tmux server exits) on Client.Kill's Go doc prose.
func TestKillGoDocStatesRepeatedKillLimitation(t *testing.T) {
	apitest.AssertDescription(t, killGoDocProse(t), apitest.DescKillRepeatedAfterLastSession("Kill"))
}

// killGoDocProse returns Client.Kill's Go doc prose outside "Errors:" and its
// CLI: line, line wrapping rejoined.
func killGoDocProse(t *testing.T) string {
	t.Helper()
	_, prose := splitGoDocErrors(clientMethodDoc(t, "Kill"))
	var lines []string
	for _, line := range strings.Split(prose, "\n") {
		if !strings.HasPrefix(line, "CLI:") {
			lines = append(lines, line)
		}
	}
	return strings.Join(strings.Fields(strings.Join(lines, "\n")), " ")
}
