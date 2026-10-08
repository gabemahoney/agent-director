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

func TestFindLayerHeader(t *testing.T) {
	tests := []struct{ name, doc, want string }{ // want "" finds nothing
		{"an MCP server's bearer value", `{"mcpServers": {"api": {"headers": {"X-Auth": "Bearer x"}}}}`, "X-Auth"},
		{"a hook's header in an array", `{"hooks": {"Stop": [{"hooks": [{"headers": {"X-Fwd": "https://x"}}]}]}}`, "X-Fwd"},
		{"the sorted first refused entry", `{"headers": {"C": "https://x", "A": "plain", "B": "authorization: x"}}`, "B"},
		{"a non-string value as JSON text", `{"headers": {"X-Auth": ["Bearer x"]}}`, "X-Auth"},
		{"plain values", `{"headers": {"X-Tenant": "acme", "Accept": "application/json", "N": 5}}`, ""},
		{"a non-object headers", `{"headers": "Bearer x"}`, ""},
		{"a url and env outside a headers object", `{"mcpServers": {"api": {"url": "https://x", "env": {"U": "https://x"}}}}`, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var doc any
			if err := json.Unmarshal([]byte(tc.doc), &doc); err != nil {
				t.Fatal(err)
			}
			if name, found := findLayerHeader(doc); name != tc.want || found != (tc.want != "") {
				t.Errorf("findLayerHeader = %q, %v; want %q", name, found, tc.want)
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

// credentialLayerCase is a layer run.sh's refuse_credential_layer refuses:
// a file named file holding body or, with link set, a file named file that
// links to one named link holding body. Both refusals contain text.
type credentialLayerCase struct {
	name, kind, file, link, body, text string
}

// credentialLayerCases are refused alike by run.sh (TestRunnerLayerReport)
// and the driver (TestCheckLayerFiles; TestPreflightRules runs the first
// through the preflight), b.vyb.
var credentialLayerCases = []credentialLayerCase{
	{"an env key", "user", "user.json", "", `{"env": {"MY_API_TOKEN": "` + layerSecret + `", "PLAIN": "x"}}`, "credential-like key MY_API_TOKEN"},
	{"apiKeyHelper", "managed", "managed.json", "", `{"apiKeyHelper": "/bin/echo ` + layerSecret + `"}`, "credential-like key apiKeyHelper"},
	{"an MCP header", "mcp", "mcp.json", "",
		`{"mcpServers": {"api": {"type": "http", "url": "https://mcp.invalid", "headers": {"Authorization": "Bearer ` + layerSecret + `"}}}}`,
		"credential-like key Authorization"},
	// A bearer value under a header name holding no credential-like part,
	// beside the MCP server's url, which is never value-checked (b.qkg).
	{"an MCP header value under a plain name", "mcp", "mcp.json", "",
		`{"mcpServers": {"api": {"type": "http", "url": "https://example.invalid/mcp", "headers": {"X-Auth": "Bearer sk-not-a-real-token-` + layerSecret + `"}}}}`,
		"sets the header X-Auth to a value that looks like a URL or an authorization header"},
	// Several refused entries: both name the sorted first, whatever the file
	// order (b.qkg): keys sorted at every depth, an object's own entries first.
	{"the sorted first refused header", "user", "user.json", "",
		`{"headers": {"C": "https://` + layerSecret + `", "A": "plain", "B": "authorization: ` + layerSecret + `"}}`,
		"sets the header B to a value"},
	{"the sorted first refused nested header", "mcp", "mcp.json", "",
		`{"z": {"headers": {"Z1": "https://` + layerSecret + `"}}, "mcpServers": {"b": {"headers": {"Y": "Bearer ` + layerSecret + `"}}, ` +
			`"a": {"alt": {"headers": {"W": "https://` + layerSecret + `"}}, "headers": {"X2": "https://` + layerSecret + `", "X1": "Bearer ` + layerSecret + `"}}}}`,
		"sets the header X1 to a value"},
	// An env entry wins over a header, and a value over a later refused name.
	{"the sorted first refused env entry", "project", "project.json", "",
		`{"a": {"headers": {"H": "https://` + layerSecret + `"}}, "env": {"CLAUDE_CODE_USE_VERTEX": "1", "A": "plain", "BASE": "Bearer ` + layerSecret + `"}}`,
		"sets BASE to a value"},
	{"a lower-case key in an array", "project", "project.json", "", `{"hooks": {"Stop": [{"hooks": [{"type": "command", "db_passwd": "` + layerSecret + `"}]}]}}`,
		"credential-like key db_passwd"},
	// The sorted first key wins over a credential-producing setting too
	// (the case below pins the other direction).
	{"the sorted first credential-like key", "user", "user.json", "",
		`{"zSecret": "` + layerSecret + `", "otelHeadersHelper": "x", "apiKeyHelper": "x", "a": {"Authorization": "x"}}`,
		"credential-like key Authorization"},
	{"Claude Code's state file", "user", ".claude.json", "", `{"oauthAccount": {"emailAddress": "` + layerSecret + `"}}`,
		"is (or links to) Claude Code's .claude.json"},
	{"Claude Code's credentials file", "project", ".credentials.json", "", `{"claudeAiOauth": {"accessToken": "` + layerSecret + `"}}`,
		"is (or links to) Claude Code's .credentials.json"},
	{"a link to the credentials file", "local", "local.json", ".credentials.json", `{"claudeAiOauth": {"accessToken": "` + layerSecret + `"}}`,
		"is (or links to) Claude Code's .credentials.json"},
	{"not JSON", "user", "user.json", "", "token=" + layerSecret, "not a JSON document"},
	// Credential-producing settings, matched by whole name at any depth (b.tba).
	{"awsAuthRefresh", "managed", "managed.json", "", `{"awsAuthRefresh": "/bin/echo ` + layerSecret + `"}`,
		"credential-producing setting awsAuthRefresh"},
	{"gcpAuthRefresh in another case", "user", "user.json", "", `{"GCPAuthRefresh": "/bin/echo ` + layerSecret + `"}`,
		"credential-producing setting GCPAuthRefresh"},
	{"otelHeadersHelper", "project", "project.json", "", `{"otelHeadersHelper": "/bin/echo ` + layerSecret + `"}`,
		"credential-producing setting otelHeadersHelper"},
	{"policyHelper", "local", "local.json", "", `{"policyHelper": {"path": "/bin/echo ` + layerSecret + `"}}`,
		"credential-producing setting policyHelper"},
	{"an MCP server's headersHelper", "mcp", "mcp.json", "",
		`{"mcpServers": {"api": {"type": "http", "url": "https://mcp.invalid", "headersHelper": "/bin/echo ` + layerSecret + `"}}}`,
		"credential-producing setting headersHelper"},
	{"the sorted first refused key of either kind", "user", "user.json", "", `{"zToken": "` + layerSecret + `", "awsAuthRefresh": "x"}`,
		"credential-producing setting awsAuthRefresh"},
}

// write writes the case's layer under fresh temp dirs and returns its path.
func (c credentialLayerCase) write(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), c.file)
	if c.link == "" {
		writeFile(t, path, c.body)
		return path
	}
	target := filepath.Join(t.TempDir(), c.link)
	writeFile(t, target, c.body)
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	return path
}

// assertLayerRefusal checks err is a real-gateway-only refusal holding every
// part and never layerSecret.
func assertLayerRefusal(t *testing.T, err error, parts ...string) {
	t.Helper()
	assertRefused(t, err, ruleRealGatewayOnly)
	for _, p := range parts {
		if !strings.Contains(err.Error(), p) {
			t.Errorf("refusal lacks %q: %v", p, err)
		}
	}
	assertAbsent(t, "refusal", err.Error(), layerSecret)
}

// TestCheckLayerFiles drives the check directly, the managed layer
// included (its fixed path is absent in the sandbox and cannot be injected).
func TestCheckLayerFiles(t *testing.T) {
	for _, tc := range credentialLayerCases {
		t.Run(tc.name, func(t *testing.T) {
			path := tc.write(t)
			assertLayerRefusal(t, checkLayerFiles([]layerFile{{tc.kind, path}}), tc.kind+" layer "+path, tc.text)
		})
	}
	dir := t.TempDir()
	file := func(name, body string) string {
		p := filepath.Join(dir, name)
		writeFile(t, p, body)
		return p
	}
	link := func(name, target string) string {
		p := filepath.Join(dir, name)
		if err := os.Symlink(target, p); err != nil {
			t.Fatal(err)
		}
		return p
	}
	clean := file("clean.json", `{"env": {"PLAIN": "x"}}`)
	state := file(".claude.json", `{"oauthAccount": {"emailAddress": "`+layerSecret+`"}}`)
	// Dangling links: the target may be created later in the run (b.vyb).
	dangling := link("dangling.json", filepath.Join(dir, "absent.json"))
	laterState := link("later-state.json", filepath.Join(dir, "later", ".claude.json"))
	laterCreds := link("creds-chain.json", link("creds-mid.json", filepath.Join(t.TempDir(), ".credentials.json")))
	const unresolved = "is a link that does not resolve"
	tests := []struct {
		name  string
		file  layerFile
		parts []string // nil passes; else in the refusal
	}{
		{"a missing file", layerFile{"managed", filepath.Join(dir, "absent.json")}, nil},
		{"a clean file", layerFile{"managed", clean}, nil},
		{"a link to a clean file", layerFile{"user", link("clean-link.json", clean)}, nil},
		{"a dangling link", layerFile{"user", dangling}, []string{"user layer " + dangling, unresolved}},
		{"a dangling link to a state file", layerFile{"user", laterState}, []string{"user layer " + laterState, unresolved}},
		{"a link chain dangling at a credentials file", layerFile{"project", laterCreds}, []string{"project layer " + laterCreds, unresolved}},
		{"credential-like words in a value", layerFile{"user", file("words.json", `{"env": {"PLAIN": "KEY TOKEN SECRET"}}`)}, nil},
		{"near-miss setting names", layerFile{"user", file("near.json", `{"otelHeadersHelperX": "x", "myPolicyHelper": "x"}`)}, nil},
		{"the dry run's MCP config", layerFile{"mcp", file("dry-mcp.json", dryMCPConfig)}, nil},
		{"plain MCP headers and a url", layerFile{"mcp", file("plain-headers.json",
			`{"mcpServers": {"api": {"type": "http", "url": "https://mcp.invalid/mcp", "headers": {"X-Tenant": "acme", "Accept": "application/json"}}}}`)}, nil},
		{"a link chain to the state file", layerFile{"local", link("chain.json", link("mid.json", state))},
			[]string{"local layer", "chain.json", "is (or links to) Claude Code's .claude.json"}},
		{"a state file path with nothing there yet", layerFile{"mcp", filepath.Join(dir, "sub", ".claude.json")},
			[]string{"mcp layer", "is (or links to) Claude Code's .claude.json"}},
		{"a symlink loop", layerFile{"user", link("loop.json", filepath.Join(dir, "loop.json"))}, []string{"cannot resolve the user layer"}},
		{"a refused name", layerFile{"managed", file("name.json", `{"env": {"AWS_PROFILE": "`+layerSecret+`"}}`)},
			[]string{"managed layer", "name.json", "sets AWS_PROFILE, which would take the agents off the gateway"}},
		{"a URL value", layerFile{"mcp", file("value.json", `{"mcpServers": {"s": {"env": {"UPSTREAM": "https://`+layerSecret+`"}}}}`)},
			[]string{"mcp layer", "value.json", "sets UPSTREAM to a value that looks like a URL or an authorization header"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := checkLayerFiles([]layerFile{tc.file})
			if tc.parts == nil {
				if err != nil {
					t.Fatalf("want pass, got %v", err)
				}
				return
			}
			assertLayerRefusal(t, err, tc.parts...)
		})
	}
	t.Run("the first refused file stops the check", func(t *testing.T) {
		err := checkLayerFiles([]layerFile{{"managed", clean}, {"user", state}, {"mcp", file("bad.json", "not json")}})
		assertLayerRefusal(t, err, "user layer", "Claude Code's .claude.json")
	})
	t.Run("an unreadable file", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root reads a mode-000 file")
		}
		p := file("locked.json", `{"env": {}}`)
		if err := os.Chmod(p, 0); err != nil {
			t.Fatal(err)
		}
		assertLayerRefusal(t, checkLayerFiles([]layerFile{{"project", p}}), "cannot read the project layer")
	})
}

