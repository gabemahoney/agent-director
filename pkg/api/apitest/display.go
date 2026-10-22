package apitest

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
)

// PermissionOption overrides exact request facts without advancing any clocks.
type PermissionOption func(map[string]any)

func permissionToken(opts []PermissionOption, fallback string) string {
	m := map[string]any{}
	for _, opt := range opts {
		opt(m)
	}
	if token, ok := m["request_token"].(string); ok {
		return token
	}
	return fallback
}

// PermissionToken uses the existing deterministic storefix tokens.
func PermissionToken(token string) PermissionOption {
	return func(m map[string]any) { m["request_token"] = token }
}

// PermissionTimes controls clocks independently; a zero time stores NULL.
func PermissionTimes(created, admitted, confirmBy, gone, fallback time.Time, basis string) PermissionOption {
	return func(m map[string]any) {
		if !created.IsZero() {
			m["created_at"] = created.UTC().Format(storeTimestampLayout)
		}
		for k, at := range map[string]time.Time{"admitted_at": admitted, "settled_at": confirmBy, "hook_gone_at": gone, "fallback_at": fallback} {
			var v any
			if !at.IsZero() {
				v = at.UnixMilli()
			}
			m[k] = v
		}
		m["fallback_basis"] = nullIfZero(basis)
	}
}

// PermissionOwner overrides launch/interval ownership, including unowned history.
func PermissionOwner(launch string, epoch int64) PermissionOption {
	return func(m map[string]any) {
		m["owner_launch_token"] = nullIfZero(launch)
		m["owner_permission_epoch"] = epoch
	}
}

// PermissionAnswer records a pane answer without proving the dialog gone.
func PermissionAnswer(answer string) PermissionOption {
	return func(m map[string]any) { m["pane_answer"] = answer }
}

// PermissionProof marks a request proven gone independently of its answer.
func PermissionProof(at time.Time) PermissionOption {
	return func(m map[string]any) {
		m["proven_gone_at"] = at.UnixMilli()
		m["proven_gone_how"] = store.ProvenGoneTurnEnd
	}
}

// PermissionDecision records a decision without delivering it or proving the
// dialog gone, so the request still awaits an answer.
func PermissionDecision(decision, reason string) PermissionOption {
	return func(m map[string]any) { m["decision"] = decision; m["decision_reason"] = nullIfZero(reason) }
}

// SetPermissionFacts updates only named request facts using a raw fixture connection.
func SetPermissionFacts(dbPath string, requestID int64, opts ...PermissionOption) error {
	if len(opts) == 0 {
		return nil
	}
	raw, err := openRawStore(dbPath)
	if err != nil {
		return err
	}
	defer raw.Close()
	m := map[string]any{}
	for _, opt := range opts {
		opt(m)
	}
	for k, v := range m {
		if _, err := raw.Exec("UPDATE permission_requests SET "+k+"=? WHERE request_id=?", v, requestID); err != nil {
			return fmt.Errorf("SetPermissionFacts %s: %w", k, err)
		}
	}
	return nil
}

// SeedNeedsIntelligenceSurface seeds needs-intelligence-claude, -codex and
// -cursor, displayed since one precommitted instant, and the ordinary row ordinary.
func SeedNeedsIntelligenceSurface(dbPath string) error {
	since := time.Date(2026, 10, 19, 23, 59, 59, 123000000, time.UTC)
	// The row's permission epoch and the observation's agree: an observation
	// of another epoch is a finished episode, not the committed one.
	const epoch = 3
	for _, agent := range []string{store.AgentClaude, store.AgentCodex, store.AgentCursor} {
		id := "needs-intelligence-" + agent
		observation, err := json.Marshal(map[string]any{"state": store.StateNeedsIntelligence, "since_ms": since.UnixMilli(), "request_token": storefix.TestRequestTokenA, "tool_name": "Bash", "candidate": store.StateNeedsIntelligence, "candidate_since_ms": since.UnixMilli(), "observed_at_ms": since.UnixMilli(), "permission_epoch": epoch})
		if err != nil {
			return err
		}
		if _, err := SeedSpawn(dbPath, id, store.StateCheckPermission, "/tmp", "on", "", true, WithAgent(agent), WithNoPane(), WithStartedAt(since.Add(-time.Hour)), WithPermissionEpoch(epoch), WithDisplayObservation(string(observation))); err != nil {
			return err
		}
		if _, err := SeedPermissionRequest(dbPath, id, "Bash", PermissionToken(storefix.TestRequestTokenA), PermissionTimes(since.Add(-time.Hour), since.Add(-time.Hour), since.Add(-time.Minute), time.Time{}, since.Add(-time.Minute), store.FallbackDeadline)); err != nil {
			return err
		}
	}
	_, err := SeedSpawn(dbPath, "ordinary", store.StateWorking, "/tmp", "off", "", false)
	return err
}

