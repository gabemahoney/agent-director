package config_test

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// TestValidateTemplateName accepts plain names and refuses empty, dot,
// separator and traversal names with ErrTemplateNameUnsafe.
func TestValidateTemplateName(t *testing.T) {
	for _, name := range []string{"dev", "prod-2", "a", "long_name_42"} {
		if err := config.ValidateTemplateName(name); err != nil {
			t.Errorf("ValidateTemplateName(%q) = %v; want nil", name, err)
		}
	}
	for _, name := range []string{"", ".", "..", ".hidden", "foo/bar", `foo\bar`, "foo..bar", "../escape"} {
		if err := config.ValidateTemplateName(name); !errors.Is(err, config.ErrTemplateNameUnsafe) {
			t.Errorf("ValidateTemplateName(%q) = %v; want ErrTemplateNameUnsafe", name, err)
		}
	}
}

// TestTemplatesDir: the templates dir is <HOME>/.config/agent-director/templates,
// whatever XDG_CONFIG_HOME says; with no HOME it is the "~/" display form
// naming the same directory.
func TestTemplatesDir(t *testing.T) {
	home := t.TempDir()
	cases := []struct {
		name, home, xdg, want string
	}{
		{"HOME set", home, "", filepath.Join(home, ".config", "agent-director", "templates")},
		{"HOME set, XDG_CONFIG_HOME elsewhere", home, filepath.Join(t.TempDir(), "xdg"), filepath.Join(home, ".config", "agent-director", "templates")},
		{"HOME empty", "", "", "~/.config/agent-director/templates"},
		{"HOME empty, XDG_CONFIG_HOME set", "", filepath.Join(t.TempDir(), "xdg"), "~/.config/agent-director/templates"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", tc.home)
			t.Setenv("XDG_CONFIG_HOME", tc.xdg)

			if got := config.TemplatesDir(); got != tc.want {
				t.Errorf("TemplatesDir() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestEnsureTemplatesDirCreatesUnderConfigDir: on a bare HOME, EnsureTemplatesDir
// creates <HOME>/.config/agent-director/templates and nothing else, and never
// the old default directory, even with XDG_CONFIG_HOME set elsewhere.
func TestEnsureTemplatesDirCreatesUnderConfigDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "xdg"))

	got, err := config.EnsureTemplatesDir()

	want := filepath.Join(home, ".config", "agent-director", "templates")
	if err != nil || got != want {
		t.Fatalf("EnsureTemplatesDir = %q, %v; want %q", got, err, want)
	}
	if info, err := os.Stat(want); err != nil || !info.IsDir() {
		t.Errorf("stat %s: %v, %v; want a directory", want, info, err)
	}
	var found []string
	err = filepath.WalkDir(home, func(p string, _ os.DirEntry, werr error) error {
		if rel, rerr := filepath.Rel(home, p); werr == nil && rerr == nil && rel != "." {
			found = append(found, rel)
		}
		return werr
	})
	wantTree := []string{".config", filepath.Join(".config", "agent-director"), filepath.Join(".config", "agent-director", "templates")}
	if err != nil || !slices.Equal(found, wantTree) {
		t.Errorf("HOME holds %v (walk error %v); want only %v", found, err, wantTree)
	}
}

func TestEnsureTemplatesDirIsIdempotent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	first, err := config.EnsureTemplatesDir()
	if err != nil {
		t.Fatalf("first EnsureTemplatesDir: %v", err)
	}
	if info, err := os.Stat(first); err != nil || info.Mode().Perm() != 0o700 {
		t.Errorf("stat %s: %v, %v; want mode 0700", first, info, err)
	}
	second, err := config.EnsureTemplatesDir()
	if err != nil || first != second {
		t.Errorf("second EnsureTemplatesDir = %q, %v; want %q", second, err, first)
	}
}

// TestLoadTemplate: a missing file is ErrTemplateNotFound and an unsafe name
// ErrTemplateNameUnsafe; an unknown key, a bad relay_mode, or a per-call key
// (SR-5.1, SR-10.1: named, so a future TemplateFile field cannot silently
// re-enable it) is ErrTemplateMalformed.
func TestLoadTemplate(t *testing.T) {
	cases := []struct {
		name, body string // body "" writes no file
		want       error
		inErr      string
	}{
		{"absent", "", config.ErrTemplateNotFound, ""},
		{"../escape", "", config.ErrTemplateNameUnsafe, ""},
		{"rogue", "cwd = \"/tmp\"\nmystery_field = \"wat\"\n", config.ErrTemplateMalformed, ""},
		{"bad", `relay_mode = "bogus"`, config.ErrTemplateMalformed, ""},
		{"session", "cwd = \"/tmp\"\ntmux_session_name = \"rogue-name\"\n", config.ErrTemplateMalformed, "tmux_session_name"},
		{"reuse", "cwd = \"/tmp\"\nreuse_finished = true\n", config.ErrTemplateMalformed, "reuse_finished"},
		{"reuse-dashed", "cwd = \"/tmp\"\nreuse-finished = true\n", config.ErrTemplateMalformed, "reuse-finished"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := templatesDir(t)
			if tc.body != "" {
				writeTemplate(t, dir, tc.name, tc.body)
			}
			_, err := config.LoadTemplate(tc.name)
			if !errors.Is(err, tc.want) || !strings.Contains(err.Error(), tc.inErr) {
				t.Fatalf("err = %v; want %v naming %q", err, tc.want, tc.inErr)
			}
		})
	}
}

