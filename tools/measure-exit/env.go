package main

import (
	"fmt"
	"os"
	"os/user"
	"sort"
	"strings"
)

// containerMarkerEnv is set only by the host runner when it starts the
// measurement container. Without it (and, in dry mode, without the sandbox
// marker) the driver refuses before doing anything: it must never run on a
// host, where agent-director's store resolution could reach the real
// ~/.agent-director (b.8dr).
const containerMarkerEnv = "AGENT_DIRECTOR_MEASURE_CONTAINER"

// sandboxMarkerEnv is the repo sandbox's marker (internal/testsupport/
// sandboxguard.EnvVar, not imported: that package is test support). Dry mode
// accepts it in place of the container marker.
const sandboxMarkerEnv = "AGENT_DIRECTOR_TEST_SANDBOX"

// sessionEndBudgetEnv is the variable that raises Claude Code's SessionEnd
// hook budget for every hook (RN-6's env-raised variant).
const sessionEndBudgetEnv = "CLAUDE_CODE_SESSIONEND_HOOKS_TIMEOUT_MS"

// credentialEnv lists every credential-bearing variable the host runner may
// forward (build-lead decision 8). Values of these never appear in any
// output; ANTHROPIC_BASE_URL is here because a URL can embed a token.
var credentialEnv = []string{
	"ANTHROPIC_API_KEY",
	"CLAUDE_CODE_OAUTH_TOKEN",
	"ANTHROPIC_AUTH_TOKEN",
	"ANTHROPIC_BASE_URL",
	"AWS_ACCESS_KEY_ID",
	"AWS_SECRET_ACCESS_KEY",
	"AWS_SESSION_TOKEN",
	"AWS_BEARER_TOKEN_BEDROCK",
}

// childEnvNames are the variables, besides credentialEnv, that a child
// process may inherit from the driver. Everything else is dropped.
var childEnvNames = []string{
	"PATH", "HOME", "USER", "LOGNAME", "SHELL", "TERM", "LANG", "LC_ALL", "TZ", "TMPDIR",
	"ANTHROPIC_MODEL", "ANTHROPIC_SMALL_FAST_MODEL", "CLAUDE_CODE_USE_BEDROCK",
	"AWS_REGION", "AWS_PROFILE", "AWS_DEFAULT_REGION",
	"CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS", "DISABLE_AUTOUPDATER",
	sessionEndBudgetEnv,
}

// childEnvPrefixes are allowed name prefixes (locale variables).
var childEnvPrefixes = []string{"LC_"}

// realModeRefusedEnv are the credential and provider variables real mode
// refuses (user decisions 2026-10-01: L1 and L2 bill to the InferenceHub
// gateway only, through ANTHROPIC_BASE_URL and ANTHROPIC_AUTH_TOKEN). An
// API key, an OAuth token (a plan or Enterprise seat) or the Bedrock/AWS
// variables would move the bill elsewhere, so the run stops before anything
// is written, as probe mode refuses every real credential; they are also
// dropped from real mode's child environment (realModeEnv).
var realModeRefusedEnv = []string{
	"ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN", "CLAUDE_CODE_USE_BEDROCK",
	"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN", "AWS_BEARER_TOKEN_BEDROCK",
	"AWS_REGION", "AWS_PROFILE", "AWS_DEFAULT_REGION",
}

// realModeRefused reports whether real mode refuses name.
func realModeRefused(name string) bool {
	for _, n := range realModeRefusedEnv {
		if name == n {
			return true
		}
	}
	return false
}

// checkRealModeEnv refuses real mode when any realModeRefusedEnv variable
// is set, even empty: only the gateway pair may carry a credential. The
// message names the variable, never its value.
func checkRealModeEnv(e environment) error {
	for _, name := range realModeRefusedEnv {
		if _, set := e.lookupEnv(name); set {
			return refuse(ruleRealGatewayOnly,
				"real mode bills to the gateway only (ANTHROPIC_BASE_URL and ANTHROPIC_AUTH_TOKEN), but %s is set; the runner never forwards it", name)
		}
	}
	return nil
}

// realModeEnv returns env (NAME=value entries) minus every
// realModeRefusedEnv variable: real mode's children never get one.
func realModeEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		if !realModeRefused(name) {
			out = append(out, kv)
		}
	}
	return out
}

// forbiddenEnvNames never reach a child, whatever a case asks for: the
// host's tmux client, the variables of a Claude Code session the operator may
// be running in (this VM sets them), and CLAUDE_CONFIG_DIR, which would move
// the agents' Claude state away from the harness's HOME.
var forbiddenEnvNames = []string{
	"TMUX", "TMUX_PANE", "CLAUDE_CONFIG_DIR", "CLAUDECODE", "CLAUDE_CODE_SESSION_ID",
	"CLAUDE_CODE_CHILD_SESSION", "CLAUDE_PID",
}

