package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Rules a preflight refusal names. Each refusal message starts with its rule
// so the operator (and a test) can tell which check stopped the run.
const (
	ruleContainerOnly    = "container-only"
	ruleTmuxUnset        = "tmux-unset"
	ruleHomeHasStore     = "home-has-no-store"
	ruleSampleFloor      = "sample-floor"
	rulePrivateTmux      = "private-tmux-tmpdir"
	rulePrivateSocket    = "private-socket"
	ruleVersionFloor     = "claude-version-floor"
	ruleClaudeStateFresh = "claude-state-fresh"
	ruleHomeSet          = "home-set"
	ruleCredential       = "credential-present"
	ruleModelSet         = "model-set"
	ruleDryStubOnly      = "dry-stub-only"
	ruleProbeCredential  = "probe-no-credentials"
	ruleLocalLayer       = "local-layer-conflict"
	ruleRealGatewayOnly  = "real-gateway-only"
)

// modelEnv pins the agents' model. The host runner forwards it by name;
// real mode refuses without it, so a run's model is always the one the
// operator chose (L1 and L2 use the gateway's production Opus id).
const modelEnv = "ANTHROPIC_MODEL"

// stubVersionMarker is in every dry-run stub's `claude --version` output.
// Dry mode refuses a claude whose version lacks it: a real Claude Code
// first on PATH would start real agents.
const stubVersionMarker = "measure-exit dry-run stub"

// probeDummyTokenPrefix starts the dummy ANTHROPIC_AUTH_TOKEN the host
// runner gives the version probe's container (which has no network). Probe
// mode refuses any other credential, so no real one reaches it.
const probeDummyTokenPrefix = "mx-probe-dummy-token"

// probeForbiddenCredentials may never be set in probe mode.
var probeForbiddenCredentials = []string{
	"ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_BASE_URL",
	"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN", "AWS_BEARER_TOKEN_BEDROCK",
}

// refusal is an isolation-preflight failure. The run subcommand exits
// exitRefused for it. Messages never name a command that ends a session or
// a tmux server.
type refusal struct {
	rule   string
	detail string
}

func (r *refusal) Error() string {
	return "measure-exit refused (" + r.rule + "): " + r.detail
}

// refuse builds a refusal.
func refuse(rule, format string, args ...any) error {
	return &refusal{rule: rule, detail: fmt.Sprintf(format, args...)}
}

// isolation is the preflight's record of the run's isolation, printed and
// written into the results before any case starts.
type isolation struct {
	RunID      string `json:"run_id"`
	Mode       mode   `json:"mode"`
	Home       string `json:"home"`
	PasswdHome string `json:"passwd_home"`
	// TmuxTmpdir is the private TMUX_TMPDIR, as its real path.
	TmuxTmpdir string `json:"tmux_tmpdir"`
	// ExpectedSocket is the socket tmux and agent-director resolve under
	// TmuxTmpdir; RecordedSocket is the first spawned row's, once known.
	ExpectedSocket string `json:"expected_socket"`
	RecordedSocket string `json:"recorded_socket,omitempty"`
	// AgentDirector and ClaudeCode are the versions the binaries report;
	// ClaudePath is where the claude binary resolved.
	AgentDirector string `json:"agent_director_version"`
	ClaudeCode    string `json:"claude_code_version"`
	ClaudePath    string `json:"claude_path"`
	// ClaudeVersionFloor is the floor applied, or "exempt" in probe and dry
	// mode.
	ClaudeVersionFloor string `json:"claude_version_floor"`
	WorkDir            string `json:"work_dir"`
	// Parallelism is always 1: samples run one at a time, so load does not
	// skew exit times.
	Parallelism int `json:"parallelism"`
}

// versionReader runs the two version commands the preflight records, with
// the private TMUX_TMPDIR the preflight has just made in their environment.
// The harness satisfies it; tests inject a double.
type versionReader interface {
	readVersions(tmuxTmpdir string) (agentDirector, claudeCode, claudePath string, err error)
}

