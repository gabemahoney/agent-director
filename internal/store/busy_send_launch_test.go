package store

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// These tests share the real-store/migration fixtures. SQL here seeds or
// observes permanent core facts; lifecycle operations always use Store.
func busyLaunchRow(t *testing.T, s *Store, id string) Spawn {
	t.Helper()
	r, e := s.GetSpawn(id)
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func busyLaunchInsert(t *testing.T, s *Store, agent string) string {
	t.Helper()
	id := uuid.NewString()
	if e := s.InsertPending(Spawn{InstanceID: id, Agent: agent, CWD: t.TempDir(), TmuxSessionName: "ad-" + id, RelayMode: "off", LaunchStartedAtMillis: 1000, Identity: LaunchIdentity{Token: "0011223344556677"}}); e != nil {
		t.Fatal(e)
	}
	return id
}
func busyLaunchCount(t *testing.T, s *Store, q string, args ...any) int {
	t.Helper()
	var n int
	if e := s.db.QueryRow(q, args...).Scan(&n); e != nil {
		t.Fatal(e)
	}
	return n
}
func busyLaunchWantCount(t *testing.T, s *Store, want int, q string, args ...any) {
	t.Helper()
	if n := busyLaunchCount(t, s, q, args...); n != want {
		t.Fatalf("%s: got %d, want %d", q, n, want)
	}
}
func busyLaunchFinish(t *testing.T, s *Store, id string) Spawn {
	t.Helper()
	mustExec(t, s.db, `UPDATE spawns SET state='ended',turn_starts=7 WHERE instance_id=?`, id)
	return busyLaunchRow(t, s, id)
}
func busyLaunchCovered(t *testing.T, s *Store, id string) {
	t.Helper()
	mustExec(t, s.db, `UPDATE busy_conversations SET busy_send='covered' WHERE instance_id=?`, id)
}

// busyLaunchExit is id's exit scope as read, taken as positive exit evidence:
// the test judges every owned process in it gone (the store never probes).
func busyLaunchExit(t *testing.T, s *Store, id string) *BusySendExitEvidence {
	t.Helper()
	scope, e := s.ReadBusySendExitScope(id)
	if e != nil {
		t.Fatal(e)
	}
	return &BusySendExitEvidence{Scope: scope}
}

// busyExitIfAny is busyLaunchExit, or nil for a row with no exit scope to
// read (absent, or no published binding); an uncovered conversation ignores it.
func busyExitIfAny(t *testing.T, s *Store, id string) *BusySendExitEvidence {
	t.Helper()
	scope, e := s.ReadBusySendExitScope(id)
	if e != nil {
		return nil
	}
	return &BusySendExitEvidence{Scope: scope}
}

// busyWithExit is fresh carrying busyExitIfAny's evidence for id, as reuse
// hands it to ResetForReuse.
func busyWithExit(t *testing.T, s *Store, id string, fresh Spawn) Spawn {
	t.Helper()
	if fresh.BusySendExit == nil {
		fresh.BusySendExit = busyExitIfAny(t, s, id)
	}
	return fresh
}

// busyDeleteWithExit deletes id's row as administrator deletion does: with
// exit evidence when its conversation is covered, else plainly.
func busyDeleteWithExit(t *testing.T, s *Store, id string) error {
	t.Helper()
	ev := busyExitIfAny(t, s, id)
	if ev == nil || ev.Scope.Coverage != BusySendCovered {
		return s.DeleteSpawn(id)
	}
	row, e := s.GetSpawn(id)
	if e != nil {
		return e
	}
	res, e := s.DeleteBusySendCovered(id, row.Snapshot, *ev)
	if e == nil && res != CondApplied {
		e = fmt.Errorf("delete covered %s: %v; want applied", id, res)
	}
	return e
}

// busyLaunchDeleteCovered is administrator deletion of id's covered row once
// exit evidence is established (busyLaunchExit).
func busyLaunchDeleteCovered(t *testing.T, s *Store, id string) {
	t.Helper()
	row := busyLaunchRow(t, s, id)
	if res, e := s.DeleteBusySendCovered(id, row.Snapshot, *busyLaunchExit(t, s, id)); e != nil || res != CondApplied {
		t.Fatalf("delete covered=%v,%v", res, e)
	}
}

func busyLaunchStage(t *testing.T, s *Store, id, kind, agent, token string) int64 {
	t.Helper()
	row := busyLaunchRow(t, s, id)
	var res CondResult
	var v int64
	var e error
	if kind == "resume" {
		res, v, e = s.MoveToPendingWithExit(id, row.Snapshot, 2000, token, "", "", LaunchOwner{}, BusySendDefaultsChoice{}, busyLaunchExit(t, s, id))
	} else {
		prior, _, err := s.ReadForReuse(id)
		if err != nil {
			t.Fatal(err)
		}
		res, _, v, e = s.ResetForReuse(id, prior.Snapshot, prior.Agent, Spawn{Agent: agent, CWD: t.TempDir(), TmuxSessionName: row.TmuxSessionName, RelayMode: "off", StartedAt: time.Unix(2, 0), LaunchStartedAtMillis: 2000, Identity: LaunchIdentity{Token: token}, BusySendExit: busyLaunchExit(t, s, id)})
	}
	if e != nil || res != CondApplied {
		t.Fatalf("stage %s: %v, %v", kind, res, e)
	}
	return v
}

// busyLaunchSnapshots reads every retained/candidate snapshot independently of phase.
func busyLaunchSnapshots(t *testing.T, s *Store, id string) map[string]*BusySendLaunchCapabilities {
	t.Helper()
	rows, err := s.db.Query(`SELECT launch_id FROM busy_send_launches WHERE instance_id=? ORDER BY launch_id`, id)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	out := make(map[string]*BusySendLaunchCapabilities)
	for _, id := range ids {
		c, err := readBusySendLaunchCapabilities(context.Background(), s.db, s.StoreID(), id)
		if err != nil {
			t.Fatal(err)
		}
		out[id] = c
	}
	return out
}

// These sequential tests simulate a covering release's authoring table, never
// changing a snapshot after creation. Parallel tests run after cleanup.
func busyLaunchAuthor(t *testing.T, provider string, c *BusySendLaunchCapabilities) {
	t.Helper()
	prior := busySendCapabilityTable
	t.Cleanup(func() { busySendCapabilityTable = prior })
	next := append([]BusySendCapabilities(nil), prior...)
	row := BusySendCapabilitiesFor(provider)
	row.Provider, row.HeldCount, row.HeldBytes, row.Modes = provider, c.HeldCount, c.HeldBytes, c.Modes
	for i := range next {
		if next[i].Provider == provider {
			next[i] = row
			busySendCapabilityTable = next
			return
		}
	}
	busySendCapabilityTable = append(next, row)
}

// TestBusySendFreshLaunchAllAgents: a fresh Launch records this release's
// coverage for its agent, written out here (only Claude is covered), on the
// row, its conversation and its current Launch alike.
func TestBusySendFreshLaunchAllAgents(t *testing.T) {
	for agent, want := range map[string]BusySendCoverage{
		AgentClaude: BusySendCovered, AgentCodex: BusySendNotCovered, AgentCursor: BusySendNotCovered,
		AgentDroid: BusySendNotCovered, AgentOpenCode: BusySendNotCovered,
	} {
		t.Run(agent, func(t *testing.T) {
			s, path := openTempStore(t)
			id := busyLaunchInsert(t, s, agent)
			r := busyLaunchRow(t, s, id)
			provider := agent
			if r.BusySend != want || r.BusyConversationID == "" || r.TurnStarts != 0 {
				t.Fatalf("fresh row: %+v; want coverage %s", r, want)
			}
			busyLaunchWantCount(t, s, 1, `SELECT count(*) FROM busy_conversations c JOIN busy_send_launches l ON l.conversation_id=c.conversation_id WHERE c.instance_id=? AND c.publication='published' AND l.phase='current' AND c.provider=? AND l.provider=? AND c.busy_send=? AND l.busy_send=? AND l.launch_token=? AND l.life_number=?`, id, provider, provider, string(want), string(want), r.Identity.Token, r.LifeNumber)
			var expected *BusySendLaunchCapabilities
			if want == BusySendCovered {
				expected = v21ClaudeCapabilities()
			}
			for launch, got := range busyLaunchSnapshots(t, s, id) {
				if !reflect.DeepEqual(got, expected) {
					t.Fatalf("fresh snapshot %s = %+v; want %+v", launch, got, expected)
				}
			}
			reopened, e := Open(path)
			if e != nil {
				t.Fatal(e)
			}
			defer reopened.Close()
			if got := busyLaunchRow(t, reopened, id); got.BusyConversationID != r.BusyConversationID || got.BusySend != r.BusySend {
				t.Fatal("reopen changed recorded facts")
			}
		})
	}
}

func TestBusySendReadersUsePersistedFacts(t *testing.T) {
	s, path := openTempStore(t)
	id := busyLaunchInsert(t, s, AgentCodex)
	busyLaunchCovered(t, s, id) // Conversation differs from the current Launch and empty membership.
	binding := busyLaunchRow(t, s, id).BusyConversationID
	for _, missingBinding := range []bool{false, true} {
		var storedBinding any = binding
		wantBinding := binding
		if missingBinding {
			storedBinding, wantBinding = nil, ""
		}
		mustExec(t, s.db, `UPDATE spawns SET busy_conversation_id=? WHERE instance_id=?`, storedBinding, id)
		for _, want := range []BusySendCoverage{BusySendNotCovered, BusySendCovered} {
			mustExec(t, s.db, `UPDATE spawns SET busy_send=? WHERE instance_id=?`, string(want), id)
			tx, err := s.db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			var launch string
			if err := tx.QueryRow(`SELECT launch_id FROM busy_send_launches WHERE instance_id=?`, id).Scan(&launch); err != nil {
				t.Fatal(err)
			}
			var caps *BusySendLaunchCapabilities
			if want == BusySendCovered {
				caps = v21ClaudeCapabilities()
			}
			seedV21Capabilities(t, tx, launch, caps)
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			before := dbDump(t, path)
			got, e := s.GetBusySendCoverage(id)
			if e != nil || got != want {
				t.Fatalf("Launch coverage=%q,%v; want %q", got, e, want)
			}
			c, e := s.GetBusySendConversationCoverage(id)
			if missingBinding {
				if e == nil || c != "" || errors.Is(e, ErrSpawnNotFound) {
					t.Fatalf("missing conversation binding=%q,%v", c, e)
				}
			} else if e != nil || c != BusySendCovered {
				t.Fatalf("conversation=%q,%v", c, e)
			}

			for _, read := range []struct {
				name string
				call func() (Spawn, error)
			}{
				{"GetSpawn", func() (Spawn, error) { return s.GetSpawn(id) }},
				{"GetSpawnWithin", func() (Spawn, error) { return s.GetSpawnWithin(id, time.Second) }},
				{"GetSpawnSettled off", func() (Spawn, error) { return s.GetSpawnSettled(id, SettleRead{}) }},
				{"GetSpawnSettled on", func() (Spawn, error) { return s.GetSpawnSettled(id, SettleRead{SettleMs: 1000, NowMs: 2000}) }},
			} {
				got, e := read.call()
				if e != nil || got.BusySend != want || got.BusyConversationID != wantBinding {
					t.Fatalf("%s (NULL binding=%t)=%+v,%v; want %s/%q", read.name, missingBinding, got, e, want, wantBinding)
				}
			}
			for _, settle := range []SettleRead{{}, {SettleMs: 1000, NowMs: 2000}} {
				listed, e := s.ListSpawns(ListFilters{Settle: settle})
				if e != nil || len(listed) != 1 || listed[0].BusySend != want || listed[0].BusyConversationID != wantBinding {
					t.Fatalf("internal list (NULL binding=%t, settle=%+v)=%+v,%v", missingBinding, settle, listed, e)
				}
			}
			if !reflect.DeepEqual(before, dbDump(t, path)) {
				t.Fatal("coverage reads wrote facts")
			}
		}
	}
	if _, e := s.GetBusySendCoverage(uuid.NewString()); !errors.Is(e, ErrSpawnNotFound) {
		t.Fatalf("missing Launch=%v", e)
	}
	if _, e := s.GetBusySendConversationCoverage(uuid.NewString()); !errors.Is(e, ErrSpawnNotFound) {
		t.Fatalf("missing conversation=%v", e)
	}
	if e := s.Close(); e != nil {
		t.Fatal(e)
	}
	if got, e := s.GetBusySendCoverage(id); e == nil || got != "" || errors.Is(e, ErrSpawnNotFound) {
		t.Fatalf("closed read=%q,%v", got, e)
	}
}

func TestBusySendReaderCorruptionIsError(t *testing.T) {
	for _, tc := range []struct {
		name, q      string
		conversation bool
	}{
		{"invalid Launch code", `UPDATE spawns SET busy_send='invalid'`, false},
		{"missing binding", `UPDATE spawns SET busy_conversation_id=NULL`, true},
		{"orphan binding", `UPDATE spawns SET busy_conversation_id='missing'`, true},
		{"unpublished binding", `UPDATE busy_conversations SET publication='abandoned'`, true},
		{"wrong owner", `UPDATE busy_conversations SET instance_id='another'`, true},
		{"wrong store", `UPDATE busy_conversations SET store_id='0000000000000000'`, true},
		{"invalid conversation code", `UPDATE busy_conversations SET busy_send='invalid'`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := openTempStore(t)
			id := busyLaunchInsert(t, s, AgentClaude)
			s.db.SetMaxOpenConns(1)
			mustExec(t, s.db, `PRAGMA ignore_check_constraints=ON`)
			mustExec(t, s.db, tc.q)
			var got BusySendCoverage
			var e error
			if tc.conversation {
				got, e = s.GetBusySendConversationCoverage(id)
			} else {
				got, e = s.GetBusySendCoverage(id)
			}
			if e == nil || got != "" || errors.Is(e, ErrSpawnNotFound) {
				t.Fatalf("corrupt read=%q,%v", got, e)
			}
			if !tc.conversation {
				if _, e := s.GetSpawn(id); e == nil || errors.Is(e, ErrSpawnNotFound) {
					t.Fatalf("full-row read accepted invalid Launch coverage: %v", e)
				}
			}
		})
	}
}

