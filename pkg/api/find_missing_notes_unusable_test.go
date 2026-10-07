package api_test

// find_missing_notes_unusable_test.go: find-missing's note for a row whose recorded tmux session name cannot be
// used (SR-3.2, SR-11.3, SR-11.4; AC-LKP-10, AC-LKP-15) on a real store: entry, overwrite and same note, with no
// tmux call. Each fixture's note with either evidence is the call table's (lookup_calltable_unusable_test.go); the
// same-life guard and store errors are the shared write tests' (find_missing_notes_test.go).

import (
	"reflect"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// fmuNamed records name as the row's tmux session name.
func fmuNamed(name string) fmRowOpt {
	return func(r *store.LiveSpawnIdentity) { r.TmuxSessionName = name }
}

// fmuReps is the first unusable-name fixture of each kind, in SR-3.2's order.
func fmuReps() []unusableNameFixture {
	var out []unusableNameFixture
	for _, tok := range unusableNameTokens() {
		for _, f := range unusableNameFixtures() {
			if f.kind == tok.kind {
				out = append(out, f)
				break
			}
		}
	}
	return out
}

// assertNoTmux fails unless rec saw no tmux call at all.
func assertNoTmux(t *testing.T, rec *tmuxfix.Recorder) {
	t.Helper()
	if len(rec.SocketCalls()) != 0 || len(rec.Calls()) != 0 {
		t.Errorf("tmux calls = %+v %+v; want none", rec.SocketCalls(), rec.Calls())
	}
}

// TestFindMissingUnusableNameNoteRealStore: on a real store, entry writes the note and its first unverified time
// with one tick; an overwrite keeps that time; the same note leaves the row untouched; none makes a tmux call.
func TestFindMissingUnusableNameNoteRealStore(t *testing.T) {
	// Serial: it checks the shared trail by literal row ids other find-missing tests reuse.
	const since = "2026-09-01 10:00:00"
	for _, f := range fmuReps() {
		cases := []struct {
			name  string
			opts  []apitest.SpawnOption
			delta int64
			tick  string
		}{
			{"entry from no note", nil, 1, f.note},
			{"overwrite of probe_eacces", []apitest.SpawnOption{apitest.WithLivenessNote("probe_eacces"),
				apitest.WithLivenessUnverifiedSince(since)}, 1, ""},
			{"same note", []apitest.SpawnOption{apitest.WithLivenessNote(f.note), apitest.WithLivenessUnverifiedSince(since)}, 0, ""},
		}
		for _, tc := range cases {
			t.Run(f.label+"/"+tc.name, func(t *testing.T) {
				s, dbPath := seedPaneRow(t, append([]apitest.SpawnOption{apitest.WithTmuxSessionName(f.raw)}, tc.opts...)...)
				was := readRow(t, dbPath)
				rec := tmuxfix.NewRecorder()
				before := trailLen(t)

				res := mustSweep(t, s, paneChecker(procfix.Unreadable()), fmSweep{tmux: rec})
				assertLists(t, res, nil, []string{"r"})
				now := readRow(t, dbPath)
				if now.LivenessNote != f.note || now.RowVersion.(int64)-was.RowVersion.(int64) != tc.delta {
					t.Errorf("note %v, row_version %v -> %v; want %s, +%d", now.LivenessNote, was.RowVersion, now.RowVersion, f.note, tc.delta)
				}
				switch {
				case tc.delta == 0 && !reflect.DeepEqual(now, was):
					t.Errorf("row changed: %+v -> %+v; want untouched", was, now)
				case tc.opts == nil && now.LivenessUnverifiedSince == nil:
					t.Errorf("liveness_unverified_since = NULL; want set on entry")
				case tc.opts != nil && now.LivenessUnverifiedSince != since:
					t.Errorf("liveness_unverified_since = %v; want the first stored %s", now.LivenessUnverifiedSince, since)
				}
				assertNoteTick(t, before, "r", tc.tick)
				assertNoTmux(t, rec)
			})
		}
	}
}
