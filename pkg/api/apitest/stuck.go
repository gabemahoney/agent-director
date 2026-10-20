package apitest

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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

// CheckPermissionWire applies the shared independent wire expectations to one read row.
func CheckPermissionWire(t testing.TB, raw []byte, agent, verb string) {
	t.Helper()
	var row map[string]any
	if err := json.Unmarshal(raw, &row); err != nil {
		t.Fatal(err)
	}
	expected := LoadPermissionWire(t)
	if row["state"] != "stuck" {
		t.Fatalf("%s/%s state: %s", agent, verb, raw)
	}
	for k, v := range expected {
		if verb == "list" && k != "state" && k != "stuck_reason" {
			if _, ok := row[k]; ok {
				t.Errorf("list emitted detail %s", k)
			}
			continue
		}
		if row[k] != v {
			t.Errorf("%s/%s %s=%v; want %v", agent, verb, k, row[k], v)
		}
	}
	if row["tmux_session_name"] != "ts-stuck-"+agent {
		t.Errorf("session=%v", row["tmux_session_name"])
	}
	if _, ok := row["unchanged_minutes"]; ok {
		t.Error("permission episode emitted unchanged_minutes")
	}
	if verb != "status" {
		if _, ok := row["permission_requests"].([]any); !ok {
			t.Error("permission_requests must remain an array")
		}
	}
}

// LoadPermissionWire reads the shared, hand-authored expected permission details.
func LoadPermissionWire(t testing.TB) map[string]any {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		raw, err := os.ReadFile(filepath.Join(dir, "cmd", "agent-director", "testdata", "stuck", "permission.json"))
		if err == nil {
			var m map[string]any
			if err := json.Unmarshal(raw, &m); err != nil {
				t.Fatal(err)
			}
			return m
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("cannot find shared permission wire fixture")
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

// StuckReading is the display part of one reader result.
type StuckReading struct {
	State, Reason, Since, RequestToken, ToolName string
}

// observeAttempts bounds how often a read that had to commit may be repeated.
const observeAttempts = 3

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
// the same injected time, without sleeping, up to observeAttempts times, and
// the test then fails with "observation never committed". Once the commit
// landed (or none was expected) the display must be want and, for stuck, equal
// the committed detail of the row.
func ObserveStuckRead(t testing.TB, probe StuckProbe, id, want string, commit CommitCheck, read func() (StuckReading, error)) StuckReading {
	t.Helper()
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
			t.Logf("observation attempt %d/%d did not commit (read=%+v stuck_version %d -> %d)", attempt, observeAttempts, got, before.StuckVersion, after.StuckVersion)
			if attempt == observeAttempts {
				t.Fatalf("observation never committed: read=%+v", got)
			}
			continue
		}
		if got.State != want {
			t.Fatalf("read=%+v; want state %s (stuck_version %d -> %d)", got, want, before.StuckVersion, after.StuckVersion)
		}
		if want == store.StateStuck {
			committed := after.CommittedStuckDisplay()
			if committed.State != store.StateStuck ||
				got != (StuckReading{State: committed.State, Reason: committed.Reason, Since: time.UnixMilli(committed.SinceMillis).UTC().Format("2006-01-02T15:04:05.000Z"), RequestToken: committed.RequestToken, ToolName: committed.ToolName}) {
				t.Fatalf("stuck read=%+v is not the committed display %+v (stuck_version %d -> %d)", got, committed, before.StuckVersion, after.StuckVersion)
			}
		}
		return got
	}
}