// checkEnvironment runs the checks that need no file system change: the
// container marker, TMUX, $HOME, both homes free of .agent-director, and the
// sample floor when sampled is true (an RN-6 or RN-2 case is selected). It
// returns the two homes.
func checkEnvironment(c config, e environment, sampled bool) (home, passwdHome string, err error) {
	if e.getenv(containerMarkerEnv) == "" {
		if c.mode != modeDry || e.getenv(sandboxMarkerEnv) == "" {
			return "", "", refuse(ruleContainerOnly,
				"%s is not set: this driver must only run inside the measurement container (dry mode also runs inside the repo sandbox, %s=1); it never runs on a host",
				containerMarkerEnv, sandboxMarkerEnv)
		}
	}
	if _, set := e.lookupEnv("TMUX"); set {
		return "", "", refuse(ruleTmuxUnset, "TMUX is set: the driver must not run inside a tmux client of another server")
	}
	home = e.getenv("HOME")
	if home == "" || !filepath.IsAbs(home) {
		return "", "", refuse(ruleHomeSet, "HOME %q is not an absolute path", home)
	}
	passwdHome, err = e.passwdHome()
	if err != nil {
		return "", "", refuse(ruleHomeSet, "cannot read the passwd-entry home: %v", err)
	}
	for _, h := range []string{home, passwdHome} {
		if err := checkNoStore(h); err != nil {
			return "", "", err
		}
	}
	if sampled && c.mode == modeReal && c.samples < minSamples {
		return "", "", refuse(ruleSampleFloor, "real mode needs at least %d samples per RN-6/RN-2 case, got %d", minSamples, c.samples)
	}
	if c.mode == modeReal {
		if err := checkRealModeEnv(e); err != nil {
			return "", "", err
		}
	}
	if c.mode == modeReal && credentialMode(e) == credNone {
		return "", "", refuse(ruleCredential, "real mode needs the gateway credential ANTHROPIC_AUTH_TOKEN; it is not set")
	}
	if c.mode == modeReal && e.getenv(modelEnv) == "" {
		return "", "", refuse(ruleModelSet, "real mode needs %s (the model the runner pins for the run); it is not set", modelEnv)
	}
	if c.mode == modeProbe {
		if err := checkProbeCredentials(e); err != nil {
			return "", "", err
		}
	}
	return home, passwdHome, nil
}

// checkProbeCredentials refuses probe mode with any real credential: the
// only one allowed is the runner's dummy ANTHROPIC_AUTH_TOKEN.
func checkProbeCredentials(e environment) error {
	for _, name := range probeForbiddenCredentials {
		if _, set := e.lookupEnv(name); set {
			return refuse(ruleProbeCredential, "probe mode runs with no credential, but %s is set", name)
		}
	}
	if v, set := e.lookupEnv("ANTHROPIC_AUTH_TOKEN"); set && !strings.HasPrefix(v, probeDummyTokenPrefix) {
		return refuse(ruleProbeCredential, "probe mode allows only the runner's dummy ANTHROPIC_AUTH_TOKEN (%s…)", probeDummyTokenPrefix)
	}
	return nil
}

// checkLocalLayer refuses a real run that was given a deployment local
// layer when a selected case needs the harness's generated layer, which is
// the same file (lead decision 8): the two cannot sit side by side.
func checkLocalLayer(c config, cases []caseSpec) error {
	if c.mode != modeReal || c.localSettings == "" {
		return nil
	}
	for _, cs := range cases {
		if cs.generated {
			return refuse(ruleLocalLayer,
				"case %s needs the harness's generated .claude/settings.local.json, so the deployment local layer (-local-settings) cannot sit beside it; run that case without -local-settings or deselect it",
				cs.id)
		}
	}
	return nil
}

// checkNoStore refuses when home already holds a .agent-director: the run's
// store must be created fresh by this run, never be one that existed.
func checkNoStore(home string) error {
	p := filepath.Join(home, ".agent-director")
	_, err := os.Lstat(p)
	switch {
	case err == nil:
		return refuse(ruleHomeHasStore, "%s exists: the run's home must start with no agent-director store", p)
	case errors.Is(err, fs.ErrNotExist):
		return nil
	default:
		return refuse(ruleHomeHasStore, "cannot check %s: %v", p, err)
	}
}

// makePrivateTmuxDir creates the run's private TMUX_TMPDIR under base (mode
// 0700) and returns its real path and the socket tmux will use there:
// <dir>/tmux-<uid>/default, as agent-director's socket resolution computes it.
func makePrivateTmuxDir(base string, uid int) (dir, socket string, err error) {
	created, err := os.MkdirTemp(base, "mx-tmux-")
	if err != nil {
		return "", "", refuse(rulePrivateTmux, "cannot create a private TMUX_TMPDIR under %s: %v", base, err)
	}
	if err := os.Chmod(created, 0o700); err != nil {
		return "", "", refuse(rulePrivateTmux, "cannot set mode 0700 on %s: %v", created, err)
	}
	dir, err = filepath.EvalSymlinks(created)
	if err != nil {
		return "", "", refuse(rulePrivateTmux, "cannot resolve %s: %v", created, err)
	}
	return dir, filepath.Join(dir, "tmux-"+strconv.Itoa(uid), "default"), nil
}

