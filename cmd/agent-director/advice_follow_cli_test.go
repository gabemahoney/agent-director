package main_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/faketmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// b.fji literal-follow tests for the CLI's own advice (inventory section H)
// and I4's CLI half: each test triggers the refusal, asserts the advice text,
// then does exactly what the text says and asserts the promised outcome.

// advCLIRequiredRe matches a "--X is required" message; group 2 is the first
// choice of an "(a|b)" list when the message names one.
var advCLIRequiredRe = regexp.MustCompile(`^(--[a-z-]+) is required(?: \(([a-z]+)\|)?`)

// advCLINamedClassRe matches the invalid --outcome message; group 2 is the
// first named class it lists.
var advCLINamedClassRe = regexp.MustCompile(`^(--[a-z-]+) "[^"]*" is not a valid .* named class \(([a-z_]+),`)

// advCLIRun runs the built binary in home with the fake tmux first on PATH and
// a private TMUX_TMPDIR, so no case can reach a real tmux server.
func advCLIRun(t *testing.T, home string, args ...string) (string, string, int) {
	t.Helper()
	return runSpawnCLI(t, home, buildFakeTmux(t), args...)
}

// advCLIQuoted returns the text between the first occurrence of open in s and
// the next close after it.
func advCLIQuoted(t *testing.T, s, open, close string) string {
	t.Helper()
	i := strings.Index(s, open)
	if i < 0 {
		t.Fatalf("%q has no %q", s, open)
	}
	rest := s[i+len(open):]
	j := strings.Index(rest, close)
	if j < 0 {
		t.Fatalf("%q has no closing %q after %q", s, close, open)
	}
	return rest[:j]
}

// advCLIBinArgs splits an advised "agent-director <args>" command and returns
// <args>, checking the command names the agent-director binary.
func advCLIBinArgs(t *testing.T, command string) []string {
	t.Helper()
	f := strings.Fields(command)
	if len(f) < 2 || f[0] != "agent-director" {
		t.Fatalf("advised command %q is not an agent-director invocation", command)
	}
	return f[1:]
}

// advCLIFollow returns argv changed the way desc tells a caller to change it:
// "try: <argv>" replaces argv, "--X is required" adds --X with value (or the
// first listed choice), and an invalid named-class value takes the first class.
func advCLIFollow(t *testing.T, argv []string, desc, value string) []string {
	t.Helper()
	if i := strings.Index(desc, "try: "); i >= 0 {
		return strings.Fields(desc[i+len("try: "):])
	}
	if m := advCLIRequiredRe.FindStringSubmatch(desc); m != nil {
		if m[2] != "" {
			value = m[2]
		}
		return append(append([]string{}, argv...), m[1], value)
	}
	if m := advCLINamedClassRe.FindStringSubmatch(desc); m != nil {
		out := append([]string{}, argv...)
		for i := range out[:len(out)-1] {
			if out[i] == m[1] {
				out[i+1] = m[2]
				return out
			}
		}
		t.Fatalf("argv %q has no %s to correct", argv, m[1])
	}
	t.Fatalf("description %q carries no advice a caller can follow", desc)
	return nil
}

// TestAdviceFollow_H1_UnknownVerbTryHelp: H1 "unknown verb %q; try 'agent-director help'".
func TestAdviceFollow_H1_UnknownVerbTryHelp(t *testing.T) {
	home := t.TempDir()
	stdout, stderr, code := advCLIRun(t, home, "frob")
	desc := assertOnlyEnvelope(t, stdout, stderr, code, "ErrUnknownVerb").ErrDescription
	if want := `unknown verb "frob"; try 'agent-director help'`; !strings.Contains(desc, want) {
		t.Fatalf("description %q lacks %q", desc, want)
	}

	stdout, stderr, code = advCLIRun(t, home, advCLIBinArgs(t, advCLIQuoted(t, desc, "try '", "'"))...)
	if code != 0 {
		t.Fatalf("advised help exit=%d stderr=%q", code, stderr)
	}
	for _, verb := range []string{`"name":"help"`, `"name":"spawn"`} {
		if !strings.Contains(stdout, verb) {
			t.Errorf("advised help stdout lacks %s: %q", verb, stdout)
		}
	}
}

