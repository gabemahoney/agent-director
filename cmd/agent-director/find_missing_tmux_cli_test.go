package main_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/faketmuxfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// Identity-less variants of the SR-20.6 find-missing CLI tests: the rows
// record no process evidence, so the built CLI's one lookup on
// apitest.TestSocket, answered by test/fake-tmux, decides them (SR-11.3).
// Each variant runs under both answers in tmuxAnswers: Gone marks every row
// tmux_absent; tmux unavailable (the control) marks nothing and notes every
// row process_not_seen_tmux_unchecked.

// The identity-less rows every variant seeds; ids sort in this order.
const (
	tmuxOrphanID  = "id-fm-tmux-a-orphan"  // check_permission, open request, no identity
	tmuxPendingID = "id-fm-tmux-b-pending" // pending past grace, lost create reply
	tmuxWorkingID = "id-fm-tmux-c-working" // working, no token, pane or process
)

// tmuxRowIDs is every seeded row's id, sorted.
var tmuxRowIDs = []string{tmuxOrphanID, tmuxPendingID, tmuxWorkingID}

// noteTmuxUnchecked is the note a row with no evidence gets when its lookup
// did not answer (SR-11.3).
const noteTmuxUnchecked = "process_not_seen_tmux_unchecked"

// tmuxAnswer is how the fake answers the lookup on apitest.TestSocket.
type tmuxAnswer struct {
	name   string
	inject []faketmuxfix.Injection
	gone   bool // Gone (every row marked); else tmux unavailable (none marked)
}

// tmuxAnswers is each variant's table: Gone (an empty table: no server) and
// the tmux-unavailable control (the socket's permission-denied reply).
func tmuxAnswers() []tmuxAnswer {
	return []tmuxAnswer{
		{name: "gone", gone: true},
		{name: "tmux unavailable", inject: []faketmuxfix.Injection{
			faketmuxfix.Reply(tmux.CallLookup, tmuxfix.SocketDenied(apitest.TestSocket))}},
	}
}

// tmuxSweep is one identity-less find-missing run's outcome.
type tmuxSweep struct {
	home, dbPath string
	res          findMissingResult
	stdout       string
	token        string // the orphan row's open request token
}

// runTmuxSweep seeds the identity-less rows under a fresh HOME, runs the
// built find-missing with the fake tmux first on PATH answering a, requires
// exit 0 and returns the outcome.
func runTmuxSweep(t *testing.T, a tmuxAnswer) tmuxSweep {
	t.Helper()
	home, dbPath := findMissingHome(t)
	tables := faketmuxfix.Tables{Dir: t.TempDir()}
	if len(a.inject) > 0 {
		tables.Inject(t, apitest.TestSocket, a.inject...)
	}

	noIdentity := recordProcess(nil, nil) // TestSocket only: no token, pane or process
	seedRow(t, dbPath, tmuxOrphanID, store.StateCheckPermission, "on", noIdentity...)
	req, err := apitest.SeedPermissionRequest(dbPath, tmuxOrphanID, "Bash")
	if err != nil {
		t.Fatalf("seed permission request: %v", err)
	}
	pastGrace := time.Now().Add(-2 * time.Duration(config.DefaultPendingGraceSeconds) * time.Second)
	seedRow(t, dbPath, tmuxPendingID, store.StatePending, "off",
		apitest.WithNoPane(), apitest.WithLaunchStartedAt(pastGrace.UnixMilli()))
	seedRow(t, dbPath, tmuxWorkingID, store.StateWorking, "off", noIdentity...)

	stdout, stderr, code := runSpawnCLIEnv(t, home, faketmuxfix.Dir(t),
		map[string]string{faketmuxfix.EnvTables: tables.Dir}, "find-missing")
	if code != 0 {
		t.Fatalf("find-missing exit = %d; want 0\nstderr=%s", code, stderr)
	}
	return tmuxSweep{home: home, dbPath: dbPath, res: parseFindMissingResult(t, stdout),
		stdout: stdout, token: req.RequestToken}
}

// sorted returns a sorted copy of ids.
func sorted(ids []string) []string {
	out := slices.Clone(ids)
	slices.Sort(out)
	return out
}

// TestFindMissingTmuxCLIPostRebootShape: Gone marks every identity-less row
// with unverified 0; tmux unavailable marks none and notes every row.
func TestFindMissingTmuxCLIPostRebootShape(t *testing.T) {
	for _, a := range tmuxAnswers() {
		t.Run(a.name, func(t *testing.T) {
			sw := runTmuxSweep(t, a)
			assertInvocationKinds(t, sw.home, "list-sessions") // one lookup, one socket

			for _, key := range []string{`"unverified":`, `"unverified_ids":`} {
				if !strings.Contains(sw.stdout, key) {
					t.Errorf("stdout missing %s key: %s", key, sw.stdout)
				}
			}
			if a.gone {
				if sw.res.Count != len(tmuxRowIDs) || !slices.Equal(sorted(sw.res.IDs), tmuxRowIDs) {
					t.Errorf("count = %d, ids = %v; want %d, %v", sw.res.Count, sw.res.IDs, len(tmuxRowIDs), tmuxRowIDs)
				}
				if sw.res.Unverified != 0 || sw.res.UnverifiedIDs == nil || len(sw.res.UnverifiedIDs) != 0 {
					t.Errorf("unverified = %d, unverified_ids = %v; want 0, []", sw.res.Unverified, sw.res.UnverifiedIDs)
				}
				for _, id := range tmuxRowIDs {
					if got := readSpawnState(t, sw.dbPath, id); got != "missing" {
						t.Errorf("row %s state = %q; want missing", id, got)
					}
				}
				return
			}
			if sw.res.Count != 0 || sw.res.IDs == nil || len(sw.res.IDs) != 0 {
				t.Errorf("count = %d, ids = %v; want 0, []", sw.res.Count, sw.res.IDs)
			}
			if sw.res.Unverified != len(tmuxRowIDs) || !slices.Equal(sorted(sw.res.UnverifiedIDs), tmuxRowIDs) {
				t.Errorf("unverified = %d, unverified_ids = %v; want %d, %v",
					sw.res.Unverified, sw.res.UnverifiedIDs, len(tmuxRowIDs), tmuxRowIDs)
			}
			wantState := map[string]string{
				tmuxOrphanID: "check_permission", tmuxPendingID: "pending", tmuxWorkingID: "working"}
			for _, id := range tmuxRowIDs {
				cols := rowColumns(t, sw.home, id)
				if cols.State != wantState[id] || cols.LivenessNote != noteTmuxUnchecked {
					t.Errorf("row %s state = %v, liveness_note = %v; want %s, %s",
						id, cols.State, cols.LivenessNote, wantState[id], noteTmuxUnchecked)
				}
			}
		})
	}
}

