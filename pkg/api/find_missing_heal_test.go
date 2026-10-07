package api_test

import (
	"slices"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// find-missing's provisional-transcript healing (b.v2c AC3, SR-11.7); fakes and runFindMissing live in
// find_missing_test.go.

// TestFindMissingHealsProvisionalTranscript: a provisional row (NULL jsonl_path) whose transcript has since
// appeared is healed with the path recomposed under a usable CLAUDE_CONFIG_DIR, else ~/.claude (b.v2c AC3/AC4,
// b.nje), also while the row is pending inside its grace period (not judged); one whose transcript is still absent
// stays provisional.
func TestFindMissingHealsProvisionalTranscript(t *testing.T) {
	// Serial: it sets HOME with t.Setenv.
	cases := []struct {
		name      string
		configDir string
		absolute  bool // configDir is a per-test absolute directory
		absent    bool // the transcript has not appeared
		pending   bool // the row is pending inside its grace period
	}{
		{name: "no CLAUDE_CONFIG_DIR"},
		{name: "absolute CLAUDE_CONFIG_DIR", absolute: true},
		{name: "relative CLAUDE_CONFIG_DIR falls back to ~/.claude", configDir: "rel"},
		{name: "pending inside grace", pending: true},
		{name: "transcript still absent", absent: true},
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
			var want []healCall
			switch {
			case tc.absent:
			case tc.absolute:
				cfg = t.TempDir()
				want = []healCall{{id, session, apitest.SeedJsonlUnder(t, cfg, cwd, session)}}
			default:
				want = []healCall{{id, session, apitest.SeedJsonl(t, cwd, session)}}
			}
			pc := procfix.New()
			pc.Set(11, procfix.Alive(fmStart))
			r := liveRow(id, withSessionStart(11, fmStart))
			if tc.pending {
				r = liveRow(id, withLaunch(store.StatePending, fmNow.Add(-fmGrace+time.Second).UnixMilli()), withPane(10, fmStart))
			}
			st := &fakeFindMissingStore{
				rows:        []store.LiveSpawnIdentity{r},
				provisional: []store.ProvisionalTranscript{{ClaudeInstanceID: id, ClaudeSessionID: session, CWD: cwd, ConfigDir: cfg}},
			}

			res := mustFindMissing(t, st, pc)
			assertLists(t, res, nil, nil)
			if !slices.Equal(st.healed, want) || len(st.calls) != 0 {
				t.Errorf("heal calls = %+v, writes %+v; want %+v and no write", st.healed, st.calls, want)
			}
		})
	}
}