// TestAdviceFollow_H2_TrailEmitTryRelayAttempt: H2 "try: trail-emit relay-attempt",
// then each next refusal's own advice, until the emit succeeds.
func TestAdviceFollow_H2_TrailEmitTryRelayAttempt(t *testing.T) {
	for _, start := range [][]string{{"trail-emit"}, {"trail-emit", "foo"}} {
		t.Run(strings.Join(start, "_"), func(t *testing.T) {
			home := t.TempDir()
			argv := start
			var advice []string
			for {
				if len(advice) > 8 {
					t.Fatalf("advice chain did not end in success; advice seen: %q", advice)
				}
				stdout, stderr, code := advCLIRun(t, home, argv...)
				if code == 0 {
					if strings.TrimSpace(stdout) != "{}" {
						t.Errorf("final stdout = %q; want {}", stdout)
					}
					break
				}
				desc := assertOnlyEnvelope(t, stdout, stderr, code, "ErrInvalidFlags").ErrDescription
				advice = append(advice, desc)
				argv = advCLIFollow(t, argv, desc, "advcli-x")
			}
			if len(advice) == 0 || !strings.Contains(advice[0], "; try: trail-emit relay-attempt") {
				t.Fatalf("first advice = %q; want it to end in \"; try: trail-emit relay-attempt\"", advice)
			}
			if n := len(relayAttemptLines(readTrailLines(t, home))); n != 1 {
				t.Errorf("ad.relay_attempt.completed lines = %d; want 1 (advice chain %q)", n, advice)
			}
		})
	}
}

// TestAdviceFollow_H3_FlagIsRequired: H3 "--X is required" family. Dropping the
// flag gives the message; adding the flag it names gets past flag validation.
func TestAdviceFollow_H3_FlagIsRequired(t *testing.T) {
	const id, token = "advcli-no-such-row", "5b3c8f0e-2d4a-4c6b-9e1f-7a8b9c0d1e2f"
	relay := []string{"trail-emit", "relay-attempt"}
	cases := []struct {
		name  string
		argv  []string // without the flag
		want  string   // exact message
		value string   // value a caller passes with the named flag
	}{
		{"status", []string{"status"}, "--claude-instance-id is required", id},
		{"send-keys", []string{"send-keys", "--text", "hi"}, "--claude-instance-id is required", id},
		{"read-pane", []string{"read-pane"}, "--claude-instance-id is required", id},
		{"make-template", []string{"make-template"}, "--name is required", "advcli-template"},
		{"pause", []string{"pause"}, "--claude-instance-id is required", id},
		{"decide id", []string{"decide", "--request-token", token, "--decision", "allow"}, "--claude-instance-id is required", id},
		{"decide token", []string{"decide", "--claude-instance-id", id, "--decision", "deny"}, "--request-token is required", token},
		{"decide decision", []string{"decide", "--claude-instance-id", id, "--request-token", token}, "--decision is required (allow|deny)", ""},
		{"resume", []string{"resume"}, "--claude-instance-id is required", id},
		{"kill", []string{"kill"}, "--claude-instance-id is required", id},
		{"get", []string{"get"}, "--claude-instance-id is required", id},
		{"get-permission", []string{"get-permission"}, "--request-token is required", token},
		{"trail-emit token", append(relay, "--endpoint", "http://127.0.0.1:9/r", "--outcome", "200", "--instance-id", id), "--token is required", token},
		{"trail-emit endpoint", append(relay, "--token", token, "--outcome", "200", "--instance-id", id), "--endpoint is required", "http://127.0.0.1:9/r"},
		{"trail-emit outcome", append(relay, "--token", token, "--endpoint", "http://127.0.0.1:9/r", "--instance-id", id), "--outcome is required", "200"},
		{"trail-emit instance-id", append(relay, "--token", token, "--endpoint", "http://127.0.0.1:9/r", "--outcome", "200"), "--instance-id is required", id},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			stdout, stderr, code := advCLIRun(t, home, tc.argv...)
			desc := assertOnlyEnvelope(t, stdout, stderr, code, "ErrInvalidFlags").ErrDescription
			if desc != tc.want {
				t.Fatalf("description = %q; want %q", desc, tc.want)
			}

			stdout, stderr, code = advCLIRun(t, home, advCLIFollow(t, tc.argv, desc, tc.value)...)
			if code == 0 {
				return
			}
			if env := parseEnvelope(t, stderr); env.ErrName == "ErrInvalidFlags" {
				t.Fatalf("with the named flag added the verb still fails flag validation: %q", env.ErrDescription)
			}
		})
	}
}