func TestLoadTemplateValidFileDecodes(t *testing.T) {
	dir := templatesDir(t)
	writeTemplate(t, dir, "valid", apitest.TemplateBody(apitest.TemplateFields{
		CWD:         "/tmp",
		RelayMode:   "off",
		Agent:       "claude",
		AgentArgs:   []string{"--model", "opus"},
		Labels:      map[string]string{"project": "foo"},
		Permissions: &config.TemplatePermissions{Allow: []string{"Bash(jq)"}},
	}))

	tf, err := config.LoadTemplate("valid")

	if err != nil {
		t.Fatalf("LoadTemplate: %v", err)
	}
	if tf.CWD != "/tmp" || tf.RelayMode != "off" || tf.Agent != "claude" {
		t.Errorf("scalars: %+v", tf)
	}
	if !slices.Equal(tf.AgentArgs, []string{"--model", "opus"}) {
		t.Errorf("AgentArgs: %v", tf.AgentArgs)
	}
	if tf.AgentDirectorLabels["project"] != "foo" {
		t.Errorf("Labels: %v", tf.AgentDirectorLabels)
	}
	if tf.Permissions == nil || tf.Permissions.Allow[0] != "Bash(jq)" {
		t.Errorf("Permissions: %+v", tf.Permissions)
	}
}

// oldTemplateKeyText is LoadTemplate's refusal of the old claude_args key
// (SR-4.3) for template name: it names the key that replaced it.
func oldTemplateKeyText(name string) string {
	return config.ErrTemplateMalformed.Error() + ": " + name + ": claude_args is no longer accepted; use agent_args"
}

