package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// layerSecret is a layer env value no refusal may print.
const layerSecret = "layer-env-sentinel-0123456789"

func TestLayerEnvNameRefused(t *testing.T) {
	refused := append([]string{"CLAUDE_CODE_USE_VERTEX", "claude_code_use_bedrock", "Anthropic_Base_Url", "ANTHROPIC_CUSTOM_HEADERS"},
		append(append([]string(nil), realModeRefusedEnv...), credentialEnv...)...)
	for _, name := range refused {
		if !layerEnvNameRefused(name) {
			t.Errorf("layerEnvNameRefused(%q) = false; want true", name)
		}
	}
	for _, name := range []string{"PLAIN", "ANTHROPIC_MODEL", "CLAUDE_CODE_USER", "DISABLE_AUTOUPDATER"} {
		if layerEnvNameRefused(name) {
			t.Errorf("layerEnvNameRefused(%q) = true; want false", name)
		}
	}
}

func TestFindLayerEnv(t *testing.T) {
	tests := []struct {
		name, doc, want string
		byValue, found  bool
	}{
		{"nested in an array and an object", `{"a": [{"x": {"env": {"ANTHROPIC_BASE_URL": "v"}}}]}`, "ANTHROPIC_BASE_URL", false, true},
		{"the sorted first entry", `{"env": {"Z_URL": "http://x", "AWS_REGION": "r"}}`, "AWS_REGION", false, true},
		{"the sorted first object", `{"b": {"env": {"AWS_PROFILE": "p"}}, "a": {"env": {"AWS_REGION": "r"}}}`, "AWS_REGION", false, true},
		{"a URL value", `{"env": {"PLAIN": "https://x"}}`, "PLAIN", true, true},
		{"a bearer value after a tab", `{"env": {"PLAIN": "BEARER\tx"}}`, "PLAIN", true, true},
		{"an authorization header value", `{"env": {"PLAIN": "Proxy-Authorization : x"}}`, "PLAIN", true, true},
		{"a word ending in bearer", `{"env": {"PLAIN": "forbearer x"}}`, "", false, false},
		{"a non-string value as JSON text", `{"env": {"PLAIN": {"u": "https://x"}}}`, "PLAIN", true, true},
		{"a number", `{"env": {"PLAIN": 5}}`, "", false, false},
		{"a non-object env", `{"env": "https://x"}`, "", false, false},
		{"a URL outside an env object", `{"apiUrl": "https://x"}`, "", false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var doc any
			if err := json.Unmarshal([]byte(tc.doc), &doc); err != nil {
				t.Fatal(err)
			}
			name, byValue, found := findLayerEnv(doc)
			if name != tc.want || byValue != tc.byValue || found != tc.found {
				t.Errorf("findLayerEnv = %q, %v, %v; want %q, %v, %v", name, byValue, found, tc.want, tc.byValue, tc.found)
			}
		})
	}
}

func TestRealModeLayerFiles(t *testing.T) {
	home := t.TempDir()
	managed := layerFile{kind: "managed", path: managedSettingsPath}
	user := layerFile{kind: "user", path: filepath.Join(home, ".claude", "settings.json")}
	if got, want := realModeLayerFiles(config{}, home), []layerFile{managed, user}; !reflect.DeepEqual(got, want) {
		t.Errorf("no layer flags: %+v; want %+v", got, want)
	}
	c := config{projectSettings: "/p.json", localSettings: "/l.json", mcpConfig: "/m.json"}
	want := []layerFile{managed, user, {"project", "/p.json"}, {"local", "/l.json"}, {"mcp", "/m.json"}}
	if got := realModeLayerFiles(c, home); !reflect.DeepEqual(got, want) {
		t.Errorf("every layer flag: %+v; want %+v", got, want)
	}
}

// TestCheckLayerFilesEnv drives the check directly, the managed layer
// included (its fixed path is absent in the sandbox and cannot be injected).
func TestCheckLayerFilesEnv(t *testing.T) {
	dir := t.TempDir()
	file := func(name, body string) string {
		p := filepath.Join(dir, name)
		writeFile(t, p, body)
		return p
	}
	tests := []struct {
		name  string
		file  layerFile
		parts []string // nil passes; else in the refusal
	}{
		{"a missing file", layerFile{"managed", filepath.Join(dir, "absent.json")}, nil},
		{"a clean file", layerFile{"managed", file("clean.json", `{"env": {"PLAIN": "x"}}`)}, nil},
		{"a refused name", layerFile{"managed", file("name.json", `{"env": {"AWS_PROFILE": "`+layerSecret+`"}}`)},
			[]string{"managed layer", "name.json", "sets AWS_PROFILE, which would take the agents off the gateway"}},
		{"a URL value", layerFile{"mcp", file("value.json", `{"mcpServers": {"s": {"env": {"UPSTREAM": "https://`+layerSecret+`"}}}}`)},
			[]string{"mcp layer", "value.json", "sets UPSTREAM to a value that looks like a URL or an authorization header"}},
		{"not JSON", layerFile{"user", file("bad.json", "token="+layerSecret)}, []string{"user layer", "bad.json", "is not a JSON document"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := checkLayerFilesEnv([]layerFile{tc.file})
			if tc.parts == nil {
				if err != nil {
					t.Fatalf("want pass, got %v", err)
				}
				return
			}
			assertRefused(t, err, ruleRealGatewayOnly)
			for _, p := range tc.parts {
				if !strings.Contains(err.Error(), p) {
					t.Errorf("refusal lacks %q: %v", p, err)
				}
			}
			assertAbsent(t, "refusal", err.Error(), layerSecret)
		})
	}
	t.Run("an unreadable file", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root reads a mode-000 file")
		}
		p := file("locked.json", `{"env": {}}`)
		if err := os.Chmod(p, 0); err != nil {
			t.Fatal(err)
		}
		err := checkLayerFilesEnv([]layerFile{{"project", p}})
		assertRefused(t, err, ruleRealGatewayOnly)
		if !strings.Contains(err.Error(), "cannot read the project layer") {
			t.Errorf("refusal = %v; want it to name the unreadable project layer", err)
		}
	})
}