// TestFindMissingTmuxTrailEmitsTmuxAbsentTick: Gone gives each row one
// tmux_absent tick carrying lookup_outcome gone; unavailable ticks the note.
func TestFindMissingTmuxTrailEmitsTmuxAbsentTick(t *testing.T) {
	gone := tmux.Result{Verdict: tmux.Gone}.Token()
	for _, a := range tmuxAnswers() {
		t.Run(a.name, func(t *testing.T) {
			sw := runTmuxSweep(t, a)
			want := map[string]any{"reconciliation_reason": noteTmuxUnchecked, "new_state": nil}
			if a.gone {
				want = map[string]any{"reconciliation_reason": "tmux_absent", "new_state": "missing",
					"lookup_outcome": gone}
			}
			want["source"] = "ad_find_missing"

			byRow := map[any][]map[string]any{}
			for _, tk := range findMissingTicks(t, sw.home) {
				if tk["reconciliation_reason"] != "permission_orphan_closeout" {
					byRow[tk["claude_instance_id"]] = append(byRow[tk["claude_instance_id"]], tk)
				}
			}
			if len(byRow) != len(tmuxRowIDs) {
				t.Errorf("ticked rows = %d; want %d: %v", len(byRow), len(tmuxRowIDs), byRow)
			}
			for _, id := range tmuxRowIDs {
				ticks := byRow[id]
				if len(ticks) != 1 {
					t.Errorf("row %s ticks = %v; want exactly one", id, ticks)
					continue
				}
				for k, v := range want {
					if got, ok := ticks[0][k]; !ok || got != v {
						t.Errorf("row %s tick %s = %v (present %v); want %v", id, k, got, ok, v)
					}
				}
			}
		})
	}
}

// TestFindMissingTmuxTrailEmitsPermissionOrphanCloseoutTick: after Gone's
// tmux_absent tick the orphan's request is closed out; unavailable closes none.
func TestFindMissingTmuxTrailEmitsPermissionOrphanCloseoutTick(t *testing.T) {
	for _, a := range tmuxAnswers() {
		t.Run(a.name, func(t *testing.T) {
			sw := runTmuxSweep(t, a)
			absentAt, closeoutAt := -1, -1
			var closeout map[string]any
			for i, tk := range findMissingTicks(t, sw.home) {
				if tk["claude_instance_id"] != tmuxOrphanID {
					continue
				}
				switch tk["reconciliation_reason"] {
				case "tmux_absent":
					absentAt = i
				case "permission_orphan_closeout":
					closeoutAt, closeout = i, tk
				}
			}
			if !a.gone {
				if closeoutAt != -1 {
					t.Errorf("closeout tick %v; want none", closeout)
				}
				return
			}
			if absentAt == -1 || closeoutAt <= absentAt {
				t.Fatalf("tmux_absent tick at %d, closeout tick at %d; want both, closeout after", absentAt, closeoutAt)
			}
			if closeout["request_token"] != sw.token || closeout["source"] != "ad_find_missing" {
				t.Errorf("closeout request_token = %v, source = %v; want %q, ad_find_missing",
					closeout["request_token"], closeout["source"], sw.token)
			}
		})
	}
}

// TestFindMissingTmuxTrailEmitsRowMutation: closing out the Gone-marked
// orphan's request writes one find_missing update line; unavailable writes none.
func TestFindMissingTmuxTrailEmitsRowMutation(t *testing.T) {
	for _, a := range tmuxAnswers() {
		t.Run(a.name, func(t *testing.T) {
			sw := runTmuxSweep(t, a)
			rm := rowMutationCommittedLines(trailLinesOrNil(t, trailDir(sw.home)))
			if !a.gone {
				if len(rm) != 0 {
					t.Errorf("ad.row_mutation.committed lines = %v; want none", rm)
				}
				return
			}
			if len(rm) != 1 {
				t.Fatalf("ad.row_mutation.committed line count = %d; want 1", len(rm))
			}
			if rm[0]["writer_process"] != "find_missing" || rm[0]["mutation_kind"] != "update" {
				t.Errorf("writer_process = %v, mutation_kind = %v; want find_missing, update",
					rm[0]["writer_process"], rm[0]["mutation_kind"])
			}
		})
	}
}