// TestLoadTemplateRefusesOldKeys is the SR-4.3 template refusal: claude_args,
// in any letter case and beside any other keys, is ErrTemplateMalformed
// naming agent_args, and is never read as the new key. The refusal comes at
// the unknown-key check, before the relay_mode check.
func TestLoadTemplateRefusesOldKeys(t *testing.T) {
	args := []string{"--model", "opus"}
	cases := []struct {
		name   string
		fields apitest.TemplateFields
	}{
		{"lower", apitest.TemplateFields{RawKeys: []string{`claude_args = ["--model", "opus"]`}}},
		{"empty_list", apitest.TemplateFields{RawKeys: []string{`claude_args = []`}}},
		{"upper", apitest.TemplateFields{RawKeys: []string{`CLAUDE_ARGS = ["--model", "opus"]`}}},
		{"mixed", apitest.TemplateFields{RawKeys: []string{`Claude_Args = ["--model", "opus"]`}}},
		{"capitalised", apitest.TemplateFields{RawKeys: []string{`Claude_args = ["--model", "opus"]`}}},
		{"beside_valid_fields", apitest.TemplateFields{CWD: "/tmp", RelayMode: "on", Labels: map[string]string{"team": "x"},
			RawKeys: []string{`claude_args = ["--model", "opus"]`}}},
		{"both_old_and_new", apitest.TemplateFields{AgentArgs: args, RawKeys: []string{`claude_args = ["--model", "opus"]`}}},
		{"both_new_then_old", apitest.TemplateFields{RawKeys: []string{`agent_args = ["--model", "opus"]`, `claude_args = ["--model", "opus"]`}}},
		{"both_old_then_new", apitest.TemplateFields{RawKeys: []string{`claude_args = ["--model", "opus"]`, `agent_args = ["--model", "opus"]`}}},
		{"both_upper_old_and_new", apitest.TemplateFields{AgentArgs: args, RawKeys: []string{`CLAUDE_ARGS = ["--model", "opus"]`}}},
		{"old_key_in_two_spellings", apitest.TemplateFields{RawKeys: []string{`claude_args = ["--a"]`, `CLAUDE_ARGS = ["--b"]`}}},
		{"beside_bad_relay_mode", apitest.TemplateFields{RelayMode: "bogus", RawKeys: []string{`claude_args = ["--model", "opus"]`}}},
		{"beside_another_unknown_key", apitest.TemplateFields{RawKeys: []string{`mystery_field = "wat"`, `claude_args = ["--model", "opus"]`}}},
		{"another_unknown_key_after", apitest.TemplateFields{RawKeys: []string{`claude_args = ["--model", "opus"]`, `mystery_field = "wat"`}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			writeTemplate(t, templatesDir(t), tc.name, apitest.TemplateBody(tc.fields))

			_, err := config.LoadTemplate(tc.name)

			if !errors.Is(err, config.ErrTemplateMalformed) || err.Error() != oldTemplateKeyText(tc.name) {
				t.Fatalf("err = %v\nwant  %s", err, oldTemplateKeyText(tc.name))
			}
		})
	}
}

// TestLoadTemplateLoadsAgent: agent loads as claude or codex, in any letter
// case of the key, with or without agent_args (SR-1.1, SR-1.3 step 2); a
// template naming no agent supplies none, an empty agent_args needing no
// agent. agent_args load as the template's agent arguments, alone or beside
// the other fields.
func TestLoadTemplateLoadsAgent(t *testing.T) {
	args := []string{"--model", "opus", "--verbose"}
	cases := []struct {
		name   string
		fields apitest.TemplateFields
		want   config.TemplateFile
	}{
		{"claude", apitest.TemplateFields{Agent: "claude"}, config.TemplateFile{Agent: "claude"}},
		{"codex", apitest.TemplateFields{Agent: "codex"}, config.TemplateFile{Agent: "codex"}},
		{"cursor", apitest.TemplateFields{Agent: "cursor"}, config.TemplateFile{Agent: "cursor"}},
		{"cursor_with_args", apitest.TemplateFields{Agent: "cursor", AgentArgs: []string{"--model", "x"}}, config.TemplateFile{Agent: "cursor", AgentArgs: []string{"--model", "x"}}},
		{"upper_case_key", apitest.TemplateFields{RawKeys: []string{`AGENT = "codex"`}}, config.TemplateFile{Agent: "codex"}},
		{"no_agent_supplies_none", apitest.TemplateFields{CWD: "/tmp"}, config.TemplateFile{CWD: "/tmp"}},
		{"empty_agent_is_none", apitest.TemplateFields{RawKeys: []string{`agent = ""`}}, config.TemplateFile{}},
		{"empty_agent_args_without_agent", apitest.TemplateFields{RawKeys: []string{`agent_args = []`}},
			config.TemplateFile{AgentArgs: []string{}}},
		{"agent_args_with_claude", apitest.TemplateFields{Agent: "claude", AgentArgs: args},
			config.TemplateFile{Agent: "claude", AgentArgs: args}},
		{"agent_args_with_codex", apitest.TemplateFields{Agent: "codex", AgentArgs: []string{"--x"}},
			config.TemplateFile{Agent: "codex", AgentArgs: []string{"--x"}}},
		{"agent_args_upper_case_key", apitest.TemplateFields{Agent: "claude", RawKeys: []string{`AGENT_ARGS = ["--model", "opus", "--verbose"]`}},
			config.TemplateFile{Agent: "claude", AgentArgs: args}},
		{"beside_other_fields", apitest.TemplateFields{Agent: "claude", AgentArgs: args, CWD: "/tmp", RelayMode: "on"},
			config.TemplateFile{Agent: "claude", AgentArgs: args, CWD: "/tmp", RelayMode: "on"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			writeTemplate(t, templatesDir(t), tc.name, apitest.TemplateBody(tc.fields))

			tf, err := config.LoadTemplate(tc.name)

			if err != nil || !reflect.DeepEqual(tf, tc.want) {
				t.Fatalf("LoadTemplate = %+v, %v; want %+v", tf, err, tc.want)
			}
		})
	}
}

