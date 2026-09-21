package api_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/probe"
	"github.com/gabemahoney/agent-director/internal/spawn"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api"
)

// fakeFindMissingStore is the narrow store the verb sees.
//
// Two read shapes are supported so the same fake serves both the verdict-engine
// tests (this file) and the sibling trail tests (find_missing_trail_test.go):
//
//   - liveIDs: id-only rows. Each is wrapped as a LiveSpawnIdentity with a
//     zero pid and empty proc_starttime (a partial/absent identity), so the
//     verb routes them through the SR-7.5 environ probe-set fallback exactly as
//     the pre-verdict-engine sweep behaved. The trail tests and the legacy
//     fallback tests depend on this shape.
//   - liveRows: full LiveSpawnIdentity rows (pid + proc_starttime set). These
//     carry a recorded identity, so the verb asks the liveness checker for a
//     per-row verdict. Verdict-engine tests use this shape.
//
// When both are set, liveRows are returned first, then the liveIDs wrappers.
//
// The fake records every store call the verb makes so tests can pin the marking
// order adversarially:
//   - marked: ordered MarkSpawnMissing ids.
//   - clearedIDs: ordered ClearLivenessUnverified ids.
//   - closedIDs: ordered CloseOrphanedPermissionRequests ids.
//   - setUnverified: ordered (id, note) pairs from SetLivenessUnverified.
//   - callSeq: the interleaved call log across all four methods, so a single
//     recorded row's Mark→Clear→Close ordering can be asserted directly.
//
// Programmable knobs:
//   - listErr / markErr: force the corresponding call to fail.
//   - markPrior: overrides the prior-state MarkSpawnMissing returns per id
//     (default "working"); the empty string models an already-terminal/absent
//     row (no write happened → no clear/tick/close).
//   - setTransitioned: the transitioned bool SetLivenessUnverified returns per
//     id (default true, i.e. a fresh NULL→set transition).
//   - setErr / clearErr / closeErr: force those calls to fail per invocation.
type fakeFindMissingStore struct {
	liveIDs  []string
	liveRows []store.LiveSpawnIdentity

	marked        []string
	clearedIDs    []string
	closedIDs     []string
	setUnverified []unverifiedCall
	callSeq       []storeCall

	listErr error
	markErr error
	setErr  error

	markPrior       map[string]string
	setTransitioned map[string]bool

	// b.v2c AC3 lazy-heal knobs.
	provisional []store.ProvisionalTranscript
	healed      []healCall
}

// healCall records one HealJsonlPath(instanceID, sessionID, jsonlPath) call.
type healCall struct {
	id, sessionID, jsonlPath string
}

// unverifiedCall records one SetLivenessUnverified(id, note) invocation.
type unverifiedCall struct {
	id   string
	note string
}

// storeCall is one entry in the interleaved call log. op is the method name
// ("mark", "clear", "close", "set"); id is the instance id it acted on.
type storeCall struct {
	op string
	id string
}

func (f *fakeFindMissingStore) ListLiveSpawnIdentities() ([]store.LiveSpawnIdentity, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := make([]store.LiveSpawnIdentity, 0, len(f.liveRows)+len(f.liveIDs))
	out = append(out, f.liveRows...)
	for _, id := range f.liveIDs {
		out = append(out, store.LiveSpawnIdentity{ClaudeInstanceID: id})
	}
	return out, nil
}

func (f *fakeFindMissingStore) MarkSpawnMissing(id string) (string, error) {
	if f.markErr != nil {
		return "", f.markErr
	}
	f.marked = append(f.marked, id)
	f.callSeq = append(f.callSeq, storeCall{op: "mark", id: id})
	// prior state: programmable per id; default "working" (a live row that was
	// actually written). "" models an already-terminal/absent row.
	if f.markPrior != nil {
		if p, ok := f.markPrior[id]; ok {
			return p, nil
		}
	}
	return "working", nil
}

func (f *fakeFindMissingStore) SetLivenessUnverified(id, note string) (bool, error) {
	if f.setErr != nil {
		return false, f.setErr
	}
	f.setUnverified = append(f.setUnverified, unverifiedCall{id: id, note: note})
	f.callSeq = append(f.callSeq, storeCall{op: "set", id: id})
	if f.setTransitioned != nil {
		if t, ok := f.setTransitioned[id]; ok {
			return t, nil
		}
	}
	return true, nil
}