// SeedIdleScreenSurface seeds idle-screen-claude and -codex, displayed since that
// instant, and ordinary-cursor, a working Cursor row; read them under SettleOnConfig.
func SeedIdleScreenSurface(dbPath string) error {
	since := time.Date(2026, 10, 19, 23, 59, 59, 123000000, time.UTC)
	const epoch = 3
	for _, agent := range []string{store.AgentClaude, store.AgentCodex} {
		observation, err := json.Marshal(map[string]any{"state": store.StateIdleScreen, "since_ms": since.UnixMilli(), "unchanged_minutes": 21,
			"candidate": store.StateIdleScreen, "candidate_since_ms": since.UnixMilli(), "observed_at_ms": since.UnixMilli(), "permission_epoch": epoch})
		if err != nil {
			return err
		}
		if _, err := SeedSpawn(dbPath, "idle-screen-"+agent, store.StateWorking, "/tmp", "off", "", true, WithAgent(agent), WithStartedAt(since.Add(-time.Hour)),
			WithPermissionEpoch(epoch), WithDisplayObservation(string(observation)),
			WithPaneBaseline(strings.Repeat("ab", 32), since.Add(-21*time.Minute).UnixMilli(), int64(epoch))); err != nil {
			return err
		}
	}
	_, err := SeedSpawn(dbPath, "ordinary-cursor", store.StateWorking, "/tmp", "off", "", true, WithAgent(store.AgentCursor), WithNoPane())
	return err
}

// DisplayWireSurface is one shared surface's displayed rows: their Displayed
// State, id prefix and agents, and the check of one read row's wire detail.
type DisplayWireSurface struct {
	State  string
	Prefix string
	Agents []string
	Check  func(t testing.TB, raw []byte, agent, verb string)
}

// DisplayWireSurfaces are SeedNeedsIntelligenceSurface's and
// SeedIdleScreenSurface's displayed rows; DisplayWireOrdinary maps their
// ordinary rows to their agents. Every surface test (Go, CLI, MCP) reads them
// all.
var (
	DisplayWireSurfaces = []DisplayWireSurface{
		{store.StateNeedsIntelligence, "needs-intelligence-", []string{store.AgentClaude, store.AgentCodex, store.AgentCursor}, CheckNeedsIntelligenceWire},
		{store.StateIdleScreen, "idle-screen-", []string{store.AgentClaude, store.AgentCodex}, CheckIdleScreenWire},
	}
	DisplayWireOrdinary = map[string]string{"ordinary": store.AgentClaude, "ordinary-cursor": store.AgentCursor}
)

// RemovedDisplayNames matches, in any case or dash/underscore spelling, the
// naming b.ob8 SR-1.3 removed; the manifest, CLI flag-usage and pkg/api guards share it.
var RemovedDisplayNames = regexp.MustCompile(`(?i)stuck|permission[-_]?unanswered|no[-_]?screen[-_]?change`)

// CheckListedDisplayWire applies its surface's check to raw, one list row,
// which must be one of DisplayWireSurfaces' displayed rows.
func CheckListedDisplayWire(t testing.TB, raw []byte) {
	t.Helper()
	var row struct {
		ID string `json:"instance_id"`
	}
	if err := json.Unmarshal(raw, &row); err != nil {
		t.Fatal(err)
	}
	for _, s := range DisplayWireSurfaces {
		for _, agent := range s.Agents {
			if row.ID == s.Prefix+agent {
				s.Check(t, raw, agent, "list")
				return
			}
		}
	}
	t.Errorf("list row %s is no shared displayed row", row.ID)
}

// CheckNeedsIntelligenceWire applies the shared independent needs_intelligence
// wire expectations to one read row of SeedNeedsIntelligenceSurface's.
func CheckNeedsIntelligenceWire(t testing.TB, raw []byte, agent, verb string) {
	t.Helper()
	checkDisplayWire(t, raw, agent, verb, "ts-needs-intelligence-"+agent, LoadNeedsIntelligenceWire(t), "unchanged_minutes")
}