// TestLoadTemplateRefusesAgent: an agent other than exactly claude or codex is
// ErrTemplateMalformed, and agent_args with no agent is ErrTemplateAgentMissing
// (never ErrTemplateMalformed), each with its exact text (SR-1.3 step 2). The
// checks run in load order, relay_mode then agent then agent_args without an
// agent, so each row beside an earlier failure reports the earlier one.
func TestLoadTemplateRefusesAgent(t *testing.T) {
	const missingText = "agent_args are set but the template names no agent; " +
		`add agent = "claude" or "codex", or remove agent_args`
	args := []string{"--model", "opus"}
	cases := []struct {
		name   string
		fields apitest.TemplateFields
		want   error
		text   string // what follows "<name>: "
	}{
		{"cursor_wrong_case", apitest.TemplateFields{Agent: "Cursor"}, config.ErrTemplateMalformed, `agent="Cursor" (want claude or codex)`},
		{"wrong_case", apitest.TemplateFields{Agent: "Claude"}, config.ErrTemplateMalformed, `agent="Claude" (want claude or codex)`},
		{"upper_case_value", apitest.TemplateFields{Agent: "CODEX"}, config.ErrTemplateMalformed, `agent="CODEX" (want claude or codex)`},
		{"padded_before", apitest.TemplateFields{Agent: " claude"}, config.ErrTemplateMalformed, `agent=" claude" (want claude or codex)`},
		{"padded_after", apitest.TemplateFields{Agent: "codex "}, config.ErrTemplateMalformed, `agent="codex " (want claude or codex)`},
		{"unknown", apitest.TemplateFields{Agent: "gpt"}, config.ErrTemplateMalformed, `agent="gpt" (want claude or codex)`},
		{"unknown_with_agent_args", apitest.TemplateFields{Agent: "gpt", AgentArgs: args}, config.ErrTemplateMalformed,
			`agent="gpt" (want claude or codex)`},
		{"agent_args_without_agent", apitest.TemplateFields{AgentArgs: args}, config.ErrTemplateAgentMissing, missingText},
		{"agent_args_beside_empty_agent", apitest.TemplateFields{AgentArgs: args, RawKeys: []string{`agent = ""`}},
			config.ErrTemplateAgentMissing, missingText},
		{"agent_args_without_agent_beside_other_fields", apitest.TemplateFields{AgentArgs: args, CWD: "/tmp", RelayMode: "on"},
			config.ErrTemplateAgentMissing, missingText},
		// Adjacent pairs of the load order: relay_mode, agent, agent_args without agent.
		{"bad_relay_mode_and_bad_agent", apitest.TemplateFields{RelayMode: "bogus", Agent: "gpt"}, config.ErrTemplateMalformed,
			`relay_mode="bogus" (want on/off)`},
		{"bad_relay_mode_and_agent_args_without_agent", apitest.TemplateFields{RelayMode: "bogus", AgentArgs: args}, config.ErrTemplateMalformed,
			`relay_mode="bogus" (want on/off)`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			writeTemplate(t, templatesDir(t), tc.name, apitest.TemplateBody(tc.fields))

			_, err := config.LoadTemplate(tc.name)

			want := tc.want.Error() + ": " + tc.name + ": " + tc.text
			if !errors.Is(err, tc.want) || err.Error() != want {
				t.Fatalf("err = %v\nwant  %s", err, want)
			}
			if tc.want == config.ErrTemplateAgentMissing && errors.Is(err, config.ErrTemplateMalformed) {
				t.Errorf("err = %v is also ErrTemplateMalformed", err)
			}
		})
	}
}

