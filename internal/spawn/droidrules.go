package spawn

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/inputdelivery"
	"golang.org/x/sys/unix"
)

// DroidRulesCheckDeadline bounds Droid's own `rules check --file` of a
// launch's Rule Set, from writing the private copy to the check's exit and
// process-group cleanup.
// On Droid 1.5.0 a check of a 17-rule, 3.9 KB Rule Set (SR-4.1's table) took
// 0.135 to 0.142 s over 10 runs (AD test VM, 2026-11-16; t3.ba6.3q.u6.1m);
// the margin covers a loaded host and a cold start while bounding a hung check.
const DroidRulesCheckDeadline = 10 * time.Second

// droidRuleSetMaxBytes caps the Rule Set File a launch reads; a larger file
// is refused as unreadable rather than read whole.
const droidRuleSetMaxBytes = launchInputMaxBytes

// launchInputMaxBytes bounds both operator-supplied launch input files.
const launchInputMaxBytes = 1 << 20

// droidRulesCheckOutputLimit caps each output stream of Droid's check, which
// is never parsed or echoed; more refuses the launch. Droid 1.5.0 wrote about
// 3 KB for 17 rules, so this allows a Rule Set File of the largest size read.
const droidRulesCheckOutputLimit = 1 << 20

// droidRulesDoc is where an operator finds the Rule Set's format and refusals.
const droidRulesDoc = "docs/experimental.md#droid-rules"

// droidRuleIDPrefix starts every launch rule's ID (SR-3.2); the launch token
// follows, so no rule written before the launch can carry it.
const droidRuleIDPrefix = "agent-director/"

// droidReservedIDPrefix starts the IDs of Droid's built-in rules.
const droidReservedIDPrefix = "builtin/"

// droidRulesCheckDirPattern names the private temporary directory of Droid's
// check, which a removal failure leaves for an operator to delete.
const droidRulesCheckDirPattern = "agent-director-droid-rules-"

// The Rule Set's fixed problem kinds (SR-2.5). None carries a value from the
// file, so a refusal never echoes rule text, file content or Droid's output.
const (
	ruleSetUnreadable   = "the file is not a readable regular file of at most 1 MiB"
	ruleSetNotObject    = "the file is not a rules object: a JSON object with version 1 and a non-empty rules array"
	ruleSetTopField     = "the file has a top-level field other than version and rules"
	ruleNotObject       = "is not a rule object with id, decision, match.prefix, tests.match and tests.noMatch, each of the JSON type Droid documents"
	ruleField           = "has a field Droid's rule format does not define"
	ruleDecision        = "has a decision other than allow, ask or block"
	ruleEmptyID         = "has an empty id"
	ruleRepeatedID      = "repeats an earlier rule's id"
	ruleReservedID      = "has an id starting with Droid's reserved " + droidReservedIDPrefix
	ruleDisabled        = "sets enabled to something other than true; remove a disabled rule instead"
	ruleProgram         = "is an allow rule whose program is not tail, head or an absolute path to a program other than a shell, interpreter or command runner"
	checkCopyFailed     = "the private copy for Droid's rules check could not be written"
	checkCleanupFailed  = "the private copy for Droid's rules check could not be removed; delete the " + droidRulesCheckDirPattern + "* directory left in the temporary directory"
	checkCouldNotRun    = "Droid's rules check could not run (the droid program was not found or could not start, or its output exceeded the limit)"
	checkStopFailed     = "Droid's rules check left processes that could not be stopped"
	checkNonzeroExit    = "Droid's rules check failed with a nonzero exit status (a rule is invalid or one of its tests fails)"
	checkDeadlineFormat = "Droid's rules check did not finish within %s"
)