// TestBusySendStagedPublication: a staged resume or reuse leaves the published
// Launch's projection in place until its own token publishes the candidate,
// whose coverage is the new Launch's: a resume keeps its covered conversation's
// coverage even for an agent the set doesn't cover, and a reuse takes its new
// agent's.
func TestBusySendStagedPublication(t *testing.T) {
	for _, tc := range []struct {
		kind, base, agent string
		lostReply         bool
		published, want   BusySendCoverage
	}{
		{"resume", AgentClaude, AgentClaude, false, BusySendCovered, BusySendCovered},
		{"resume", AgentCodex, AgentCodex, false, BusySendNotCovered, BusySendCovered},
		{"reuse", AgentClaude, AgentClaude, false, BusySendCovered, BusySendCovered},
		{"reuse", AgentClaude, AgentCodex, true, BusySendCovered, BusySendNotCovered},
	} {
		t.Run(tc.kind+tc.base+tc.agent, func(t *testing.T) {
			s, path := openTempStore(t)
			id := busyLaunchInsert(t, s, tc.base)
			old := busyLaunchFinish(t, s, id)
			busyLaunchCovered(t, s, id)
			const token = "1122334455667788"
			oldSnapshots := busyLaunchSnapshots(t, s, id)
			if tc.base == AgentCodex {
				before := dbDump(t, path)
				_, _, err := s.MoveToPendingWithExit(id, old.Snapshot, 2000, token, "", "", LaunchOwner{}, BusySendDefaultsChoice{}, busyLaunchExit(t, s, id))
				if err == nil || !strings.Contains(err.Error(), "no covering capability table row") || !reflect.DeepEqual(before, dbDump(t, path)) {
					t.Fatalf("covered resume without an author = %v; want refusal without writes", err)
				}
			}
			candidate := v21ClaudeCapabilities()
			candidate.HeldCount, candidate.HeldBytes = 7, 16384
			candidate.Modes[0].MessageBytes, candidate.Modes[1].MessageBytes = 512, 256
			if tc.want == BusySendCovered {
				busyLaunchAuthor(t, tc.agent, candidate)
			}
			v := busyLaunchStage(t, s, id, tc.kind, tc.agent, token)
			staged := busyLaunchRow(t, s, id)
			if staged.BusyConversationID != old.BusyConversationID || staged.BusySend != tc.published || staged.TurnStarts != 0 {
				t.Fatalf("stage changed published projection (coverage %s, want %s) or failed counter reset", staged.BusySend, tc.published)
			}
			wantCoverage := string(tc.want)
			busyLaunchWantCount(t, s, 1, `SELECT count(*) FROM busy_send_launches WHERE instance_id=? AND phase='candidate' AND busy_send=?`, id, wantCoverage)
			snapshots := busyLaunchSnapshots(t, s, id)
			if len(snapshots) != 2 {
				t.Fatalf("staged snapshots: %+v", snapshots)
			}
			for launch, got := range snapshots {
				want, old := oldSnapshots[launch]
				if !old && tc.want == BusySendCovered {
					want = candidate
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("staged snapshot %s = %+v; want %+v", launch, got, want)
				}
			}
			if tc.published == BusySendCovered {
				if a, err := s.AdmitBusySend(BusySendAdmissionRequest{InstanceID: id, Text: []byte(strings.Repeat("x", 513))}); err != nil || a.Outcome != BusySendAdmitted {
					t.Fatalf("staged admission did not use old published limits: %+v, %v", a, err)
				}
			}

			before := dbDump(t, path)
			// A callback for another token must leave the entire staged write alone.

			res, e := s.RecordLaunchIdentity(id, v, "ffffffffffffffff", LaunchIdentity{})
			if e != nil || res != CondChanged {
				t.Fatalf("wrong-token identity=%v,%v", res, e)
			}
			res, e = s.PublishBusySendLaunch(id, v, "ffffffffffffffff")
			if e != nil || res != CondChanged {
				t.Fatalf("wrong-token publication=%v,%v", res, e)
			}

			if !reflect.DeepEqual(before, dbDump(t, path)) {
				t.Fatal("stale publication mutated facts")
			}
			// A second writer commits conversation facts during external creation.
			other, e := Open(path)
			if e != nil {
				t.Fatal(e)
			}
			mustExec(t, other.db, `UPDATE busy_conversations SET accepted_seq=9,revision=revision+1 WHERE conversation_id=?`, old.BusyConversationID)
			mustExec(t, other.db, `UPDATE busy_send_launches SET ended_at_ms=42,end_cause='process_exited',revision=revision+1 WHERE instance_id=? AND phase='current'`, id)
			_ = other.Close()
			if tc.lostReply {
				// An unrelated observation/adoption may advance the row while
				// creation succeeds; its token still owns publication.
				if e = s.SetParentID(id, ""); e != nil {
					t.Fatal(e)
				}
				if e = s.MarkBusySendLaunchUncertain(id, token); e != nil {
					t.Fatal(e)
				}
				res, e = s.PublishBusySendLaunch(id, v, token)
			} else {
				res, e = s.RecordLaunchIdentity(id, v, token, LaunchIdentity{})
			}
			if e != nil || res != CondApplied {
				t.Fatalf("publication=%v,%v", res, e)
			}
			now := busyLaunchRow(t, s, id)
			if string(now.BusySend) != wantCoverage {
				t.Fatalf("published coverage=%s", now.BusySend)
			}
			if (now.BusyConversationID == old.BusyConversationID) != (tc.kind == "resume") {
				t.Fatal("wrong conversation retention")
			}
			busyLaunchWantCount(t, s, 1, `SELECT count(*) FROM busy_send_launches WHERE instance_id=? AND phase='current' AND launch_token=?`, id, token)
			busyLaunchWantCount(t, s, 1, `SELECT count(*) FROM busy_send_launches WHERE instance_id=? AND phase='superseded' AND ended_at_ms=42 AND end_cause='process_exited'`, id)
			busyLaunchWantCount(t, s, 1, `SELECT count(*) FROM busy_conversations WHERE conversation_id=? AND accepted_seq=9`, old.BusyConversationID)
			if tc.kind == "reuse" {
				busyLaunchWantCount(t, s, 1, `SELECT count(*) FROM busy_conversations WHERE conversation_id=? AND publication='retired' AND retired_reason='conversation_replaced' AND replaced_by=?`, old.BusyConversationID, now.BusyConversationID)
			}
			wantVersion := v + 1
			if tc.lostReply {
				wantVersion++
			}
			if now.RowVersion != wantVersion {
				t.Fatalf("publication version=%d; want %d", now.RowVersion, wantVersion)
			}
			if !reflect.DeepEqual(snapshots, busyLaunchSnapshots(t, s, id)) {
				t.Fatal("publication rewrote a snapshot")
			}

			_, err := s.AdmitBusySend(BusySendAdmissionRequest{InstanceID: id, Text: []byte(strings.Repeat("x", 513))})
			var refusal *BusySendRefusal
			wantReason := BusySendRefuseTextBytes
			var wantLimit *BusySendLimit = &BusySendLimit{Actual: 513, Allowed: 512}
			if tc.want == BusySendNotCovered {
				wantReason, wantLimit = BusySendRefuseAgentNotCovered, nil
			}
			if !errors.As(err, &refusal) || refusal.Reason != wantReason || !reflect.DeepEqual(refusal.Limit, wantLimit) {
				t.Fatalf("published admission = %v; want %s %+v", err, wantReason, wantLimit)
			}

			committed := dbDump(t, path)
			if res, e = s.PublishBusySendLaunch(id, v, token); e != nil || res != CondApplied {
				t.Fatalf("idempotent publication=%v,%v", res, e)
			}
			if !reflect.DeepEqual(committed, dbDump(t, path)) {
				t.Fatal("duplicate success republished core")
			}

		})
	}
}

