package spawn

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestValidateOrder pins SRD §7.2's validation precedence: each case would
// fail more than one check and must get the first one's sentinel. It also
// pins step 3's denied set, each flag in split and equals form, and SRD §19
// Q5's --setting-sources, which passes.
func TestValidateOrder(t *testing.T) {
	cwdGood := t.TempDir()
	file := filepath.Join(t.TempDir(), "x")
	if err := os.WriteFile(file, []byte("data"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	type validateCase struct {
		name string
		in   SpawnParams
		want error
	}
	cases := []validateCase{
		{"missing cwd outranks invalid relay_mode", SpawnParams{RelayMode: "bogus"}, ErrCwdMissing},
		{"non-path cwd outranks invalid relay_mode", SpawnParams{CWD: "https://example.com", RelayMode: "bogus"}, ErrCwdNotAPath},
		{"relative cwd is not absolute", SpawnParams{CWD: "./relative"}, ErrCwdNotAPath},
		{"absolute cwd that does not exist", SpawnParams{CWD: "/this/does/not/exist/anywhere"}, ErrCwdNotFound},
		{"cwd is a regular file", SpawnParams{CWD: file}, ErrCwdNotADirectory},
		{"invalid relay_mode after valid cwd", SpawnParams{CWD: cwdGood, RelayMode: "bogus", ClaudeArgs: []string{"--print"}},
			ErrRelayModeInvalid},
		{"denied flag outranks reserved env key", SpawnParams{CWD: cwdGood, ClaudeArgs: []string{"--print"},
			ExtraEnv: map[string]string{"AGENT_DIRECTOR_FOO": "bar"}}, ErrSpawnDeniedFlag},
		{"reserved env key outranks the session name", SpawnParams{CWD: cwdGood, ExtraEnv: map[string]string{"AGENT_DIRECTOR_FOO": "bar"},
			TmuxSessionName: "bad:name", TmuxSessionNameSupplied: true}, ErrReservedEnvKey},
		{"auth env vars are not reserved", SpawnParams{CWD: cwdGood,
			ExtraEnv: map[string]string{"ANTHROPIC_API_KEY": "sk-ant-test", "CLAUDE_CODE_OAUTH_TOKEN": "sk-ant-oat01-test"}}, nil},
		{"--setting-sources", SpawnParams{CWD: cwdGood, ClaudeArgs: []string{"--setting-sources", "project,local"}}, nil},
		{"--setting-sources=", SpawnParams{CWD: cwdGood, ClaudeArgs: []string{"--setting-sources=project,local"}}, nil},
		{"happy path", SpawnParams{CWD: cwdGood}, nil},
	}
	for _, flag := range []string{"--settings", "--resume", "--continue", "--print", "--output-format"} {
		cases = append(cases,
			validateCase{"denied " + flag, SpawnParams{CWD: cwdGood, ClaudeArgs: []string{flag, "value"}}, ErrSpawnDeniedFlag},
			validateCase{"denied " + flag + "=", SpawnParams{CWD: cwdGood, ClaudeArgs: []string{flag + "=value"}}, ErrSpawnDeniedFlag})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := Resolved{SpawnParams: tc.in}
			if err := Validate(&r); !errors.Is(err, tc.want) || (tc.want == nil) != (err == nil) {
				t.Fatalf("Validate err = %v; want %v", err, tc.want)
			}
		})
	}
}

