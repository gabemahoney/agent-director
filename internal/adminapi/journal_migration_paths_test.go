package adminapi

import (
	"debug/buildinfo"
	"runtime/debug"
	"testing"
)

// The one module rule both verifiers use, the Linux scan's only build-metadata
// test. Q2 ruling (SRD t1.x9o.g1 `## Decisions`): a Go test binary, whose path
// ends in ".test", names no mutator wherever this module appears in it; only
// the suffix exempts.
func TestJournalBuildInfoNamesModule(t *testing.T) {
	t.Parallel()
	const caller = "example.com/caller"
	for _, tc := range []struct {
		name, path, main, dep string
		want                  bool
	}{
		{"path", journalModulePath + "/cmd/agent-director", "", "", true},
		{"main", caller, journalModulePath, "", true},
		{"dependency", caller, caller, journalModulePath, true},
		{"test-path", journalModulePath + "/internal/adminapi.test", "", "", false},
		{"test-main", caller + ".test", journalModulePath, "", false},
		{"test-dependency", caller + ".test", caller, journalModulePath, false},
		{"mid-string-test", "example.test/program", journalModulePath, "", true},
		{"none", "", "", "", false}, // Go 1.22's test binaries carry only the Go version
	} {
		t.Run(tc.name, func(t *testing.T) {
			bi := &buildinfo.BuildInfo{GoVersion: "go1.22.10", Path: tc.path, Main: debug.Module{Path: tc.main}}
			if tc.dep != "" {
				bi.Deps = []*debug.Module{{Path: tc.dep, Version: "v1.0.0"}}
			}
			if got := buildInfoNamesModule(bi); got != tc.want {
				t.Fatalf("buildInfoNamesModule = %v; want %v", got, tc.want)
			}
		})
	}
}