// generalPurposeLaunchers are SR-2.3's shells, interpreters and command
// runners: an allow rule may not name one as its program, because any command
// could follow it. This recognises the kinds the PRD names; it is not a safety
// guarantee.
var generalPurposeLaunchers = []string{
	// shells
	"sh", "bash", "dash", "zsh", "ksh", "mksh", "fish", "csh", "tcsh", "busybox",
	// interpreters
	"python", "pypy", "perl", "ruby", "jruby", "node", "nodejs", "bun", "deno", "php",
	"lua", "luajit", "tclsh", "wish", "awk", "gawk", "mawk", "nawk",
	// command runners
	"env", "xargs", "find", "sudo", "su", "doas", "pkexec", "runuser", "sg", "nohup",
	"nice", "ionice", "timeout", "setsid", "stdbuf", "time", "chroot", "unshare",
	"nsenter", "flock", "script", "watch", "parallel", "ssh", "docker", "podman",
}

// launcherVersionSuffix is the one trailing version suffix SR-2.3 removes
// before comparing a program's name: an optional "-", then digits and dots
// (python3.11, lua5.4, pypy3, node18, ruby-3.1).
var launcherVersionSuffix = regexp.MustCompile(`-?[0-9][0-9.]*$`)

// droidRuleSet is the standalone rules format (SR-1.3), which the overlay
// embeds as permissionRules. Field order fixes the serialized bytes.
type droidRuleSet struct {
	Version int         `json:"version"`
	Rules   []droidRule `json:"rules"`
}

type droidRule struct {
	ID       string     `json:"id"`
	Decision string     `json:"decision"`
	Match    droidMatch `json:"match"`
	Tests    droidTests `json:"tests"`
	Reason   *string    `json:"reason,omitempty"`
	Enabled  *bool      `json:"enabled,omitempty"`
}

type droidMatch struct {
	Prefix []droidPrefixWord `json:"prefix"`
}

// droidPrefixWord is one match.prefix position: a word, or a list of
// alternative words, serialized as the file wrote it.
type droidPrefixWord struct {
	alternatives []string
	list         bool
}

func (w droidPrefixWord) MarshalJSON() ([]byte, error) {
	if w.list {
		return json.Marshal(w.alternatives)
	}
	return json.Marshal(w.alternatives[0])
}

// droidTests are a rule's embedded examples, which Droid's check runs; Droid
// 1.5.0 requires both lists.
type droidTests struct {
	Match   []string `json:"match"`
	NoMatch []string `json:"noMatch"`
}

// ResolveDroidRules is the Droid launch's Rule Set step (SR-2), run by spawn
// and finished-ID reuse after request validation, the launch token and
// ResolveDroidGuard, before composition and any launch-file, trust, row or
// session write. For any other agent, or with [launch] droid_rules_path unset, it returns r unchanged and
// reads nothing. Otherwise it reads the Rule Set File once, checks it
// (checkDroidRuleSet), resolves the droid program as the version probe does,
// runs Droid's own check on a private copy of exactly the bytes the overlay
// will embed, and returns r carrying those bytes and that program
// (Resolved.DroidRules, Resolved.DroidProgram). Every failure is
// ErrPermissionRulesRefused and writes none of agent-director's launch resources:
// no row, session, trust entry or launch file. Only a failed removal of the
// check's temporary copy, which the refusal names, leaves that copy behind.
// Every Droid launch path must call ResolveDroidRules before composition and
// any launch write, including spawn and finished-ID reuse.
func ResolveDroidRules(r Resolved, cfg config.Config) (Resolved, error) {
	return resolveDroidRules(r, cfg, DroidRulesCheckDeadline)
}

func resolveDroidRules(r Resolved, cfg config.Config, deadline time.Duration) (Resolved, error) {
	if r.Agent != config.AgentDroid || cfg.Launch.DroidRulesPath == "" {
		return r, nil
	}
	data, err := readDroidRuleSet(cfg.Launch.DroidRulesPath)
	if err != nil {
		return r, err
	}
	rules, err := checkDroidRuleSet(data, r.LaunchToken)
	if err != nil {
		return r, err
	}
	env := composeEnv(r)
	program, err := inputdelivery.ResolveProgram(droidProgram(cfg), env)
	if err != nil {
		return r, droidRulesRefusal(-1, checkCouldNotRun)
	}
	ctx, cancel := context.WithTimeout(context.Background(), deadline)
	defer cancel()
	if err := runDroidRulesCheck(ctx, deadline, program, env, rules); err != nil {
		return r, err
	}
	r.DroidRules, r.DroidProgram = rules, program
	return r, nil
}

