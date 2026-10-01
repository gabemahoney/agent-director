package manifest_test

// spawn_reuse_description_test.go pins what spawn's texts say about reuse
// (Epic 17 Task 3; SR-1.7, AC-CAT-03, SR-10): the Description states both
// reuse ErrInternal triggers; spawn's ErrorNames, on the manifest and in
// surface.json, carry every name reuse returns and never ErrInternal; and
// Client.Spawn's Go "Errors:" list matches them and states the reuse opt-in's
// cases. The reuse parameter's text and the collision text are Task 4's.

import (
	"slices"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/pkg/api/apitest"
	"github.com/gabemahoney/agent-director/pkg/api/manifest"
)

// spawnReuseErrorNames are the catalogued names spawn with the reuse opt-in
// returns (SR-1.4, SR-10).
var spawnReuseErrorNames = []string{
	"ErrInstanceIdCollision", "ErrTmuxSessionConflict", "ErrTmuxUnresponsive", "ErrTmuxNotAvailable", "ErrTmuxSessionCreate",
}

// TestSpawnDescriptionStatesReuseErrInternal pins AC-CAT-03 for reuse:
// spawn's Description states the archive failure and the failed reuse change
// as ErrInternal triggers, on the manifest and in surface.json.
func TestSpawnDescriptionStatesReuseErrInternal(t *testing.T) {
	c := apitest.DescCase{Name: "spawn manifest, reuse ErrInternal triggers", Require: []string{
		"ErrInternal, nothing created or changed", "a reuse's archive of the previous session",
		"or its change failed", "a busy store included",
	}}
	for source, text := range spawnTexts(t, "") {
		apitest.AssertAgentTextCase(t, source+": spawn description", text, c)
	}
}

// TestSpawnErrorNamesCoverReuse: spawn's ErrorNames (manifest and
// surface.json) include every name reuse returns and not ErrInternal;
// Client.Spawn's "Errors:" list matches, and each tmux name's bullet states
// its reuse cases.
func TestSpawnErrorNamesCoverReuse(t *testing.T) {
	v, ok := manifest.Lookup("spawn")
	if !ok {
		t.Fatal("spawn not in manifest")
	}
	names := map[string][]string{"manifest": v.ErrorNames}
	_, surface := readSurfaceJSON(t)
	for _, sv := range surface.Verbs {
		if sv.Name == "spawn" {
			names["surface.json"] = sv.ErrorNames
		}
	}
	if _, ok := names["surface.json"]; !ok {
		t.Fatal("surface.json has no spawn verb")
	}
	for source, got := range names {
		for _, want := range spawnReuseErrorNames {
			if !slices.Contains(got, want) {
				t.Errorf("%s: spawn ErrorNames %v lack %s", source, got, want)
			}
		}
		if slices.Contains(got, "ErrInternal") {
			t.Errorf("%s: spawn ErrorNames list ErrInternal", source)
		}
	}
	assertGoDocErrorsMatchManifest(t, "Spawn", "spawn")
	for _, name := range spawnReuseErrorNames[1:] { // ErrInstanceIdCollision's bullet is Task 4's
		if text := goDocErrorBulletText(t, "Spawn", name); !strings.Contains(text, "reuse opt-in") {
			t.Errorf("(*Client).Spawn %s bullet does not state its reuse opt-in cases: %q", name, text)
		}
	}
}

// TestSpawnReuseTextsForbiddenForms runs the helper's forbidden-only check
// over every spawn text in surface.json (the manifest's are
// TestManifestTextsNameNoSessionEndingCommand's).
func TestSpawnReuseTextsForbiddenForms(t *testing.T) {
	_, surface := readSurfaceJSON(t)
	for _, sv := range surface.Verbs {
		if sv.Name != "spawn" {
			continue
		}
		apitest.AssertAgentText(t, "surface.json: spawn description", sv.Description)
		for _, p := range sv.Params {
			apitest.AssertAgentText(t, "surface.json: spawn param "+p.Name, p.Description)
		}
		for _, f := range sv.ResultFields {
			apitest.AssertAgentText(t, "surface.json: spawn result field "+f.Name, f.Description)
		}
		return
	}
	t.Fatal("surface.json has no spawn verb")
}