func TestBusySendLifecycleTransactionsRollback(t *testing.T) {
	for _, kind := range []string{"resume", "reuse"} {
		for _, point := range []string{"stage", "publish", "restore"} {
			t.Run(kind+point, func(t *testing.T) {
				s, path := openTempStore(t)
				id := busyLaunchInsert(t, s, AgentClaude)
				old := busyLaunchFinish(t, s, id)
				busyLaunchCovered(t, s, id)
				prior, _, err := s.ReadForReuse(id)
				if err != nil {
					t.Fatal(err)
				}
				var v int64
				if point != "stage" {
					v = busyLaunchStage(t, s, id, kind, AgentCodex, "1122334455667788")
				}
				trigger := `CREATE TRIGGER fail_busy BEFORE INSERT ON busy_send_launches WHEN NEW.phase='candidate' BEGIN SELECT RAISE(ABORT,'injected busy failure'); END`
				if point != "stage" {
					trigger = `CREATE TRIGGER fail_busy BEFORE UPDATE ON busy_send_launches BEGIN SELECT RAISE(ABORT,'injected busy failure'); END`
				}
				mustExec(t, s.db, trigger)
				before := dbDump(t, path)
				var e error
				switch point {
				case "stage":
					if kind == "resume" {
						_, _, e = s.MoveToPendingWithExit(id, old.Snapshot, 2000, "1122334455667788", "", "", LaunchOwner{}, BusySendDefaultsChoice{}, busyLaunchExit(t, s, id))
					} else {
						prior, _, err := s.ReadForReuse(id)
						if err != nil {
							t.Fatal(err)
						}
						_, _, _, e = s.ResetForReuse(id, prior.Snapshot, prior.Agent, Spawn{Agent: AgentCodex, CWD: "/tmp", TmuxSessionName: old.TmuxSessionName, RelayMode: "off", BusySendExit: busyLaunchExit(t, s, id)})
					}
				case "publish":
					_, e = s.RecordLaunchIdentity(id, v, "1122334455667788", LaunchIdentity{})
				case "restore":
					if kind == "resume" {
						_, e = s.RestoreAfterFailedResume(id, v, ResumePrior{State: old.State, Identity: old.Identity, TurnStarts: old.TurnStarts})
					} else {
						_, e = s.RestoreAfterFailedReuse(id, v, prior.Life, time.Unix(3, 0))
					}
				}
				if e == nil || !strings.Contains(e.Error(), "injected busy failure") {
					t.Fatalf("expected injected failure, got %v", e)
				}
				if !reflect.DeepEqual(before, dbDump(t, path)) {
					t.Fatal("transaction failure changed row/core/history")
				}
			})
		}
	}
}