// droidRulesRefusal is ErrPermissionRulesRefused for its fixed problem kinds
// (one, or a check problem with a cleanup failure), naming the rule's position
// when index is not negative. Only a cleanup failure, which says so, leaves
// anything behind: the check's temporary copy, never a launch's own records.
func droidRulesRefusal(index int, problem string) error {
	where := ""
	if index >= 0 {
		where = fmt.Sprintf("rules[%d] ", index)
	}
	return fmt.Errorf("%w: Droid Rule Set refused: %s%s; an operator must fix the Rule Set File [launch] droid_rules_path names, or the droid program (see %s); no row, session, trust entry or launch file was written and nothing was launched",
		ErrPermissionRulesRefused, where, problem, droidRulesDoc)
}

// readDroidRuleSet preserves the Rule Set's sanitized refusal while sharing
// the Path List's one bounded reader.
func readDroidRuleSet(name string) ([]byte, error) {
	data, err := readBoundedLaunchFile(name)
	if err != nil {
		return nil, droidRulesRefusal(-1, ruleSetUnreadable)
	}
	return data, nil
}

// readBoundedLaunchFile is the reusable reader for operator launch inputs.
// Must use: Rule Set and Path List reads share this nonblocking open,
// regular-file check and 1 MiB limit, including growth during the read. Each
// caller supplies its own sanitized refusal; no raw OS error escapes here.
func readBoundedLaunchFile(name string) ([]byte, error) {
	unreadable := errors.New("unreadable launch input")
	f, err := os.OpenFile(name, os.O_RDONLY|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, unreadable
	}
	// Closing a read-only descriptor cannot lose data; read failures refuse.
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > launchInputMaxBytes {
		return nil, unreadable
	}
	data, err := io.ReadAll(io.LimitReader(f, launchInputMaxBytes+1))
	if err != nil || len(data) > launchInputMaxBytes {
		return nil, unreadable
	}
	return data, nil
}

// checkDroidRuleSet is agent-director's own check of a Rule Set (SR-2.2,
// SR-2.3), with no I/O: it parses data strictly, refuses the first problem it
// finds, rewrites each rule's ID to agent-director/<token>/<operator id>
// (SR-3.2) and serializes the result once. Droid's check and the overlay both
// take exactly the bytes it returns.
func checkDroidRuleSet(data []byte, token string) ([]byte, error) {
	if !utf8.Valid(data) {
		return nil, droidRulesRefusal(-1, ruleSetNotObject)
	}
	top, problem := parseObject(data, "version", "rules")
	switch problem {
	case ruleNotObject:
		return nil, droidRulesRefusal(-1, ruleSetNotObject)
	case ruleField:
		return nil, droidRulesRefusal(-1, ruleSetTopField)
	}
	// Only a JSON number (or null, which leaves 0) unmarshals into a float64,
	// and only an array (or null, which leaves none) into a slice.
	var version float64
	var raws []json.RawMessage
	if json.Unmarshal(top["version"], &version) != nil || version != 1 ||
		json.Unmarshal(top["rules"], &raws) != nil || len(raws) == 0 {
		return nil, droidRulesRefusal(-1, ruleSetNotObject)
	}
	set := droidRuleSet{Version: 1, Rules: make([]droidRule, 0, len(raws))}
	seen := make(map[string]bool, len(raws))
	for i, raw := range raws {
		rule, problem := parseDroidRule(raw)
		switch {
		case problem != "":
		case rule.ID == "":
			problem = ruleEmptyID
		case seen[rule.ID]:
			problem = ruleRepeatedID
		case strings.HasPrefix(rule.ID, droidReservedIDPrefix):
			problem = ruleReservedID
		case rule.Enabled != nil && !*rule.Enabled:
			problem = ruleDisabled
		case rule.Decision == "allow" && slices.ContainsFunc(rule.Match.Prefix[0].alternatives, refusedDroidProgram):
			problem = ruleProgram
		}
		if problem != "" {
			return nil, droidRulesRefusal(i, problem)
		}
		seen[rule.ID] = true
		rule.ID = droidRuleIDPrefix + token + "/" + rule.ID
		set.Rules = append(set.Rules, rule)
	}
	return json.Marshal(set)
}

