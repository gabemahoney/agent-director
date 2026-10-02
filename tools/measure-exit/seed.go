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

// apiKeySuffixLen is how much of ANTHROPIC_API_KEY Claude Code keeps to
// remember an approved custom key (customApiKeyResponses.approved): the
// last 20 characters, never the whole key.
const apiKeySuffixLen = 20

// Credential modes, recorded in the results for the operator. credentialMode
// checks them in this order; the record is informational, and the API-key
// approval entry is seeded whenever ANTHROPIC_API_KEY is set, whatever the
// recorded mode.
const (
	credBedrock = "bedrock"
	credGateway = "auth_token"
	credAPIKey  = "api_key"
	credOAuth   = "oauth_token"
	credNone    = "none"
)

// credentialMode names the credential mode the environment's variables
// select (gateway and Bedrock modes need no approval entry).
func credentialMode(e environment) string {
	switch {
	case e.getenv("CLAUDE_CODE_USE_BEDROCK") != "" && e.getenv("CLAUDE_CODE_USE_BEDROCK") != "0":
		return credBedrock
	case e.getenv("ANTHROPIC_AUTH_TOKEN") != "":
		return credGateway
	case e.getenv("ANTHROPIC_API_KEY") != "":
		return credAPIKey
	case e.getenv("CLAUDE_CODE_OAUTH_TOKEN") != "":
		return credOAuth
	default:
		return credNone
	}
}

// seedClaudeState creates home/.claude.json for this run, before the first
// spawn, so spawn's pre-trust has a file to write its folder-trust entries
// into and the agents start without a first-run dialog. It marks onboarding
// complete for claudeCode (the measured version) and, when
// ANTHROPIC_API_KEY is set, approves that key by its last 20 characters,
// the form Claude Code stores (never the whole value). A state file that
// already exists was not created by this run: seeding refuses it. The file
// is created exclusively with mode 0600 and only in home.
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
	if key := e.getenv("ANTHROPIC_API_KEY"); key != "" {
		if len(key) <= apiKeySuffixLen {
			return rec, fmt.Errorf("ANTHROPIC_API_KEY is too short to approve without writing it whole")
		}
		state["customApiKeyResponses"] = map[string]any{
			"approved": []string{key[len(key)-apiKeySuffixLen:]},
			"rejected": []string{},
		}
		rec.APIKeyApproved = true
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