// CheckIdleScreenWire applies the shared independent idle_screen wire
// expectations to one read row of SeedIdleScreenSurface's.
func CheckIdleScreenWire(t testing.TB, raw []byte, agent, verb string) {
	t.Helper()
	checkDisplayWire(t, raw, agent, verb, "ts-idle-screen-"+agent, LoadIdleScreenWire(t), "request_token", "tool_name")
}

// CheckOrdinaryWire fails unless raw, one read row of an ordinary row of
// agent, carries no detail of either Displayed State and the agent's
// idle_screen_availability.
func CheckOrdinaryWire(t testing.TB, raw []byte, agent string) {
	t.Helper()
	var row map[string]any
	if err := json.Unmarshal(raw, &row); err != nil {
		t.Fatal(err)
	}
	for _, wire := range []map[string]any{LoadNeedsIntelligenceWire(t), LoadIdleScreenWire(t)} {
		for k := range wire {
			if _, ok := row[k]; ok && k != "state" {
				t.Errorf("ordinary %s row emitted %s: %s", agent, k, raw)
			}
		}
	}
	checkAvailability(t, row, agent)
}

// checkDisplayWire checks one read row: expected's detail (list: state only), no absent
// key or removed reason member, its session, availability, raw stamp and request arrays.
func checkDisplayWire(t testing.TB, raw []byte, agent, verb, session string, expected map[string]any, absent ...string) {
	t.Helper()
	var row map[string]any
	if err := json.Unmarshal(raw, &row); err != nil {
		t.Fatal(err)
	}
	if row["state"] != expected["state"] {
		t.Fatalf("%s/%s state: %s", agent, verb, raw)
	}
	for k, v := range expected {
		if verb == "list" && k != "state" {
			absent = append(absent, k)
			continue
		}
		if row[k] != v {
			t.Errorf("%s/%s %s=%v; want %v", agent, verb, k, row[k], v)
		}
	}
	for _, k := range append(absent, "stuck_reason") {
		if _, ok := row[k]; ok {
			t.Errorf("%s/%s emitted %s", agent, verb, k)
		}
	}
	if row["tmux_session_name"] != session {
		t.Errorf("session=%v; want %s", row["tmux_session_name"], session)
	}
	checkAvailability(t, row, agent)
	// b.k6i: get and list carry the raw stamp, the seed's zero stamp, shown
	// as the Unix epoch; status carries none.
	if at, ok := row["state_changed_at"]; verb == "status" && ok {
		t.Errorf("%s/status emitted state_changed_at %v", agent, at)
	} else if verb != "status" && at != "1970-01-01T00:00:00Z" {
		t.Errorf("%s/%s state_changed_at=%v; want 1970-01-01T00:00:00Z", agent, verb, at)
	}
	if verb != "status" {
		if _, ok := row["permission_requests"].([]any); !ok {
			t.Error("permission_requests must remain an array")
		}
	}
}

// checkAvailability fails unless row carries agent's
// idle_screen_availability: unavailable for Cursor, else available.
func checkAvailability(t testing.TB, row map[string]any, agent string) {
	t.Helper()
	want := "available"
	if agent == store.AgentCursor {
		want = "unavailable"
	}
	if row["idle_screen_availability"] != want {
		t.Errorf("%s idle_screen_availability=%v; want %s", agent, row["idle_screen_availability"], want)
	}
}

// LoadNeedsIntelligenceWire reads the shared, hand-authored expected
// needs_intelligence details.
func LoadNeedsIntelligenceWire(t testing.TB) map[string]any {
	t.Helper()
	return loadDisplayWire(t, "needs_intelligence.json")
}

// LoadIdleScreenWire reads the shared, hand-authored expected idle_screen details.
func LoadIdleScreenWire(t testing.TB) map[string]any {
	t.Helper()
	return loadDisplayWire(t, "idle_screen.json")
}

// loadDisplayWire reads cmd/agent-director/testdata/display/<name>, found from
// the working directory up.
func loadDisplayWire(t testing.TB, name string) map[string]any {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		raw, err := os.ReadFile(filepath.Join(dir, "cmd", "agent-director", "testdata", "display", name))
		if err == nil {
			var m map[string]any
			if err := json.Unmarshal(raw, &m); err != nil {
				t.Fatal(err)
			}
			return m
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("cannot find the shared display wire fixture %s", name)
		}
		dir = parent
	}
}

// PermissionClosed records a lifecycle close independently of delivery.
func PermissionClosed(at time.Time) PermissionOption {
	return func(m map[string]any) { m["closed_at"] = at.UnixMilli() }
}

