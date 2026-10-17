package api_test

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

func TestSpawnCursorConfigurationInheritance(t *testing.T) {
	// Serial: newSpawnEnv owns HOME and the process environment.
	for _, tc := range []struct {
		name, templateAgent string
		args, want          []string
		config              string
	}{
		{"nil inherits", "cursor", nil, []string{"--plan", "saved\n🦋"}, ""},
		{"empty replaces", "cursor", []string{}, []string{}, ""},
		{"foreign empty actually replaces", "claude", []string{}, []string{}, ""},
		{"foreign nonempty replaces", "claude", []string{"--mode=ask", "--", "--resume\n多行"}, []string{"--mode=ask", "--", "--resume\n多行"}, ""},
		{"suppress questions alone", "cursor", []string{}, []string{}, "[defaults]\ndisable_ask_user = true\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newSpawnEnv(t)
			seedAgentTemplate(t, e, apitest.TemplateFields{Agent: tc.templateAgent, AgentArgs: []string{"--plan", "saved\n🦋"}})
			c := e.c
			if tc.config != "" {
				c = agentClientWithConfig(t, e, tc.config)
			}
			p := api.SpawnParams{Agent: "cursor", CWD: t.TempDir(), Template: "t", AgentArgs: tc.args, Permissions: &api.Permissions{}}
			before := slices.Clone(p.AgentArgs)
			res, err := c.Spawn(p)
			if err != nil {
				t.Fatal(err)
			}
			cols, err := apitest.ReadSpawnColumns(e.dbPath, res.InstanceID)
			if err != nil || !slices.Equal(storedAgentArgs(t, cols), tc.want) || !slices.Equal(p.AgentArgs, before) {
				t.Fatalf("saved/caller arguments: %+v err=%v caller=%q", cols, err, p.AgentArgs)
			}
			creates := e.rec.SocketCallsOf(tmux.CallCreate)
			if len(creates) != 1 || creates[0].Command[1] != "cursor-host" {
				t.Fatalf("gateway create: %+v", creates)
			}
			if tf, err := config.LoadTemplate("t"); err != nil || !reflect.DeepEqual(tf.AgentArgs, []string{"--plan", "saved\n🦋"}) {
				t.Fatalf("template mutated: %+v %v", tf, err)
			}
		})
	}
}

func TestAdviceFollow_CursorRemoveOwnedArgumentOrProtectPrompt(t *testing.T) {
	// Serial: the shared spawn fixture owns HOME and process environment.
	for _, tc := range []struct {
		name           string
		args, repaired []string
		key, advice    string
	}{
		{"split policy", []string{"--sandbox", "sensitive-value", "intentional prompt"}, []string{"intentional prompt"}, "--sandbox", "remove this option and its following value, then retry"},
		{"unsupported mode", []string{"--mode", "sensitive-value", "intentional prompt"}, []string{"intentional prompt"}, "--mode", "remove this option and its following value, then retry"},
		{"equals policy", []string{"--sandbox=sensitive-value", "intentional prompt"}, []string{"intentional prompt"}, "--sandbox", "remove this argument and retry"},
		{"command prompt", []string{"acp"}, []string{"--", "acp"}, "", "use -- before a literal initial prompt"},
		{"chat prompt", []string{"chat"}, []string{"--", "chat"}, "", "use -- before a literal initial prompt"},
		{"resume prompt", []string{"resume"}, []string{"--", "resume"}, "", "remove this argument and retry"},
		{"flag prompt", []string{"--explain"}, []string{"--", "--explain"}, "--explain", "remove this argument and retry"},
		{"bad delimiter", []string{"acp", "--"}, []string{"--", "acp"}, "", "supply it as one argument after --"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newSpawnEnv(t)
			p := api.SpawnParams{Agent: "cursor", CWD: t.TempDir(), AgentArgs: tc.args}
			_, err := e.c.Spawn(p)
			assertOneName(t, err, "ErrSpawnDeniedFlag")
			if err == nil || !strings.Contains(err.Error(), tc.advice) || (tc.key != "" && !strings.Contains(err.Error(), tc.key)) || strings.Contains(err.Error(), "sensitive-value") {
				t.Fatalf("static repair instruction: %v", err)
			}
			assertNoTmuxCalls(t, e.rec)
			if got := listIDs(t, e.c); len(got) != 0 {
				t.Fatalf("refusal wrote rows: %v", got)
			}
			p.AgentArgs = tc.repaired
			advAgentSpawn(t, e, p, "cursor")
			argv := e.rec.SocketCallsOf(tmux.CallCreate)[0].Command
			index := slices.Index(argv, "--initial-prompt")
			if index < 0 || argv[index+1] != tc.repaired[len(tc.repaired)-1] {
				t.Fatalf("repair promoted an owned value to prompt: %q", argv)
			}
		})
	}
}
