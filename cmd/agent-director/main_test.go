package main_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// TestHelpVerbAndAliases: help and the no-verb run exit 0 with byte-identical
// stdout: one JSON object, no preamble, whose verbs each have a name; help is
// listed with a description, trail-emit is and the read verb trail-path is not
// (b.ruo), kill is and delete is not, and nothing names an operator action
// (SR-6.8, b.vqr). TestVerbAliases covers --help and -h.
func TestHelpVerbAndAliases(t *testing.T) {
	home := t.TempDir()
	help, stderr, code := runCLIWithHome(t, home, "help")
	if code != 0 || stderr != "" || !strings.HasPrefix(help, "{") {
		t.Fatalf("help: exit=%d stderr=%q stdout=%.80q; want 0, empty stderr, a JSON object", code, stderr, help)
	}
	if stdout, stderr, code := runCLIWithHome(t, home); code != 0 || stderr != "" || stdout != help {
		t.Errorf("no verb: exit=%d, stdout differs from help: %.200q (stderr=%q)", code, stdout, stderr)
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

// TestVerbAliases: --help and -h run help, --version and -v run version
// (b.fv2) as the first argument after the global flags: the alias gives the
// exit code, stdout and stderr of its verb with the same other args (exit 0).
func TestVerbAliases(t *testing.T) {
	home := t.TempDir()
	cases := []struct {
		name        string
		alias, verb []string
	}{
		{"--help", []string{"--help"}, []string{"help"}},
		{"-h", []string{"-h"}, []string{"help"}},
		{"-h before --home", []string{"-h", "--home", home}, []string{"help", "--home", home}},
		{"--version", []string{"--version"}, []string{"version"}},
		{"-v", []string{"-v"}, []string{"version"}},
		{"--home before -v", []string{"--home", home, "-v"}, []string{"--home", home, "version"}},
		{"--version --json", []string{"--version", "--json"}, []string{"version", "--json"}},
		{"-v --help", []string{"-v", "--help"}, []string{"version", "--help"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wantOut, wantErr, wantCode := runCLIWithHome(t, home, tc.verb...)
			if wantCode != 0 || wantErr != "" || wantOut == "" {
				t.Fatalf("%q: exit=%d stderr=%q stdout=%.200q; want 0, empty stderr and output",
					tc.verb, wantCode, wantErr, wantOut)
			}
			stdout, stderr, code := runCLIWithHome(t, home, tc.alias...)
			if code != wantCode || stderr != wantErr || stdout != wantOut {
				t.Errorf("%q: exit=%d stderr=%q stdout=%.200q; want %q's exit=%d stderr=%q stdout=%.200q",
					tc.alias, code, stderr, stdout, tc.verb, wantCode, wantErr, wantOut)
			}
		})
	}
}

// TestVerbAliasAfterVerbIsNoAlias: an alias after a verb is that verb's
// unknown flag, ErrInvalidFlags, not a run of help or version (b.fv2).
func TestVerbAliasAfterVerbIsNoAlias(t *testing.T) {
	for _, flag := range []string{"--version", "-v"} {
		t.Run(flag, func(t *testing.T) {
			stdout, stderr, code := runCLI(t, "list", flag)
			assertOnlyEnvelope(t, stdout, stderr, code, "ErrInvalidFlags")
		})
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
