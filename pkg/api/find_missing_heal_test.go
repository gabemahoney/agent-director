package api_test

import (
	"slices"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// find-missing's provisional-transcript healing (b.v2c AC3); fakes and runFindMissing live in find_missing_test.go.
// TestFindMissingHealsProvisionalTranscript: a provisional row (NULL jsonl_path) whose transcript has since
// appeared is healed with the path recomposed under a usable CLAUDE_CONFIG_DIR, else ~/.claude (b.v2c AC3/AC4, b.nje).
func TestFindMissingHealsProvisionalTranscript(t *testing.T) {
	// Serial: it sets HOME with t.Setenv.
	cases := []struct {
		name      string
		configDir string
		absolute  bool // configDir is a per-test absolute directory
	}{
		{"no CLAUDE_CONFIG_DIR", "", false},
		{"absolute CLAUDE_CONFIG_DIR", "", true},
		{"relative CLAUDE_CONFIG_DIR falls back to ~/.claude", "rel", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			const (
				id      = "prov-heal-1"
				session = "session-heal-uuid"
				cwd     = "/tmp/proj"
			)
			cfg := tc.configDir
			var appeared string
			if tc.absolute {
				cfg = t.TempDir()
				appeared = apitest.SeedJsonlUnder(t, cfg, cwd, session)
			} else {
				appeared = apitest.SeedJsonl(t, cwd, session)
			}
			pc := procfix.New()
			pc.Set(11, procfix.Alive(fmStart))
			pt := store.ProvisionalTranscript{ClaudeInstanceID: id, ClaudeSessionID: session, CWD: cwd, ConfigDir: cfg}
			st := &fakeFindMissingStore{
				rows:        []store.LiveSpawnIdentity{liveRow(id, withSessionStart(11, fmStart))},
				provisional: []store.ProvisionalTranscript{pt},
			}

			mustFindMissing(t, st, pc)
			want := []healCall{{id, session, appeared}}
			if !slices.Equal(st.healed, want) {
				t.Errorf("heal calls = %+v; want %+v", st.healed, want)
			}
		})
	}
}

// TestFindMissingSkipsProvisionalWhenTranscriptStillAbsent: a provisional row whose transcript has not appeared
// stays provisional.
func TestFindMissingSkipsProvisionalWhenTranscriptStillAbsent(t *testing.T) {
	// Serial: it sets HOME with t.Setenv.
	t.Setenv("HOME", t.TempDir())
	st := &fakeFindMissingStore{provisional: []store.ProvisionalTranscript{
		{ClaudeInstanceID: "prov-absent-1", ClaudeSessionID: "no-file-uuid", CWD: "/tmp/proj"},
	}}

	mustFindMissing(t, st, procfix.New())
	if len(st.healed) != 0 {
		t.Errorf("HealJsonlPath called %d times; want 0 (transcript still absent)", len(st.healed))
	}
}