func TestBusySendResumeCannotChangeProvider(t *testing.T) {
	s, path := openTempStore(t)
	id := busyLaunchInsert(t, s, AgentClaude)
	busyLaunchFinish(t, s, id)
	mustExec(t, s.db, `UPDATE spawns SET agent='codex' WHERE instance_id=?`, id)
	row := busyLaunchRow(t, s, id)
	before := dbDump(t, path)
	_, _, e := s.MoveToPendingWithExit(id, row.Snapshot, 2000, "1122334455667788", "", "", LaunchOwner{}, BusySendDefaultsChoice{}, busyExitIfAny(t, s, id))
	if e == nil || !strings.Contains(e.Error(), "resume changes busy-send provider") {
		t.Fatalf("changed provider=%v", e)
	}
	if !reflect.DeepEqual(before, dbDump(t, path)) {
		t.Fatal("refused provider change mutated facts")
	}
}

func TestBusySendRestoreAndUncertainCandidates(t *testing.T) {
	for _, kind := range []string{"resume", "reuse"} {
		t.Run(kind, func(t *testing.T) {
			s, path := openTempStore(t)
			id := busyLaunchInsert(t, s, AgentClaude)
			old := busyLaunchFinish(t, s, id)
			busyLaunchCovered(t, s, id)
			prior, _, e := s.ReadForReuse(id)
			if e != nil {
				t.Fatal(e)
			}
			oldSnapshots := busyLaunchSnapshots(t, s, id)
			candidate := v21ClaudeCapabilities()
			candidate.HeldCount = 9
			candidate.Modes[0].MessageBytes = 512
			busyLaunchAuthor(t, AgentClaude, candidate)
			v := busyLaunchStage(t, s, id, kind, AgentClaude, "1122334455667788")
			restore := func(version int64) (CondResult, error) {
				if kind == "resume" {
					return s.RestoreAfterFailedResume(id, version, ResumePrior{State: old.State, Identity: old.Identity, TurnStarts: 7})
				}
				return s.RestoreAfterFailedReuse(id, version, prior.Life, time.Unix(3, 0))
			}
			for launch, want := range oldSnapshots {
				if !reflect.DeepEqual(busyLaunchSnapshots(t, s, id)[launch], want) {
					t.Fatal("staging rewrote the old snapshot")
				}
			}
			snapshots := busyLaunchSnapshots(t, s, id)
			before := dbDump(t, path)
			res, e := restore(v - 1)
			if e != nil || res != CondChanged {
				t.Fatalf("stale restore=%v,%v", res, e)
			}
			if !reflect.DeepEqual(before, dbDump(t, path)) {
				t.Fatal("stale restore mutated core")
			}
			res, e = restore(v)
			if e != nil || res != CondApplied {
				t.Fatalf("restore=%v,%v", res, e)
			}
			if !reflect.DeepEqual(snapshots, busyLaunchSnapshots(t, s, id)) {
				t.Fatal("restoration rewrote snapshots")
			}
			row := busyLaunchRow(t, s, id)
			if row.BusyConversationID != old.BusyConversationID || row.BusySend != old.BusySend || row.TurnStarts != 7 {
				t.Fatal("restore lost published facts/counter")
			}
			busyLaunchWantCount(t, s, 1, `SELECT count(*) FROM busy_conversations WHERE conversation_id=? AND publication='published' AND busy_send='covered'`, old.BusyConversationID)
			busyLaunchWantCount(t, s, 1, `SELECT count(*) FROM busy_send_launches WHERE instance_id=? AND phase='failed' AND end_cause='creation_failed'`, id)
			if kind == "reuse" {
				busyLaunchWantCount(t, s, 1, `SELECT count(*) FROM busy_conversations WHERE instance_id=? AND publication='abandoned'`, id)
			}
			v = busyLaunchStage(t, s, id, kind, AgentClaude, "2233445566778899")
			snapshots = busyLaunchSnapshots(t, s, id)
			if e = s.MarkBusySendLaunchUncertain(id, "2233445566778899"); e != nil {
				t.Fatal(e)
			}
			busyLaunchWantCount(t, s, 1, `SELECT count(*) FROM busy_send_launches WHERE instance_id=? AND phase='uncertain'`, id)
			if !reflect.DeepEqual(snapshots, busyLaunchSnapshots(t, s, id)) {
				t.Fatal("uncertainty rewrote snapshots")
			}
			before = dbDump(t, path)
			if e = s.MarkBusySendLaunchUncertain(id, "1122334455667788"); e != nil {
				t.Fatal(e)
			}
			res, e = restore(v - 1)
			if e != nil || res != CondChanged || !reflect.DeepEqual(before, dbDump(t, path)) {
				t.Fatal("stale callback changed uncertain candidate")
			}
			// Only a later guarded start, after lifecycle exclusion, may close uncertainty.
			busyLaunchFinish(t, s, id)
			busyLaunchStage(t, s, id, kind, AgentClaude, "3344556677889900")
			busyLaunchWantCount(t, s, 1, `SELECT count(*) FROM busy_send_launches WHERE instance_id=? AND launch_token='2233445566778899' AND phase='superseded' AND end_cause='candidate_replaced'`, id)
			before = dbDump(t, path)
			res, e = s.PublishBusySendLaunch(id, v, "2233445566778899")
			if e != nil || res != CondChanged || !reflect.DeepEqual(before, dbDump(t, path)) {
				t.Fatal("old publication replaced newer candidate")
			}
		})
	}
}

