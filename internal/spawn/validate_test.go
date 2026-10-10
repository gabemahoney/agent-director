package spawn

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestValidateOrder pins SRD §7.2's validation precedence: each case would
// fail more than one check and must get the first one's sentinel. It also
// pins step 3's denied set, each flag in split and equals form, SRD §19 Q5's
// --setting-sources, which passes, step 4's reserved HOME (b.nas) and its
// malformed keys, refused after the reserved ones (b.vpb); text, when set, is
// in the message and tells step 4's refusals apart, which share one sentinel.
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
		text string
	}
	const setsHome, malformed = "sets HOME, which is reserved: ", "is not a valid env-var name: "
	cases := []validateCase{
		{"missing cwd outranks invalid relay_mode", SpawnParams{RelayMode: "bogus"}, ErrCwdMissing, ""},
		{"non-path cwd outranks invalid relay_mode", SpawnParams{CWD: "https://example.com", RelayMode: "bogus"}, ErrCwdNotAPath, ""},
		{"relative cwd is not absolute", SpawnParams{CWD: "./relative"}, ErrCwdNotAPath, ""},
		{"absolute cwd that does not exist", SpawnParams{CWD: "/this/does/not/exist/anywhere"}, ErrCwdNotFound, ""},
		{"cwd is a regular file", SpawnParams{CWD: file}, ErrCwdNotADirectory, ""},
		{"invalid relay_mode after valid cwd", SpawnParams{CWD: cwdGood, RelayMode: "bogus", ClaudeArgs: []string{"--print"}},
			ErrRelayModeInvalid, ""},
		{"denied flag outranks reserved env key", SpawnParams{CWD: cwdGood, ClaudeArgs: []string{"--print"},
			ExtraEnv: map[string]string{"AGENT_DIRECTOR_FOO": "bar"}}, ErrSpawnDeniedFlag, ""},
		{"reserved env key outranks the session name", SpawnParams{CWD: cwdGood, ExtraEnv: map[string]string{"AGENT_DIRECTOR_FOO": "bar"},
			TmuxSessionName: "bad:name", TmuxSessionNameSupplied: true}, ErrReservedEnvKey, `ErrReservedEnvKey: "AGENT_DIRECTOR_FOO"`},
		{"auth env vars are not reserved", SpawnParams{CWD: cwdGood,
			ExtraEnv: map[string]string{"ANTHROPIC_API_KEY": "sk-ant-test", "CLAUDE_CODE_OAUTH_TOKEN": "sk-ant-oat01-test"}}, nil, ""},
		{"keys near HOME are not reserved (b.nas)", SpawnParams{CWD: cwdGood, ExtraEnv: map[string]string{
			"HOMEDIR": "/x", "home": "/x", "MY_HOME": "/x", "CLAUDE_CONFIG_DIR": "/x"}}, nil, ""},
		{"key =HOME is malformed, not HOME (b.vpb)", SpawnParams{CWD: cwdGood,
			ExtraEnv: map[string]string{"=HOME": "/x"}}, ErrReservedEnvKey, `"=HOME" ` + malformed},
		{"key X=HOME is malformed, not HOME (b.vpb)", SpawnParams{CWD: cwdGood,
			ExtraEnv: map[string]string{"X=HOME": "/x"}}, ErrReservedEnvKey, `"X=HOME" ` + malformed},
		{"key HOME=/tmp/b sets HOME (b.nas)", SpawnParams{CWD: cwdGood,
			ExtraEnv: map[string]string{"HOME=/tmp/b": ""}}, ErrReservedEnvKey, `"HOME=/tmp/b" ` + setsHome},
		{"key AGENT_DIRECTOR_X=y is reserved by prefix", SpawnParams{CWD: cwdGood,
			ExtraEnv: map[string]string{"AGENT_DIRECTOR_X=y": ""}}, ErrReservedEnvKey, `ErrReservedEnvKey: "AGENT_DIRECTOR_X=y"`},
		{"HOME outranks a smaller malformed key (b.vpb)", SpawnParams{CWD: cwdGood,
			ExtraEnv: map[string]string{"": "x", "HOME": "/x"}}, ErrReservedEnvKey, `"HOME" ` + setsHome},
		{"AGENT_DIRECTOR_ outranks a smaller malformed key (b.vpb)", SpawnParams{CWD: cwdGood,
			ExtraEnv: map[string]string{"A=B": "", "AGENT_DIRECTOR_FOO": "bar"}}, ErrReservedEnvKey, `ErrReservedEnvKey: "AGENT_DIRECTOR_FOO"`},
		{"denied flag outranks a malformed key (b.vpb)", SpawnParams{CWD: cwdGood, ClaudeArgs: []string{"--print"},
			ExtraEnv: map[string]string{"": "x"}}, ErrSpawnDeniedFlag, ""},
		{"malformed key outranks the session name (b.vpb)", SpawnParams{CWD: cwdGood, ExtraEnv: map[string]string{"A=B": ""},
			TmuxSessionName: "bad:name", TmuxSessionNameSupplied: true}, ErrReservedEnvKey, `"A=B" ` + malformed},
		{"--setting-sources", SpawnParams{CWD: cwdGood, ClaudeArgs: []string{"--setting-sources", "project,local"}}, nil, ""},
		{"--setting-sources=", SpawnParams{CWD: cwdGood, ClaudeArgs: []string{"--setting-sources=project,local"}}, nil, ""},
		{"happy path", SpawnParams{CWD: cwdGood}, nil, ""},
	}
	for _, flag := range []string{"--settings", "--resume", "--continue", "--print", "--output-format"} {
		cases = append(cases,
			validateCase{"denied " + flag, SpawnParams{CWD: cwdGood, ClaudeArgs: []string{flag, "value"}}, ErrSpawnDeniedFlag, ""},
			validateCase{"denied " + flag + "=", SpawnParams{CWD: cwdGood, ClaudeArgs: []string{flag + "=value"}}, ErrSpawnDeniedFlag, ""})
	}
	for _, home := range []string{"/abs/home", "rel", ""} { // b.nas: HOME is reserved whatever its value
		cases = append(cases, validateCase{fmt.Sprintf("HOME=%q is reserved", home), SpawnParams{CWD: cwdGood,
			ExtraEnv: map[string]string{"HOME": home, "CLAUDE_CONFIG_DIR": cwdGood}}, ErrReservedEnvKey, `"HOME" ` + setsHome})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := Resolved{SpawnParams: tc.in}
			err := Validate(&r)
			if !errors.Is(err, tc.want) || (tc.want == nil) != (err == nil) {
				t.Fatalf("Validate err = %v; want %v", err, tc.want)
			}
			if tc.text != "" && !strings.Contains(err.Error(), tc.text) {
				t.Errorf("Validate err = %v; want it to carry %q", err, tc.text)
			}
		})
	}
}