// TestValidateCanonicalizesCwd pins SRD §7.2: cwd forms naming one directory
// ("/", "/./" and "~/" suffixes) give the same canonical Resolved.CWD, which
// the row stores verbatim; a failed validation leaves CWD as given (no
// partial side effects).
func TestValidateCanonicalizesCwd(t *testing.T) {
	tmp := t.TempDir()
	canonical, err := filepath.EvalSymlinks(tmp)
	if err != nil {
		t.Fatalf("EvalSymlinks(tmp): %v", err)
	}
	forms := map[string]string{tmp: canonical, tmp + "/": canonical, tmp + "/./": canonical}
	if home, err := os.UserHomeDir(); err == nil {
		if h, err := filepath.EvalSymlinks(home); err == nil {
			forms["~/"] = h
		}
	}
	for in, want := range forms {
		r := Resolved{SpawnParams: SpawnParams{CWD: in}}
		if err := Validate(&r); err != nil || (r.CWD != want && (in != "~/" || !strings.EqualFold(r.CWD, want))) {
			t.Errorf("Validate(%q): CWD = %q, %v; want %q", in, r.CWD, err, want)
		}
	}
	r := Resolved{SpawnParams: SpawnParams{CWD: "/this/does/not/exist", ClaudeArgs: []string{"--settings={}"}}}
	if err := Validate(&r); err == nil || r.CWD != "/this/does/not/exist" {
		t.Errorf("failed Validate: err %v, CWD %q; want an error and the CWD as given", err, r.CWD)
	}
}

// TestValidateTmuxSessionName covers SR-2.1, SR-2.2, SR-2.3, SR-2.5 and SR-9.2:
// the check runs only when the name was supplied; wantText is a substring of
// the error, naming a reserved character in Go %q form.
func TestValidateTmuxSessionName(t *testing.T) {
	cwd := t.TempDir()
	cases := []struct {
		name     string
		supplied bool
		value    string
		want     error
		wantText string
	}{
		{"omitted flag bypasses check", false, "", nil, ""},
		{"omitted flag with stale invalid value bypasses", false, `bad$na\me`, nil, ""},
		{"explicit empty trips Empty", true, "", ErrTmuxSessionNameEmpty, "tmux_session_name (--tmux-session-name on the CLI) was supplied with an empty value"},
		{"reserved char colon", true, "bad:name", ErrTmuxSessionNameInvalid, "reserved character ':'"},
		{"reserved char hash", true, "bad#name", ErrTmuxSessionNameInvalid, "reserved character '#'"},
		{"reserved char dot", true, "bad.name", ErrTmuxSessionNameInvalid, "reserved character '.'"},
		{"reserved char dollar at start", true, "$7", ErrTmuxSessionNameInvalid, "reserved character '$'"},
		{"reserved char dollar at end", true, "badname$", ErrTmuxSessionNameInvalid, "reserved character '$'"},
		{"reserved char backslash at start", true, `\badname`, ErrTmuxSessionNameInvalid, `reserved character '\\'`},
		{"reserved char backslash in middle", true, `bad\name`, ErrTmuxSessionNameInvalid, `reserved character '\\'`},
		{"control char NUL", true, "bad\x00name", ErrTmuxSessionNameInvalid, ""},
		{"control char newline", true, "bad\nname", ErrTmuxSessionNameInvalid, ""},
		{"control char DEL", true, "bad\x7fname", ErrTmuxSessionNameInvalid, ""},
		{"non-UTF-8 bytes", true, string([]byte{0xff, 0xfe, 0x80}), ErrTmuxSessionNameInvalid, ""},
		{"exactly max-byte allowed", true, strings.Repeat("a", 64), nil, ""},
		{"one byte over max trips TooLong", true, strings.Repeat("a", 65), ErrTmuxSessionNameTooLong, ""},
		{"happy path with slash, space, bang, percent and at", true, "team/bot one!%@", nil, ""},
		{"happy path with non-ASCII UTF-8", true, "agent-café-日本", nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := Resolved{SpawnParams: SpawnParams{CWD: cwd, TmuxSessionName: tc.value, TmuxSessionNameSupplied: tc.supplied}}
			err := Validate(&r)
			if !errors.Is(err, tc.want) || (tc.want == nil) != (err == nil) {
				t.Fatalf("Validate err = %v; want %v", err, tc.want)
			}
			if tc.wantText != "" && !strings.Contains(err.Error(), tc.wantText) {
				t.Fatalf("Validate err text = %q; want it to contain %q", err.Error(), tc.wantText)
			}
		})
	}
}