// checkSocketPrivate refuses a recorded socket that does not lie under the
// private TMUX_TMPDIR. The run aborts on it: agents would otherwise run on a
// server the run does not own.
func checkSocketPrivate(tmuxTmpdir, socket string) error {
	rel, err := filepath.Rel(tmuxTmpdir, socket)
	if socket == "" || !filepath.IsAbs(socket) || err != nil || rel == "." || rel == ".." ||
		strings.HasPrefix(rel, "../") {
		return refuse(rulePrivateSocket, "the row's tmux socket %q is not under the private TMUX_TMPDIR %s", socket, tmuxTmpdir)
	}
	return nil
}

// preflight runs every isolation check of the run subcommand in order and
// returns the isolation record. Nothing is spawned before it succeeds.
func preflight(c config, e environment, vr versionReader, sampled bool) (isolation, error) {
	home, passwdHome, err := checkEnvironment(c, e, sampled)
	if err != nil {
		return isolation{}, err
	}
	iso := isolation{RunID: c.runID, Mode: c.mode, Home: home, PasswdHome: passwdHome, Parallelism: 1}
	iso.WorkDir = c.workDir
	if iso.WorkDir == "" {
		iso.WorkDir = filepath.Join(home, "measure-exit-work")
	}
	iso.TmuxTmpdir, iso.ExpectedSocket, err = makePrivateTmuxDir(c.tmuxBase, e.getuid())
	if err != nil {
		return iso, err
	}
	if err := checkSocketPrivate(iso.TmuxTmpdir, iso.ExpectedSocket); err != nil {
		return iso, err
	}
	if iso.AgentDirector, iso.ClaudeCode, iso.ClaudePath, err = vr.readVersions(iso.TmuxTmpdir); err != nil {
		return iso, err
	}
	iso.ClaudeVersionFloor = "exempt"
	if c.mode == modeDry && !strings.Contains(iso.ClaudeCode, stubVersionMarker) {
		return iso, refuse(ruleDryStubOnly,
			"dry mode needs the stub claude first on PATH, but %s reports %q; a real Claude Code would start real agents",
			iso.ClaudePath, iso.ClaudeCode)
	}
	if c.mode == modeReal {
		iso.ClaudeVersionFloor = realModeMinClaudeCode
		if !versionAtLeast(iso.ClaudeCode, realModeMinClaudeCode) {
			return iso, refuse(ruleVersionFloor,
				"Claude Code %q is older than %s, the deployed version and the oldest a real run accepts; older versions are expected to ignore exec-form hooks, which would write no_exec_form so that no agent reports in",
				iso.ClaudeCode, realModeMinClaudeCode)
		}
	}
	return iso, nil
}

// semverRe finds the first X.Y.Z in a version string ("2.1.285 (Claude
// Code)").
var semverRe = regexp.MustCompile(`\d+\.\d+\.\d+`)

// parseVersion returns the first X.Y.Z of s as three numbers.
func parseVersion(s string) ([3]int, bool) {
	var v [3]int
	m := semverRe.FindString(s)
	if m == "" {
		return v, false
	}
	for i, part := range strings.Split(m, ".") {
		n, err := strconv.Atoi(part)
		if err != nil {
			return v, false
		}
		v[i] = n
	}
	return v, true
}

// versionAtLeast reports whether have is at least want. An unparseable have
// is never at least anything.
func versionAtLeast(have, want string) bool {
	h, ok := parseVersion(have)
	w, okw := parseVersion(want)
	if !ok || !okw {
		return false
	}
	for i := range h {
		if h[i] != w[i] {
			return h[i] > w[i]
		}
	}
	return true
}

// parseAgentDirectorVersion reads the version verb's {"version": …} output.
func parseAgentDirectorVersion(out []byte) (string, error) {
	var v struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(out, &v); err != nil || v.Version == "" {
		return "", fmt.Errorf("unexpected version output %q", strings.TrimSpace(string(out)))
	}
	return v.Version, nil
}

// summaryLines is the isolation summary the run prints before any case.
func (iso isolation) summaryLines() []string {
	return []string{
		"run id:            " + iso.RunID,
		"mode:              " + string(iso.Mode),
		"home:              " + iso.Home + " (passwd entry: " + iso.PasswdHome + ")",
		"private TMUX_TMPDIR: " + iso.TmuxTmpdir,
		"expected socket:   " + iso.ExpectedSocket,
		"agent-director:    " + iso.AgentDirector,
		"Claude Code:       " + iso.ClaudeCode + " at " + iso.ClaudePath + " (floor: " + iso.ClaudeVersionFloor + ")",
		"work dir:          " + iso.WorkDir,
		"parallelism:       1 (samples run one at a time)",
	}
}
