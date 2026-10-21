package apitest

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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

// SeedPermissionSurface uses a precommitted episode so independent processes share an exact since.
// Its rows keep SeedSpawn's zero state_changed_ms (b.k6i).
func SeedPermissionSurface(dbPath string) error {
	since := time.Date(2026, 10, 19, 23, 59, 59, 123000000, time.UTC)
	// The row's permission epoch and the observation's agree: an observation
	// of another epoch is a finished episode, not the committed one.
	const epoch = 3
	for _, agent := range []string{store.AgentClaude, store.AgentCodex, store.AgentCursor} {
		id := "stuck-" + agent
		observation, err := json.Marshal(map[string]any{"state": "stuck", "reason": "permission_unanswered", "since_ms": since.UnixMilli(), "request_token": storefix.TestRequestTokenA, "tool_name": "Bash", "candidate": "stuck", "candidate_since_ms": since.UnixMilli(), "observed_at_ms": since.UnixMilli(), "permission_epoch": epoch})
		if err != nil {
			return err
		}
		if _, err := SeedSpawn(dbPath, id, store.StateCheckPermission, "/tmp", "on", "", true, WithAgent(agent), WithNoPane(), WithStartedAt(since.Add(-time.Hour)), WithPermissionEpoch(epoch), WithStuckObservation(string(observation))); err != nil {
			return err
		}
		if _, err := SeedPermissionRequest(dbPath, id, "Bash", PermissionToken(storefix.TestRequestTokenA), PermissionTimes(since.Add(-time.Hour), since.Add(-time.Hour), since.Add(-time.Minute), time.Time{}, since.Add(-time.Minute), store.FallbackDeadline)); err != nil {
			return err
		}
	}
	_, err := SeedSpawn(dbPath, "ordinary", store.StateWorking, "/tmp", "off", "", false)
	return err
}

// SeedPaneSurface seeds pane-claude and pane-codex displayed no_screen_change
// at SeedPermissionSurface's since, and pane-cursor; read them under SettleOnConfig.
func SeedPaneSurface(dbPath string) error {
	since := time.Date(2026, 10, 19, 23, 59, 59, 123000000, time.UTC)
	const epoch = 3
	for _, agent := range []string{store.AgentClaude, store.AgentCodex} {
		observation, err := json.Marshal(map[string]any{"state": "stuck", "reason": "no_screen_change", "since_ms": since.UnixMilli(), "unchanged_minutes": 21,
			"candidate": "stuck", "candidate_since_ms": since.UnixMilli(), "observed_at_ms": since.UnixMilli(), "permission_epoch": epoch})
		if err != nil {
			return err
		}
		if _, err := SeedSpawn(dbPath, "pane-"+agent, store.StateWorking, "/tmp", "off", "", true, WithAgent(agent), WithStartedAt(since.Add(-time.Hour)),
			WithPermissionEpoch(epoch), WithStuckObservation(string(observation)),
			WithPaneBaseline(strings.Repeat("ab", 32), since.Add(-21*time.Minute).UnixMilli(), int64(epoch))); err != nil {
			return err
		}
	}
	_, err := SeedSpawn(dbPath, "pane-cursor", store.StateWorking, "/tmp", "off", "", true, WithAgent(store.AgentCursor), WithNoPane())
	return err
}

// StuckWireSurface is one shared surface's displayed stuck rows: their id
// prefix, their agents and the check of one read row's wire detail.
type StuckWireSurface struct {
	Prefix string
	Agents []string
	Check  func(t testing.TB, raw []byte, agent, verb string)
}

// StuckWireSurfaces are SeedPermissionSurface's and SeedPaneSurface's
// displayed stuck rows; StuckWireOrdinary maps their ordinary rows to their
// agents. Every surface test (Go, CLI, MCP) reads them all.
var (
	StuckWireSurfaces = []StuckWireSurface{
		{"stuck-", []string{store.AgentClaude, store.AgentCodex, store.AgentCursor}, CheckPermissionWire},
		{"pane-", []string{store.AgentClaude, store.AgentCodex}, CheckPaneWire},
	}
	StuckWireOrdinary = map[string]string{"ordinary": store.AgentClaude, "pane-cursor": store.AgentCursor}
)

