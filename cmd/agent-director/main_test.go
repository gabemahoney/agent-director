package main_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// TestHelpVerbAndAliases: help, --help and the no-verb run exit 0 with
// byte-identical stdout: one JSON object, no preamble, whose verbs each have a
// name; help is listed with a description, trail-emit is and the read verb
// trail-path is not (b.ruo), kill is and delete is not, and nothing names an
// operator action (SR-6.8, b.vqr).
func TestHelpVerbAndAliases(t *testing.T) {
	home := t.TempDir()
	help, stderr, code := runCLIWithHome(t, home, "help")
	if code != 0 || stderr != "" || !strings.HasPrefix(help, "{") {
		t.Fatalf("help: exit=%d stderr=%q stdout=%.80q; want 0, empty stderr, a JSON object", code, stderr, help)
	}
	for _, argv := range [][]string{{"--help"}, nil} {
		stdout, stderr, code := runCLIWithHome(t, home, argv...)
		if code != 0 || stderr != "" || stdout != help {
			t.Errorf("%q: exit=%d, stdout differs from help: %.200q (stderr=%q)", argv, code, stdout, stderr)
		}
	}
	if m := apitest.OperatorActionNames.FindString(help); m != "" {
		t.Errorf("SR-6.8: help names %q; nothing the main CLI prints may name an operator action", m)
	}
	var parsed struct {
		Verbs []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		} `json:"verbs"`
	}
	if err := json.Unmarshal([]byte(help), &parsed); err != nil || len(parsed.Verbs) == 0 {
		t.Fatalf("help stdout %.200q: %v; want a non-empty verbs array", help, err)
	}
	listed := map[string]string{}
	for i, v := range parsed.Verbs {
		if v.Name == "" {
			t.Errorf("verbs[%d].name is empty", i)
		}
		listed[v.Name] = v.Description
	}
	for verb, want := range map[string]bool{"help": true, "kill": true, "trail-emit": true, "trail-path": false, "delete": false} {
		if _, got := listed[verb]; got != want {
			t.Errorf("help lists %s = %v; want %v", verb, got, want)
		}
	}
	if listed["help"] == "" {
		t.Error("help verb has an empty description")
	}
}

// TestUnknownVerbWritesErrorEnvelope: an unknown verb, the admin binary's
// delete (b.vqr; the row it names stays) and migrate (no self-service
// migration) each exit 1 with only an ErrUnknownVerb envelope.
func TestUnknownVerbWritesErrorEnvelope(t *testing.T) {
	home := t.TempDir()
	id, err := apitest.SeedSpawn(stateDB(home), "", store.StateEnded, "", "", "", true)
	if err != nil {
		t.Fatalf("SeedSpawn: %v", err)
	}
	for _, argv := range [][]string{{"bogusverb"}, {"delete", "--claude-instance-id", id}, {"migrate"}} {
		t.Run(argv[0], func(t *testing.T) {
			stdout, stderr, code := runCLIWithHome(t, home, argv...)
			if env := assertOnlyEnvelope(t, stdout, stderr, code, "ErrUnknownVerb"); env.ErrDescription == "" {
				t.Errorf("err_description empty in %q", stderr)
			}
		})
	}
	if _, err := apitest.ReadSpawnColumns(stateDB(home), id); err != nil {
		t.Errorf("row %s after delete: %v; want it kept", id, err)
	}
}