// TestAdviceFollow_H4_FlagValueShape: H4 "%s requires a value",
// "%s expects KEY=VALUE, got %q" and "%s must be true or false, got %q".
// Re-issuing with a valid value runs the verb.
func TestAdviceFollow_H4_FlagValueShape(t *testing.T) {
	fakeTmux := filepath.Join(buildFakeTmux(t), "tmux")
	cases := []struct {
		name   string
		argv   []string
		want   string
		follow func(t *testing.T, argv []string, scratch string) []string
		check  func(t *testing.T, home, scratch string)
	}{
		{
			name: "store-path last", argv: []string{"list", "--store-path"}, want: "--store-path requires a value",
			follow: func(_ *testing.T, argv []string, s string) []string {
				return append(argv, filepath.Join(s, "alt.db"))
			},
			check: func(t *testing.T, _, s string) { advCLIExists(t, filepath.Join(s, "alt.db")) },
		},
		{
			name: "store-path empty", argv: []string{"--store-path=", "list"}, want: "--store-path requires a value",
			follow: func(_ *testing.T, argv []string, s string) []string {
				return []string{"--store-path=" + filepath.Join(s, "alt.db"), "list"}
			},
			check: func(t *testing.T, _, s string) { advCLIExists(t, filepath.Join(s, "alt.db")) },
		},
		{
			name: "home last", argv: []string{"list", "--home"}, want: "--home requires a value",
			follow: func(_ *testing.T, argv []string, s string) []string { return append(argv, s) },
			check:  func(t *testing.T, _, s string) { advCLIExists(t, filepath.Join(s, ".agent-director", "state.db")) },
		},
		{
			name: "tmux-command last", argv: []string{"list", "--tmux-command"}, want: "--tmux-command requires a value",
			follow: func(_ *testing.T, argv []string, _ string) []string { return append(argv, fakeTmux) },
		},
		{
			name: "create-if-missing last (b.78b)", argv: []string{"list", "--create-if-missing"},
			want:   "--create-if-missing requires a value",
			follow: func(_ *testing.T, argv []string, _ string) []string { return append(argv, "true") },
			check:  func(t *testing.T, home, _ string) { advCLIExists(t, stateDB(home)) },
		},
		{
			name: "create-if-missing not a bool (b.78b)", argv: []string{"--create-if-missing", "no", "list"},
			want: `--create-if-missing must be true or false, got "no"`,
			follow: func(_ *testing.T, _ []string, _ string) []string {
				return []string{"--create-if-missing", "true", "list"}
			},
		},
		{
			name: "label", argv: []string{"make-template", "--name", "advcli-label", "--label", "foo"},
			want:   `--label expects KEY=VALUE, got "foo"`,
			follow: advCLIKeyValue("foo"),
		},
		{
			name: "extra-env", argv: []string{"make-template", "--name", "advcli-env", "--extra-env", "FOO"},
			want:   `--extra-env expects KEY=VALUE, got "FOO"`,
			follow: advCLIKeyValue("FOO"),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home, scratch := t.TempDir(), t.TempDir()
			stdout, stderr, code := advCLIRun(t, home, tc.argv...)
			if desc := assertOnlyEnvelope(t, stdout, stderr, code, "ErrInvalidFlags").ErrDescription; !strings.Contains(desc, tc.want) {
				t.Fatalf("description %q lacks %q", desc, tc.want)
			}
			argv := tc.follow(t, append([]string{}, tc.argv...), scratch)
			if _, stderr, code = advCLIRun(t, home, argv...); code != 0 {
				t.Fatalf("followed argv %q exit=%d stderr=%q", argv, code, stderr)
			}
			if tc.check != nil {
				tc.check(t, home, scratch)
			}
		})
	}
}