// PermissionCreation stores malformed or missing historical creation evidence.
func PermissionCreation(raw string) PermissionOption {
	return func(m map[string]any) { m["created_at"] = raw }
}

// PermissionDelivered records a relay acknowledgement independently of proof.
func PermissionDelivered(at time.Time) PermissionOption {
	return func(m map[string]any) { m["delivered_at"] = at.UnixMilli() }
}

// DisplayProbe is the committed-row read the observation helper needs; both
// *store.Store and the reopened stores of other packages satisfy it.
type DisplayProbe interface {
	GetSpawn(id string) (store.Spawn, error)
}

// DisplayReading is the display part of one reader result; UnchangedMinutes is
// 0 when the result carries none.
type DisplayReading struct {
	State, Since, RequestToken, ToolName string
	UnchangedMinutes                     int64
}

// observeAttempts and observeWindow bound the repeats of a read that had to
// commit: at least observeAttempts reads, then reads until observeWindow of real
// time has passed, as load can make several back-to-back commits miss (t2.sp6.rk.1k).
const (
	observeAttempts = 3
	observeWindow   = 10 * time.Second
)

// RepeatUntilCommitted is the one bounded repeat of a nonwaiting write that
// had to commit (ObserveDisplayRead and internal/store's commitUnlessBusy): it
// calls try, which keeps the caller's injected time and must not sleep, until
// try reports the commit landed, giving up only after at least observeAttempts
// calls and observeWindow of real time. It returns the calls made, the time
// they took and whether the last one committed.
func RepeatUntilCommitted(try func() bool) (attempts int, elapsed time.Duration, committed bool) {
	start := time.Now()
	for attempts = 1; ; attempts++ {
		committed = try()
		elapsed = time.Since(start)
		if committed || attempts >= observeAttempts && elapsed >= observeWindow {
			return attempts, elapsed, committed
		}
	}
}

// CommitCheck reports whether the commit a read was expected to make landed,
// judged from the row before and after the read.
type CommitCheck func(before, after store.Spawn) bool

// VersionAdvanced is the CommitCheck of a read that stores an observation or
// starts a request's fallback clock: the observer advances observation_version
// in the one transaction that does either.
func VersionAdvanced(before, after store.Spawn) bool {
	return after.ObservationVersion != before.ObservationVersion
}

// ObserveDisplayRead runs one reader and checks it against the committed row.
// The observation write is nonwaiting and best effort, so a read that had to
// commit may lose a race for the write lock and answer from its snapshot.
// When commit is non-nil and reports no commit, the same read is repeated at
// the same injected time, without sleeping, under RepeatUntilCommitted's
// bound, and the test then fails with "observation never committed".
// Once the commit landed (or none was expected) the display must be want and,
// for either Displayed State, equal the committed detail of the row.
func ObserveDisplayRead(t testing.TB, probe DisplayProbe, id, want string, commit CommitCheck, read func() (DisplayReading, error)) DisplayReading {
	t.Helper()
	var got DisplayReading
	var before, after store.Spawn
	attempts, elapsed, committed := RepeatUntilCommitted(func() bool {
		var err error
		if before, err = probe.GetSpawn(id); err != nil {
			t.Fatal(err)
		}
		if got, err = read(); err != nil {
			t.Fatal(err)
		}
		if after, err = probe.GetSpawn(id); err != nil {
			t.Fatal(err)
		}
		return commit == nil || commit(before, after)
	})
	if !committed {
		t.Fatalf("observation never committed in %d attempts over %v: read=%+v (observation_version %d -> %d)", attempts, elapsed, got, before.ObservationVersion, after.ObservationVersion)
	}
	if attempts > 1 {
		t.Logf("observation committed on attempt %d after %v", attempts, elapsed)
	}
	if got.State != want {
		t.Fatalf("read=%+v; want state %s (observation_version %d -> %d)", got, want, before.ObservationVersion, after.ObservationVersion)
	}
	if want == store.StateNeedsIntelligence || want == store.StateIdleScreen {
		committed := after.CommittedDisplay()
		if got != (DisplayReading{State: committed.State, Since: time.UnixMilli(committed.SinceMillis).UTC().Format("2006-01-02T15:04:05.000Z"),
			RequestToken: committed.RequestToken, ToolName: committed.ToolName, UnchangedMinutes: committed.UnchangedMinutes}) {
			t.Fatalf("%s read=%+v is not the committed display %+v (observation_version %d -> %d)", want, got, committed, before.ObservationVersion, after.ObservationVersion)
		}
	}
	return got
}