// Delete has two callers: guarded expiry and unconditional admin deletion.
// Keep the retained custody/force/claim fixtures byte-for-byte: record_deleted
// is logical deletion, never process-exit proof. The fixture's standing claim
// has already ended launch_end at the reuse staging's exit evidence (SR-6.5).
func TestBusySendDeletionRetirementAndRespawn(t *testing.T) {
	for _, mode := range []string{"expire", "admin"} {
		for _, noCore := range []bool{false, true} {
			t.Run(mode+map[bool]string{true: "-no-core", false: "-retained"}[noCore], func(t *testing.T) {
				s, path := openTempStore(t)
				id := busyLaunchInsert(t, s, AgentClaude)
				if noCore {
					tx, err := s.db.Begin()
					if err != nil {
						t.Fatal(err)
					}
					defer tx.Rollback()
					for _, table := range []string{"busy_send_launch_escapes", "busy_send_launch_modes"} {
						if _, err := tx.Exec("DELETE FROM "+table+" WHERE launch_id IN (SELECT launch_id FROM busy_send_launches WHERE instance_id=?)", id); err != nil {
							t.Fatal(err)
						}
					}
					for _, table := range []string{"busy_send_launches", "busy_conversations"} {
						if _, err := tx.Exec("DELETE FROM "+table+" WHERE instance_id=?", id); err != nil {
							t.Fatal(err)
						}
					}
					if err := tx.Commit(); err != nil {
						t.Fatal(err)
					}
				}
				var rec busySendRecords
				if !noCore {
					rec = seedBusySendRecords(t, s.db, s.StoreID(), id)
					mustExec(t, s.db, `UPDATE spawns SET busy_conversation_id=?,busy_send='covered',life_number=?,launch_token=? WHERE instance_id=?`, rec.conversation, recLife, recToken, id)
					// Existing End evidence is retained; a second current/candidate gets deletion-only evidence.
					mustExec(t, s.db, `UPDATE busy_send_launches SET ended_at_ms=42,end_cause='process_exited' WHERE launch_id=?`, rec.launch)
				}
				old := busyLaunchFinish(t, s, id)
				if !noCore {
					busyLaunchStage(t, s, id, "reuse", AgentCodex, "1122334455667788")
					busyLaunchWantCount(t, s, 1, `SELECT count(*) FROM busy_send_pause_claims WHERE claim_id=? AND state='ended' AND closed_by='launch_end'`, rec.standingClaim)
					if e := s.MarkBusySendLaunchUncertain(id, "1122334455667788"); mode == "admin" && e != nil {
						t.Fatal(e)
					}
					old = busyLaunchFinish(t, s, id)
					// Expire keeps a row whose conversation still holds a Held
					// Message (SR-5.5), and covered deletion finalizes it; this
					// case checks the other records' retention, so the fixture's
					// Held Message is first an expired tombstone.
					mustExec(t, s.db, `UPDATE busy_send_messages SET text = NULL, text_sha256 = NULL, place = 'closed', result_class = NULL,
						result_reason = NULL, wait_reason = NULL, wait_since_ms = NULL, result_final = 1, final_at_ms = 5000,
						retention = 'expired', expired_at_ms = 9000 WHERE message_id = ?`, rec.held)
				}
				snapshots := busyLaunchSnapshots(t, s, id)
				before := dbDump(t, path)
				remove := func(snapshot RowSnapshot) (CondResult, error) {
					if mode == "expire" {
						return s.DeleteFinishedIfSameLife(id, snapshot)
					}
					if !noCore {
						// A covered conversation is deleted only with exit evidence.
						return s.DeleteBusySendCovered(id, snapshot, *busyLaunchExit(t, s, id))
					}
					e := s.DeleteSpawn(id)
					return CondApplied, e
				}
				if mode == "expire" {
					mustExec(t, s.db, `UPDATE spawns SET row_version=row_version+1 WHERE instance_id=?`, id)
					stale := dbDump(t, path)
					res, e := remove(old.Snapshot)
					if e != nil || res != CondChanged || !reflect.DeepEqual(stale, dbDump(t, path)) {
						t.Fatal("stale expiry changed records")
					}
					old = busyLaunchRow(t, s, id)
				}
				res, e := remove(old.Snapshot)
				if e != nil || res != CondApplied {
					t.Fatalf("delete=%v,%v", res, e)
				}
				if _, e = s.GetSpawn(id); !errors.Is(e, ErrSpawnNotFound) {
					t.Fatalf("deleted row=%v", e)
				}
				if !noCore {
					busyLaunchWantCount(t, s, 1, `SELECT count(*) FROM busy_conversations WHERE instance_id=? AND publication='retired' AND retired_reason='record_deleted'`, id)
					busyLaunchWantCount(t, s, 1, `SELECT count(*) FROM busy_conversations WHERE instance_id=? AND publication='abandoned'`, id)
					busyLaunchWantCount(t, s, 1, `SELECT count(*) FROM busy_send_launches WHERE instance_id=? AND phase='superseded' AND end_cause='record_deleted' AND ended_at_ms IS NOT NULL`, id)
					busyLaunchWantCount(t, s, 1, `SELECT count(*) FROM busy_send_launches WHERE instance_id=? AND phase='superseded' AND end_cause='process_exited' AND ended_at_ms=42`, id)
					if _, e = s.db.Exec(`UPDATE busy_send_launches SET phase='current' WHERE instance_id=? AND end_cause='record_deleted'`, id); e == nil {
						t.Fatal("record_deleted accepted as current")
					}
				}
				if mode == "expire" {
					res, e = remove(old.Snapshot)
					if e != nil || res != CondAbsent {
						t.Fatal("repeat expiry must be absent")
					}
				} else if e = s.DeleteSpawn(id); !errors.Is(e, ErrSpawnNotFound) {
					t.Fatalf("repeat delete=%v", e)
				}
				// The respawn's agent is one the set doesn't cover, so inheriting
				// the retired conversation's coverage would show.
				if e = s.InsertPending(Spawn{InstanceID: id, Agent: AgentCodex, CWD: "/tmp", TmuxSessionName: "ad-" + id, RelayMode: "off"}); e != nil {
					t.Fatal(e)
				}
				fresh := busyLaunchRow(t, s, id)
				if fresh.BusyConversationID == old.BusyConversationID || fresh.BusySend != BusySendNotCovered {
					t.Fatal("respawn inherited retired conversation/coverage")
				}
				busyLaunchWantCount(t, s, 1, `SELECT count(*) FROM busy_send_launches WHERE instance_id=? AND phase='current'`, id)
				if !noCore {
					busyLaunchWantCount(t, s, 1, `SELECT count(*) FROM busy_conversations WHERE instance_id=? AND publication='published' AND generation=4`, id)
				}
				afterSnapshots := busyLaunchSnapshots(t, s, id)
				for launch, want := range snapshots {
					got, ok := afterSnapshots[launch]
					if !ok || !reflect.DeepEqual(got, want) {
						t.Fatal("delete/respawn changed retained capabilities")
					}
				}
				after := dbDump(t, path)
				for _, table := range []string{"busy_send_messages", "busy_send_results", "busy_send_forces", "busy_send_force_members", "busy_send_pause_claims", "busy_send_handovers", "busy_send_handover_members", "busy_send_prompts", "busy_send_launch_processes", "busy_send_launch_modes", "busy_send_launch_escapes"} {
					if !reflect.DeepEqual(before["rows "+table], after["rows "+table]) {
						t.Errorf("delete/respawn changed retained %s", table)
					}
				}
				busyLaunchWantCount(t, s, 0, `SELECT count(*) FROM pragma_foreign_key_check`)
			})
		}
	}
}