// advCLIKeyValue returns a follow that turns the refused bare value into
// KEY=VALUE, as "expects KEY=VALUE" says.
func advCLIKeyValue(bare string) func(*testing.T, []string, string) []string {
	return func(t *testing.T, argv []string, _ string) []string {
		for i, a := range argv {
			if a == bare {
				argv[i] = bare + "=advcli"
				return argv
			}
		}
		t.Fatalf("argv %q has no %q", argv, bare)
		return nil
	}
}

// advCLIExists fails unless path exists.
func advCLIExists(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Errorf("the value given was not used: %v", err)
	}
}

// TestAdviceFollow_H5_ServeUsageRegister: H5 "usage: agent-director serve --stdio
// ... Register with: claude mcp add agent-director <binary-path> serve --stdio".
func TestAdviceFollow_H5_ServeUsageRegister(t *testing.T) {
	home := t.TempDir()
	stdout, stderr, code := advCLIRun(t, home, "serve")
	if code != 0 || stdout != "" {
		t.Fatalf("serve without --stdio: exit=%d stdout=%q; want usage on stderr, exit 0", code, stdout)
	}
	for _, want := range []string{"usage: agent-director serve --stdio", "Register with:",
		"claude mcp add agent-director <binary-path> serve --stdio"} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("usage %q lacks %q", stderr, want)
		}
	}
	registered := strings.Fields(advCLIQuoted(t, stderr, "<binary-path> ", "\n"))

	stdout, stderr, code, timedOut := runBounded(t, home, nil, mcpInitialize, false, surfaceDeadline, registered...)
	if timedOut || code != 0 {
		t.Fatalf("registered command %q: exit=%d timedOut=%v stderr=%q", registered, code, timedOut, stderr)
	}
	if !strings.Contains(stdout, `"id":1`) || !strings.Contains(stdout, `"serverInfo"`) {
		t.Errorf("registered command did not answer initialize: stdout=%q", stdout)
	}
}

// TestAdviceFollow_H6_OlderThanDurationForm: H6 "--older-than: invalid duration:
// %s (expected a Go duration like "12h" or trailing-d days like "7d" up to
// "106751d", with no sign)", ErrInvalidFlags with no tmux call and nothing
// deleted (b.hxn, b.sgw, b.c4n). The row ended in January, so only "106751d" keeps it.
func TestAdviceFollow_H6_OlderThanDurationForm(t *testing.T) {
	const form = `a Go duration like "12h" or trailing-d days like "7d" up to "106751d", with no sign`
	follows := []struct{ value, wantIDs string }{
		{"12h", `["` + expireGoneID + `"]`},
		{"7d", `["` + expireGoneID + `"]`},
		{"106751d", `[]`},
	}
	for _, follow := range follows {
		t.Run(follow.value, func(t *testing.T) {
			home, _ := seedExpireRows(t, []string{expireGoneID})
			for _, bad := range []string{"-2h", "-7d", "-0s", "+12h", "+7d", "soon", "106752d", "365000d"} {
				stdout, stderr, code := advCLIRun(t, home, "expire", "--older-than", bad)
				want := "--older-than: invalid duration: " + bad + " (expected " + form + ")"
				if desc := assertOnlyEnvelope(t, stdout, stderr, code, "ErrInvalidFlags").ErrDescription; desc != want {
					t.Errorf("--older-than %s description = %q; want %q", bad, desc, want)
				}
			}
			if _, err := apitest.ReadSpawnColumns(stateDB(home), expireGoneID); err != nil {
				t.Fatalf("row after the refusals: %v; want it kept", err)
			}
			assertInvocationKinds(t, home) // no tmux call

			stdout, stderr, code := advCLIRun(t, home, "expire", "--older-than", follow.value)
			if code != 0 {
				t.Fatalf("expire --older-than %s exit = %d; want 0\nstderr=%s", follow.value, code, stderr)
			}
			var res map[string]json.RawMessage
			if err := json.Unmarshal([]byte(stdout), &res); err != nil {
				t.Fatalf("parse stdout %q: %v", stdout, err)
			}
			if got := string(res["ids"]); got != follow.wantIDs {
				t.Errorf("expire --older-than %s ids = %s; want %s", follow.value, got, follow.wantIDs)
			}
		})
	}
}