func (f *fakeFindMissingStore) ClearLivenessUnverified(id string) error {
	f.clearedIDs = append(f.clearedIDs, id)
	f.callSeq = append(f.callSeq, storeCall{op: "clear", id: id})
	return nil
}

func (f *fakeFindMissingStore) CloseOrphanedPermissionRequests(id string) error {
	f.closedIDs = append(f.closedIDs, id)
	f.callSeq = append(f.callSeq, storeCall{op: "close", id: id})
	return nil
}

// b.v2c AC3 lazy-heal surface, programmable via the provisional/healed fields.
func (f *fakeFindMissingStore) ListProvisionalTranscripts() ([]store.ProvisionalTranscript, error) {
	return f.provisional, nil
}
func (f *fakeFindMissingStore) HealJsonlPath(id, sessionID, jsonlPath string) (bool, error) {
	f.healed = append(f.healed, healCall{id, sessionID, jsonlPath})
	return true, nil
}

// fakeChecker is the SR-12.3 verdict-level liveness checker. It is programmable
// per instance id (verdicts["id"] → the verdict to return) and records every
// row it was queried about, so a test can prove BOTH the verdict routing AND
// that a partial-identity row never reached the checker (fallback path).
//
// The default (an id absent from verdicts) is VerdictUnknown — the fail-open
// value — but every verdict-engine test sets an explicit verdict per queried
// row, so the default only ever applies to rows the test did not intend to
// query (which would itself be a bug the recording catches).
type fakeChecker struct {
	verdicts    map[string]probe.LivenessVerdict
	queriedIDs  []string
	queriedPIDs map[string]int
	queriedST   map[string]string
}

func newFakeChecker() *fakeChecker {
	return &fakeChecker{
		verdicts:    map[string]probe.LivenessVerdict{},
		queriedPIDs: map[string]int{},
		queriedST:   map[string]string{},
	}
}

func (c *fakeChecker) CheckLiveness(pid int, storedProcStartTime, instanceID string) probe.LivenessVerdict {
	c.queriedIDs = append(c.queriedIDs, instanceID)
	c.queriedPIDs[instanceID] = pid
	c.queriedST[instanceID] = storedProcStartTime
	if v, ok := c.verdicts[instanceID]; ok {
		return v
	}
	return probe.VerdictUnknown
}

func (c *fakeChecker) queried(id string) bool {
	for _, q := range c.queriedIDs {
		if q == id {
			return true
		}
	}
	return false
}

// fakeProber returns a fixed set; nil err.
type fakeProber struct {
	set map[string]struct{}
	err error
}

func (f *fakeProber) Probe(_ context.Context) (map[string]struct{}, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.set, nil
}

// recordingLogger captures Printf invocations so per-row error tests can assert
// the continue-and-log behavior.
type recordingLogger struct {
	lines []string
}

func (r *recordingLogger) Printf(format string, v ...any) {
	r.lines = append(r.lines, formatLine(format, v...))
}

func formatLine(format string, v ...any) string {
	var sb strings.Builder
	// minimal sprintf to avoid importing fmt-as-test-helper sprawl
	// — passing through fmt.Sprintf would be fine, but inlining keeps
	// the helper local and small.
	for i, c := range format {
		if c == '%' && i+1 < len(format) && len(v) > 0 {
			// just print the verb's underlying string form
			sb.WriteString(toString(v[0]))
			v = v[1:]
			// consume the next char (verb char like 's','d','v')
			// — assumes single-byte verbs which is all the test
			// uses
			continue
		}
		// skip the verb char emitted above
		if i > 0 && format[i-1] == '%' && c >= 'a' && c <= 'z' {
			continue
		}
		sb.WriteRune(c)
	}
	return sb.String()
}

func toString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case int:
		return intToStr(t)
	default:
		return "?"
	}
}

func intToStr(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var digits [20]byte
	i := len(digits)
	for n > 0 {
		i--
		digits[i] = byte('0' + n%10)
		n /= 10
	}
	out := string(digits[i:])
	if neg {
		out = "-" + out
	}
	return out
}