// forbiddenEnvPrefixes never reach a child either. AGENT_DIRECTOR_* covers
// the caller's AGENT_DIRECTOR_INSTANCE_ID, which spawn would otherwise record
// as the new row's parent.
var forbiddenEnvPrefixes = []string{"CLAUDE_CODE_MESSAGING_", "AGENT_DIRECTOR_"}

// envForbidden reports whether name may never be passed to a child.
func envForbidden(name string) bool {
	for _, n := range forbiddenEnvNames {
		if name == n {
			return true
		}
	}
	for _, p := range forbiddenEnvPrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// envAllowed reports whether a child may inherit name from the driver.
func envAllowed(name string) bool {
	if envForbidden(name) {
		return false
	}
	for _, n := range credentialEnv {
		if name == n {
			return true
		}
	}
	for _, n := range childEnvNames {
		if name == n {
			return true
		}
	}
	for _, p := range childEnvPrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// environment is the driver's view of its process environment. Tests inject
// every field; productionEnvironment wires the real process.
type environment struct {
	lookupEnv func(string) (string, bool)
	environ   func() []string
	// passwdHome is the running user's home from the passwd entry, which
	// agent-director's store resolution has used (b.8dr); it can differ
	// from $HOME.
	passwdHome func() (string, error)
	getuid     func() int
}

// productionEnvironment is the real process environment.
func productionEnvironment() environment {
	return environment{
		lookupEnv: os.LookupEnv,
		environ:   os.Environ,
		passwdHome: func() (string, error) {
			u, err := user.Current()
			if err != nil {
				return "", err
			}
			return u.HomeDir, nil
		},
		getuid: os.Getuid,
	}
}

// getenv is lookupEnv without the presence flag.
func (e environment) getenv(name string) string {
	v, _ := e.lookupEnv(name)
	return v
}

// childEnv builds a child process environment: the allowed variables of the
// driver's own environment, then TMUX_TMPDIR set to the private directory,
// then extra (a case's additions). It refuses an extra variable that is
// forbidden, so no case can hand a child TMUX or a host session variable.
// The result is sorted, so the same inputs give the same environment.
func (e environment) childEnv(tmuxTmpdir string, extra map[string]string) ([]string, error) {
	vars := map[string]string{}
	for _, kv := range e.environ() {
		name, value, ok := strings.Cut(kv, "=")
		if ok && envAllowed(name) {
			vars[name] = value
		}
	}
	vars["TMUX_TMPDIR"] = tmuxTmpdir
	for name, value := range extra {
		if envForbidden(name) || name == "TMUX_TMPDIR" {
			return nil, fmt.Errorf("refusing to pass %s to a child process", name)
		}
		vars[name] = value
	}
	out := make([]string, 0, len(vars))
	for name, value := range vars {
		out = append(out, name+"="+value)
	}
	sort.Strings(out)
	return out, nil
}

// withoutCredentials returns env (NAME=value entries) minus every
// credentialEnv variable. Dry mode's children get no credential at all.
func withoutCredentials(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		if !isCredentialName(name) {
			out = append(out, kv)
		}
	}
	return out
}

// credentialFree is e with every credentialEnv variable hidden: dry mode
// seeds its Claude state from it, so no part of a credential is written.
func (e environment) credentialFree() environment {
	inner, innerEnviron := e.lookupEnv, e.environ
	e.lookupEnv = func(name string) (string, bool) {
		if isCredentialName(name) {
			return "", false
		}
		return inner(name)
	}
	e.environ = func() []string { return withoutCredentials(innerEnviron()) }
	return e
}

// isCredentialName reports whether name is in credentialEnv.
func isCredentialName(name string) bool {
	for _, n := range credentialEnv {
		if name == n {
			return true
		}
	}
	return false
}

// minScrubLen is the shortest credential value the scrubber replaces; a
// shorter value (a stray "1") would mangle unrelated text and is no secret.
const minScrubLen = 6

// scrubbedValue replaces a credential value in any output.
const scrubbedValue = "<redacted>"

// scrubber removes credential values from text before it is written
// anywhere (run log, results, table, error messages).
type scrubber struct {
	secrets []string
}

// newScrubber collects the values of every credentialEnv variable set in
// the environment.
func newScrubber(e environment) scrubber {
	var s scrubber
	for _, name := range credentialEnv {
		if v := e.getenv(name); len(v) >= minScrubLen {
			s.secrets = append(s.secrets, v)
		}
	}
	return s
}

// scrub returns text with every credential value replaced.
func (s scrubber) scrub(text string) string {
	for _, v := range s.secrets {
		text = strings.ReplaceAll(text, v, scrubbedValue)
	}
	return text
}
