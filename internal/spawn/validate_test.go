package spawn

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestValidateOrder pins SRD §7.2's validation precedence. The cases are
// constructed so multiple checks would fail, and the assertion is that the
// FIRST applicable check fires — a regression that reorders the validate
// chain would surface as a different sentinel here.
func TestValidateOrder(t *testing.T) {
	cwdGood := t.TempDir()

	cases := []struct {
		name string
		in   SpawnParams
		want error
	}{
		{
			name: "missing cwd outranks invalid relay_mode",
			in:   SpawnParams{RelayMode: "bogus"},
			want: ErrCwdMissing,
		},
		{
			name: "non-path cwd outranks invalid relay_mode",
			in:   SpawnParams{CWD: "https://example.com", RelayMode: "bogus"},
			want: ErrCwdNotAPath,
		},
		{
			name: "relative cwd is not absolute",
			in:   SpawnParams{CWD: "./relative"},
			want: ErrCwdNotAPath,
		},
		{
			name: "absolute cwd that does not exist",
			in:   SpawnParams{CWD: "/this/does/not/exist/anywhere"},
			want: ErrCwdNotFound,
		},
		{
			name: "cwd is a regular file",
			in: SpawnParams{
				CWD: writeTempFile(t, "x", "data"),
			},
			want: ErrCwdNotADirectory,
		},
		{
			name: "invalid relay_mode after valid cwd",
			in: SpawnParams{
				CWD:       cwdGood,
				RelayMode: "bogus",
			},
			want: ErrRelayModeInvalid,
		},
		{
			name: "denied flag in claude_args (split form)",
			in: SpawnParams{
				CWD:        cwdGood,
				ClaudeArgs: []string{"--settings", "{}"},
			},
			want: ErrSpawnDeniedFlag,
		},
		{
			name: "denied flag in claude_args (equals form)",
			in: SpawnParams{
				CWD:        cwdGood,
				ClaudeArgs: []string{"--settings={}"},
			},
			want: ErrSpawnDeniedFlag,
		},
		{
			name: "denied --print flag",
			in: SpawnParams{
				CWD:        cwdGood,
				ClaudeArgs: []string{"--print"},
			},
			want: ErrSpawnDeniedFlag,
		},
		{
			name: "denied --output-format flag (equals form)",
			in: SpawnParams{
				CWD:        cwdGood,
				ClaudeArgs: []string{"--output-format=json"},
			},
			want: ErrSpawnDeniedFlag,
		},
		{
			name: "denied --resume flag",
			in: SpawnParams{
				CWD:        cwdGood,
				ClaudeArgs: []string{"--resume", "abc"},
			},
			want: ErrSpawnDeniedFlag,
		},
		{
			name: "reserved env key CLAUDE_DIRECTOR_FOO",
			in: SpawnParams{
				CWD:      cwdGood,
				ExtraEnv: map[string]string{"CLAUDE_DIRECTOR_FOO": "bar"},
			},
			want: ErrReservedEnvKey,
		},
		{
			name: "auth env vars are not reserved",
			in: SpawnParams{
				CWD: cwdGood,
				ExtraEnv: map[string]string{
					"ANTHROPIC_API_KEY":        "sk-ant-test",
					"CLAUDE_CODE_OAUTH_TOKEN":  "sk-ant-oat01-test",
					"SOMETHING_ELSE_OK_TO_SET": "ok",
				},
			},
			want: nil,
		},
		{
			name: "happy path",
			in:   SpawnParams{CWD: cwdGood},
			want: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := Resolved{SpawnParams: tc.in}
			err := Validate(&r)
			if tc.want == nil {
				if err != nil {
					t.Fatalf("Validate: unexpected err %v", err)
				}
				return
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("Validate err = %v; want %v", err, tc.want)
			}
		})
	}
}

// TestValidateCanonicalizesCwd checks that cwd values resolving to the same
// canonical path (./ form, trailing slash, symlink) yield identical
// Resolved.CWD values. The downstream UPSERT writes Resolved.CWD verbatim,
// so this is the place the SRD §7.2 "two callers spawning into /foo/bar
// and /foo/./bar produce the same row" invariant is pinned.
func TestValidateCanonicalizesCwd(t *testing.T) {
	tmp := t.TempDir()
	// EvalSymlinks resolves the chain end-to-end; compare against the
	// canonical form of the underlying temp dir.
	canonical, err := filepath.EvalSymlinks(tmp)
	if err != nil {
		t.Fatalf("EvalSymlinks(tmp): %v", err)
	}
	inputs := []string{
		tmp,
		tmp + "/",
		tmp + "/./",
	}
	for _, in := range inputs {
		r := Resolved{SpawnParams: SpawnParams{CWD: in}}
		if err := Validate(&r); err != nil {
			t.Fatalf("Validate(%q): %v", in, err)
		}
		if r.CWD != canonical {
			t.Fatalf("Validate left CWD = %q; want canonical %q (input=%q)", r.CWD, canonical, in)
		}
	}
}

