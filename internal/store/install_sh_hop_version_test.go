package store

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
)

// installShHopVersionLine is the one line of install.sh that holds the
// agent-rename migration's gate version.
var installShHopVersionLine = regexp.MustCompile(`(?m)^AGENT_RENAME_HOP_VERSION=([0-9]+)$`)

// TestInstallShHopVersionMatchesStore (b.ssz): install.sh migrates config.toml
// and the templates only for a store below AGENT_RENAME_HOP_VERSION, so that
// number is the user_version the agent-rename hop produces
// (agentRenameHopVersion), and a store written by the current schema is never
// below it.
func TestInstallShHopVersionMatchesStore(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "skills", "install-agent-director", "install.sh"))
	if err != nil {
		t.Fatalf("read install.sh: %v", err)
	}
	found := installShHopVersionLine.FindAllStringSubmatch(string(data), -1)
	if len(found) != 1 {
		t.Fatalf("install.sh has %d lines of the form AGENT_RENAME_HOP_VERSION=<n>; want exactly 1", len(found))
	}
	got, err := strconv.Atoi(found[0][1])
	if err != nil {
		t.Fatalf("AGENT_RENAME_HOP_VERSION %q: %v", found[0][1], err)
	}
	if got != agentRenameHopVersion {
		t.Errorf("install.sh AGENT_RENAME_HOP_VERSION = %d; internal/store's agentRenameHopVersion = %d", got, agentRenameHopVersion)
	}
	if agentRenameHopVersion > schemaVersion {
		t.Errorf("agentRenameHopVersion %d is above schemaVersion %d", agentRenameHopVersion, schemaVersion)
	}
}
