package adminapi

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/gabemahoney/agent-director/internal/installversion"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/version"
)

func TestVersionFactsCompiledAndDetached(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	core, digest := store.BusySendCoreContractIdentity()
	agents := store.BusySendCoveredAgents()
	slices.Sort(agents)
	want := installversion.Facts{FormatVersion: 1, Version: version.Version, Commit: version.Commit,
		CoreVersion: core, CoreDeclarationsSHA256: digest, CoveredAgents: agents}
	got := VersionFacts()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("facts=%+v want %+v", got, want)
	}
	if len(got.CoveredAgents) > 0 {
		got.CoveredAgents[0] = "mutated"
	}
	if !reflect.DeepEqual(VersionFacts(), want) {
		t.Fatal("facts leaked the compiled coverage slice")
	}
	root, err := VersionInstallRoot()
	if err != nil || root != filepath.Join(home, ".config", "agent-director") {
		t.Fatalf("root=%q err=%v", root, err)
	}
	entries, err := os.ReadDir(home)
	if err != nil || len(entries) != 0 {
		t.Fatalf("facts/root created files: %v %v", entries, err)
	}
}