// templateCaseVariantText is LoadTemplate's refusal of template name setting
// keys under names that differ only in letter case (b.2u1), each of groups
// listing one key's names.
func templateCaseVariantText(name string, groups ...string) string {
	return config.ErrTemplateMalformed.Error() + ": " + name + ": " +
		caseVariantTextExcept("[extra_env] and [labels]", groups...)
}

// TestLoadTemplateRefusesCaseVariantKeys is the b.2u1 regression: a key set
// under names differing only in letter case is ErrTemplateMalformed naming
// them in file order, with one text on each of 100 loads (once random).
func TestLoadTemplateRefusesCaseVariantKeys(t *testing.T) {
	cases := []struct{ name, body, want string }{
		{"two_key_spellings", "RELAY_MODE = \"on\"\nrelay_mode = \"off\"\n",
			templateCaseVariantText("two_key_spellings", "RELAY_MODE and relay_mode")},
		// The relay_mode check would have checked whichever value the decoder kept.
		{"refused_value_under_one_name", "RELAY_MODE = \"on\"\nrelay_mode = \"bogus\"\n",
			templateCaseVariantText("refused_value_under_one_name", "RELAY_MODE and relay_mode")},
		{"agent_two_spellings", "AGENT = \"claude\"\nagent = \"codex\"\n",
			templateCaseVariantText("agent_two_spellings", "AGENT and agent")},
		// The agent check would have checked whichever value the decoder kept.
		{"agent_refused_value_under_one_name", "AGENT = \"gpt\"\nagent = \"claude\"\n",
			templateCaseVariantText("agent_refused_value_under_one_name", "AGENT and agent")},
		{"three_key_spellings", "cwd = \"/a\"\nCWD = \"/b\"\nCwd = \"/c\"\n",
			templateCaseVariantText("three_key_spellings", "cwd, CWD and Cwd")},
		{"two_table_spellings", "[permissions]\nallow = [\"a\"]\n\n[Permissions]\nallow = [\"b\"]\n",
			templateCaseVariantText("two_table_spellings", "[permissions] allow and [Permissions] allow")},
		{"two_nested_key_spellings", "[permissions]\nallow = [\"a\"]\nALLOW = [\"b\"]\n",
			templateCaseVariantText("two_nested_key_spellings", "[permissions] allow and [permissions] ALLOW")},
		{"map_table_two_spellings", "[extra_env]\nFOO = \"a\"\n\n[EXTRA_ENV]\nFOO = \"b\"\n",
			templateCaseVariantText("map_table_two_spellings", "[extra_env] FOO and [EXTRA_ENV] FOO")},
		{"map_table_dotted", "labels.team = \"a\"\n\n[LABELS]\nteam = \"b\"\n",
			templateCaseVariantText("map_table_dotted", "[labels] team and [LABELS] team")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			writeTemplate(t, templatesDir(t), tc.name, tc.body)
			for i := range 100 {
				_, err := config.LoadTemplate(tc.name)
				if !errors.Is(err, config.ErrTemplateMalformed) || err.Error() != tc.want {
					t.Fatalf("load %d: err = %v\nwant       %s", i+1, err, tc.want)
				}
			}
		})
	}
}

