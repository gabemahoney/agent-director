package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

// claudeStateFile is Claude Code's state file in a HOME. The harness writes
// its own minimal one before the first spawn; it never reads, copies or
// mounts a host's.
const claudeStateFile = ".claude.json"

// Credential modes, recorded in the results for the operator. The gateway
// (ANTHROPIC_AUTH_TOKEN, with ANTHROPIC_BASE_URL) is the only credential a
// run may carry: real mode refuses every other one (checkRealModeEnv) and
// probe mode allows only the runner's dummy token (checkProbeCredentials).
const (
	credGateway = "auth_token"
	credNone    = "none"
)

// credentialMode names the credential mode the environment selects: the
// gateway when ANTHROPIC_AUTH_TOKEN is set, else none.
func credentialMode(e environment) string {
	if e.getenv("ANTHROPIC_AUTH_TOKEN") != "" {
		return credGateway
	}
	return credNone
}

// seedClaudeState creates home/.claude.json for this run, before the first
// spawn, so spawn's pre-trust has a file to write its folder-trust entries
// into and the agents start without a first-run dialog. It marks onboarding
// complete for claudeCode (the measured version) and writes no credential.
// A state file that already exists was not created by this run: seeding
// refuses it. The file is created exclusively with mode 0600 and only in
// home.
func seedClaudeState(home string, e environment, claudeCode string) (seedRecord, error) {
	path := filepath.Join(home, claudeStateFile)
	rec := seedRecord{Path: path, CredentialMode: credentialMode(e)}
	state := map[string]any{
		"hasCompletedOnboarding": true,
		"theme":                  "dark",
		"projects":               map[string]any{},
	}
	if v, ok := parseVersion(claudeCode); ok {
		state["lastOnboardingVersion"] = fmt.Sprintf("%d.%d.%d", v[0], v[1], v[2])
	}
	for k := range state {
		rec.Keys = append(rec.Keys, k)
	}
	sort.Strings(rec.Keys)
	b, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return rec, err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		return rec, refuse(ruleClaudeStateFresh, "%s already exists and was not created by this run", path)
	}
	if err != nil {
		return rec, err
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		_ = f.Close()
		return rec, err
	}
	return rec, f.Close()
}
