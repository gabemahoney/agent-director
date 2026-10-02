package main

import (
	"fmt"
	"os"
	"os/user"
	"regexp"
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

// credentialEnv lists every credential-bearing variable the driver knows
// (build-lead decision 8). Values of these never appear in any output, and
// dry mode hides them all; ANTHROPIC_BASE_URL is here because a URL can
// embed a token. Only the gateway pair (ANTHROPIC_BASE_URL and
// ANTHROPIC_AUTH_TOKEN) may be set in real mode (checkRealModeEnv).
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
	"ANTHROPIC_MODEL", "ANTHROPIC_SMALL_FAST_MODEL",
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

// minPartLen is the length of the credential pieces the results scrub
// removes and the shortest URL path part it removes: the figures the leak
// gate of a live run's command file checks every result file for.
const minPartLen = 12

// withheldLine replaces a line of Claude's text that names a credential.
const withheldLine = "[line withheld: it names a credential]"

// credentialWordRE matches a line that names a credential, whatever it
// holds (the same words as that command file's withhold filter).
var credentialWordRE = regexp.MustCompile(`(?i)(auth|token|bearer|api[ _-]?key|cookie|secret|passw)`)

// scrubber removes credential text before it is written anywhere. scrub
// replaces the exact values (run log, results, table, error messages, and
// the pane text inputReady reads); scrubParts, for everything written to
// the results directory, also replaces their parts; scrubEvidence, for
// Claude's own text (pane captures, debug-log copies, pane excerpts in a
// reason or note), also withholds every line naming a credential.
type scrubber struct {
	secrets []string
	// parts are lower-cased (ASCII) and matched without regard to case:
	// each value; for ANTHROPIC_BASE_URL also the URL without a trailing
	// slash, its host with and without the port and its path and query
	// parts of minPartLen or more; for every other value each piece of
	// minPartLen characters.
	parts []string
}

// newScrubber collects the values of every credentialEnv variable set in
// the environment, and their parts.
func newScrubber(e environment) scrubber {
	var s scrubber
	seen := map[string]bool{}
	addPart := func(p string) {
		if p = asciiLower(p); len(p) >= minScrubLen && !seen[p] {
			seen[p] = true
			s.parts = append(s.parts, p)
		}
	}
	for _, name := range credentialEnv {
		v := e.getenv(name)
		if len(v) < minScrubLen {
			continue
		}
		s.secrets = append(s.secrets, v)
		addPart(v)
		if name == "ANTHROPIC_BASE_URL" {
			for _, p := range urlParts(v) {
				addPart(p)
			}
			continue
		}
		for i := 0; i+minPartLen <= len(v); i++ {
			addPart(v[i : i+minPartLen])
		}
	}
	return s
}

// urlParts are a URL's parts the results scrub removes besides the URL
// itself: the URL without a trailing slash, its host with and without a
// numeric port (any user info dropped), and every part of its path and
// query of minPartLen or more characters (split at / ? & = # ;).
func urlParts(u string) []string {
	parts := []string{strings.TrimRight(u, "/")}
	rest := u
	if i := strings.Index(rest, "://"); i >= 0 {
		rest = rest[i+len("://"):]
	}
	authority, tail := rest, ""
	if i := strings.IndexAny(rest, "/?#"); i >= 0 {
		authority, tail = rest[:i], rest[i:]
	}
	host := authority[strings.LastIndex(authority, "@")+1:]
	parts = append(parts, host)
	if i := strings.LastIndex(host, ":"); i >= 0 && isDigits(host[i+1:]) {
		parts = append(parts, host[:i])
	}
	for _, p := range strings.FieldsFunc(tail, func(r rune) bool { return strings.ContainsRune("/?&=#;", r) }) {
		if len(p) >= minPartLen {
			parts = append(parts, p)
		}
	}
	return parts
}

// isDigits reports whether s is one or more ASCII digits.
func isDigits(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return s != ""
}

// asciiLower lower-cases ASCII letters only, so byte offsets are kept.
func asciiLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if 'A' <= c && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	return string(b)
}

// scrub returns text with every credential value replaced.
func (s scrubber) scrub(text string) string {
	for _, v := range s.secrets {
		text = strings.ReplaceAll(text, v, scrubbedValue)
	}
	return text
}

// scrubParts returns text with every credential value and every part
// (s.parts) replaced, matched without regard to ASCII case; overlapping or
// adjacent matches become one scrubbedValue. It is for text written to the
// results directory, never for the text inputReady reads.
func (s scrubber) scrubParts(text string) string {
	text = s.scrub(text)
	if len(s.parts) == 0 {
		return text
	}
	lower := asciiLower(text)
	var hit []bool
	for _, p := range s.parts {
		for from := 0; ; {
			i := strings.Index(lower[from:], p)
			if i < 0 {
				break
			}
			if hit == nil {
				hit = make([]bool, len(text))
			}
			for k := from + i; k < from+i+len(p); k++ {
				hit[k] = true
			}
			from += i + 1
		}
	}
	if hit == nil {
		return text
	}
	var b strings.Builder
	for i := 0; i < len(text); {
		j := i
		for j < len(text) && hit[j] == hit[i] {
			j++
		}
		if hit[i] {
			b.WriteString(scrubbedValue)
		} else {
			b.WriteString(text[i:j])
		}
		i = j
	}
	return b.String()
}

// scrubEvidence is scrubParts, then every line naming a credential (auth,
// token, bearer, api key, cookie, secret, passw; any case) is replaced by
// withheldLine. It is for Claude's own text kept in the results: pane
// captures, debug-log copies and the pane excerpt a reason or note quotes.
func (s scrubber) scrubEvidence(text string) string {
	lines := strings.Split(s.scrubParts(text), "\n")
	for i, l := range lines {
		if credentialWordRE.MatchString(l) {
			lines[i] = withheldLine
		}
	}
	return strings.Join(lines, "\n")
}