func TestBusySendDeletionRollback(t *testing.T) {
	for _, mode := range []string{"expire", "admin"} {
		t.Run(mode, func(t *testing.T) {
			s, path := openTempStore(t)
			id := busyLaunchInsert(t, s, AgentClaude)
			old := busyLaunchFinish(t, s, id)
			mustExec(t, s.db, `CREATE TRIGGER fail_retirement BEFORE UPDATE ON busy_conversations BEGIN SELECT RAISE(ABORT,'injected retirement failure'); END`)
			exit := busyLaunchExit(t, s, id)
			before := dbDump(t, path)
			var e error
			if mode == "expire" {
				_, e = s.DeleteFinishedIfSameLife(id, old.Snapshot)
			} else {
				// The fresh Claude row is covered: administrator deletion
				// goes through its exit evidence.
				_, e = s.DeleteBusySendCovered(id, old.Snapshot, *exit)
			}
			if e == nil || !strings.Contains(e.Error(), "injected retirement failure") {
				t.Fatalf("delete=%v", e)
			}
			if !reflect.DeepEqual(before, dbDump(t, path)) {
				t.Fatal("failed retirement did not roll back row/core deletion")
			}
		})
	}
}

// CR1: send-keys/recovery can adopt the successfully created pane before the
// creator records its reply. Publication must catch up with that verified
// success, retain intervening conversation facts, and tolerate stale replies.
func TestBusySendAdoptionPublishesCandidate(t *testing.T) {
	for _, kind := range []string{"resume", "reuse"} {
		for _, recovery := range []string{"command", "same-life", "recovery"} {
			t.Run(kind+"-"+recovery, func(t *testing.T) {
				s, path := openTempStore(t)
				id := busyLaunchInsert(t, s, AgentClaude)
				old := busyLaunchFinish(t, s, id)
				busyLaunchCovered(t, s, id)
				v := busyLaunchStage(t, s, id, kind, AgentClaude, "1122334455667788")
				mustExec(t, s.db, `UPDATE busy_conversations SET accepted_seq=12,revision=revision+1 WHERE conversation_id=?`, old.BusyConversationID)
				row := busyLaunchRow(t, s, id)
				var res CondResult
				var e error
				if recovery == "same-life" {
					res, _, e = s.AdoptIdentityIfSameLife(id, row.Snapshot, testPane())
				} else if recovery == "recovery" {
					res, _, e = s.RecoveryWrites(row).AdoptIdentityIfSameLife(id, row.Snapshot, testPane())
				} else {
					res, e = s.AdoptIdentityIfUnchanged(id, row.Snapshot, testPane())
				}
				if e != nil || res != CondApplied {
					t.Fatalf("adoption=%v,%v", res, e)
				}
				// A Claude Launch, resumed or reused, is covered.
				want := "covered"
				busyLaunchWantCount(t, s, 1, `SELECT count(*) FROM busy_send_launches WHERE instance_id=? AND launch_token='1122334455667788' AND phase='current' AND busy_send=?`, id, want)
				published := busyLaunchRow(t, s, id)
				if string(published.BusySend) != want {
					t.Fatalf("adoption left old coverage: %+v", published)
				}
				if (published.BusyConversationID == old.BusyConversationID) != (kind == "resume") {
					t.Fatal("adoption published wrong conversation")
				}
				busyLaunchWantCount(t, s, 1, `SELECT count(*) FROM busy_conversations WHERE conversation_id=? AND accepted_seq=12`, old.BusyConversationID)
				// A late original-version creator reply and uncertainty marker are harmless.
				res, e = s.RecordLaunchIdentity(id, v, "1122334455667788", testPane())
				if e != nil || res != CondChanged {
					t.Fatalf("late creator=%v,%v", res, e)
				}
				if e = s.MarkBusySendLaunchUncertain(id, "1122334455667788"); e != nil {
					t.Fatal(e)
				}
				before := dbDump(t, path)
				if e = agentSessionStart(s, id, "sess-adopted", "/tmp/adopted.jsonl", true); e != nil {
					t.Fatal(e)
				}
				// Hook state/session facts may change; already-published core facts do not.
				after := dbDump(t, path)
				for _, table := range []string{"busy_send_launches", "busy_conversations", "busy_send_launch_modes", "busy_send_launch_escapes"} {
					if !reflect.DeepEqual(before["rows "+table], after["rows "+table]) {
						t.Errorf("hook republished %s", table)
					}
				}
			})
		}
	}
}