// CheckListedStuckWire applies its surface's check to raw, one list row.
func CheckListedStuckWire(t testing.TB, raw []byte) {
	t.Helper()
	var row struct {
		ID    string `json:"instance_id"`
		Agent string `json:"agent"`
	}
	if err := json.Unmarshal(raw, &row); err != nil {
		t.Fatal(err)
	}
	for _, s := range StuckWireSurfaces {
		if strings.HasPrefix(row.ID, s.Prefix) {
			s.Check(t, raw, row.Agent, "list")
			return
		}
	}
	t.Errorf("list row %s is no shared stuck row", row.ID)
}

// CheckPermissionWire applies the shared independent wire expectations to one
// read row of SeedPermissionSurface's.
func CheckPermissionWire(t testing.TB, raw []byte, agent, verb string) {
	t.Helper()
	checkStuckWire(t, raw, agent, verb, "ts-stuck-"+agent, LoadPermissionWire(t), "unchanged_minutes")
}

// CheckPaneWire applies the shared independent no_screen_change wire
// expectations to one read row of SeedPaneSurface's.
func CheckPaneWire(t testing.TB, raw []byte, agent, verb string) {
	t.Helper()
	checkStuckWire(t, raw, agent, verb, "ts-pane-"+agent, LoadPaneWire(t), "request_token", "tool_name")
}

// CheckOrdinaryWire fails unless raw, one read row of an ordinary row of
// agent, carries no stuck detail of either reason and the agent's
// no_screen_change_availability.
func CheckOrdinaryWire(t testing.TB, raw []byte, agent string) {
	t.Helper()
	var row map[string]any
	if err := json.Unmarshal(raw, &row); err != nil {
		t.Fatal(err)
	}
	for _, wire := range []map[string]any{LoadPermissionWire(t), LoadPaneWire(t)} {
		for k := range wire {
			if _, ok := row[k]; ok && k != "state" {
				t.Errorf("ordinary %s row emitted %s: %s", agent, k, raw)
			}
		}
	}
	checkAvailability(t, row, agent)
}