// parseDroidRule reads one rule, refusing any field Droid's format does not
// define and any value not of its documented JSON type: id and decision
// strings, match.prefix a non-empty array of words or non-empty lists of
// words, tests.match and tests.noMatch arrays of strings, and the optional
// reason a string and enabled a boolean. It returns the rule, or the problem
// kind. Droid's own check judges the rest, such as empty test lists.
func parseDroidRule(raw json.RawMessage) (droidRule, string) {
	var rule droidRule
	fields, problem := parseObject(raw, "id", "decision", "match", "tests", "reason", "enabled")
	if problem != "" {
		return rule, problem
	}
	var idOK, decisionOK bool
	rule.ID, idOK = parseString(fields["id"])
	rule.Decision, decisionOK = parseString(fields["decision"])
	if !idOK || !decisionOK {
		return rule, ruleNotObject
	}
	if rule.Decision != "allow" && rule.Decision != "ask" && rule.Decision != "block" {
		return rule, ruleDecision
	}
	match, problem := parseObject(fields["match"], "prefix")
	if problem != "" {
		return rule, problem
	}
	var words []json.RawMessage
	if jsonKind(match["prefix"]) != '[' || json.Unmarshal(match["prefix"], &words) != nil || len(words) == 0 {
		return rule, ruleNotObject
	}
	for _, word := range words {
		if s, ok := parseString(word); ok {
			rule.Match.Prefix = append(rule.Match.Prefix, droidPrefixWord{alternatives: []string{s}})
			continue
		}
		alternatives, ok := parseStrings(word)
		if !ok || len(alternatives) == 0 {
			return rule, ruleNotObject
		}
		rule.Match.Prefix = append(rule.Match.Prefix, droidPrefixWord{alternatives: alternatives, list: true})
	}
	tests, problem := parseObject(fields["tests"], "match", "noMatch")
	if problem != "" {
		return rule, problem
	}
	var matchOK, noMatchOK bool
	rule.Tests.Match, matchOK = parseStrings(tests["match"])
	rule.Tests.NoMatch, noMatchOK = parseStrings(tests["noMatch"])
	if !matchOK || !noMatchOK {
		return rule, ruleNotObject
	}
	if raw, present := fields["reason"]; present {
		reason, ok := parseString(raw)
		if !ok {
			return rule, ruleNotObject
		}
		rule.Reason = &reason
	}
	if raw, present := fields["enabled"]; present {
		// null would unmarshal into a bool without error.
		var enabled bool
		if jsonKind(raw) == 'n' || json.Unmarshal(raw, &enabled) != nil {
			return rule, ruleNotObject
		}
		rule.Enabled = &enabled
	}
	return rule, ""
}

// refusedDroidProgram is SR-2.3's program check for one alternative at an
// allow rule's match.prefix position 0. It passes exactly tail or head, and an
// absolute path whose final component, less one version suffix, is not a
// General-Purpose Launcher; any other absolute path is trusted as listed.
func refusedDroidProgram(word string) bool {
	if word == "tail" || word == "head" {
		return false
	}
	if !filepath.IsAbs(word) {
		return true
	}
	name := path.Base(word)
	if stem := launcherVersionSuffix.ReplaceAllString(name, ""); stem != "" {
		name = stem
	}
	return slices.Contains(generalPurposeLaunchers, name)
}

// jsonKind is the first byte of a JSON value, which names its type; 0 for
// none.
func jsonKind(raw []byte) byte {
	trimmed := bytes.TrimLeft(raw, " \t\r\n")
	if len(trimmed) == 0 {
		return 0
	}
	return trimmed[0]
}

