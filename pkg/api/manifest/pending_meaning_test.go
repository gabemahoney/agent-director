package manifest_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/pkg/api/manifest"
)

// pendingSite is one manifest text that must define pending as a launch in
// progress (SR-22.1, SR-20.6): a verb's description, or one param's or result
// field's description. require and mustNot are lower-case key phrases.
type pendingSite struct {
	name    string
	verb    string
	param   string
	result  string
	require []string
	mustNot []string
}

// launchInProgress is the core of SR-22.1's one meaning of pending, shared by
// the state texts.
var launchInProgress = []string{
	"a launch (spawn, reuse or resume)",
	"in progress",
	"has not reported in",
	"resumed pending row keeps its session id and history",
}

// pendingSites lists every site that states the meaning of pending; later
// Epics add rows (e.g. the allow_pending params) here.
var pendingSites = []pendingSite{
	{name: "status description", verb: "status", require: launchInProgress},
	{name: "status state", verb: "status", result: "state", require: launchInProgress},
	{name: "get state", verb: "get", result: "state", require: launchInProgress},
	{name: "list spawns", verb: "list", result: "spawns", require: []string{
		"same meaning of pending",
		"a launch (spawn, reuse or resume) in progress",
		"has not reported in",
		"resumed pending row keeps its session id and history",
	}},
	{
		name:    "spawn description",
		verb:    "spawn",
		require: []string{"the row is pending from its insert until the agent reports in"},
		mustNot: []string{"state moves from pending to waiting on the first sessionstart hook"},
	},
	{name: "resume description", verb: "resume", require: []string{
		"moves the row to pending, keeping its session id and history",
		"stays pending until the agent reports in",
		"restores the row to its prior ended or missing state",
		"the session may have been created and the row stays pending",
		"do not retry until get shows the row ended or missing",
		"a pending row (a launch in progress",
	}},
}

// manifestText returns the site's description from the manifest source of
// truth, failing the test when the verb, param or result field is missing.
func manifestText(t *testing.T, s pendingSite) string {
	t.Helper()
	v, ok := manifest.Lookup(s.verb)
	if !ok {
		t.Fatalf("%s: verb %q not in manifest", s.name, s.verb)
	}
	switch {
	case s.param != "":
		for _, p := range v.Params {
			if p.Name == s.param {
				return p.Description
			}
		}
		t.Fatalf("%s: %s has no param %q", s.name, s.verb, s.param)
	case s.result != "":
		for _, f := range v.ResultFields {
			if f.Name == s.result {
				return f.Description
			}
		}
		t.Fatalf("%s: %s has no result field %q", s.name, s.verb, s.result)
	}
	return v.Description
}

// TestManifestDefinesPendingAsLaunchInProgress pins SR-22.1's meaning of
// pending at every site in pendingSites (AC-DOC-17, AC-DOC-12).
func TestManifestDefinesPendingAsLaunchInProgress(t *testing.T) {
	for _, s := range pendingSites {
		t.Run(s.name, func(t *testing.T) {
			text := strings.ToLower(manifestText(t, s))
			for _, p := range s.require {
				if !strings.Contains(text, p) {
					t.Errorf("%s: missing %q in %q", s.name, p, text)
				}
			}
			for _, p := range s.mustNot {
				if strings.Contains(text, p) {
					t.Errorf("%s: still carries retired %q in %q", s.name, p, text)
				}
			}
		})
	}
}

// retiredPendingConcepts matches the concepts SR-20.6 retires, in hyphen and
// space spellings (twin: tools/gen-docs/main_test.go).
var retiredPendingConcepts = regexp.MustCompile(`(?i)resume[-\s]?starting|young[-\s]claim|launched[-\s]mark|claim[-\s]withdrawal`)

// resumedStaysFinished matches a sentence saying a row stays ended, missing
// or terminal until SessionStart (the pre-SR-22.1 resume model).
var resumedStaysFinished = regexp.MustCompile(`(?i)\b(stays?|staying|remains?|remaining|keeps?|kept|left)\b[^.]*\b(ended|missing|terminal)\b[^.]*\buntil\b[^.]*sessionstart`)

// agentTexts returns every verb, param and result-field description from the
// manifest and from the committed surface.json, keyed by source, verb and field.
func agentTexts(t *testing.T) map[string]string {
	t.Helper()
	texts := map[string]string{}
	for _, v := range manifest.Verbs {
		texts["manifest "+v.Name+" description"] = v.Description
		for _, p := range v.Params {
			texts["manifest "+v.Name+" param "+p.Name] = p.Description
		}
		for _, f := range v.ResultFields {
			texts["manifest "+v.Name+" result field "+f.Name] = f.Description
		}
	}
	_, doc := readSurfaceJSON(t)
	for _, v := range doc.Verbs {
		texts["surface.json "+v.Name+" description"] = v.Description
		for _, p := range v.Params {
			texts["surface.json "+v.Name+" param "+p.Name] = p.Description
		}
		for _, f := range v.ResultFields {
			texts["surface.json "+v.Name+" result field "+f.Name] = f.Description
		}
	}
	return texts
}

// TestManifestNamesNoRetiredPendingConcept checks every manifest and
// surface.json text for a retired concept or a resumed row left finished.
func TestManifestNamesNoRetiredPendingConcept(t *testing.T) {
	cases := []struct {
		name string
		re   *regexp.Regexp
	}{
		{"retired concept", retiredPendingConcepts},
		{"resumed row stays finished until SessionStart", resumedStaysFinished},
	}
	texts := agentTexts(t)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			for site, text := range texts {
				if m := c.re.FindString(text); m != "" {
					t.Errorf("%s: %s %q", site, c.name, m)
				}
			}
		})
	}
}

// TestSurfaceJSONNamesNoRetiredPendingConcept scans the committed surface.json
// bytes, so a retired concept outside a description is caught too.
func TestSurfaceJSONNamesNoRetiredPendingConcept(t *testing.T) {
	raw, _ := readSurfaceJSON(t)
	if m := retiredPendingConcepts.Find(raw); m != nil {
		t.Errorf("surface.json names retired concept %q", m)
	}
}