// CR1: a transient publication error leaves no partial identity or binding;
// verified adoption or an authenticated report-in can resolve it later.
func TestBusySendPublicationFailureCanRecover(t *testing.T) {
	for _, via := range []string{"adoption", "hook"} {
		t.Run(via, func(t *testing.T) {
			s, path := openTempStore(t)
			id := busyLaunchInsert(t, s, AgentClaude)
			busyLaunchFinish(t, s, id)
			busyLaunchCovered(t, s, id)
			v := busyLaunchStage(t, s, id, "resume", AgentClaude, "1122334455667788")
			mustExec(t, s.db, `CREATE TRIGGER fail_publication BEFORE UPDATE ON busy_send_launches BEGIN SELECT RAISE(ABORT,'transient publication failure'); END`)
			before := dbDump(t, path)
			if _, e := s.RecordLaunchIdentity(id, v, "1122334455667788", testPane()); e == nil {
				t.Fatal("publication did not fail")
			}
			if !reflect.DeepEqual(before, dbDump(t, path)) {
				t.Fatal("failed publication wrote partial identity/core")
			}
			mustExec(t, s.db, `DROP TRIGGER fail_publication`)
			if e := s.MarkBusySendLaunchUncertain(id, "1122334455667788"); e != nil {
				t.Fatal(e)
			}
			if via == "adoption" {
				row := busyLaunchRow(t, s, id)
				if res, e := s.AdoptIdentityIfUnchanged(id, row.Snapshot, testPane()); e != nil || res != CondApplied {
					t.Fatalf("retry adoption=%v,%v", res, e)
				}
			} else {
				// A row whose identity was established independently (e.g. adoption by an
				// earlier binary) still has to publish when its authenticated agent reports.
				pane := testPane()
				mustExec(t, s.db, `UPDATE spawns SET pane_id=?,pane_pid=?,pane_starttime=? WHERE instance_id=?`, pane.PaneID, pane.PanePID, pane.PaneStarttime, id)
				if e := agentSessionStart(s, id, "sess-recovered", "/tmp/recovered.jsonl", true); e != nil {
					t.Fatal(e)
				}
			}
			row := busyLaunchRow(t, s, id)
			if row.BusySend != BusySendCovered {
				t.Fatal("recovery left old coverage")
			}
			busyLaunchWantCount(t, s, 1, `SELECT count(*) FROM busy_send_launches WHERE instance_id=? AND phase='current' AND launch_token='1122334455667788'`, id)
		})
	}
}

