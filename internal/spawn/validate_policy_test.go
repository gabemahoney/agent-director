package spawn

import (
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
)

// validatePolicy checks observable policy results without constructing a store.
func validatePolicy(t *testing.T, agent string, args []string, eligible bool, wantArgs, wantDropped []string, wantError string) {
	t.Helper()
	cwd := t.TempDir()
	original := slices.Clone(args)
	r := Resolved{SpawnParams: SpawnParams{Agent: agent, CWD: cwd, AgentArgs: args}, DropResumeArgs: eligible}
	err := Validate(&r)
	if wantError != "" {
		if !errors.Is(err, ErrSpawnDeniedFlag) || err.Error() != "ErrSpawnDeniedFlag: "+wantError {
			t.Fatalf("Validate(%q, eligible=%t) = %v; want ErrSpawnDeniedFlag: %s", args, eligible, err, wantError)
		}
		if !slices.Equal(r.AgentArgs, original) || r.DroppedArgs != nil {
			t.Fatalf("refused arguments changed: args %q, drops %q", r.AgentArgs, r.DroppedArgs)
		}
	} else {
		if err != nil {
			t.Fatalf("Validate(%q, eligible=%t): %v", args, eligible, err)
		}
		if !slices.Equal(r.AgentArgs, wantArgs) || !slices.Equal(r.DroppedArgs, wantDropped) {
			t.Fatalf("Validate args %q, drops %q; want args %q, drops %q", r.AgentArgs, r.DroppedArgs, wantArgs, wantDropped)
		}
	}
	if !slices.Equal(args, original) {
		t.Fatalf("caller's arguments changed: %q; want %q", args, original)
	}
	entries, readErr := os.ReadDir(cwd)
	if readErr != nil || len(entries) != 0 {
		t.Fatalf("Validate wrote files: entries %v, error %v", entries, readErr)
	}
}

func TestValidateClaudeResumePolicy(t *testing.T) {
	// Expected spellings and values come from the verified parser evidence,
	// independently of the production selector/option tables.
	for _, tc := range []struct {
		name, bare string
		args       []string
	}{
		{"resume optional", "--resume", []string{"--resume"}},
		{"resume split", "--resume", []string{"--resume", "S1"}},
		{"resume equals", "--resume", []string{"--resume=S1"}},
		{"short split", "-r", []string{"-r", "S1"}},
		{"short attached", "-r", []string{"-rS1"}},
		{"short equals", "-r", []string{"-r=S1"}},
		{"continue", "--continue", []string{"--continue"}},
		{"short continue", "-c", []string{"-c"}},
		{"continue cluster", "-c", []string{"-cd"}},
		{"resume cluster", "-r", []string{"-hrS1"}},
		{"fork", "--fork-session", []string{"--fork-session"}},
		{"from pr optional", "--from-pr", []string{"--from-pr"}},
		{"from pr split", "--from-pr", []string{"--from-pr", "123"}},
		{"from pr equals", "--from-pr", []string{"--from-pr=123"}},
		{"teleport optional", "--teleport", []string{"--teleport"}},
		{"teleport split", "--teleport", []string{"--teleport", "S1"}},
		{"teleport equals", "--teleport", []string{"--teleport=S1"}},
		{"resume at", "--resume-session-at", []string{"--resume-session-at", "M1"}},
		{"resume at equals", "--resume-session-at", []string{"--resume-session-at=M1"}},
		{"drops turn", "--resume-drops-turn", []string{"--resume-drops-turn", "M1"}},
		{"drops turn equals", "--resume-drops-turn", []string{"--resume-drops-turn=M1"}},
		{"rewind", "--rewind-files", []string{"--rewind-files", "M1"}},
		{"rewind equals", "--rewind-files", []string{"--rewind-files=M1"}},
		{"reply", "--reply-on-resume", []string{"--reply-on-resume"}},
		{"cloud optional", "--cloud", []string{"--cloud"}},
		{"cloud split", "--cloud", []string{"--cloud", "S1"}},
		{"cloud equals", "--cloud", []string{"--cloud=S1"}},
		{"remote optional", "--remote", []string{"--remote"}},
		{"remote split", "--remote", []string{"--remote", "S1"}},
		{"remote equals", "--remote", []string{"--remote=S1"}},
		{"attach", "--attach-serve", []string{"--attach-serve", "S1"}},
		{"attach equals", "--attach-serve", []string{"--attach-serve=S1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			validatePolicy(t, "claude", tc.args, false, nil, nil, tc.bare)
			want := []string(nil)
			if tc.name == "continue cluster" {
				want = []string{"-d"}
			} else if tc.name == "resume cluster" {
				want = []string{"-h"}
			}
			validatePolicy(t, "claude", tc.args, true, want, tc.args, "")
		})
	}
}