// TestReservedHomeKey (b.nas): the key named is the smallest whose name before
// the first '=' is HOME, the same on every call whatever the map order.
func TestReservedHomeKey(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"no key", nil, ""},
		{"HOME", map[string]string{"HOME": "/x", "TEAM": "core"}, "HOME"},
		{"HOME=/tmp/b", map[string]string{"HOME=/tmp/b": "", "TEAM": "core"}, "HOME=/tmp/b"},
		{"HOME before HOME=...", map[string]string{"HOME=/x": "", "HOME": "/a", "HOME=/a": "", "HOMEDIR": ""}, "HOME"},
		{"the smallest HOME=...", map[string]string{"HOME=/x": "", "HOME=/a": "", "HOME=": "", "=HOME": ""}, "HOME="},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for range 100 {
				if got, ok := ReservedHomeKey(tc.env); got != tc.want || ok != (tc.want != "") {
					t.Fatalf("ReservedHomeKey(%q) = %q, %t; want %q, %t", tc.env, got, ok, tc.want, tc.want != "")
				}
			}
		})
	}
}

// TestValidateRefusesInvalidEnvKey (b.vpb): a key that is empty or holds '=' or
// a NUL byte is ErrReservedEnvKey, quoting it (%q) and saying what is wrong.
func TestValidateRefusesInvalidEnvKey(t *testing.T) {
	cwdGood := t.TempDir()
	cases := []struct{ name, key, problem string }{
		{"empty", "", "it is empty"},
		{"CLAUDE_CONFIG_DIR=/tmp/cfg", "CLAUDE_CONFIG_DIR=/tmp/cfg", "it contains '='"},
		{"NUL byte", "A\x00B", "it contains a NUL byte"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := Resolved{SpawnParams: SpawnParams{CWD: cwdGood, ExtraEnv: map[string]string{tc.key: "", "TEAM": "core"}}}

			err := Validate(&r)

			if !errors.Is(err, ErrReservedEnvKey) {
				t.Fatalf("Validate err = %v; want ErrReservedEnvKey", err)
			}
			q := strconv.Quote(tc.key)
			want := "ErrReservedEnvKey: extra_env key " + q + " is not a valid env-var name: " + tc.problem
			if msg := err.Error(); !strings.HasPrefix(msg, want) || !strings.Contains(msg, "; remove "+q+" from extra_env, and "+InvalidEnvKeyAlternative) ||
				strings.ContainsRune(msg, 0) {
				t.Errorf("message %q\nwant it to start %q, say to remove that key and carry no raw NUL", msg, want)
			}
		})
	}
}

// TestInvalidEnvKey (b.vpb): the key named is the smallest malformed one, the
// same on every call whatever the map order; other odd names are left alone.
func TestInvalidEnvKey(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want string
		ok   bool
	}{
		{"no key", nil, "", false},
		{"valid names only", map[string]string{"HOMEDIR": "", "home": "", "MY-VAR": "", "1ST": "", "CLAUDE_CONFIG_DIR": "/c"}, "", false},
		{"the empty key", map[string]string{"": "x", "A=B": "", "TEAM": "core"}, "", true},
		{"the smallest of '=' and NUL", map[string]string{"Z=1": "", "B=2": "", "A\x00": "", "OK": ""}, "A\x00", true},
		{"a CLAUDE_CONFIG_DIR=... beside CLAUDE_CONFIG_DIR", map[string]string{"CLAUDE_CONFIG_DIR=/tmp/cfg": "", "CLAUDE_CONFIG_DIR": "/c"},
			"CLAUDE_CONFIG_DIR=/tmp/cfg", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for range 100 {
				if got, ok := InvalidEnvKey(tc.env); got != tc.want || ok != tc.ok {
					t.Fatalf("InvalidEnvKey(%q) = %q, %t; want %q, %t", tc.env, got, ok, tc.want, tc.ok)
				}
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
