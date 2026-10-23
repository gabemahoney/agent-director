package store_test

import (
	"context"
	"database/sql"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// Native layout is irrelevant to atomic publication: these are synthetic persisted binding facts.
func TestCursorLoadedBindingConditionalAndDurable(t *testing.T) {
	for _, change := range []string{"none", "session", "path", "snapshot", "token", "life", "pane", "connection", "runtime", "terminated", "already bound", "retired before commit"} {
		t.Run(change, func(t *testing.T) {
			f, old, b := cursorRowBinding(t)
			db, err := sql.Open("sqlite", f.path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if _, err := db.Exec(`UPDATE spawns SET session_id='latest-loaded',jsonl_path='protected-gateway-path' WHERE instance_id=?`, old.InstanceID); err != nil {
				t.Fatal(err)
			}
			row, err := f.s.GetSpawn(old.InstanceID)
			if err != nil {
				t.Fatal(err)
			}
			b.SessionID = row.SessionID
			queries := map[string]string{
				"session":       `UPDATE spawns SET session_id='older' WHERE instance_id=?`,
				"path":          `UPDATE spawns SET jsonl_path='replacement' WHERE instance_id=?`,
				"snapshot":      `UPDATE spawns SET row_version=row_version+1 WHERE instance_id=?`,
				"token":         `UPDATE spawns SET launch_token='replacement' WHERE instance_id=?`,
				"life":          `UPDATE spawns SET life_number=life_number+1 WHERE instance_id=?`,
				"pane":          `UPDATE spawns SET pane_id='%replacement' WHERE instance_id=?`,
				"connection":    `UPDATE cursor_runtimes SET bound_connection_id='replacement' WHERE bound_instance_id=?`,
				"runtime":       `UPDATE cursor_runtimes SET bound_runtime_start='replacement' WHERE bound_instance_id=?`,
				"terminated":    `UPDATE cursor_runtimes SET bound_terminated=1 WHERE bound_instance_id=?`,
				"already bound": `UPDATE cursor_runtimes SET bound_session_id='foreign' WHERE bound_instance_id=?`,
			}
			if q := queries[change]; q != "" {
				if _, err := db.Exec(q, row.InstanceID); err != nil {
					t.Fatal(err)
				}
			}
			before := hgColumns(t, f, row.InstanceID)
			bindingBefore, err := f.s.ReadCursorRuntime(row.InstanceID)
			if err != nil {
				t.Fatal(err)
			}
			checks := 0
			ok, err := f.s.BindLoadedCursorSession(context.Background(), row, b, row.JSONLPath, time.Now().Add(5*time.Second), func() bool { checks++; return change != "retired before commit" || checks == 1 })
			if change == "none" {
				if err != nil || !ok {
					t.Fatalf("current publication: %v %v", ok, err)
				}
			} else if ok || (err != nil && change != "retired before commit") {
				t.Fatalf("stale publication: %v %v", ok, err)
			}
			reopened, err := store.Open(f.path)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			got, err := reopened.ReadCursorRuntime(row.InstanceID)
			if err != nil {
				t.Fatal(err)
			}
			want := bindingBefore
			if change == "none" {
				want.SessionID = row.SessionID
			}
			if !reflect.DeepEqual(want, got) || !reflect.DeepEqual(before, hgColumns(t, f, row.InstanceID)) {
				t.Fatalf("durable publication changed wrong facts: %+v want %+v", got, want)
			}
		})
	}
}

// TestCursorLoadedPendingPromptHolds: a loaded pending Cursor prompt is
// admitted only when no request holds it. The b.m8h regression cells: in
// "earlier launch" the request's owner is an earlier launch token, so it stops
// holding and the prompt is admitted; in "unknown owner" the owner is unknown
// (a pre-v14 row), so it keeps holding. Each cell with a request checks
// HoldsDialog against the SQL admission.
func TestCursorLoadedPendingPromptHolds(t *testing.T) {
	owners := map[string][]apitest.PermissionOption{
		"earlier launch": {apitest.PermissionOwner("0000000000000000", 0)},
		"unknown owner":  {apitest.PermissionOwner("", 0)},
	}
	for _, hold := range []string{"none", "unanswered", "decided unproven", "earlier launch", "unknown owner"} {
		t.Run(hold, func(t *testing.T) {
			held := hold != "none" && hold != "earlier launch"
			f, old, b := cursorRowBinding(t)
			db, err := sql.Open("sqlite", f.path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			var token string
			if hold != "none" {
				p, err := apitest.SeedPermissionRequest(f.path, old.InstanceID, "Bash", owners[hold]...)
				if err != nil {
					t.Fatal(err)
				}
				token = p.RequestToken
				if hold == "decided unproven" {
					if _, err := db.Exec(`UPDATE permission_requests SET decision='deny' WHERE request_token=?`, p.RequestToken); err != nil {
						t.Fatal(err)
					}
				}
			}
			if _, err := db.Exec(`UPDATE spawns SET state='pending',session_id='latest-loaded',jsonl_path='protected-gateway-path' WHERE instance_id=?`, old.InstanceID); err != nil {
				t.Fatal(err)
			}
			row, err := f.s.GetSpawn(old.InstanceID)
			if err != nil {
				t.Fatal(err)
			}
			b.SessionID = row.SessionID
			until := time.Now().Add(5 * time.Second)
			if ok, err := f.s.BindLoadedCursorSession(context.Background(), row, b, row.JSONLPath, until, func() bool { return true }); err != nil || !ok {
				t.Fatalf("load binding %v %v", ok, err)
			}
			if ok, err := f.s.CheckCursorPrompt(b, until, cursorTrailCheck); err == nil || ok {
				t.Fatal("general prompt authority admitted pending")
			}
			request := store.RelayRequest{RequestToken: "pending-permission", ToolUseID: "pending-tool", ToolName: "Bash", ToolInput: "{}", SettledAt: until}
			if err := f.s.InsertCursorRequest(b, request, 0, until, store.DefaultLockWait, cursorTrailCheck); err == nil {
				t.Fatal("loaded pending granted general permission authority")
			}
			if ok, err := f.s.CheckLoadedCursorPrompt(b, "different", until, cursorTrailCheck); err == nil || ok {
				t.Fatal("wrong transcript admitted pending")
			}
			if ok, err := f.s.CheckLoadedCursorPrompt(b, row.JSONLPath, until, cursorTrailCheck); err != nil || ok == held {
				t.Fatalf("loaded pending hold=%s: %v %v", hold, ok, err)
			}
			if token != "" {
				pr, err := f.s.GetPermissionRequest(old.InstanceID, token)
				if err != nil || pr.HoldsDialog(store.AgentCursor, row.Identity.Token) != held {
					t.Fatalf("HoldsDialog disagrees with prompt admission: %+v %v", pr, err)
				}
			}
		})
	}
}

func TestCursorLoadedWriterCancellationCannotPublishAfterRelease(t *testing.T) {
	f, old, b := cursorRowBinding(t)
	db, err := sql.Open("sqlite", f.path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`UPDATE spawns SET session_id='latest-loaded',jsonl_path='protected-gateway-path' WHERE instance_id=?`, old.InstanceID); err != nil {
		t.Fatal(err)
	}
	row, err := f.s.GetSpawn(old.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	b.SessionID = row.SessionID
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	defer conn.ExecContext(context.Background(), "ROLLBACK")
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	type result struct {
		ok  bool
		err error
	}
	done := make(chan result, 1)
	started := time.Now()
	go func() {
		ok, err := f.s.BindLoadedCursorSession(ctx, row, b, row.JSONLPath, time.Now().Add(5*time.Second), func() bool { return true })
		done <- result{ok, err}
	}()
	select {
	case got := <-done:
		if got.ok || got.err == nil {
			t.Fatalf("held writer ignored cancellation: %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("load publication ignored caller cancellation")
	}
	if time.Since(started) > time.Second {
		t.Fatal("cancellation deadline renewed")
	}
	if _, err := conn.ExecContext(context.Background(), "ROLLBACK"); err != nil {
		t.Fatal(err)
	}
	// A separate connection reads committed facts only after the canceled caller has returned.
	reader, err := store.Open(f.path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	got, err := reader.ReadCursorRuntime(row.InstanceID)
	if err != nil || got.SessionID != "" {
		t.Fatalf("canceled publication became durable: %+v %v", got, err)
	}
	if !reflect.DeepEqual(hgColumns(t, f, row.InstanceID).SessionID, row.SessionID) {
		t.Fatal("cancellation changed durable latest identity")
	}
}

func TestCursorResumeRegistrationPreservesDurableRequestFacts(t *testing.T) {
	e := newCursorTrailEnv(t)
	e.insert(t, "old-choice")
	if ok, err := e.f.s.DecidePermissionRequest(e.b.InstanceID, "old-choice", "deny", "user", "test"); err != nil || !ok {
		t.Fatalf("old decision: %v %v", ok, err)
	}
	if decision, ok, err := e.f.s.AckCursorDecision(e.b, "old-choice", false, e.until, e.until, store.DefaultLockWait, cursorTrailCheck); err != nil || !ok || decision != "deny" {
		t.Fatalf("old delivery: %s %v %v", decision, ok, err)
	}
	priorRequest := e.read(t, "old-choice")
	e.exec(t, `UPDATE spawns SET state='ended',jsonl_path='retained-protected-path' WHERE instance_id=?`, e.b.InstanceID)
	old, err := e.f.s.GetSpawn(e.b.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	history, err := e.f.s.ListSessionHistory(old.InstanceID, old.LifeNumber)
	if err != nil {
		t.Fatal(err)
	}
	const token = "2222222222222222"
	result, version, err := e.f.s.MoveToPending(old.InstanceID, old.Snapshot, time.Now().UnixMilli(), token, old.Identity.Socket, "", store.LaunchOwner{})
	if err != nil || result != store.CondApplied {
		t.Fatalf("resume move %v %v", result, err)
	}
	identity := old.Identity
	identity.Token = token
	if result, err := e.f.s.RecordLaunchIdentity(old.InstanceID, version, token, identity); err != nil || result != store.CondApplied {
		t.Fatalf("resume identity %v %v", result, err)
	}
	row, err := e.f.s.GetSpawn(old.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	current := e.b
	current.Token = token
	current.Connection = "resumed-connection"
	current.SessionID = ""
	if ok, err := e.f.s.RegisterCursorRuntime(row, current); err != nil || !ok {
		t.Fatalf("resume registration %v %v", ok, err)
	}
	reader, err := store.Open(e.f.path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	registered, err := reader.ReadCursorRuntime(row.InstanceID)
	if err != nil || registered.SessionID != "" {
		t.Fatalf("registration inherited session authority: %+v %v", registered, err)
	}
	current.SessionID = row.SessionID
	if ok, err := e.f.s.BindLoadedCursorSession(context.Background(), row, current, row.JSONLPath, time.Now().Add(5*time.Second), func() bool { return true }); err != nil || !ok {
		t.Fatalf("load publication %v %v", ok, err)
	}
	request, err := reader.GetPermissionRequest(row.InstanceID, "old-choice")
	if err != nil || !reflect.DeepEqual(priorRequest, request) {
		t.Fatalf("resume changed choice/delivery/proof: %+v %v", request, err)
	}
	got, err := reader.GetSpawn(row.InstanceID)
	if err != nil || got.SessionID != old.SessionID || got.JSONLPath != old.JSONLPath || got.LifeNumber != old.LifeNumber || got.Agent != store.AgentCursor || got.State != store.StatePending {
		t.Fatalf("durable facts: %+v %v", got, err)
	}
	gotHistory, err := reader.ListSessionHistory(row.InstanceID, row.LifeNumber)
	if err != nil || !reflect.DeepEqual(history, gotHistory) {
		t.Fatal("resume changed history")
	}
	if ok, err := e.f.s.CheckCursorPrompt(current, time.Now().Add(5*time.Second), cursorTrailCheck); err == nil || ok {
		t.Fatal("loaded pending granted general authority")
	}
	if err := e.f.s.CheckCursorAnswer(e.b, "old-choice", true, time.Now().Add(5*time.Second), cursorTrailCheck); err == nil {
		t.Fatal("old connection retained answer authority")
	}
}

func TestCursorLoadedStorageFailureDoesNotPartiallyPublish(t *testing.T) {
	for _, kind := range []string{"closed store", "SQL abort", "COMMIT foreign key failure"} {
		t.Run(kind, func(t *testing.T) {
			f, old, b := cursorRowBinding(t)
			db, err := sql.Open("sqlite", f.path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if _, err := db.Exec(`UPDATE spawns SET session_id='latest-loaded',jsonl_path='protected-gateway-path' WHERE instance_id=?`, old.InstanceID); err != nil {
				t.Fatal(err)
			}
			row, err := f.s.GetSpawn(old.InstanceID)
			if err != nil {
				t.Fatal(err)
			}
			b.SessionID = row.SessionID
			before := hgColumns(t, f, row.InstanceID)
			if kind == "closed store" {
				if err := f.s.Close(); err != nil {
					t.Fatal(err)
				}
			} else if kind == "SQL abort" {
				if _, err := db.Exec(`CREATE TRIGGER reject_loaded BEFORE UPDATE ON cursor_runtimes BEGIN SELECT RAISE(ABORT,'synthetic loaded storage failure'); END`); err != nil {
					t.Fatal(err)
				}
			} else {
				for _, q := range []string{
					`CREATE TABLE loaded_commit_parent(id INTEGER PRIMARY KEY)`,
					`CREATE TABLE loaded_commit_child(id INTEGER REFERENCES loaded_commit_parent(id) DEFERRABLE INITIALLY DEFERRED)`,
					`CREATE TRIGGER reject_loaded_commit AFTER UPDATE OF bound_session_id ON cursor_runtimes BEGIN INSERT INTO loaded_commit_child VALUES(1); END`,
				} {
					if _, err := db.Exec(q); err != nil {
						t.Fatal(err)
					}
				}
			}
			if ok, err := f.s.BindLoadedCursorSession(context.Background(), row, b, row.JSONLPath, time.Now().Add(5*time.Second), func() bool { return true }); err == nil || ok {
				t.Fatalf("storage failure granted binding: %v %v", ok, err)
			}
			reader, err := store.Open(f.path)
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			got, err := reader.ReadCursorRuntime(row.InstanceID)
			if err != nil || got.SessionID != "" {
				t.Fatalf("failed publication durable: %+v %v", got, err)
			}
			after, err := apitest.ReadSpawnColumns(f.path, row.InstanceID)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("failed binding changed durable row")
			}
		})
	}
}

func TestCursorLoadedNativeCommitCancellation(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(map[bool]string{false: "COMMIT entry", true: "COMMIT response"}[committed], func(t *testing.T) {
			f, old, binding := cursorRowBinding(t)
			db, err := sql.Open("sqlite", f.path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if _, err := db.Exec(`UPDATE spawns SET session_id='latest-loaded',jsonl_path='protected-gateway-path' WHERE instance_id=?`, old.InstanceID); err != nil {
				t.Fatal(err)
			}
			row, err := f.s.GetSpawn(old.InstanceID)
			if err != nil {
				t.Fatal(err)
			}
			binding.SessionID = row.SessionID
			before := hgColumns(t, f, row.InstanceID)
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			timer := time.AfterFunc(3*time.Second, unblock)
			defer func() { unblock(); timer.Stop() }()
			var commits atomic.Int32
			store.ObserveDecisionBoundaryForTest(t, f.s, f.path, func(ctx context.Context, q string) error {
				if q == "COMMIT" {
					commits.Add(1)
					if !committed {
						close(entered)
						<-release
						return ctx.Err()
					}
				}
				return nil
			}, func(ctx context.Context) error {
				if committed {
					close(entered)
					<-release
					return ctx.Err()
				}
				return nil
			})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			type result struct {
				ok  bool
				err error
			}
			done := make(chan result, 1)
			go func() {
				ok, err := f.s.BindLoadedCursorSession(ctx, row, binding, row.JSONLPath, time.Now().Add(5*time.Second), func() bool { return true })
				done <- result{ok, err}
			}()
			select {
			case <-entered:
			case <-time.After(2 * time.Second):
				t.Fatal("loaded publication did not reach native COMMIT")
			}
			// Read through a separate real connection while native COMMIT entry/response is held.
			var durable string
			if err := db.QueryRow(`SELECT bound_session_id FROM cursor_runtimes WHERE bound_instance_id=?`, row.InstanceID).Scan(&durable); err != nil {
				t.Fatal(err)
			}
			want := ""
			if committed {
				want = row.SessionID
			}
			if durable != want {
				t.Fatalf("native COMMIT visibility=%q want %q", durable, want)
			}
			cancel()
			// Release the observed native call only after cancellation, then retain the same return bound.
			unblock()
			select {
			case got := <-done:
				if got.ok || got.err == nil || (committed && !strings.Contains(got.err.Error(), "unconfirmed")) {
					t.Fatalf("canceled COMMIT granted confirmed publication: %+v", got)
				}
			case <-time.After(time.Second):
				t.Fatal("native COMMIT ignored original caller cancellation")
			}
			unblock()
			// The ordinary lease drains native cleanup before reopening the durable reader.
			if _, err := f.s.GetSpawn(row.InstanceID); err != nil {
				t.Fatal(err)
			}
			reader, err := store.Open(f.path)
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			got, err := reader.ReadCursorRuntime(row.InstanceID)
			if err != nil || got.SessionID != want || !reflect.DeepEqual(before, hgColumns(t, f, row.InstanceID)) || commits.Load() != 1 {
				t.Fatalf("canceled COMMIT durable facts: %+v %v attempts=%d", got, err, commits.Load())
			}
		})
	}
}
