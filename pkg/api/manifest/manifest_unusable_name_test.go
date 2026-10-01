package manifest_test

// manifest_unusable_name_test.go pins SR-1.7's unusable recorded-name
// ErrInternal trigger (SR-3.2; Epic 19) on the verbs that gained the refusal:
// the pointer in each manifest and surface.json Description, and the full
// sentence in each Client method's Go doc prose. It also pins the sweeps'
// statement (SR-11.3, SR-12.2, SR-18.11), which gains no ErrInternal and no
// Description text: find-missing's unverified_ids and expire's kept and
// kept_ids result fields, and the Client.FindMissing and Client.Expire Go doc
// prose. Kill's are manifest_kill_description_test.go's; ErrorNames is
// TestNoVerbListsErrInternal's.

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

// TestUnusableNameSweepResultFields: find-missing's unverified_ids and
// expire's kept and kept_ids, in the manifest and surface.json, state how
// such a row is reported, under agent-text rules.
func TestUnusableNameSweepResultFields(t *testing.T) {
	_, surface := readSurfaceJSON(t)
	for _, c := range []struct {
		verb, field string
		want        apitest.DescCase
	}{
		{"find-missing", string(apitest.FindMissingUnverifiedIDs), apitest.DescUnusableNameFindMissingField()},
		{"expire", string(apitest.ExpireKept), apitest.DescUnusableNameExpireField(apitest.ExpireKept)},
		{"expire", string(apitest.ExpireKeptIDs), apitest.DescUnusableNameExpireField(apitest.ExpireKeptIDs)},
	} {
		t.Run(c.verb+"/"+c.field, func(t *testing.T) {
			sources := resultFieldSources(t, surface, c.verb, c.field)
			for _, source := range []string{"manifest", "surface.json"} {
				f, ok := sources[source]
				if !ok {
					t.Errorf("%s: %s has no %q result field", source, c.verb, c.field)
					continue
				}
				apitest.AssertAgentTextCase(t, source+": "+c.verb+" result field "+c.field, f.desc, c.want)
			}
		})
	}
}

// unusableNameSweepGoDoc is a sweep's Go doc statement for such a row: the
// name's kinds and precedence, req, and a human's decision with the
// "Operator actions" pointer; never ErrInternal or an instruction to delete.
func unusableNameSweepGoDoc(method string, req ...string) apitest.DescCase {
	return apitest.DescCase{
		Name: method + " Go doc, unusable recorded name",
		Require: append([]string{
			"recorded tmux session name cannot be used",
			"it is empty, contains a control character, or contains a character tmux stores differently",
			"the first fault in that order", "is a human's decision",
		}, req...),
		MustNot: []string{"ErrInternal", "delete the row", "delete such a row", "delete it"},
	}.PointsToOperatorActions()
}

// TestUnusableNameSweepGoDoc: the Client.FindMissing and Client.Expire Go doc
// prose, outside "Errors:", states how such a row is reported.
func TestUnusableNameSweepGoDoc(t *testing.T) {
	for method, req := range map[string][]string{
		"FindMissing": {"a liveness note of its own", "with no tmux call"},
		"Expire":      {"is kept on every run", "before any other check", "no process check or tmux call"},
	} {
		t.Run(method, func(t *testing.T) {
			apitest.AssertDescription(t, clientGoDocProse(t, method), unusableNameSweepGoDoc(method, req...))
		})
	}
}