func TestValidateClaudePrintFullList(t *testing.T) {
	for _, tc := range []struct {
		name, bare string
		args       []string
	}{
		{"print short", "-p", []string{"-p"}},
		{"print long", "--print", []string{"--print"}},
		{"print equals", "--print", []string{"--print=true"}},
		{"print continue cluster", "-p", []string{"-pc"}},
		{"print after continue cluster", "-p", []string{"-cp"}},
		{"print after resume", "-p", []string{"--resume", "S1", "-p"}},
		{"print long owned", "--print", []string{"--append-system-prompt", "--print"}},
		{"print equals owned", "--print", []string{"--model", "--print=true"}},
		{"print long after delimiter", "--print", []string{"--", "--print"}},
		{"print beats earlier denied setting", "-p", []string{"--settings={}", "--resume=S1", "-pc"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, eligible := range []bool{false, true} {
				validatePolicy(t, "claude", tc.args, eligible, nil, nil, tc.bare)
			}
		})
	}
}

func TestValidateOwnedDeniedLongNames(t *testing.T) {
	for _, tc := range []struct {
		agent, owner string
		flags        []string
	}{
		{"claude", "--append-system-prompt", []string{"--settings", "--output-format", "--resume", "--continue", "--fork-session", "--from-pr", "--teleport", "--resume-session-at", "--resume-drops-turn", "--rewind-files", "--reply-on-resume", "--cloud", "--remote", "--attach-serve"}},
		{"claude", "--tools", []string{"--resume", "--settings"}},
		{"codex", "--model", []string{"--last", "--all", "--include-non-interactive", "--remote", "--remote-auth-token-env"}},
	} {
		for _, flag := range tc.flags {
			for _, suffix := range []string{"", "=value"} {
				t.Run(tc.agent+"/"+tc.owner+"/"+flag+suffix, func(t *testing.T) {
					for _, eligible := range []bool{false, true} {
						validatePolicy(t, tc.agent, []string{tc.owner, flag + suffix, "prompt"}, eligible, nil, nil, flag)
					}
				})
			}
		}
	}
}

func TestValidateClaudeBenignValues(t *testing.T) {
	for _, args := range [][]string{
		{"-dc"}, {"-nrocket"}, {"--model", "-c"}, {"--append-system-prompt", "-concise answers"},
		{"--tools", "-c", "Read"}, {"--session-id", "new-session"}, {"-xc"},
		{"--future=value", "prompt"}, {"--", "-c", "-p"}, {"--model", "-p"},
		{"--ref=main", "--on-branch=work", "--forward-home-settings=false"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			for _, eligible := range []bool{false, true} {
				validatePolicy(t, "claude", args, eligible, args, nil, "")
			}
		})
	}
}

func TestValidateSwitchSurvivors(t *testing.T) {
	for _, tc := range []struct {
		name, agent            string
		args, survivors, drops []string
	}{
		{"safe order and spelling", "claude", []string{"--model=sonnet", "--resume=S1", "-dc", "prompt", "--name=rocket"}, []string{"--model=sonnet", "-dc", "prompt", "--name=rocket"}, []string{"--resume=S1"}},
		{"prompt protected from optional value", "claude", []string{"--debug", "--continue", "prompt", "--model", "sonnet"}, []string{"--debug", "--model", "sonnet", "--", "prompt"}, []string{"--continue"}},
		{"surviving cluster keeps debug meaning", "claude", []string{"-cd2e"}, []string{"--debug=2e"}, []string{"-cd2e"}},
		{"incomplete agent option remains literal", "claude", []string{"--debug", "--continue", "prompt", "--model"}, []string{"--debug", "prompt", "--model"}, []string{"--continue"}},
		{"unknown cannot own selector", "claude", []string{"--future", "-rS1", "prompt"}, []string{"--future", "prompt"}, []string{"-rS1"}},
		{"orphan control before selector", "claude", []string{"--ref", "main", "--cloud=S1", "--forward-home-settings", "false", "--on-branch=work", "--model=sonnet"}, []string{"--model=sonnet"}, []string{"--ref", "main", "--cloud=S1", "--forward-home-settings", "false", "--on-branch=work"}},
		{"environment preserves controls", "claude", []string{"--ref", "main", "--remote=S1", "--environment", "ENV", "--forward-home-settings", "false", "--on-branch=work"}, []string{"--ref", "main", "--environment", "ENV", "--forward-home-settings", "false", "--on-branch=work"}, []string{"--remote=S1"}},
		{"pool preserves controls", "claude", []string{"--cloud=S1", "--pool=POOL", "--ref=main"}, []string{"--pool=POOL", "--ref=main"}, []string{"--cloud=S1"}},
		{"owned environment not context", "claude", []string{"--cloud=S1", "--append-system-prompt", "--environment", "--ref", "main"}, []string{"--append-system-prompt", "--environment"}, []string{"--cloud=S1", "--ref", "main"}},
		{"prompt pool not context", "claude", []string{"--remote=S1", "--ref", "main", "--", "--pool", "POOL"}, []string{"--", "--pool", "POOL"}, []string{"--remote=S1", "--ref", "main"}},
		{"no selector no cleanup", "claude", []string{"--ref=main", "--forward-home-settings=false"}, []string{"--ref=main", "--forward-home-settings=false"}, nil},
		{"codex retains protected prompt", "codex", []string{"--model=gpt", "resume", "S1", "hello"}, []string{"--model=gpt", "--", "hello"}, []string{"resume", "S1"}},
		{"codex last prompt", "codex", []string{"resume", "--last", "hello"}, []string{"--", "hello"}, []string{"resume", "--last"}},
		{"codex all and hidden selector", "codex", []string{"resume", "--all", "--include-non-interactive", "S1"}, nil, []string{"resume", "--all", "--include-non-interactive", "S1"}},
		{"codex fork", "codex", []string{"fork", "--all", "S1"}, nil, []string{"fork", "--all", "S1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			validatePolicy(t, tc.agent, tc.args, true, tc.survivors, tc.drops, "")
		})
	}
}

