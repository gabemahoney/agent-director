package apitest

import (
	"reflect"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/config"
)

// TestTemplateBody_LoadsWithGivenFields: a built body loads through the template loader with every field given.
func TestTemplateBody_LoadsWithGivenFields(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	perms := &config.TemplatePermissions{Allow: []string{"Read"}, Deny: []string{"Bash(rm:*)"}, Ask: []string{"Write"}}
	want := config.TemplateFile{
		CWD:                 "/work/here",
		RelayMode:           "on",
		AgentArgs:           []string{"--model", "opus"},
		ExtraEnv:            map[string]string{"FOO": "1"},
		AgentDirectorLabels: map[string]string{"team": "a"},
		Permissions:         perms,
	}
	body := TemplateBody(TemplateFields{
		AgentArgs: want.AgentArgs, CWD: want.CWD, RelayMode: want.RelayMode,
		ExtraEnv: want.ExtraEnv, Labels: want.AgentDirectorLabels, Permissions: perms,
	})
	if _, err := SeedTemplate(config.TemplatesDir(), "full", body); err != nil {
		t.Fatalf("SeedTemplate: %v", err)
	}
	got, err := config.LoadTemplate("full")
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("LoadTemplate = %+v, %v; want %+v", got, err, want)
	}
}

// TestTemplateBody_RawKeys: raw keys appear as given, one per line, ahead of the encoded fields.
func TestTemplateBody_RawKeys(t *testing.T) {
	raw := []string{`claude_args = ["--old"]`, `agent_args = ["--new"]`, `CWD = "/x"`}
	body := TemplateBody(TemplateFields{RelayMode: "off", RawKeys: raw})
	lines := strings.Split(strings.TrimSuffix(body, "\n"), "\n")
	if len(lines) != 4 || !reflect.DeepEqual(lines[:3], raw) || lines[3] != `relay_mode = "off"` {
		t.Errorf("body lines = %q; want the raw keys %q, then relay_mode", lines, raw)
	}
}
