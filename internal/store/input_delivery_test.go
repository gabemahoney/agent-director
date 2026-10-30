package store_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/inputdelivery"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
	"github.com/google/uuid"
)

type inputStoreFixture struct {
	s    *store.Store
	path string
	l    *inputdelivery.Launch
	g    inputdelivery.Guard
	a    *inputdelivery.Attempt
}

func newInputStore(t *testing.T) *inputStoreFixture {
	t.Helper()
	s, path := storefix.OpenTempStore(t)
	li := store.LaunchIdentity{Token: "fedcba9876543210", Socket: apitest.TestSocket, PaneID: apitest.TestPaneID, PanePID: apitest.TestPanePID, PaneStarttime: apitest.LinuxProcStarttime}
	session := uuid.NewString()
	id, err := apitest.SeedSpawn(path, "", store.StatePending, t.TempDir(), "off", session, false, apitest.WithLaunchIdentity(li), apitest.WithLifeNumber(2))
	if err != nil {
		t.Fatal(err)
	}
	row, err := s.GetSpawn(id)
	if err != nil {
		t.Fatal(err)
	}
	i := inputdelivery.Identity{StoreID: s.StoreID(), Token: li.Token, InstanceID: id, Socket: li.Socket, Agent: "claude", CWD: row.CWD, LifeNumber: row.LifeNumber}
	l := inputdelivery.Launch{Identity: i, Revision: 1, EvidenceDir: t.TempDir(), Capability: inputdelivery.Capability{Agent: i.Agent, Version: "2.1.280", SessionID: session, CWD: i.CWD, Socket: i.Socket, Token: i.Token, InstanceID: i.InstanceID, Created: time.Now().UTC(), Safe: true}}
	l.Capability.Transcript = filepath.Join(l.EvidenceDir, "native.jsonl")
	if err := s.CreateInputLaunch(context.Background(), l); err != nil {
		t.Fatal(err)
	}
	committed, err := s.ReadInputLaunch(context.Background(), li.Token)
	if err != nil {
		t.Fatal(err)
	}
	i.PaneID, i.PID, i.Starttime = li.PaneID, li.PanePID, li.PaneStarttime
	g := inputdelivery.Guard{Identity: i, RowVersion: row.RowVersion, PermissionEpoch: row.PermissionEpoch, State: row.State, SessionID: session}
	committed, err = s.BindInputLaunch(context.Background(), *committed, g)
	if err != nil {
		t.Fatal(err)
	}
	a, err := inputdelivery.NewAttempt(i, "hello <>& café 🌍", map[string]inputdelivery.Watermark{l.Capability.Transcript: {PrefixSHA256: inputdelivery.Hash(nil)}})
	if err != nil {
		t.Fatal(err)
	}
	a.NativeSessionID = session
	return &inputStoreFixture{s: s, path: path, l: committed, g: g, a: a}
}

