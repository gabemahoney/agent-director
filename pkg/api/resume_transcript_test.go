package api_test

// resume_transcript_test.go covers how resume finds the transcript it resumes
// (b.1ba, b.v2c, b.5jm): the persisted path, the CLAUDE_CONFIG_DIR-aware
// fallback, the visible history of the row's life, and the refusals naming the
// paths tried. It uses recordingResumeStore and helpers from resume_test.go.

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/gabemahoney/agent-director/internal/spawn"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// inRowLife is a history entry of the row's life (resumableLife).
func inRowLife(sessionID, jsonlPath string) apitest.SpawnOption {
	return apitest.WithSessionHistory(apitest.SessionHistorySeed{SessionID: sessionID, JSONLPath: jsonlPath, Life: resumableLife})
}

// transcriptPath is where resume computes sessionID's transcript for cwd, under
// cfgDir ("" = ~/.claude).
func transcriptPath(t *testing.T, cfgDir, cwd, sessionID string) string {
	t.Helper()
	p, err := spawn.JsonlPath(cwd, sessionID)
	if cfgDir != "" {
		p, err = spawn.JsonlPathIn(cfgDir, cwd, sessionID)
	}
	if err != nil {
		t.Fatalf("transcript path: %v", err)
	}
	return p
}

// assertAbsent fails the test unless p does not exist (a test's premise).
func assertAbsent(t *testing.T, p string) {
	t.Helper()
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatalf("premise: %q exists (stat err %v)", p, err)
	}
}

// triedPaths is the case of a transcript refusal naming the paths it tried.
func triedPaths(name string, paths ...string) apitest.DescCase {
	return apitest.DescCase{Name: name, Require: paths}
}

// TestResumeJsonlMissingReturnsErrJsonlMissing: with a rotted history entry and
// no current transcript, ErrJsonlMissing names the computed path (b.v2c AC2).
func TestResumeJsonlMissingReturnsErrJsonlMissing(t *testing.T) {
	t.Parallel()
	e, rs := newRecordingEnv(t)
	cwd := t.TempDir()
	id := seedBareResumeRow(t, e, cwd, "sess-1", inRowLife("prior-rotted", filepath.Join(t.TempDir(), "gone", "prior-rotted.jsonl")))
	before := e.columns(t, id)
	_, err := rs.resume(e, id)
	if !errors.Is(err, api.ErrJsonlMissing) {
		t.Fatalf("err = %v; want ErrJsonlMissing", err)
	}
	apitest.AssertDescription(t, err.Error(), triedPaths("ErrJsonlMissing, rotted history", "fallback "+transcriptPath(t, "", cwd, "sess-1")))
	assertNothingLaunched(t, e, rs, id, &before)
}

// TestResumeNeverWrittenReturnsErrJsonlNeverWritten: a NULL jsonl_path and an
// empty visible history give ErrJsonlNeverWritten, not ErrJsonlMissing (AC2).
func TestResumeNeverWrittenReturnsErrJsonlNeverWritten(t *testing.T) {
	t.Parallel()
	e, rs := newRecordingEnv(t)
	id := seedBareResumeRow(t, e, t.TempDir(), "sess-1")
	before := e.columns(t, id)
	_, err := rs.resume(e, id)
	if !errors.Is(err, api.ErrJsonlNeverWritten) || errors.Is(err, api.ErrJsonlMissing) {
		t.Fatalf("err = %v; want ErrJsonlNeverWritten only", err)
	}
	assertNothingLaunched(t, e, rs, id, &before)
}

// TestResumeRecoversRotatedSessionFromHistory: with no current transcript,
// resume relaunches the archived session whose recorded transcript exists (AC6).
func TestResumeRecoversRotatedSessionFromHistory(t *testing.T) {
	t.Parallel()
	e, rs := newRecordingEnv(t)
	cwd := t.TempDir()
	prior := apitest.SeedJsonlUnder(t, t.TempDir(), cwd, "prior-session")
	id := seedBareResumeRow(t, e, cwd, "sess-1", inRowLife("prior-session", prior))
	assertAbsent(t, transcriptPath(t, "", cwd, "sess-1"))
	snap := columnSnapshot(e.columns(t, id))
	if _, err := rs.resume(e, id); err != nil {
		t.Fatalf("Resume: %v; want recovery via session history", err)
	}
	assertResumed(t, e, rs, snap, "prior-session")
}