// TestLoadTemplateLoadsCaseVariantNames: one non-lowercase spelling loads as
// its field, map keys differing in case are separate entries, and one table
// under two spellings setting different keys merges, on each of 100 loads.
func TestLoadTemplateLoadsCaseVariantNames(t *testing.T) {
	cases := []struct {
		name, body string
		want       config.TemplateFile
	}{
		{"single_spellings", "RELAY_MODE = \"on\"\nCWD = \"/tmp\"\n\n[Permissions]\nALLOW = [\"Bash(jq)\"]\n",
			config.TemplateFile{CWD: "/tmp", RelayMode: "on",
				Permissions: &config.TemplatePermissions{Allow: []string{"Bash(jq)"}}}},
		{"map_keys", "[extra_env]\nFOO = \"a\"\nfoo = \"b\"\n\n[labels]\nTeam = \"x\"\nteam = \"y\"\n",
			config.TemplateFile{ExtraEnv: map[string]string{"FOO": "a", "foo": "b"},
				AgentDirectorLabels: map[string]string{"Team": "x", "team": "y"}}},
		{"tables_different_keys",
			"[extra_env]\nFOO = \"a\"\n\n[EXTRA_ENV]\nBAR = \"b\"\n\n[permissions]\nallow = [\"x\"]\n\n[PERMISSIONS]\ndeny = [\"y\"]\n",
			config.TemplateFile{ExtraEnv: map[string]string{"FOO": "a", "BAR": "b"},
				Permissions: &config.TemplatePermissions{Allow: []string{"x"}, Deny: []string{"y"}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			writeTemplate(t, templatesDir(t), tc.name, tc.body)
			for i := range 100 {
				tf, err := config.LoadTemplate(tc.name)
				if err != nil || !reflect.DeepEqual(tf, tc.want) {
					t.Fatalf("load %d: LoadTemplate = %+v, %v; want %+v", i+1, tf, err, tc.want)
				}
			}
		})
	}
}

// TestTemplateMapTablesMatchTemplateFile: every TemplateFile map field, at any
// depth, loads FOO and foo as two entries, as only a top-level table named in
// templateMapTables does. The refusal texts above pin the list's contents.
func TestTemplateMapTablesMatchTemplateFile(t *testing.T) {
	var walk func(typ reflect.Type, path string, index []int)
	walk = func(typ reflect.Type, path string, index []int) {
		for i := range typ.NumField() {
			f := typ.Field(i)
			name, _, _ := strings.Cut(f.Tag.Get("toml"), ",")
			if path != "" {
				name = path + "." + name
			}
			at := append(slices.Clone(index), i)
			ft := f.Type
			if ft.Kind() == reflect.Pointer {
				ft = ft.Elem()
			}
			switch ft.Kind() {
			case reflect.Struct:
				walk(ft, name, at)
			case reflect.Map:
				t.Run(name, func(t *testing.T) {
					writeTemplate(t, templatesDir(t), "map", "["+name+"]\nFOO = \"a\"\nfoo = \"b\"\n")
					tf, err := config.LoadTemplate("map")
					var got any
					if v, ferr := reflect.ValueOf(tf).FieldByIndexErr(at); ferr == nil {
						got = v.Interface()
					}
					if want := map[string]string{"FOO": "a", "foo": "b"}; err != nil || !reflect.DeepEqual(got, want) {
						t.Errorf("[%s] FOO and foo: LoadTemplate = %v, %v; want %v. A map field must be a top-level"+
							" table named in templateMapTables: decoderKey keeps key names as written only there", name, got, err, want)
					}
				})
			}
		}
	}
	walk(reflect.TypeOf(config.TemplateFile{}), "", nil)
}

// templatesDir points HOME at a temp dir and returns its templates dir.
func templatesDir(t *testing.T) string {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	dir, err := config.EnsureTemplatesDir()
	if err != nil {
		t.Fatalf("EnsureTemplatesDir: %v", err)
	}
	return dir
}

// writeTemplate writes body as <dir>/<name>.toml.
func writeTemplate(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name+".toml"), []byte(body), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
}