// row is a small constructor for a full-identity LiveSpawnIdentity.
func row(id string, pid int, starttime string) store.LiveSpawnIdentity {
	return store.LiveSpawnIdentity{ClaudeInstanceID: id, PID: pid, ProcStarttime: starttime}
}

// ---------------------------------------------------------------------------
// Fallback-path tests (id-only rows, no recorded identity) — the legacy
// behavior, re-verified under the new verdict-engine arity. Rows without a
// recorded identity NEVER reach the checker: they route straight to the
// environ probe-set diff. The fake checker records prove no queries fire.
// ---------------------------------------------------------------------------

// TestFindMissingHealsProvisionalTranscript is the b.v2c AC3/AC4 REGRESSION
// test for bug mode (a): a session that started before its first user turn has a
// NULL jsonl_path (a provisional row). Once the transcript appears on disk, a
// find-missing sweep recomposes the path, stats it, and records it — no operator
// intervention. This exercises the lazy-heal that finishes the idle-session
// story: the row started NOT asserting a dead pointer, and heals when messaged.
//
// PRE-FIX find-missing did no transcript healing, so HealJsonlPath is never
// called; POST-FIX the sweep records the now-present recomposed path.
func TestFindMissingHealsProvisionalTranscript(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	const (
		id      = "prov-heal-1"
		session = "session-heal-uuid"
		cwd     = "/tmp/proj"
	)
	// The transcript has appeared at the CLAUDE_CONFIG_DIR-free (~/.claude) slug
	// location — plant it there so the sweep's Stat succeeds.
	appeared, err := spawn.JsonlPath(cwd, session)
	if err != nil {
		t.Fatalf("compute path: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(appeared), 0o700); err != nil {
		t.Fatalf("mkdir transcript parent: %v", err)
	}
	if err := os.WriteFile(appeared, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	st := &fakeFindMissingStore{
		liveIDs: []string{id}, // keep the row alive so the sweep completes cleanly
		provisional: []store.ProvisionalTranscript{
			{ClaudeInstanceID: id, ClaudeSessionID: session, CWD: cwd},
		},
	}
	prober := &fakeProber{set: map[string]struct{}{id: {}}}
	if _, err := api.FindMissing(context.Background(), st, prober, newFakeChecker(), &recordingLogger{}); err != nil {
		t.Fatalf("FindMissing: %v", err)
	}

	if len(st.healed) != 1 {
		t.Fatalf("HealJsonlPath called %d times; want 1 (transcript appeared → row healed)", len(st.healed))
	}
	got := st.healed[0]
	if got.id != id || got.sessionID != session || got.jsonlPath != appeared {
		t.Errorf("heal call = %+v; want {%s %s %s}", got, id, session, appeared)
	}
}

// TestFindMissingSkipsProvisionalWhenTranscriptStillAbsent pins the guard: a
// provisional row whose transcript has NOT appeared is left untouched — the row
// stays provisional rather than being healed to a still-dead path.
func TestFindMissingSkipsProvisionalWhenTranscriptStillAbsent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	st := &fakeFindMissingStore{
		liveIDs: []string{"prov-absent-1"},
		provisional: []store.ProvisionalTranscript{
			{ClaudeInstanceID: "prov-absent-1", ClaudeSessionID: "no-file-uuid", CWD: "/tmp/proj"},
		},
	}
	prober := &fakeProber{set: map[string]struct{}{"prov-absent-1": {}}}
	if _, err := api.FindMissing(context.Background(), st, prober, newFakeChecker(), &recordingLogger{}); err != nil {
		t.Fatalf("FindMissing: %v", err)
	}
	if len(st.healed) != 0 {
		t.Errorf("HealJsonlPath called %d times; want 0 (transcript still absent)", len(st.healed))
	}
}

func TestFindMissingNoChangesWhenAllAlive(t *testing.T) {
	st := &fakeFindMissingStore{liveIDs: []string{"a", "b"}}
	prober := &fakeProber{set: map[string]struct{}{"a": {}, "b": {}}}
	chk := newFakeChecker()
	res, err := api.FindMissing(context.Background(), st, prober, chk, &recordingLogger{})
	if err != nil {
		t.Fatalf("FindMissing: %v", err)
	}
	if res.Count != 0 || len(res.IDs) != 0 {
		t.Errorf("res = %+v; want count=0 ids=[]", res)
	}
	if len(st.marked) != 0 {
		t.Errorf("MarkSpawnMissing called: %v", st.marked)
	}
	if len(chk.queriedIDs) != 0 {
		t.Errorf("checker queried for id-only rows: %v; want none (fallback path)", chk.queriedIDs)
	}
}

func TestFindMissingTransitionsUnprobeableRows(t *testing.T) {
	st := &fakeFindMissingStore{liveIDs: []string{"a", "b", "c"}}
	prober := &fakeProber{set: map[string]struct{}{"b": {}}}
	res, err := api.FindMissing(context.Background(), st, prober, newFakeChecker(), &recordingLogger{})
	if err != nil {
		t.Fatalf("FindMissing: %v", err)
	}
	// a and c are missing from the probe; they should both be marked.
	if res.Count != 2 {
		t.Errorf("count = %d; want 2", res.Count)
	}
	want := []string{"a", "c"}
	if got := res.IDs; !equalStrings(got, want) {
		t.Errorf("ids = %v; want %v (sorted)", got, want)
	}
	if !equalStrings(st.marked, want) {
		t.Errorf("MarkSpawnMissing calls = %v; want %v", st.marked, want)
	}
}

// TestFindMissingNullPidFallbackGuardFree pins the post-reboot shape (SR-7.5,
// success criterion of t2.93m.hp.3p): an EMPTY probe set plus live NULL-identity
// rows must mark ALL of them — no degraded-mode refusal, no error, count>0.
// This is the exact scenario the deleted degraded-mode guard used to REFUSE.
func TestFindMissingNullPidFallbackGuardFree(t *testing.T) {
	st := &fakeFindMissingStore{liveIDs: []string{"a", "b", "c"}}
	prober := &fakeProber{set: map[string]struct{}{}}
	lg := &recordingLogger{}

	res, err := api.FindMissing(context.Background(), st, prober, newFakeChecker(), lg)
	if err != nil {
		t.Fatalf("FindMissing: %v (post-reboot shape must not error)", err)
	}
	if res.Count != 3 {
		t.Errorf("count = %d; want 3 (all NULL-identity rows marked)", res.Count)
	}
	want := []string{"a", "b", "c"}
	if !equalStrings(res.IDs, want) {
		t.Errorf("ids = %v; want %v", res.IDs, want)
	}
	if !equalStrings(st.marked, want) {
		t.Errorf("marked = %v; want %v (no refusal)", st.marked, want)
	}
}

func TestFindMissingZeroLiveRowsZeroProbeIsNoopSuccess(t *testing.T) {
	// Legitimate empty case: no live rows + no live processes. With the
	// degraded-mode guard removed, a 0/0 state is simply a no-op: no probe
	// happens (no fallback rows), no marks, no error.
	st := &fakeFindMissingStore{}
	prober := &fakeProber{set: map[string]struct{}{}}
	lg := &recordingLogger{}

	res, err := api.FindMissing(context.Background(), st, prober, newFakeChecker(), lg)
	if err != nil {
		t.Fatalf("FindMissing: %v", err)
	}
	if res.Count != 0 {
		t.Errorf("count = %d; want 0", res.Count)
	}
	if len(lg.lines) != 0 {
		t.Errorf("0/0 case emitted a warning: %v", lg.lines)
	}
}

func TestFindMissingPendingRowIsScanned(t *testing.T) {
	// Per SRD §5.2: a `pending` row whose tmux session vanished before
	// SessionStart fired must reconcile to `missing` on the next sweep. A
	// pending row has no recorded identity yet (pid/starttime NULL), so it
	// rides the fallback path. ListLiveSpawnIdentities includes `pending` in
	// its IN-list; this test pins the downstream effect.
	st := &fakeFindMissingStore{liveIDs: []string{"p-1"}}
	prober := &fakeProber{set: map[string]struct{}{"other": {}}}
	res, err := api.FindMissing(context.Background(), st, prober, newFakeChecker(), &recordingLogger{})
	if err != nil {
		t.Fatalf("FindMissing: %v", err)
	}
	if res.Count != 1 || res.IDs[0] != "p-1" {
		t.Errorf("res = %+v; want count=1 ids=[p-1]", res)
	}
}

func TestFindMissingResultIDsSorted(t *testing.T) {
	// The result envelope must be deterministic. Feed an unsorted live-id
	// list and assert the result IDs come back sorted. No probe entry covers
	// them, so all three are marked via the fallback path.
	st := &fakeFindMissingStore{liveIDs: []string{"z", "a", "m"}}
	prober := &fakeProber{set: map[string]struct{}{}}

	res, err := api.FindMissing(context.Background(), st, prober, newFakeChecker(), &recordingLogger{})
	if err != nil {
		t.Fatalf("FindMissing: %v", err)
	}
	want := []string{"a", "m", "z"}
	if got := res.IDs; !equalStrings(got, want) {
		t.Errorf("ids = %v; want %v (sorted)", got, want)
	}
}

// TestFindMissingUnverifiedIDsNeverNull mirrors the sorted-ids discipline for
// the new UnverifiedIDs slice: it must be non-nil (encode as []) even when no
// row is unverified, and sorted when populated.
func TestFindMissingUnverifiedIDsNeverNull(t *testing.T) {
	// No unverified rows: slice must still be non-nil.
	st := &fakeFindMissingStore{liveIDs: []string{"a"}}
	prober := &fakeProber{set: map[string]struct{}{"a": {}}}
	res, err := api.FindMissing(context.Background(), st, prober, newFakeChecker(), &recordingLogger{})
	if err != nil {
		t.Fatalf("FindMissing: %v", err)
	}
	if res.UnverifiedIDs == nil {
		t.Errorf("UnverifiedIDs = nil; want non-nil ([]) empty slice")
	}
	if res.Unverified != 0 {
		t.Errorf("Unverified = %d; want 0", res.Unverified)
	}

	// Multiple unverified rows fed unsorted: slice must come back sorted.
	chk := newFakeChecker()
	chk.verdicts["z"] = probe.VerdictUnknown
	chk.verdicts["a"] = probe.VerdictUnknown
	chk.verdicts["m"] = probe.VerdictUnknown
	st2 := &fakeFindMissingStore{liveRows: []store.LiveSpawnIdentity{
		row("z", 10, "100"), row("a", 11, "101"), row("m", 12, "102"),
	}}
	res2, err := api.FindMissing(context.Background(), st2, &fakeProber{}, chk, &recordingLogger{})
	if err != nil {
		t.Fatalf("FindMissing: %v", err)
	}
	wantU := []string{"a", "m", "z"}
	if !equalStrings(res2.UnverifiedIDs, wantU) {
		t.Errorf("UnverifiedIDs = %v; want %v (sorted)", res2.UnverifiedIDs, wantU)
	}
	if res2.Unverified != 3 {
		t.Errorf("Unverified = %d; want 3", res2.Unverified)
	}
}

// ---------------------------------------------------------------------------
// Verdict-engine disposition tests (full-identity rows → checker seam).
// Named per the SR-7.3/7.4 disposition table.
// ---------------------------------------------------------------------------

// TestFindMissingCheckerDeadMarks: a full-identity row the checker calls
// provably-dead is marked missing — MarkSpawnMissing, ClearLivenessUnverified,
// and CloseOrphanedPermissionRequests all fire for that row.
func TestFindMissingCheckerDeadMarks(t *testing.T) {
	chk := newFakeChecker()
	chk.verdicts["dead-1"] = probe.VerdictProvablyDead
	st := &fakeFindMissingStore{liveRows: []store.LiveSpawnIdentity{row("dead-1", 4242, "9988")}}

	res, err := api.FindMissing(context.Background(), st, &fakeProber{}, chk, &recordingLogger{})
	if err != nil {
		t.Fatalf("FindMissing: %v", err)
	}
	if res.Count != 1 || len(res.IDs) != 1 || res.IDs[0] != "dead-1" {
		t.Errorf("res = %+v; want count=1 ids=[dead-1]", res)
	}
	// The checker was consulted with the recorded identity.
	if !chk.queried("dead-1") {
		t.Errorf("checker not queried for dead-1")
	}
	if chk.queriedPIDs["dead-1"] != 4242 || chk.queriedST["dead-1"] != "9988" {
		t.Errorf("checker queried with (pid=%d, st=%q); want (4242, \"9988\")",
			chk.queriedPIDs["dead-1"], chk.queriedST["dead-1"])
	}
	if !equalStrings(st.marked, []string{"dead-1"}) {
		t.Errorf("marked = %v; want [dead-1]", st.marked)
	}
	if !equalStrings(st.clearedIDs, []string{"dead-1"}) {
		t.Errorf("clearedIDs = %v; want [dead-1] (clear fires in marking path)", st.clearedIDs)
	}
	if !equalStrings(st.closedIDs, []string{"dead-1"}) {
		t.Errorf("closedIDs = %v; want [dead-1]", st.closedIDs)
	}
}

// TestFindMissingCheckerAliveSkipsAndClears: a readable+matching row the checker
// calls verified-alive is skipped (not marked), and its stale liveness fields
// are cleared via ClearLivenessUnverified.
func TestFindMissingCheckerAliveSkipsAndClears(t *testing.T) {
	chk := newFakeChecker()
	chk.verdicts["alive-1"] = probe.VerdictVerifiedAlive
	st := &fakeFindMissingStore{liveRows: []store.LiveSpawnIdentity{row("alive-1", 500, "700")}}

	res, err := api.FindMissing(context.Background(), st, &fakeProber{}, chk, &recordingLogger{})
	if err != nil {
		t.Fatalf("FindMissing: %v", err)
	}
	if res.Count != 0 {
		t.Errorf("count = %d; want 0 (alive row skipped)", res.Count)
	}
	if len(st.marked) != 0 {
		t.Errorf("marked = %v; want none (alive)", st.marked)
	}
	if !equalStrings(st.clearedIDs, []string{"alive-1"}) {
		t.Errorf("clearedIDs = %v; want [alive-1] (verified-alive clears stale fields)", st.clearedIDs)
	}
	if len(st.setUnverified) != 0 {
		t.Errorf("setUnverified = %v; want none (alive)", st.setUnverified)
	}
}

// TestFindMissingCheckerUnknownIsolatesRow: an unknown/EACCES row is skipped
// THIS ROW ONLY — SetLivenessUnverified fires with note "probe_eacces", the id
// lands in UnverifiedIDs, and a sibling dead row IN THE SAME SWEEP still
// reconciles to missing. This pins the fail-open isolation contract.
func TestFindMissingCheckerUnknownIsolatesRow(t *testing.T) {
	chk := newFakeChecker()
	chk.verdicts["walled"] = probe.VerdictUnknown
	chk.verdicts["dead-2"] = probe.VerdictProvablyDead
	st := &fakeFindMissingStore{liveRows: []store.LiveSpawnIdentity{
		row("walled", 10, "100"),
		row("dead-2", 20, "200"),
	}}

	res, err := api.FindMissing(context.Background(), st, &fakeProber{}, chk, &recordingLogger{})
	if err != nil {
		t.Fatalf("FindMissing: %v", err)
	}
	// The dead sibling still reconciles despite the walled row.
	if !equalStrings(res.IDs, []string{"dead-2"}) {
		t.Errorf("IDs = %v; want [dead-2] (dead sibling reconciles)", res.IDs)
	}
	if !equalStrings(st.marked, []string{"dead-2"}) {
		t.Errorf("marked = %v; want [dead-2]", st.marked)
	}
	// The walled row is recorded unverified with the pinned note.
	if len(st.setUnverified) != 1 || st.setUnverified[0].id != "walled" || st.setUnverified[0].note != "probe_eacces" {
		t.Errorf("setUnverified = %+v; want one (walled, probe_eacces)", st.setUnverified)
	}
	if !equalStrings(res.UnverifiedIDs, []string{"walled"}) {
		t.Errorf("UnverifiedIDs = %v; want [walled]", res.UnverifiedIDs)
	}
	if res.Unverified != 1 {
		t.Errorf("Unverified = %d; want 1", res.Unverified)
	}
	// The walled row must not be marked or closed.
	for _, id := range st.marked {
		if id == "walled" {
			t.Errorf("walled row was marked; want skipped")
		}
	}
	if len(st.closedIDs) != 1 || st.closedIDs[0] != "dead-2" {
		t.Errorf("closedIDs = %v; want [dead-2] only", st.closedIDs)
	}
}

// TestFindMissingUnknownRepeatNoDoubleTick: SetLivenessUnverified returning
// transitioned=false (a repeat, timestamp already set) still leaves the row in
// UnverifiedIDs but signals the caller not to double-count the NULL→set tick.
// We assert the id is unverified and the setter was consulted; the tick gating
// itself is asserted in the trail sibling's file.
func TestFindMissingUnknownRepeatStillUnverified(t *testing.T) {
	chk := newFakeChecker()
	chk.verdicts["walled"] = probe.VerdictUnknown
	st := &fakeFindMissingStore{
		liveRows:        []store.LiveSpawnIdentity{row("walled", 10, "100")},
		setTransitioned: map[string]bool{"walled": false},
	}

	res, err := api.FindMissing(context.Background(), st, &fakeProber{}, chk, &recordingLogger{})
	if err != nil {
		t.Fatalf("FindMissing: %v", err)
	}
	if !equalStrings(res.UnverifiedIDs, []string{"walled"}) {
		t.Errorf("UnverifiedIDs = %v; want [walled] even on repeat", res.UnverifiedIDs)
	}
	if len(st.setUnverified) != 1 {
		t.Errorf("setUnverified count = %d; want 1", len(st.setUnverified))
	}
}

// ---------------------------------------------------------------------------
// Partial-identity fallback (SR-7.5): pid set + starttime NULL, and vice versa.
// Such a row has an INCOMPLETE recorded identity, so the verb must NOT query the
// checker for it — it routes to the environ probe-set diff instead. The fake
// checker's recording proves no query fired.
// ---------------------------------------------------------------------------

func TestFindMissingPartialIdentityPidOnlyFallsBack(t *testing.T) {
	chk := newFakeChecker()
	// pid set, starttime NULL → partial identity → fallback, absent from probe.
	st := &fakeFindMissingStore{liveRows: []store.LiveSpawnIdentity{row("pid-only", 999, "")}}
	prober := &fakeProber{set: map[string]struct{}{}} // not in probe set → marked

	res, err := api.FindMissing(context.Background(), st, prober, chk, &recordingLogger{})
	if err != nil {
		t.Fatalf("FindMissing: %v", err)
	}
	if chk.queried("pid-only") {
		t.Errorf("checker queried for partial-identity (pid-only) row; want fallback (no query)")
	}
	if !equalStrings(res.IDs, []string{"pid-only"}) {
		t.Errorf("IDs = %v; want [pid-only] (fallback marked it)", res.IDs)
	}
}

func TestFindMissingPartialIdentityStarttimeOnlyFallsBack(t *testing.T) {
	chk := newFakeChecker()
	// starttime set, pid 0 (NULL) → partial identity → fallback.
	st := &fakeFindMissingStore{liveRows: []store.LiveSpawnIdentity{row("st-only", 0, "12345")}}
	// Present in the probe set → NOT marked (proves the probe-set diff drove it,
	// not the checker).
	prober := &fakeProber{set: map[string]struct{}{"st-only": {}}}

	res, err := api.FindMissing(context.Background(), st, prober, chk, &recordingLogger{})
	if err != nil {
		t.Fatalf("FindMissing: %v", err)
	}
	if chk.queried("st-only") {
		t.Errorf("checker queried for partial-identity (starttime-only) row; want fallback (no query)")
	}
	if res.Count != 0 {
		t.Errorf("count = %d; want 0 (row present in probe set)", res.Count)
	}
	if len(st.marked) != 0 {
		t.Errorf("marked = %v; want none (probe-set diff kept it)", st.marked)
	}
}

// ---------------------------------------------------------------------------
// Adversarial marking-order pin (SR-8.2). The trail is a file, not the fake, so
// we assert what the FAKE sees: per marked row, Mark strictly precedes Clear,
// which strictly precedes Close (the proc_absent tick sits between Clear and
// Close but is not observable on the fake). Any other store-call order fails.
// ---------------------------------------------------------------------------

func TestFindMissingMarkingOrderPinned(t *testing.T) {
	chk := newFakeChecker()
	chk.verdicts["ord-1"] = probe.VerdictProvablyDead
	st := &fakeFindMissingStore{liveRows: []store.LiveSpawnIdentity{row("ord-1", 7, "77")}}

	if _, err := api.FindMissing(context.Background(), st, &fakeProber{}, chk, &recordingLogger{}); err != nil {
		t.Fatalf("FindMissing: %v", err)
	}

	// Extract the ordered ops for ord-1 from the interleaved call log.
	var ops []string
	for _, c := range st.callSeq {
		if c.id == "ord-1" {
			ops = append(ops, c.op)
		}
	}
	want := []string{"mark", "clear", "close"}
	if !equalStrings(ops, want) {
		t.Fatalf("marking call order for ord-1 = %v; want %v (Mark → Clear → Close)", ops, want)
	}
}

// TestFindMissingAlreadyTerminalRowMarkOnly: when MarkSpawnMissing returns an
// EMPTY prior state (row absent or already terminal), the write did not happen,
// so the marking path must fire Mark ONLY — no ClearLivenessUnverified, no
// CloseOrphanedPermissionRequests, and the row is NOT counted. This pins the
// priorState != "" gate adversarially.
func TestFindMissingAlreadyTerminalRowMarkOnly(t *testing.T) {
	chk := newFakeChecker()
	chk.verdicts["gone"] = probe.VerdictProvablyDead
	st := &fakeFindMissingStore{
		liveRows:  []store.LiveSpawnIdentity{row("gone", 3, "33")},
		markPrior: map[string]string{"gone": ""}, // no write happened
	}

	res, err := api.FindMissing(context.Background(), st, &fakeProber{}, chk, &recordingLogger{})
	if err != nil {
		t.Fatalf("FindMissing: %v", err)
	}
	if res.Count != 0 {
		t.Errorf("count = %d; want 0 (no write happened)", res.Count)
	}
	if !equalStrings(st.marked, []string{"gone"}) {
		t.Errorf("marked = %v; want [gone] (Mark still attempted)", st.marked)
	}
	if len(st.clearedIDs) != 0 {
		t.Errorf("clearedIDs = %v; want none (gated on priorState != \"\")", st.clearedIDs)
	}
	if len(st.closedIDs) != 0 {
		t.Errorf("closedIDs = %v; want none (gated on priorState != \"\")", st.closedIDs)
	}
	// Confirm the fake saw ONLY the mark op for this row.
	var ops []string
	for _, c := range st.callSeq {
		if c.id == "gone" {
			ops = append(ops, c.op)
		}
	}
	if !equalStrings(ops, []string{"mark"}) {
		t.Errorf("ops for gone = %v; want [mark] only", ops)
	}
}

// ---------------------------------------------------------------------------
// Hard-error propagation: a list error and a probe error (with fallback rows
// present) both abort the sweep. Per-row errors are covered above via the
// isolation test; these pin the two hard aborts.
// ---------------------------------------------------------------------------

func TestFindMissingListErrorAborts(t *testing.T) {
	st := &fakeFindMissingStore{listErr: errSentinel}
	_, err := api.FindMissing(context.Background(), st, &fakeProber{}, newFakeChecker(), &recordingLogger{})
	if err == nil {
		t.Fatalf("FindMissing: nil error; want the list error to bubble up")
	}
}

func TestFindMissingProbeErrorWithFallbackAborts(t *testing.T) {
	// A fallback row exists (id-only), so the verb calls Probe; a hard probe
	// error must abort the sweep.
	st := &fakeFindMissingStore{liveIDs: []string{"a"}}
	prober := &fakeProber{err: errSentinel}
	_, err := api.FindMissing(context.Background(), st, prober, newFakeChecker(), &recordingLogger{})
	if err == nil {
		t.Fatalf("FindMissing: nil error; want the probe error to bubble up")
	}
}

// errSentinel (a shared package-level error, declared in sendkeys_test.go) is
// reused here for the hard-abort tests above.
