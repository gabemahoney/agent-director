package store

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
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
	if got != 9 {
		t.Errorf("agent-rename threshold = %d; immutable v8→v9 hop must remain 9", got)
	}
	if got != agentRenameHopVersion {
		t.Errorf("install.sh AGENT_RENAME_HOP_VERSION = %d; internal/store's agentRenameHopVersion = %d", got, agentRenameHopVersion)
	}
	if agentRenameHopVersion > schemaVersion {
		t.Errorf("agentRenameHopVersion %d is above schemaVersion %d", agentRenameHopVersion, schemaVersion)
	}
}

// TestInstallShCutoverVersionsMatchStore: install.sh's pre-flight decides
// before the swap whether an upgrade crosses the native-input cutover, from
// INPUT_CUTOVER_VERSION (the user_version the inputCutover step produces,
// immutably 16) and STORE_SCHEMA_VERSION (schemaVersion). Its own outcome
// reasons sit on one AD_OUTCOME_REASONS line that ad_outcome_exit checks.
func TestInstallShCutoverVersionsMatchStore(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "skills", "install-agent-director", "install.sh"))
	if err != nil {
		t.Fatalf("read install.sh: %v", err)
	}
	cutover := -1
	for _, step := range migrationSteps {
		if step.inputCutover {
			cutover = step.from + 1
		}
	}
	for _, tc := range []struct {
		name string
		want int
	}{{"INPUT_CUTOVER_VERSION", cutover}, {"STORE_SCHEMA_VERSION", schemaVersion}} {
		found := regexp.MustCompile(`(?m)^`+tc.name+`=([0-9]+)$`).FindAllStringSubmatch(string(data), -1)
		if len(found) != 1 {
			t.Errorf("install.sh has %d lines of the form %s=<n>; want exactly 1", len(found), tc.name)
			continue
		}
		if got, _ := strconv.Atoi(found[0][1]); got != tc.want {
			t.Errorf("install.sh %s = %d; internal/store's is %d", tc.name, got, tc.want)
		}
	}
	if cutover != 16 || cutover > schemaVersion {
		t.Errorf("the inputCutover step produces v%d; the immutable cutover is v16, at or below schemaVersion %d", cutover, schemaVersion)
	}
	assignments := regexp.MustCompile(`(?m)^AD_OUTCOME_REASONS=`).FindAllIndex(data, -1)
	found := regexp.MustCompile(`(?m)^AD_OUTCOME_REASONS='([a-z0-9_]+(?: [a-z0-9_]+)*)'$`).FindAllStringSubmatch(string(data), -1)
	if len(assignments) != 1 || len(found) != 1 {
		t.Fatalf("install.sh assigns AD_OUTCOME_REASONS on %d lines, %d of the form AD_OUTCOME_REASONS='<reason> ...'; want exactly 1 of that form", len(assignments), len(found))
	}
	listed := map[string]bool{}
	for _, reason := range strings.Split(found[0][1], " ") {
		if listed[reason] {
			t.Errorf("AD_OUTCOME_REASONS lists %s twice", reason)
		}
		listed[reason] = true
	}
	for _, reason := range []string{"cutover_state_unreadable", "native_input_authority_missing", "check_only_unestablished"} {
		if !listed[reason] {
			t.Errorf("AD_OUTCOME_REASONS %q lacks %s", found[0][1], reason)
		}
	}
}
