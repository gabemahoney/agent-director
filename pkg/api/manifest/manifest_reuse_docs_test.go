package manifest_test

// manifest_reuse_docs_test.go pins reuse's documentation (Epic 17 Task 4;
// SR-18.9, SR-18.10, SR-18.16): the reuse_finished parameter text, the
// reserved lists of make-template and docs/settings.md, and the collision
// text on claude_instance_id and Client.Spawn's ErrInstanceIdCollision line.

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// reuseParam is the reuse opt-in's manifest name (b.c4u), the key T1 pins it under.
const reuseParam = "reuse_finished"

// TestReuseFinishedParamText pins SR-18.10's points and SR-18.16's
// forbidden claims on the reuse_finished text, manifest and surface.json.
func TestReuseFinishedParamText(t *testing.T) {
	for source, text := range spawnTexts(t, reuseParam) {
		apitest.AssertAgentTextCase(t, source+": spawn param "+reuseParam, text, apitest.DescReuseFinishedParam())
	}
}

// TestReuseReservedLists: make-template's Description (both surfaces) and
// docs/settings.md's reserved section name the opt-in by its manifest name.
func TestReuseReservedLists(t *testing.T) {
	name := reuseParam
	spawnTexts(t, reuseParam) // fails unless spawn has the param on both surfaces
	for source, desc := range verbDescriptionsBoth(t, "make-template") {
		if !strings.Contains(desc, name) {
			t.Errorf("%s: make-template description does not reserve %s: %q", source, name, desc)
		}
	}
	section := reservedSettingsSection(t)
	if !strings.Contains(section, "`"+name+"`") {
		t.Errorf("docs/settings.md reserved per-invocation params do not list `%s`: %q", name, section)
	}
}

// reservedSettingsSection returns docs/settings.md's "Reserved per-invocation
// params" section, up to the next heading.
func reservedSettingsSection(t *testing.T) string {
	t.Helper()
	_, thisFile, _, _ := runtime.Caller(0)
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(thisFile), "..", "..", "..", "docs", "settings.md"))
	if err != nil {
		t.Fatalf("read docs/settings.md: %v", err)
	}
	_, rest, ok := strings.Cut(string(raw), "\n### Reserved per-invocation params\n")
	if !ok {
		t.Fatal("docs/settings.md has no \"Reserved per-invocation params\" section")
	}
	section, _, _ := strings.Cut(rest, "\n#")
	return section
}

// TestReuseCollisionText pins SR-18.9: claude_instance_id's text states the
// without-opt-in form and Client.Spawn's ErrInstanceIdCollision line both.
func TestReuseCollisionText(t *testing.T) {
	for source, text := range spawnTexts(t, "claude_instance_id") {
		apitest.AssertAgentTextCase(t, source+": spawn param claude_instance_id", text,
			apitest.DescInstanceIDCollision(apitest.CollisionSite{}))
	}
	line := goDocErrorBulletText(t, "Spawn", "ErrInstanceIdCollision")
	apitest.AssertAgentTextCase(t, "(*Client).Spawn Errors: ErrInstanceIdCollision", line,
		apitest.DescInstanceIDCollision(apitest.CollisionSite{Full: true}))
}