func (f *inputStoreFixture) save(t *testing.T) {
	t.Helper()
	l, a, err := f.s.SaveInputAttempt(context.Background(), *f.l, f.g, f.a)
	if err != nil {
		t.Fatal(err)
	}
	f.l, f.a = l, a
}
func (f *inputStoreFixture) raw(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", f.path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}
func (f *inputStoreFixture) proof() []byte {
	text, _ := json.Marshal(f.a.Payload)
	raw := []byte(fmt.Sprintf(" \t{ \"type\":\"user\", \"uuid\":\"cde93519-6972-4b1b-a650-830a0042e876\", \"sessionId\":%q, \"cwd\":%q, \"version\":\"2.1.280\", \"message\":{\"role\":\"user\",\"content\":%s} } \t", f.g.SessionID, f.g.Identity.CWD, text))
	f.a.Phase = "submitted"
	f.a.Proof = &inputdelivery.Proof{Path: f.l.Capability.Transcript, SessionID: f.g.SessionID, RecordID: "cde93519-6972-4b1b-a650-830a0042e876", Raw: raw, RawSHA256: inputdelivery.Hash(raw)}
	return raw
}

func TestInputDeliveryRecordsRetainPhasesBytesAndHistory(t *testing.T) {
	t.Parallel()
	f := newInputStore(t)
	f.a.Phase = "not_started"
	f.save(t)
	first := *f.a
	f.a, _ = inputdelivery.NewAttempt(f.g.Identity, first.Payload, first.Watermark)
	f.a.NativeSessionID = f.g.SessionID
	f.save(t)
	if f.a.LastQueue != nil || f.a.Confirmation.Source != "store" {
		t.Fatalf("unknown queue/intent=%+v", f.a)
	}
	f.a.Phase, f.a.BytesAttempted = "text_attempted", len(f.a.Payload)
	zero := 0
	f.a.LastQueue = &zero
	f.save(t)
	f.a.Phase, f.a.EnterAttempted = "enter_attempted", true
	f.save(t)
	raw := f.proof()
	f.g.ProofOnly = true
	f.save(t)
	f.g.ProofOnly = false
	proved := *f.a
	f.a, _ = inputdelivery.NewAttempt(f.g.Identity, "", nil)
	f.a.Phase, f.a.ManualInterference = "manual_input", true
	f.g.Manual = true
	f.save(t)
	if err := f.s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(f.path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	l, err := s.ReadInputLaunch(context.Background(), f.g.Identity.Token)
	if err != nil || !reflect.DeepEqual(l, f.l) {
		t.Fatalf("launch=%+v %v", l, err)
	}
	for _, want := range []*inputdelivery.Attempt{&first, &proved, f.a} {
		got, err := s.ReadInputAttempt(context.Background(), *l, want.RequestID)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("reopened=%+v want=%+v err=%v", got, want, err)
		}
	}
	db := f.raw(t)
	var kind, record string
	var blob []byte
	if err := db.QueryRow(`SELECT typeof(proof_raw),proof_raw,record FROM input_attempts WHERE request_id=?`, proved.RequestID).Scan(&kind, &blob, &record); err != nil {
		t.Fatal(err)
	}
	if kind != "blob" || !bytes.Equal(blob, raw) || proved.Proof.RawSHA256 != inputdelivery.Hash(blob) {
		t.Fatal("proof bytes were reencoded")
	}
	var metadata inputdelivery.Attempt
	if err := json.Unmarshal([]byte(record), &metadata); err != nil {
		t.Fatal(err)
	}
	if len(metadata.Proof.Raw) != 0 || proved.Confirmation.Source != "transcript" || proved.Confirmation.Revision != proved.Revision {
		t.Fatal("proof has duplicate authority or wrong provenance")
	}
}

func TestInputDeliveryImmutableRequestsAndCapturedGuards(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"payload", "snapshot", "watermark", "life", "process", "session", "epoch", "row revision", "launch revision", "new unresolved request", "proof creates request", "proof adds Enter"} {
		t.Run(mode, func(t *testing.T) {
			f := newInputStore(t)
			f.save(t)
			before := *f.a
			l := *f.l
			g := f.g
			switch mode {
			case "payload":
				f.a.Payload += "changed"
				f.a.PayloadSHA256 = inputdelivery.Hash([]byte(f.a.Payload))
			case "snapshot":
				f.a.SnapshotAt = f.a.SnapshotAt.Add(time.Second)
			case "watermark":
				f.a.Watermark = map[string]inputdelivery.Watermark{}
			case "life":
				g.Identity.LifeNumber++
			case "process":
				g.Identity.Starttime += "0"
			case "session":
				g.SessionID = "another"
			case "epoch":
				g.PermissionEpoch++
			case "row revision":
				g.RowVersion++
			case "launch revision":
				l.Revision++
			case "new unresolved request", "proof creates request":
				f.a, _ = inputdelivery.NewAttempt(g.Identity, "new", nil)
				g.ProofOnly = mode == "proof creates request"
			case "proof adds Enter":
				g.ProofOnly = true
				f.a.Phase, f.a.EnterAttempted = "enter_attempted", true
			}
			got, a, err := f.s.SaveInputAttempt(context.Background(), l, g, f.a)
			if err == nil || got != nil || a != nil {
				t.Fatalf("changed request/guard admitted: %+v %+v %v", got, a, err)
			}
			current, err := f.s.ReadInputAttempt(context.Background(), *f.l, before.RequestID)
			if err != nil || !reflect.DeepEqual(current, &before) {
				t.Fatalf("refusal changed durable owner: %+v %v", current, err)
			}
		})
	}
}