func TestRealModeRefusedFiles(t *testing.T) {
	type entry struct {
		kind, path string
		dir        bool
	}
	home := t.TempDir()
	var got []entry
	for _, f := range realModeRefusedFiles(home) {
		got = append(got, entry{f.kind, f.path, f.dir})
	}
	want := []entry{
		{"credentials", filepath.Join(home, ".claude", ".credentials.json"), false},
		{"managed MCP", "/etc/claude-code/managed-mcp.json", false},
		{"managed settings drop-in", "/etc/claude-code/managed-settings.d", true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("refused files %+v; want %+v", got, want)
	}
}

// TestCheckRefusedFiles: anything at a refused path is refused by path,
// without its content; nothing there passes (b.tba).
func TestCheckRefusedFiles(t *testing.T) {
	dir := t.TempDir()
	at := func(name string) refusedFile {
		return refusedFile{kind: "credentials", path: filepath.Join(dir, name), why: "test reason"}
	}
	// dropIn is a refused directory, as the managed settings drop-in is.
	dropIn := func(name string) refusedFile {
		return refusedFile{kind: "managed settings drop-in", path: filepath.Join(dir, name), why: "test reason", dir: true}
	}
	writeFile(t, filepath.Join(dir, "file.json"), `{"claudeAiOauth": {"accessToken": "`+layerSecret+`"}}`)
	for _, d := range []string{"dir.json", "empty.d"} {
		if err := os.Mkdir(filepath.Join(dir, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, filepath.Join(dir, "full.d", "10-gateway.json"), `{"env": {"ANTHROPIC_BASE_URL": "https://`+layerSecret+`"}}`)
	if err := os.Symlink(filepath.Join(dir, "absent.json"), filepath.Join(dir, "dangling.json")); err != nil {
		t.Fatal(err)
	}
	const present = "is present (its content is never read or printed): test reason"
	const dropInDir = "managed settings drop-in directory"
	for _, tc := range []struct {
		name, what string
		f          refusedFile
	}{
		{"a regular file", "credentials file", at("file.json")},
		{"a directory", "credentials file", at("dir.json")},
		{"a dangling link", "credentials file", at("dangling.json")},
		{"an empty drop-in directory", dropInDir, dropIn("empty.d")},
		{"a drop-in directory holding a settings file", dropInDir, dropIn("full.d")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertLayerRefusal(t, checkRefusedFiles([]refusedFile{tc.f}), "Claude Code's "+tc.what+" "+tc.f.path+" "+present)
		})
	}
	t.Run("an unreadable drop-in directory is present, never listed", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root lists a mode-000 directory")
		}
		f := dropIn("locked.d")
		writeFile(t, filepath.Join(f.path, "10-gateway.json"), `{"apiKeyHelper": "/bin/echo `+layerSecret+`"}`)
		if err := os.Chmod(f.path, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(f.path, 0o700) })
		assertLayerRefusal(t, checkRefusedFiles([]refusedFile{f}), "Claude Code's "+dropInDir+" "+f.path+" "+present)
	})
	t.Run("nothing there passes", func(t *testing.T) {
		if err := checkRefusedFiles([]refusedFile{at("absent.json"), at("sub/absent.json")}); err != nil {
			t.Fatalf("want pass, got %v", err)
		}
	})
	t.Run("the first refused file stops the check", func(t *testing.T) {
		second := at("dir.json")
		err := checkRefusedFiles([]refusedFile{at("absent.json"), at("file.json"), second})
		assertLayerRefusal(t, err, filepath.Join(dir, "file.json"))
		if strings.Contains(err.Error(), second.path) {
			t.Errorf("the check went on past the first refused file: %v", err)
		}
	})
	t.Run("a path that cannot be checked", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root searches a mode-000 directory")
		}
		locked := filepath.Join(t.TempDir(), "locked")
		writeFile(t, filepath.Join(locked, "managed-mcp.json"), layerSecret)
		writeFile(t, filepath.Join(locked, "managed-settings.d", "10-gateway.json"), layerSecret)
		if err := os.Chmod(locked, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })
		for _, tc := range []struct {
			what string
			f    refusedFile
		}{
			{"managed MCP file", refusedFile{kind: "managed MCP", path: filepath.Join(locked, "managed-mcp.json")}},
			{dropInDir, refusedFile{kind: "managed settings drop-in", path: filepath.Join(locked, "managed-settings.d"), dir: true}},
		} {
			assertLayerRefusal(t, checkRefusedFiles([]refusedFile{tc.f}), "cannot check for Claude Code's "+tc.what+" "+tc.f.path)
		}
	})
}