// TestValidateNoPartialSideEffects guards SRD §7.2's "no partial side
// effects on validation error". Resolved is the only mutable state spawn
// touches in this layer; a failing validation must leave it identical to
// the caller's input (modulo no-effect CWD short-circuit).
func TestValidateNoPartialSideEffects(t *testing.T) {
	in := SpawnParams{
		CWD:        "/this/does/not/exist",
		ClaudeArgs: []string{"--settings={}"},
	}
	r := Resolved{SpawnParams: in}
	if err := Validate(&r); err == nil {
		t.Fatalf("expected validation error")
	}
	// The CWD field is read once for the first check that fires; on
	// failure it must equal the input, not the canonical form.
	if r.CWD != "/this/does/not/exist" {
		t.Fatalf("CWD mutated on failure: %q", r.CWD)
	}
}

// writeTempFile creates a regular file under t.TempDir and returns its
// absolute path. Used to drive the ErrCwdNotADirectory branch.
func writeTempFile(t *testing.T, name, contents string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(contents), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return p
}

// TestDeniedClaudeArgsCoverage iterates every member of the deniedClaudeArgs
// set and confirms each form (split, equals) trips ErrSpawnDeniedFlag.
// The body intentionally re-encodes the SRD §7.2 step 3 denied set so any
// silent removal from deniedClaudeArgs surfaces as a missed assertion.
func TestDeniedClaudeArgsCoverage(t *testing.T) {
	cwd := t.TempDir()
	expectedDenied := []string{"--settings", "--resume", "--continue", "--print", "--output-format"}
	for _, flag := range expectedDenied {
		t.Run(flag+"_split", func(t *testing.T) {
			r := Resolved{SpawnParams: SpawnParams{
				CWD:        cwd,
				ClaudeArgs: []string{flag, "value"},
			}}
			err := Validate(&r)
			if !errors.Is(err, ErrSpawnDeniedFlag) {
				t.Fatalf("Validate(%s value): err = %v; want ErrSpawnDeniedFlag", flag, err)
			}
		})
		t.Run(flag+"_equals", func(t *testing.T) {
			r := Resolved{SpawnParams: SpawnParams{
				CWD:        cwd,
				ClaudeArgs: []string{flag + "=value"},
			}}
			err := Validate(&r)
			if !errors.Is(err, ErrSpawnDeniedFlag) {
				t.Fatalf("Validate(%s=value): err = %v; want ErrSpawnDeniedFlag", flag, err)
			}
		})
	}
}

// TestSettingSourcesIsNotDenied pins SRD §19 Q5: --setting-sources is the
// supported clean-slate path and must pass validation unchanged.
func TestSettingSourcesIsNotDenied(t *testing.T) {
	cwd := t.TempDir()
	r := Resolved{SpawnParams: SpawnParams{
		CWD:        cwd,
		ClaudeArgs: []string{"--setting-sources", "project,local"},
	}}
	if err := Validate(&r); err != nil {
		t.Fatalf("--setting-sources should pass; got %v", err)
	}
	// Same in equals form.
	r2 := Resolved{SpawnParams: SpawnParams{
		CWD:        cwd,
		ClaudeArgs: []string{"--setting-sources=project,local"},
	}}
	if err := Validate(&r2); err != nil {
		t.Fatalf("--setting-sources=... should pass; got %v", err)
	}
}

// TestValidateTildeCwd exercises the "~/" expansion branch — Validate
// must canonicalize it against the running user's home directory before
// running EvalSymlinks.
func TestValidateTildeCwd(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home dir: %v", err)
	}
	if _, err := os.Stat(home); err != nil {
		t.Skipf("home %q not stat-able: %v", home, err)
	}
	r := Resolved{SpawnParams: SpawnParams{CWD: "~/"}}
	if err := Validate(&r); err != nil {
		t.Fatalf("Validate(~/): %v", err)
	}
	canonical, err := filepath.EvalSymlinks(home)
	if err != nil {
		t.Fatalf("EvalSymlinks(home): %v", err)
	}
	if !strings.EqualFold(r.CWD, canonical) {
		t.Fatalf("Validate left CWD = %q; want %q", r.CWD, canonical)
	}
}