func TestBusySendStaleSuccessCannotPublishReplacement(t *testing.T) {
	for _, deleted := range []bool{false, true} {
		t.Run(map[bool]string{false: "replacement", true: "deleted-respawn"}[deleted], func(t *testing.T) {
			s, path := openTempStore(t)
			id := busyLaunchInsert(t, s, AgentClaude)
			busyLaunchFinish(t, s, id)
			busyLaunchCovered(t, s, id)
			v := busyLaunchStage(t, s, id, "reuse", AgentCodex, "1122334455667788")
			stale := busyLaunchRow(t, s, id)
			if deleted {
				busyLaunchDeleteCovered(t, s, id)
				// The absent row accepts neither publication nor adoption.
				if res, e := s.PublishBusySendLaunch(id, v, "1122334455667788"); e != nil || res != CondAbsent {
					t.Fatalf("absent publication=%v,%v", res, e)
				}
				if e := s.InsertPending(Spawn{InstanceID: id, Agent: AgentClaude, CWD: "/tmp", TmuxSessionName: "ad-" + id, RelayMode: "off", Identity: LaunchIdentity{Token: "2233445566778899"}}); e != nil {
					t.Fatal(e)
				}
			} else {
				busyLaunchFinish(t, s, id)
				busyLaunchStage(t, s, id, "reuse", AgentClaude, "2233445566778899")
			}
			before := dbDump(t, path)
			if res, e := s.RecordLaunchIdentity(id, v, "1122334455667788", testPane()); e != nil || res != CondChanged {
				t.Fatalf("stale identity=%v,%v", res, e)
			}
			if res, e := s.PublishBusySendLaunch(id, v, "1122334455667788"); e != nil || res != CondChanged {
				t.Fatalf("stale publish=%v,%v", res, e)
			}
			if res, e := s.AdoptIdentityIfUnchanged(id, stale.Snapshot, testPane()); e != nil || res != CondChanged {
				t.Fatalf("stale adoption=%v,%v", res, e)
			}
			if e := s.MarkBusySendLaunchUncertain(id, "1122334455667788"); e != nil {
				t.Fatal(e)
			}
			gate := agentGate("SessionStart", "sess-stale")
			gate.SessionStart = true
			gate.Examined = stale.Snapshot
			if applied, _, e := s.RecordSessionStartIdentity(id, gate, "/tmp/stale.jsonl", true); e != nil || applied.Applied {
				t.Fatalf("stale hook=%+v,%v", applied, e)
			}
			if !reflect.DeepEqual(before, dbDump(t, path)) {
				t.Fatal("stale callback changed replacement/deleted core")
			}
		})
	}
}

func TestBusySendFreshInsertRollback(t *testing.T) {
	s, path := openTempStore(t)
	mustExec(t, s.db, `CREATE TRIGGER fail_fresh BEFORE INSERT ON busy_send_launches BEGIN SELECT RAISE(ABORT,'injected fresh Launch failure'); END`)
	before := dbDump(t, path)
	e := s.InsertPending(Spawn{InstanceID: uuid.NewString(), Agent: AgentCodex, CWD: t.TempDir(), TmuxSessionName: "fresh", RelayMode: "off"})
	if e == nil || !strings.Contains(e.Error(), "injected fresh Launch failure") {
		t.Fatalf("insert=%v", e)
	}
	if !reflect.DeepEqual(before, dbDump(t, path)) {
		t.Fatal("failed fresh insertion left a row or conversation")
	}
}

// Each independent authenticated report-in writer can be the first evidence
// of success for an unresolved historical candidate (CR1).
func TestBusySendFirstReportInPublishes(t *testing.T) {
	for _, event := range []string{"SessionStart", "UserPromptSubmit", "Notification", "PermissionRequest"} {
		t.Run(event, func(t *testing.T) {
			s, _ := openTempStore(t)
			id := busyLaunchInsert(t, s, AgentClaude)
			busyLaunchFinish(t, s, id)
			busyLaunchCovered(t, s, id)
			busyLaunchStage(t, s, id, "resume", AgentClaude, "1122334455667788")
			pane := testPane()
			mustExec(t, s.db, `UPDATE spawns SET pane_id=?,pane_pid=?,pane_starttime=?,relay_mode='on' WHERE instance_id=?`, pane.PaneID, pane.PanePID, pane.PaneStarttime, id)
			var e error
			switch event {
			case "SessionStart":
				e = agentSessionStart(s, id, "sess-first", "/tmp/first.jsonl", true)
			case "UserPromptSubmit":
				e = agentHook(s, id, StateWorking, false, event)
			case "Notification":
				mustExec(t, s.db, `UPDATE spawns SET state='working' WHERE instance_id=?`, id)
				_, applied, err := s.ApplyHookWaitingIfWorking(id, agentGate(event, ""), event, "", false)
				e = err
				if e == nil && !applied.Applied {
					t.Fatal("waiting hook did not apply")
				}
			case "PermissionRequest":
				_, applied, err := s.InsertRelayRequest(id, agentGate(event, ""), RelayRequest{RequestToken: uuid.NewString(), ToolName: "Read", ToolInput: "{}"}, 10, time.Second)
				e = err
				if e == nil && !applied.Applied {
					t.Fatal("relay hook did not apply")
				}
			}
			if e != nil {
				t.Fatal(e)
			}
			if row := busyLaunchRow(t, s, id); row.BusySend != BusySendCovered {
				t.Fatal("report-in retained old coverage")
			}
			busyLaunchWantCount(t, s, 1, `SELECT count(*) FROM busy_send_launches WHERE instance_id=? AND launch_token='1122334455667788' AND phase='current'`, id)
		})
	}
}

// PM1: failures after some child rows exist roll back the complete creation,
// including spawn and both staged creation paths, not just the Launch row.
func TestBusySendCapabilityCreationRollback(t *testing.T) {
	for _, kind := range []string{"spawn", "resume", "reuse"} {
		t.Run(kind, func(t *testing.T) {
			s, path := openTempStore(t)
			var id string
			if kind != "spawn" {
				id = busyLaunchInsert(t, s, AgentClaude)
				busyLaunchFinish(t, s, id)
			}
			mustExec(t, s.db, `CREATE TRIGGER fail_snapshot AFTER INSERT ON busy_send_launch_escapes BEGIN SELECT RAISE(ABORT,'snapshot write failed'); END`)
			before := dbDump(t, path)
			var err error
			switch kind {
			case "spawn":
				err = s.InsertPending(Spawn{InstanceID: uuid.NewString(), Agent: AgentClaude, CWD: t.TempDir(), TmuxSessionName: "ad-" + uuid.NewString(), RelayMode: "off"})
			case "resume":
				row := busyLaunchRow(t, s, id)
				_, _, err = s.MoveToPendingWithExit(id, row.Snapshot, 2000, "1122334455667788", "", "", LaunchOwner{}, BusySendDefaultsChoice{}, busyLaunchExit(t, s, id))
			case "reuse":
				row, _, e := s.ReadForReuse(id)
				if e != nil {
					t.Fatal(e)
				}
				_, _, _, err = s.ResetForReuse(id, row.Snapshot, row.Agent, Spawn{Agent: AgentClaude, CWD: t.TempDir(), TmuxSessionName: "ad-" + id, RelayMode: "off", Identity: LaunchIdentity{Token: "1122334455667788"}, BusySendExit: busyLaunchExit(t, s, id)})
			}
			if err == nil || !strings.Contains(err.Error(), "snapshot write failed") {
				t.Fatalf("creation: %v", err)
			}
			if !reflect.DeepEqual(before, dbDump(t, path)) {
				t.Fatal("failed snapshot creation left partial facts")
			}
		})
	}
}