func TestInputDeliveryRestartRequiresConfirmedCurrentZeroIntent(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"confirmed", "unknown provenance", "historical", "manual interference"} {
		t.Run(mode, func(t *testing.T) {
			f := newInputStore(t)
			f.a.Phase = "not_started"
			f.save(t)
			old := *f.a
			switch mode {
			case "unknown provenance":
				if _, err := f.raw(t).Exec(`UPDATE input_attempts SET record=json_remove(record,'$.confirmation') WHERE request_id=?`, old.RequestID); err != nil {
					t.Fatal(err)
				}
			case "historical":
				f.a, _ = inputdelivery.NewAttempt(f.g.Identity, "new current", nil)
				f.save(t)
			case "manual interference":
				f.a.ManualInterference = true
				f.g.Manual = true
				f.save(t)
				old = *f.a
				f.g.Manual = false
			}
			next := old
			next.Phase = "attempt_started"
			next.SnapshotAt = old.SnapshotAt.Add(time.Second)
			next.Watermark = map[string]inputdelivery.Watermark{f.l.Capability.Transcript: {Size: 10, PrefixSHA256: inputdelivery.Hash([]byte("new prefix"))}}
			l, a, err := f.s.SaveInputAttempt(context.Background(), *f.l, f.g, &next)
			if mode == "confirmed" {
				if err != nil || a.RequestID != old.RequestID || a.Revision != old.Revision+1 || l.CurrentRequestID != old.RequestID || !a.SnapshotAt.Equal(next.SnapshotAt) {
					t.Fatalf("confirmed restart=%+v %+v %v", l, a, err)
				}
			} else if err == nil || l != nil || a != nil {
				t.Fatalf("unsafe restart=%+v %+v %v", l, a, err)
			}
		})
	}
}

func TestInputDeliveryActualConnectionDurabilityAndAtomicity(t *testing.T) {
	t.Parallel()
	f := newInputStore(t)
	db := f.raw(t)
	// SQLite evaluates these table-valued PRAGMAs on the connection doing INSERT.
	_, err := db.Exec(`CREATE TRIGGER require_full BEFORE INSERT ON input_attempts BEGIN SELECT CASE WHEN (SELECT synchronous FROM pragma_synchronous)!=2 OR (SELECT journal_mode FROM pragma_journal_mode)!='wal' THEN RAISE(ABORT,'intent not FULL WAL') END; END`)
	if err != nil {
		t.Fatal(err)
	}
	var observed []string
	store.ObserveDecisionBoundaryForTest(t, f.s, f.path, func(_ context.Context, q string) error { observed = append(observed, q); return nil }, nil)
	f.save(t)
	if len(observed) == 0 {
		t.Fatal("actual connection not observed")
	}
	full, begin, insert := -1, -1, -1
	for i, q := range observed {
		if q == "PRAGMA synchronous = FULL" {
			full = i
		}
		if strings.HasPrefix(q, "BEGIN") {
			begin = i
		}
		if strings.HasPrefix(q, "INSERT INTO input_attempts") {
			insert = i
		}
	}
	if full < 0 || begin <= full || insert <= begin {
		t.Fatalf("durability admission order=%q", observed)
	}
	before := *f.a
	old := *f.l
	remove := storefix.InjectWriteFailure(t, f.path, storefix.WriteFailInputCurrent, f.g.Identity.InstanceID)
	f.a.Phase, f.a.BytesAttempted = "text_attempted", len(f.a.Payload)
	if l, a, err := f.s.SaveInputAttempt(context.Background(), old, f.g, f.a); err == nil || l != nil || a != nil {
		t.Fatalf("partial transaction published: %+v %+v %v", l, a, err)
	}
	got, err := f.s.ReadInputAttempt(context.Background(), old, before.RequestID)
	if err != nil || !reflect.DeepEqual(got, &before) {
		t.Fatalf("attempt escaped rollback: %+v %v", got, err)
	}
	launch, err := f.s.ReadInputLaunch(context.Background(), old.Identity.Token)
	if err != nil || !reflect.DeepEqual(launch, &old) {
		t.Fatalf("current escaped rollback: %+v %v", launch, err)
	}
	remove()
	f.save(t)
	if f.a.Phase != "text_attempted" || f.a.Revision != before.Revision+1 || f.l.Revision != old.Revision+1 {
		t.Fatalf("retry did not atomically save current and attempt: %+v %+v", f.l, f.a)
	}
}