// parseObject reads a JSON object whose keys are all in keys, exactly as
// written. Its problem kind is ruleNotObject for a value that is not an
// object and ruleField for an object with any other key.
func parseObject(raw []byte, keys ...string) (map[string]json.RawMessage, string) {
	var fields map[string]json.RawMessage
	if jsonKind(raw) != '{' || json.Unmarshal(raw, &fields) != nil {
		return nil, ruleNotObject
	}
	for key := range fields {
		if !slices.Contains(keys, key) {
			return nil, ruleField
		}
	}
	return fields, ""
}

func parseString(raw json.RawMessage) (string, bool) {
	var s string
	if jsonKind(raw) != '"' || json.Unmarshal(raw, &s) != nil {
		return "", false
	}
	return s, true
}

func parseStrings(raw json.RawMessage) ([]string, bool) {
	var items []json.RawMessage
	if jsonKind(raw) != '[' || json.Unmarshal(raw, &items) != nil {
		return nil, false
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		s, ok := parseString(item)
		if !ok {
			return nil, false
		}
		out = append(out, s)
	}
	return out, true
}

// runDroidRulesCheck runs `<program> rules check --file <copy>` on a private
// copy of rules (an owner-only directory and file, closed before the run),
// with the version probe's environment filtering less DroidForbiddenEnvKeys,
// bounded output and ctx's deadline. Only exit status 0 passes; Droid's
// output is neither parsed nor echoed. The copy and its directory are removed
// before it returns. A removal failure refuses too, alongside any earlier
// problem, so neither is lost.
func runDroidRulesCheck(ctx context.Context, deadline time.Duration, program string, env map[string]string, rules []byte) error {
	dir, err := os.MkdirTemp("", droidRulesCheckDirPattern)
	if err != nil {
		return droidRulesRefusal(-1, checkCopyFailed)
	}
	problem := checkPrivateCopy(ctx, deadline, dir, program, env, rules)
	if os.RemoveAll(dir) != nil {
		problem = joinProblems(problem, checkCleanupFailed)
	}
	if problem != "" {
		return droidRulesRefusal(-1, problem)
	}
	return nil
}

// checkPrivateCopy writes rules into dir and runs Droid's check on that copy,
// returning "" when it passes, else the problem kind. The caller removes dir.
func checkPrivateCopy(ctx context.Context, deadline time.Duration, dir, program string, env map[string]string, rules []byte) string {
	copyPath := filepath.Join(dir, "rules.json")
	if writePrivateCopy(dir, copyPath, rules) != nil {
		return checkCopyFailed
	}
	command := inputdelivery.ProbeCommand(ctx, program, []string{"rules", "check", "--file", copyPath}, env, DroidForbiddenEnvKeys)
	// A neutral working directory, so no project folder's settings join the check.
	command.Dir = dir
	stdout := inputdelivery.ProbeOutput{Limit: droidRulesCheckOutputLimit}
	stderr := inputdelivery.ProbeOutput{Limit: droidRulesCheckOutputLimit}
	command.Stdout, command.Stderr = &stdout, &stderr
	runErr, stopErr := inputdelivery.RunProbe(command)
	problem := ""
	var exitErr *exec.ExitError
	switch {
	case runErr == nil:
	case ctx.Err() != nil:
		problem = fmt.Sprintf(checkDeadlineFormat, deadline)
	case errors.As(runErr, &exitErr) && exitErr.ExitCode() > 0:
		problem = checkNonzeroExit
	default:
		problem = checkCouldNotRun
	}
	if stopErr != nil {
		problem = joinProblems(problem, checkStopFailed)
	}
	return problem
}

// joinProblems adds a further problem kind to those a check already met, so
// none is lost.
func joinProblems(problem, more string) string {
	if problem == "" {
		return more
	}
	return problem + ", and " + more
}

// writePrivateCopy creates name exclusively as an owner-only file in the
// owner-only directory dir, without following a symlink, and closes it. Both
// modes are set explicitly, because creation applies the process umask: a
// umask that masks owner read would leave a copy Droid cannot open.
func writePrivateCopy(dir, name string, content []byte) error {
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return err
	}
	if err := f.Chmod(0o600); err != nil {
		return errors.Join(err, f.Close())
	}
	_, writeErr := f.Write(content)
	return errors.Join(writeErr, f.Close())
}
