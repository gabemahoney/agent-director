package api_test

import (
	"encoding/json"
	"reflect"
	"slices"
	"testing"

	"github.com/gabemahoney/agent-director/internal/spawn"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/codexfix"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
	"github.com/google/uuid"
)

// Public resume must launch the latest transcript interactively despite legacy arguments.
func TestResumeStoredReplay(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, agent        string
		args, want, prompt []string
		roles              [][]string
	}{
		{"claude older resume", "claude", []string{"--model", "opus", "-r", "OLD", "--verbose"}, []string{"--model", "opus", "--verbose"}, nil, nil},
		{"claude continue", "claude", []string{"-c", "--model", "opus"}, []string{"--model", "opus"}, nil, nil},
		{"claude print after resume", "claude", []string{"--resume=OLD", "--model", "opus", "--print", "-pc"}, []string{"--model", "opus"}, nil, nil},
		{"claude owned denied value", "claude", []string{"--append-system-prompt", "--resume", "foo"}, []string{"--append-system-prompt", "--resume", "foo"}, nil, nil},
		{"claude optional prompt later option", "claude", []string{"--debug", "--continue", "fix this", "--model", "sonnet"}, []string{"--debug", "--model", "sonnet", "--", "fix this"}, []string{"fix this"}, [][]string{{"--debug"}, {"--model", "sonnet"}}},
		{"claude list prompt boundary", "claude", []string{"--tools", "Read", "--continue", "prompt"}, []string{"--tools", "Read", "--", "prompt"}, []string{"prompt"}, [][]string{{"--tools", "Read"}}},
		{"claude print optional later option", "claude", []string{"--debug", "--print", "fix this", "--model", "sonnet"}, []string{"--debug", "--model", "sonnet", "--", "fix this"}, []string{"fix this"}, [][]string{{"--debug"}, {"--model", "sonnet"}}},
		{"claude print list prompt boundary", "claude", []string{"--tools", "Read", "-p", "prompt"}, []string{"--tools", "Read", "--", "prompt"}, []string{"prompt"}, [][]string{{"--tools", "Read"}}},
		{"claude continue cluster collision", "claude", []string{"-cd2e"}, []string{"--debug=2e"}, nil, [][]string{{"--debug", "2e"}}},
		{"claude print cluster collision", "claude", []string{"-pd2e"}, []string{"--debug=2e"}, nil, [][]string{{"--debug", "2e"}}},
		{"claude safe spelling and prompt position", "claude", []string{"fix this", "--continue", "--model=sonnet", "-d2e"}, []string{"fix this", "--model=sonnet", "-d2e"}, []string{"fix this"}, [][]string{{"--model", "sonnet"}, {"--debug-to-stderr"}}},
		{"codex last prompt", "codex", []string{"--model", "o3", "resume", "--last", "--all", "--include-non-interactive", "keep going --last"}, []string{"--model", "o3"}, []string{"keep going --last"}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newResumeEnv(t)
			encoded, err := json.Marshal(tc.args)
			if err != nil {
				t.Fatal(err)
			}
			opts := agentRowOpts(tc.agent, apitest.WithRawAgentArgs(string(encoded)), apitest.WithNoPreTrust())
			latest := uuid.NewString()
			if tc.agent == "codex" {
				home := t.TempDir()
				path := codexfix.RolloutPath(t, home, latest, true)
				opts = append(opts, apitest.WithJsonlPath(path), apitest.WithExtraEnv(map[string]string{"CODEX_HOME": home}))
			}
			r := e.seedRow(t, resumableSpec{SessionID: latest, Opts: opts})
			res, err := e.c.Resume(api.ResumeParams{InstanceID: r.ID})
			if err != nil {
				t.Fatalf("public Resume: %v", err)
			}
			if res.InstanceID != r.ID {
				t.Errorf("result = %+v; want instance %s", res, r.ID)
			}
			argv := rstOneCreate(t, e).Command
			t.Logf("recorded %s latest session %s; stored args %q; expected survivors %q and prompt %q", tc.agent, latest, tc.args, tc.want, tc.prompt)
			if tc.agent == "codex" {
				l := codexfix.Split(t, argv)
				if !slices.Equal(argv[:3], []string{"codex", "resume", latest}) || !slices.Equal(l.PassThrough(), tc.want) || !slices.Equal(l.AfterDashDashArgs(), tc.prompt) {
					t.Errorf("argv = %q; want latest %s, options %q, original prompt %q", argv, latest, tc.want, tc.prompt)
				}
			} else if len(argv) < 5 || !slices.Equal(argv[:4], []string{"claude", "--resume", latest, "--settings"}) || !slices.Equal(argv[5:], tc.want) {
				t.Fatalf("argv = %q; want latest %s and interactive survivors %q", argv, latest, tc.want)
			}
			if tc.agent == "claude" {
				reading := spawn.ReadClaudeArgs(argv[1:])
				if len(reading.Options) < 2 || reading.Options[0].Canonical != "--resume" || !slices.Equal(reading.Options[0].Values, []string{latest}) || reading.Options[1].Canonical != "--settings" || !slices.Equal(reading.Options[1].Values, []string{argv[4]}) || reading.HasPrint() {
					t.Fatalf("parsed AD options = %+v; want latest resume/settings and interactive mode", reading)
				}
				originalOptions, originalPrompt := resumeClaudeReplayRoles(tc.args, false)
				gotOptions, gotPrompt := resumeClaudeReplayRoles(argv[1:], true)
				if tc.roles != nil && (!reflect.DeepEqual(gotOptions, tc.roles) || !slices.Equal(gotPrompt, tc.prompt)) {
					t.Errorf("parsed roles: options %q, prompt %q; want literal options %q, prompt %q", gotOptions, gotPrompt, tc.roles, tc.prompt)
				}

				if !reflect.DeepEqual(gotOptions, originalOptions) || !slices.Equal(gotPrompt, originalPrompt) {
					t.Errorf("surviving roles: options %q, prompt %q; original options %q, prompt %q", gotOptions, gotPrompt, originalOptions, originalPrompt)
				}
			}

			got := e.columns(t, r.ID)
			if got.AgentArgs != r.Before.AgentArgs || got.SessionID != r.Before.SessionID || got.State != store.StatePending {
				t.Errorf("stored replay after resume: args %#v, session %#v, state %#v; want unchanged args/session and pending", got.AgentArgs, got.SessionID, got.State)
			}
		})
	}
}

func resumeClaudeReplayRoles(args []string, injected bool) ([][]string, []string) {
	reading := spawn.ReadClaudeArgs(args)
	var options [][]string
	var prompt []string
	for _, option := range reading.Options {
		if (injected && option.Index < 4) || option.Canonical == "--resume" || option.Canonical == "--continue" || option.Canonical == "--print" {
			continue
		}
		options = append(options, slices.Concat([]string{option.Canonical}, option.Values))
	}
	for _, index := range reading.Positionals {
		prompt = append(prompt, args[index])
	}
	return options, prompt
}
