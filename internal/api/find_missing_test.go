package api_test

import (
	"context"
	"strings"
	"testing"

	"github.com/gabemahoney/claude-director/internal/api"
)

// fakeFindMissingStore is the narrow store the verb sees. liveIDs is
// the ListLiveSpawnIDs result; marked records every successful
// MarkSpawnMissing call so the test can assert which rows were
// transitioned.
type fakeFindMissingStore struct {
	liveIDs []string
	marked  []string
	listErr error
	markErr error
}

func (f *fakeFindMissingStore) ListLiveSpawnIDs() ([]string, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return append([]string(nil), f.liveIDs...), nil
}

func (f *fakeFindMissingStore) MarkSpawnMissing(id string) error {
	if f.markErr != nil {
		return f.markErr
	}
	f.marked = append(f.marked, id)
	return nil
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

// recordingLogger captures Printf invocations so degraded-mode tests
// can assert the warning was emitted.
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

func TestFindMissingNoChangesWhenAllAlive(t *testing.T) {
	store := &fakeFindMissingStore{liveIDs: []string{"a", "b"}}
	prober := &fakeProber{set: map[string]struct{}{"a": {}, "b": {}}}
	res, err := api.FindMissing(context.Background(), store, prober, &recordingLogger{})
	if err != nil {
		t.Fatalf("FindMissing: %v", err)
	}
	if res.Count != 0 || len(res.IDs) != 0 {
		t.Errorf("res = %+v; want count=0 ids=[]", res)
	}
	if len(store.marked) != 0 {
		t.Errorf("MarkSpawnMissing called: %v", store.marked)
	}
}

func TestFindMissingTransitionsUnprobeableRows(t *testing.T) {
	store := &fakeFindMissingStore{liveIDs: []string{"a", "b", "c"}}
	prober := &fakeProber{set: map[string]struct{}{"b": {}}}
	res, err := api.FindMissing(context.Background(), store, prober, &recordingLogger{})
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
	if !equalStrings(store.marked, want) {
		t.Errorf("MarkSpawnMissing calls = %v; want %v", store.marked, want)
	}
}

func TestFindMissingDegradedModeGuardTrips(t *testing.T) {
	// SRD §14.6: zero probe IDs + non-zero live rows → refuse to
	// sweep. The warning is logged; no DB updates happen; the verb
	// returns count=0 nil error (the cron must not pager).
	store := &fakeFindMissingStore{liveIDs: []string{"a", "b"}}
	prober := &fakeProber{set: map[string]struct{}{}}
	lg := &recordingLogger{}

	res, err := api.FindMissing(context.Background(), store, prober, lg)
	if err != nil {
		t.Fatalf("FindMissing: %v", err)
	}
	if res.Count != 0 || len(res.IDs) != 0 {
		t.Errorf("guard tripped but result = %+v; want zero", res)
	}
	if len(store.marked) != 0 {
		t.Errorf("guard tripped but MarkSpawnMissing was called: %v", store.marked)
	}
	if len(lg.lines) == 0 {
		t.Errorf("guard tripped but no warning logged")
	}
}

func TestFindMissingZeroLiveRowsZeroProbeIsNoopSuccess(t *testing.T) {
	// Legitimate empty case: no live rows + no live processes. The
	// degraded-mode guard's condition is `len(probe)==0 && len(live)>0`,
	// so a 0/0 state is not the guard — it's a fast no-op.
	store := &fakeFindMissingStore{}
	prober := &fakeProber{set: map[string]struct{}{}}
	lg := &recordingLogger{}

	res, err := api.FindMissing(context.Background(), store, prober, lg)
	if err != nil {
		t.Fatalf("FindMissing: %v", err)
	}
	if res.Count != 0 {
		t.Errorf("count = %d; want 0", res.Count)
	}
	if len(lg.lines) != 0 {
		t.Errorf("0/0 case wrongly emitted a warning: %v", lg.lines)
	}
}

func TestFindMissingPendingRowIsScanned(t *testing.T) {
	// Per SRD §5.2: a `pending` row whose tmux session vanished
	// before SessionStart fired must reconcile to `missing` on the
	// next sweep. ListLiveSpawnIDs in the store-side primitive
	// includes `pending` in its IN-list; this test pins the
	// downstream effect.
	store := &fakeFindMissingStore{liveIDs: []string{"p-1"}}
	prober := &fakeProber{set: map[string]struct{}{"other": {}}}
	res, err := api.FindMissing(context.Background(), store, prober, &recordingLogger{})
	if err != nil {
		t.Fatalf("FindMissing: %v", err)
	}
	if res.Count != 1 || res.IDs[0] != "p-1" {
		t.Errorf("res = %+v; want count=1 ids=[p-1]", res)
	}
}

func TestFindMissingResultIDsSorted(t *testing.T) {
	// The result envelope must be deterministic. Feed an unsorted
	// live-id list and assert the result IDs come back sorted.
	store := &fakeFindMissingStore{liveIDs: []string{"z", "a", "m"}}
	prober := &fakeProber{set: map[string]struct{}{}}
	// Use a sneaky probe with one ID present, so degraded-mode is
	// not tripped.
	prober.set["sentinel"] = struct{}{}

	res, err := api.FindMissing(context.Background(), store, prober, &recordingLogger{})
	if err != nil {
		t.Fatalf("FindMissing: %v", err)
	}
	want := []string{"a", "m", "z"}
	if got := res.IDs; !equalStrings(got, want) {
		t.Errorf("ids = %v; want %v (sorted)", got, want)
	}
}
