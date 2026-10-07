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
	writeTemplate(t, templatesDir(t), "valid", `cwd = "/tmp"
relay_mode = "off"
claude_args = ["--model", "opus"]

[labels]
project = "foo"

[permissions]
allow = ["Bash(jq)"]
`)
	tf, err := config.LoadTemplate("valid")
	if err != nil {
		t.Fatalf("LoadTemplate: %v", err)
	}
	if tf.CWD != "/tmp" || tf.RelayMode != "off" {
		t.Errorf("scalars: %+v", tf)
	}
	if len(tf.ClaudeArgs) != 2 || tf.ClaudeArgs[0] != "--model" {
		t.Errorf("ClaudeArgs: %v", tf.ClaudeArgs)
	}
	if tf.AgentDirectorLabels["project"] != "foo" {
		t.Errorf("Labels: %v", tf.AgentDirectorLabels)
	}
	if tf.Permissions == nil || tf.Permissions.Allow[0] != "Bash(jq)" {
		t.Errorf("Permissions: %+v", tf.Permissions)
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