// checkStuckWire is the shared check: the expected detail (only state and
// stuck_reason on a list row), none of absent, the row's session, its agent's
// availability, get's and list's raw stamp and their request arrays.
func checkStuckWire(t testing.TB, raw []byte, agent, verb, session string, expected map[string]any, absent ...string) {
	t.Helper()
	var row map[string]any
	if err := json.Unmarshal(raw, &row); err != nil {
		t.Fatal(err)
	}
	if row["state"] != "stuck" {
		t.Fatalf("%s/%s state: %s", agent, verb, raw)
	}
	for k, v := range expected {
		if verb == "list" && k != "state" && k != "stuck_reason" {
			absent = append(absent, k)
			continue
		}
		if row[k] != v {
			t.Errorf("%s/%s %s=%v; want %v", agent, verb, k, row[k], v)
		}
	}
	for _, k := range absent {
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
// no_screen_change_availability: unavailable for Cursor, else available.
func checkAvailability(t testing.TB, row map[string]any, agent string) {
	t.Helper()
	want := "available"
	if agent == store.AgentCursor {
		want = "unavailable"
	}
	if row["no_screen_change_availability"] != want {
		t.Errorf("%s no_screen_change_availability=%v; want %s", agent, row["no_screen_change_availability"], want)
	}
}

// LoadPermissionWire reads the shared, hand-authored expected permission details.
func LoadPermissionWire(t testing.TB) map[string]any {
	t.Helper()
	return loadStuckWire(t, "permission.json")
}

// LoadPaneWire reads the shared, hand-authored expected no_screen_change details.
func LoadPaneWire(t testing.TB) map[string]any {
	t.Helper()
	return loadStuckWire(t, "pane.json")
}

// loadStuckWire reads cmd/agent-director/testdata/stuck/<name>, found from the
// working directory up.
func loadStuckWire(t testing.TB, name string) map[string]any {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		raw, err := os.ReadFile(filepath.Join(dir, "cmd", "agent-director", "testdata", "stuck", name))
		if err == nil {
			var m map[string]any
			if err := json.Unmarshal(raw, &m); err != nil {
				t.Fatal(err)
			}
			return m
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("cannot find the shared stuck wire fixture %s", name)
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

// StuckProbe is the committed-row read the observation helper needs; both
// *store.Store and the reopened stores of other packages satisfy it.
type StuckProbe interface {
	GetSpawn(id string) (store.Spawn, error)
}

// StuckReading is the display part of one reader result; UnchangedMinutes is
// 0 when the result carries none.
type StuckReading struct {
	State, Reason, Since, RequestToken, ToolName string
	UnchangedMinutes                             int64
}

// observeAttempts and observeWindow bound the repeats of a read that had to
// commit: at least observeAttempts reads, then reads until observeWindow of real
// time has passed, as load can make several back-to-back commits miss (t2.sp6.rk.1k).
const (
	observeAttempts = 3
	observeWindow   = 10 * time.Second
)

// CommitCheck reports whether the commit a read was expected to make landed,
// judged from the row before and after the read.
type CommitCheck func(before, after store.Spawn) bool

// VersionAdvanced is the CommitCheck of a read that stores an observation or
// starts a request's fallback clock: the observer advances stuck_version in the
// one transaction that does either.
func VersionAdvanced(before, after store.Spawn) bool {
	return after.StuckVersion != before.StuckVersion
}

// ObserveStuckRead runs one reader and checks it against the committed row.
// The observation write is nonwaiting and best effort, so a read that had to
// commit may lose a race for the write lock and answer from its snapshot.
// When commit is non-nil and reports no commit, the same read is repeated at
// the same injected time, without sleeping, at least observeAttempts times and
// until observeWindow has passed, and the test then fails with "observation
// never committed".
// Once the commit landed (or none was expected) the display must be want and,
// for stuck, equal the committed detail of the row.
func ObserveStuckRead(t testing.TB, probe StuckProbe, id, want string, commit CommitCheck, read func() (StuckReading, error)) StuckReading {
	t.Helper()
	start := time.Now()
	for attempt := 1; ; attempt++ {
		before, err := probe.GetSpawn(id)
		if err != nil {
			t.Fatal(err)
		}
		got, err := read()
		if err != nil {
			t.Fatal(err)
		}
		after, err := probe.GetSpawn(id)
		if err != nil {
			t.Fatal(err)
		}
		if commit != nil && !commit(before, after) {
			if attempt >= observeAttempts && time.Since(start) >= observeWindow {
				t.Fatalf("observation never committed in %d attempts over %v: read=%+v (stuck_version %d -> %d)", attempt, time.Since(start), got, before.StuckVersion, after.StuckVersion)
			}
			continue
		}
		if attempt > 1 {
			t.Logf("observation committed on attempt %d after %v", attempt, time.Since(start))
		}
		if got.State != want {
			t.Fatalf("read=%+v; want state %s (stuck_version %d -> %d)", got, want, before.StuckVersion, after.StuckVersion)
		}
		if want == store.StateStuck {
			committed := after.CommittedStuckDisplay()
			if committed.State != store.StateStuck ||
				got != (StuckReading{State: committed.State, Reason: committed.Reason, Since: time.UnixMilli(committed.SinceMillis).UTC().Format("2006-01-02T15:04:05.000Z"),
					RequestToken: committed.RequestToken, ToolName: committed.ToolName, UnchangedMinutes: committed.UnchangedMinutes}) {
				t.Fatalf("stuck read=%+v is not the committed display %+v (stuck_version %d -> %d)", got, committed, before.StuckVersion, after.StuckVersion)
			}
		}
		return got
	}
}