// TestResumeRecoversHistoryEntryWithEmptyPathViaConfigDir: an archived entry
// with a NULL path is recovered at its path recomputed under CLAUDE_CONFIG_DIR.
func TestResumeRecoversHistoryEntryWithEmptyPathViaConfigDir(t *testing.T) {
	t.Parallel()
	e, rs := newRecordingEnv(t)
	cwd, cfgDir := t.TempDir(), t.TempDir()
	id := seedBareResumeRow(t, e, cwd, "sess-1", inRowLife("prior-emptypath", ""),
		apitest.WithExtraEnv(map[string]string{"CLAUDE_CONFIG_DIR": cfgDir}))
	apitest.SeedJsonlUnder(t, cfgDir, cwd, "prior-emptypath")
	assertAbsent(t, transcriptPath(t, cfgDir, cwd, "sess-1"))
	snap := columnSnapshot(e.columns(t, id))
	if _, err := rs.resume(e, id); err != nil {
		t.Fatalf("Resume: %v; want recovery via the recomputed history path", err)
	}
	assertResumed(t, e, rs, snap, "prior-emptypath")
}

// TestResumeHistoryWalkFallsBackToRecomputedPathForNewerRottedEntry: a newer
// entry whose recorded path rotted but whose recomputed path exists wins (b.5jm/1).
func TestResumeHistoryWalkFallsBackToRecomputedPathForNewerRottedEntry(t *testing.T) {
	t.Parallel()
	e, rs := newRecordingEnv(t)
	cwd, cfgDir := t.TempDir(), t.TempDir()
	older := apitest.SeedJsonlUnder(t, t.TempDir(), cwd, "older-intact")
	apitest.SeedJsonlUnder(t, cfgDir, cwd, "newer-rotted")
	rotted := filepath.Join(t.TempDir(), "rotted", "newer-rotted.jsonl")
	// Seeded older first, so the newer entry reads as newer.
	id := seedBareResumeRow(t, e, cwd, "sess-1", inRowLife("older-intact", older), inRowLife("newer-rotted", rotted),
		apitest.WithExtraEnv(map[string]string{"CLAUDE_CONFIG_DIR": cfgDir}))
	assertAbsent(t, transcriptPath(t, cfgDir, cwd, "sess-1"))
	snap := columnSnapshot(e.columns(t, id))
	if _, err := rs.resume(e, id); err != nil {
		t.Fatalf("Resume: %v; want recovery via the newer entry's recomputed path", err)
	}
	assertResumed(t, e, rs, snap, "newer-rotted")
}

// TestResumeListSessionHistoryErrorPropagates: a failed history read is
// returned wrapped, with nothing launched.
func TestResumeListSessionHistoryErrorPropagates(t *testing.T) {
	t.Parallel()
	e, rs := newRecordingEnv(t)
	id := seedBareResumeRow(t, e, t.TempDir(), "sess-1")
	rs.historyErr = errors.New("history read boom")
	if _, err := rs.resume(e, id); !errors.Is(err, rs.historyErr) {
		t.Fatalf("err = %v; want wrapped %v", err, rs.historyErr)
	}
	assertNothingLaunched(t, e, rs, id, nil)
}

// TestResumeHistoryWalkReadsRowsOwnLife: with no current transcript, resume
// reads history for exactly the life of the row it read (Epic 6).
func TestResumeHistoryWalkReadsRowsOwnLife(t *testing.T) {
	t.Parallel()
	e, rs := newRecordingEnv(t)
	id := seedBareResumeRow(t, e, t.TempDir(), "sess-1")
	_, _ = rs.resume(e, id)
	if len(rs.historyLives) != 1 || rs.historyLives[0] != resumableLife {
		t.Fatalf("ListSessionHistory lives = %v; want [%d]", rs.historyLives, resumableLife)
	}
}

// TestResumePrefersPersistedJsonlPath: a persisted jsonl_path away from the
// computed location lets resume launch, so the persisted path was the one stat'd.
func TestResumePrefersPersistedJsonlPath(t *testing.T) {
	t.Parallel()
	e, rs := newRecordingEnv(t)
	cwd := t.TempDir()
	persisted := apitest.SeedJsonlUnder(t, t.TempDir(), cwd, "sess-1")
	id := seedBareResumeRow(t, e, cwd, "sess-1", apitest.WithJsonlPath(persisted))
	assertAbsent(t, transcriptPath(t, "", cwd, "sess-1"))
	snap := columnSnapshot(e.columns(t, id))
	if _, err := rs.resume(e, id); err != nil {
		t.Fatalf("Resume: %v; want proceed via persisted jsonl_path", err)
	}
	assertResumed(t, e, rs, snap, "sess-1")
}