func TestValidateResumeRefusalAndRemainderOrder(t *testing.T) {
	for _, tc := range []struct {
		name, agent, refusal string
		args                 []string
		eligible             bool
	}{
		{"unknown valueless", "claude", "-c", []string{"--future", "-c"}, false},
		{"orphan precedes selector", "claude", "--ref", []string{"--ref", "main", "--cloud=S1"}, false},
		{"environment preserves ordinary control", "claude", "--cloud", []string{"--on-branch=work", "--environment=ENV", "--cloud=S1"}, false},
		{"context preserves ordinary control", "claude", "--remote", []string{"--ref", "main", "--pool=POOL", "--remote=S1"}, false},
		{"claude denied remainder", "claude", "--settings", []string{"--resume=S1", "--settings={}"}, true},
		{"claude output remainder", "claude", "--output-format", []string{"-rS1", "--output-format=json"}, true},
		{"owned selector cannot disappear", "claude", "--resume", []string{"-c", "--append-system-prompt", "--resume", "foo"}, true},
		{"codex denied surviving control", "codex", "-c hooks.Stop: agent-director sets the hooks and their trust for every Codex launch", []string{"resume", "S1", "-c", "hooks.Stop=[]"}, true},
		{"codex retained prompt still checked", "codex", "--last", []string{"resume", "S1", "--", "--last"}, true},
		{"codex fork hidden flag not stripped", "codex", "--include-non-interactive", []string{"fork", "S1", "--include-non-interactive"}, true},
		{"codex earlier config beats owned long", "codex", "-c hooks.Stop: agent-director sets the hooks and their trust for every Codex launch", []string{"-c", "hooks.Stop=[]", "--model", "--last"}, false},
		{"codex earlier owned long beats config", "codex", "--last", []string{"--model", "--last", "-c", "hooks.Stop=[]"}, false},
		{"codex earlier command beats raw long", "codex", "codex subcommand resume: a spawn launches the interactive agent, and this subcommand picks or resumes another session, runs non-interactively or attaches to a remote or shared service", []string{"resume", "--last"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			validatePolicy(t, tc.agent, tc.args, tc.eligible, nil, nil, tc.refusal)
		})
	}
}

func TestValidateSwitchCheckPrecedence(t *testing.T) {
	cwd := t.TempDir()
	for _, tc := range []struct {
		name, relay, agent, inherited string
		want                          error
	}{
		{"relay before selector drop", "bogus", "claude", "", ErrRelayModeInvalid},
		{"agent before selector drop", "", "Claude", "", ErrAgentInvalid},
		{"ownership before selector drop", "", "claude", "codex", ErrAgentArgsMismatch},
		{"dropped selector exposes reserved env", "", "claude", "", ErrReservedEnvKey},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := []string{"--resume", "S1"}
			r := Resolved{SpawnParams: SpawnParams{Agent: tc.agent, CWD: cwd, AgentArgs: args, RelayMode: tc.relay, ExtraEnv: map[string]string{"HOME": "/x"}}, InheritedArgsAgent: tc.inherited, DropResumeArgs: true}
			if err := Validate(&r); !errors.Is(err, tc.want) {
				t.Fatalf("Validate = %v; want %v", err, tc.want)
			}
			if !slices.Equal(args, []string{"--resume", "S1"}) {
				t.Fatalf("caller's arguments changed: %q", args)
			}
			if tc.want == ErrReservedEnvKey {
				if len(r.AgentArgs) != 0 || !slices.Equal(r.DroppedArgs, args) {
					t.Fatalf("argument check did not precede env check: args %q, drops %q", r.AgentArgs, r.DroppedArgs)
				}
			} else if !slices.Equal(r.AgentArgs, args) || r.DroppedArgs != nil {
				t.Fatalf("early failure dropped args: args %q, drops %q", r.AgentArgs, r.DroppedArgs)
			}
			if entries, err := os.ReadDir(cwd); err != nil || len(entries) != 0 {
				t.Fatalf("Validate wrote files: entries %v, error %v", entries, err)
			}
		})
	}
}