// TestAdviceFollow_H7_TrailEmitNegativeBytes: H7 "--bytes-sent %d is negative;
// pass 0 or a positive byte count" (and --bytes-received's), ErrInvalidFlags with
// nothing written (b.c4n); re-issued each way, one line carries that count.
func TestAdviceFollow_H7_TrailEmitNegativeBytes(t *testing.T) {
	relay := []string{"trail-emit", "relay-attempt", "--token", "tok-c4n", "--endpoint", "http://127.0.0.1:9/r",
		"--outcome", "200", "--instance-id", "inst-c4n"}
	for _, flag := range []string{"bytes-sent", "bytes-received"} {
		for _, follow := range []struct {
			arg   string
			bytes float64
		}{{"0", 0}, {"1024", 1024}} {
			t.Run(flag+" "+follow.arg, func(t *testing.T) {
				home := t.TempDir()
				stdout, stderr, code := advCLIRun(t, home, append(append([]string{}, relay...), "--"+flag, "-1")...)
				want := "--" + flag + " -1 is negative; pass 0 or a positive byte count"
				if desc := assertOnlyEnvelope(t, stdout, stderr, code, "ErrInvalidFlags").ErrDescription; desc != want {
					t.Fatalf("description = %q; want %q", desc, want)
				}
				if n := len(relayAttemptLines(trailOrNil(t, home))); n != 0 {
					t.Fatalf("ad.relay_attempt.completed lines after the refusal = %d; want none", n)
				}

				if _, stderr, code := advCLIRun(t, home, append(append([]string{}, relay...), "--"+flag, follow.arg)...); code != 0 {
					t.Fatalf("--%s %s: exit = %d, stderr = %q; want 0", flag, follow.arg, code, stderr)
				}
				ra := relayAttemptLines(readTrailLines(t, home))
				if key := strings.ReplaceAll(flag, "-", "_"); len(ra) != 1 || ra[0][key] != follow.bytes {
					t.Errorf("ad.relay_attempt.completed lines = %v; want one with %s %v", ra, key, follow.bytes)
				}
			})
		}
	}
}

// TestAdviceFollow_I4_CLILiteralReuseSpelling: I4 (CLI half) A4's "a retry with this id uses the reuse opt-in
// reuse_finished (--reuse-finished on the CLI) once the name is free", the flag in the parenthesis copied literally.
func TestAdviceFollow_I4_CLILiteralReuseSpelling(t *testing.T) {
	const advice = "a retry with this id uses the reuse opt-in reuse_finished (--reuse-finished on the CLI) once the name is free"
	fakeDir := buildFakeTmux(t)
	home := t.TempDir()
	socket, _ := heldHome(t, home)
	writeHolders(t, socket, holderSession("$7", ""))
	// A minted-id spawn makes no label scan, so its first lookup is the
	// re-lookup after "duplicate session"; it hangs past the query timeout.
	faketmuxfix.Tables{}.Inject(t, socket, faketmuxfix.Hang(tmux.CallLookup).Bound(fakeHangBound).FirstN(1))
	writeQueryTimeout(t, filepath.Join(directorDir(home), "config.toml"), 300*time.Millisecond)

	env := spawnHeld(t, home, fakeDir, nil)
	if env.ErrName != "ErrTmuxUnresponsive" || !strings.Contains(env.ErrDescription, advice) {
		t.Fatalf("envelope %+v; want ErrTmuxUnresponsive carrying %q", env, advice)
	}
	id := heldRowID(t, home, fakeDir)
	assertHeldRowEnded(t, home, fakeDir, id)
	writeHolders(t, socket) // the holder exits: the name is free
	flag := advCLIQuoted(t, env.ErrDescription, "the reuse opt-in reuse_finished (", " on the CLI)")

	stdout, stderr, code := runSpawnCLI(t, home, fakeDir, "spawn", "--claude-instance-id", id, "--cwd", t.TempDir(),
		"--tmux-session-name", heldName, "--no-pre-trust", flag)
	if code != 0 {
		t.Fatalf("spawn %s: exit=%d stdout=%q stderr=%q; want the reuse to launch", flag, code, stdout, stderr)
	}
	if st := statusOf(t, home, fakeDir, id); st != string(store.StatePending) {
		t.Errorf("status = %q; want pending", st)
	}
}