// TestResumeFallbackSuccess (b.1ba): resume finds the transcript through the
// CLAUDE_CONFIG_DIR-aware fallback, or ~/.claude when that value is unusable.
func TestResumeFallbackSuccess(t *testing.T) {
	t.Parallel()
	cfgEnv := func(v string) func(string) map[string]string {
		return func(string) map[string]string { return map[string]string{"CLAUDE_CONFIG_DIR": v} }
	}
	toCfgDir := func(cfg string) map[string]string { return map[string]string{"CLAUDE_CONFIG_DIR": cfg} }
	cases := []struct {
		name         string
		extraEnv     func(cfgDir string) map[string]string // nil: no extra_env
		persistedRot bool                                  // a rotted persisted jsonl_path
		homeFallback bool                                  // transcript under ~/.claude, else under cfgDir
	}{
		{"legacy NULL row heals via CONFIG_DIR", toCfgDir, false, false},
		{"path rot heals via CONFIG_DIR", toCfgDir, true, false},
		{"persisted rot heals via default HOME", nil, true, true},
		{"legacy NULL row falls back to computed HOME path", nil, false, true},
		{"ExtraEnv without CONFIG_DIR key falls back to HOME", func(string) map[string]string {
			return map[string]string{"SOME_OTHER_KEY": "value"}
		}, false, true},
		{"empty-string CONFIG_DIR treated as absent", cfgEnv(""), false, true},
		{"whitespace-only CONFIG_DIR treated as absent", cfgEnv("   "), false, true},
		{"relative CONFIG_DIR treated as absent", cfgEnv("rel/dir"), false, true},
		{"tilde-prefixed CONFIG_DIR treated as absent", cfgEnv("~/cfg"), false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e, rs := newRecordingEnv(t)
			cwd, cfgDir := t.TempDir(), t.TempDir()
			var opts []apitest.SpawnOption
			if c.extraEnv != nil {
				opts = append(opts, apitest.WithExtraEnv(c.extraEnv(cfgDir)))
			}
			if c.persistedRot {
				opts = append(opts, apitest.WithJsonlPath(filepath.Join(t.TempDir(), "rotted", "gone.jsonl")))
			}
			id := seedBareResumeRow(t, e, cwd, "sess-1", opts...)
			if c.homeFallback {
				apitest.SeedJsonl(t, cwd, "sess-1")
			} else {
				apitest.SeedJsonlUnder(t, cfgDir, cwd, "sess-1")
				assertAbsent(t, transcriptPath(t, "", cwd, "sess-1"))
			}
			snap := columnSnapshot(e.columns(t, id))
			if _, err := rs.resume(e, id); err != nil {
				t.Fatalf("Resume: %v; want proceed via fallback", err)
			}
			assertResumed(t, e, rs, snap, "sess-1")
		})
	}
}

// TestResumeBothCandidatesAbsentReturnsErrJsonlMissing: with neither the
// persisted nor the fallback path present, the message names both with sources.
func TestResumeBothCandidatesAbsentReturnsErrJsonlMissing(t *testing.T) {
	t.Parallel()
	e, rs := newRecordingEnv(t)
	cwd, cfgDir := t.TempDir(), t.TempDir()
	persisted := filepath.Join(t.TempDir(), "custom", "gone.jsonl")
	id := seedBareResumeRow(t, e, cwd, "sess-1", apitest.WithJsonlPath(persisted),
		apitest.WithExtraEnv(map[string]string{"CLAUDE_CONFIG_DIR": cfgDir}))
	before := e.columns(t, id)
	_, err := rs.resume(e, id)
	if !errors.Is(err, api.ErrJsonlMissing) {
		t.Fatalf("err = %v; want ErrJsonlMissing", err)
	}
	apitest.AssertDescription(t, err.Error(), triedPaths("ErrJsonlMissing, both candidates",
		"persisted "+persisted, "fallback "+transcriptPath(t, cfgDir, cwd, "sess-1")))
	assertNothingLaunched(t, e, rs, id, &before)
}

// TestResumeNullPathBothAbsentReportsSingleFallback: a NULL jsonl_path with no
// transcript anywhere is ErrJsonlNeverWritten naming only the fallback.
func TestResumeNullPathBothAbsentReportsSingleFallback(t *testing.T) {
	t.Parallel()
	e, rs := newRecordingEnv(t)
	cwd, cfgDir := t.TempDir(), t.TempDir()
	id := seedBareResumeRow(t, e, cwd, "sess-1", apitest.WithExtraEnv(map[string]string{"CLAUDE_CONFIG_DIR": cfgDir}))
	_, err := rs.resume(e, id)
	if !errors.Is(err, api.ErrJsonlNeverWritten) {
		t.Fatalf("err = %v; want ErrJsonlNeverWritten", err)
	}
	c := triedPaths("ErrJsonlNeverWritten, fallback only", "fallback "+transcriptPath(t, cfgDir, cwd, "sess-1"))
	c.MustNot = []string{"persisted"}
	apitest.AssertDescription(t, err.Error(), c)
	assertNothingLaunched(t, e, rs, id, nil)
}