func TestInputDeliveryCommitFailureAndCancellation(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"before commit", "unknown committed", "cancel before commit", "late commit"} {
		t.Run(mode, func(t *testing.T) {
			f := newInputStore(t)
			f.save(t)
			before := *f.a
			old := *f.l
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			defer unblock()
			store.ObserveDecisionBoundaryForTest(t, f.s, f.path, func(_ context.Context, q string) error {
				if q == "COMMIT" && mode == "before commit" {
					return errors.New("injected COMMIT refusal")
				}
				if strings.HasPrefix(q, "UPDATE input_launches") && mode == "cancel before commit" {
					cancel()
				}
				return nil
			}, func(context.Context) error {
				if mode == "unknown committed" {
					return errors.New("commit acknowledgement lost")
				}
				if mode == "late commit" {
					close(entered)
					<-release
				}
				return nil
			})
			f.a.Phase, f.a.BytesAttempted = "text_attempted", len(f.a.Payload)
			type result struct {
				l   *inputdelivery.Launch
				a   *inputdelivery.Attempt
				err error
			}
			done := make(chan result, 1)
			go func() { l, a, err := f.s.SaveInputAttempt(ctx, old, f.g, f.a); done <- result{l, a, err} }()
			if mode == "late commit" {
				select {
				case <-entered:
					cancel()
				case <-time.After(5 * time.Second):
					t.Fatal("commit was not entered")
				}
			}
			var got result
			select {
			case got = <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("cancelled caller waited for native completion")
			}
			unblock()
			if got.err == nil || got.l != nil || got.a != nil {
				t.Fatalf("unconfirmed result published: %+v", got)
			}
			if mode == "unknown committed" && !errors.Is(got.err, inputdelivery.ErrUnconfirmed) {
				t.Fatalf("lost uncertainty: %v", got.err)
			}
			// Read from a fresh handle after abandonment; do not adopt its revision
			// into the old worker or reinterpret the durable intent as delivery.
			s, err := store.Open(f.path)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			l, err := s.ReadInputLaunch(context.Background(), old.Identity.Token)
			if err != nil {
				t.Fatal(err)
			}
			a, err := s.ReadInputAttempt(context.Background(), *l, before.RequestID)
			if err != nil {
				t.Fatal(err)
			}
			committed := mode == "unknown committed" || mode == "late commit"
			if committed {
				if a.Phase != "text_attempted" || a.RequestID != before.RequestID || a.Proof != nil || a.EnterAttempted {
					t.Fatalf("late intent=%+v", a)
				}
			} else if !reflect.DeepEqual(a, &before) {
				t.Fatalf("failed commit changed owner: %+v", a)
			}
			if committed {
				if err := s.CheckInputGuard(context.Background(), &old, f.g); !errors.Is(err, inputdelivery.ErrStale) {
					t.Fatalf("obsolete revision regained authority: %v", err)
				}
			}
		})
	}
}

func TestInputDeliveryCorruptCurrentAndHistoryRefuse(t *testing.T) {
	t.Parallel()
	for _, archived := range []bool{false, true} {
		for _, mode := range []string{"raw bytes", "wrong hash", "unsupported capability", "missing record"} {
			t.Run(fmt.Sprintf("archived=%t/%s", archived, mode), func(t *testing.T) {
				f := newInputStore(t)
				f.save(t)
				f.a.Phase, f.a.BytesAttempted, f.a.EnterAttempted = "enter_attempted", len(f.a.Payload), true
				f.save(t)
				f.proof()
				f.g.ProofOnly = true
				f.save(t)
				id := f.a.RequestID
				f.g.ProofOnly = false
				if archived {
					f.a, _ = inputdelivery.NewAttempt(f.g.Identity, "next", nil)
					f.save(t)
				}
				db := f.raw(t)
				var err error
				switch mode {
				case "raw bytes":
					_, err = db.Exec(`UPDATE input_attempts SET proof_raw=? WHERE request_id=?`, []byte("changed"), id)
				case "wrong hash":
					_, err = db.Exec(`UPDATE input_attempts SET record=json_set(record,'$.proof.RawSHA256','wrong') WHERE request_id=?`, id)
				case "unsupported capability":
					_, err = db.Exec(`UPDATE input_launches SET record=json_set(record,'$.Capability.Version','future')`)
				case "missing record":
					_, err = db.Exec(`DELETE FROM input_attempts WHERE request_id=?`, id)
				}
				if err != nil {
					t.Fatal(err)
				}
				got, err := f.s.ReadInputAttempt(context.Background(), *f.l, id)
				if mode == "missing record" && archived {
					if got != nil || err != nil {
						t.Fatalf("missing history=%+v %v", got, err)
					}
					return
				}
				if err == nil || got != nil {
					t.Fatalf("corrupt record accepted: %+v %v", got, err)
				}
			})
		}
	}
}
