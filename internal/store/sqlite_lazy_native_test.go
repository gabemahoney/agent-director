package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type observedConnector struct {
	driver.Connector
	before func(context.Context)
	after  func(context.Context, error)
	wrap   func(driver.Conn) driver.Conn
}

func (c observedConnector) Connect(ctx context.Context) (driver.Conn, error) {
	if c.before != nil {
		c.before(ctx)
	}
	conn, err := c.Connector.Connect(ctx)
	if c.after != nil {
		c.after(ctx, err)
	}
	if err == nil && c.wrap != nil {
		conn = c.wrap(conn)
	}
	return conn, err
}

type lazyNativeDriver struct {
	driver.Driver
	boundary *deadlineBoundary
	open     func(int32)
	close    func(int32)
}

func (d *lazyNativeDriver) Open(path string) (driver.Conn, error) {
	n := d.boundary.opens.Add(1)
	if d.open != nil {
		d.open(n)
	}
	c, err := d.Driver.Open(path)
	if err != nil {
		return nil, err
	}
	return &lazyNativeClose{deadlineBoundaryConn: &deadlineBoundaryConn{Conn: c, boundary: d.boundary}, close: func() {
		if d.close != nil {
			d.close(n)
		}
	}}, nil
}

type lazyNativeClose struct {
	*deadlineBoundaryConn
	close func()
}

func (c *lazyNativeClose) Close() error { c.close(); return c.deadlineBoundaryConn.Close() }

type lazyPoolObservation struct {
	b          *deadlineBoundary
	d          *lazyNativeDriver
	physical   chan context.Context
	background chan lazyPoolHandoff
	wrap       func(driver.Conn) driver.Conn
}

type lazyPoolHandoff struct {
	opens int32
	err   error
}

func (o *lazyPoolObservation) nextPhysical(t *testing.T) context.Context {
	t.Helper()
	select {
	case ctx := <-o.physical:
		return ctx
	case <-time.After(2 * time.Second):
		t.Fatal("native connector context not observed")
		return nil
	}
}

func (o *lazyPoolObservation) nextBackground(t *testing.T) int32 {
	t.Helper()
	select {
	case event := <-o.background:
		if event.err != nil {
			t.Errorf("background pool handoff returned synthetic acquisition failure: %v", event.err)
		}
		return event.opens
	case <-time.After(2 * time.Second):
		t.Fatal("background connector handoff not observed")
		return -1
	}
}

// Keep both production connector layers; only native I/O and its contexts are observed.
func installLazyPool(t *testing.T, s *Store, path string) *lazyPoolObservation {
	t.Helper()
	o := &lazyPoolObservation{b: &deadlineBoundary{}, physical: make(chan context.Context, 32), background: make(chan lazyPoolHandoff, 32)}
	o.d = &lazyNativeDriver{Driver: s.db.Driver(), boundary: o.b}
	s.busyTimeoutMs = 137
	inner := observedConnector{Connector: sqliteConnector{driver: o.d, path: path, busyTimeoutMs: s.busyTimeoutMs}, before: func(ctx context.Context) { o.physical <- ctx }, wrap: func(c driver.Conn) driver.Conn {
		if o.wrap != nil {
			return o.wrap(c)
		}
		return c
	}}
	outer := observedConnector{Connector: callerOwnedConnector{inner}, after: func(ctx context.Context, err error) {
		if ctx.Done() != nil && ctx.Value(connectionInitializationContextKey{}) == nil {
			o.background <- lazyPoolHandoff{o.b.opens.Load(), err}
		}
	}}
	db := sql.OpenDB(outer)
	db.SetMaxOpenConns(1)
	if err := s.db.Close(); err != nil {
		t.Fatal(err)
	}
	s.db = db
	t.Cleanup(func() { _ = db.Close() })
	return o
}

func waitPoolQueue(t *testing.T, db *sql.DB, count int64) {
	t.Helper()
	until := time.Now().Add(time.Second)
	for db.Stats().WaitCount < count {
		if time.Now().After(until) {
			t.Fatalf("pool waiters not queued: %+v", db.Stats())
		}
		time.Sleep(time.Millisecond)
	}
}

func discardLazyLease(t *testing.T, conn *sql.Conn) {
	t.Helper()
	if err := conn.Raw(func(any) error { return driver.ErrBadConn }); !errors.Is(err, driver.ErrBadConn) {
		t.Fatalf("discard: %v", err)
	}
	_ = conn.Close()
}

func requireLazySettings(t *testing.T, conn *sql.Conn) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for query, want := range map[string]int{"PRAGMA foreign_keys": 1, "PRAGMA busy_timeout": 137} {
		var got int
		if err := conn.QueryRowContext(ctx, query).Scan(&got); err != nil || got != want {
			t.Fatalf("%s=%d,%v want %d", query, got, err, want)
		}
	}
}

func TestProductionLazyReplacementRetainsBoundedNativeOwner(t *testing.T) {
	s, path := newRelayRow(t, "on", StateWorking)
	insertRelay(t, s, relayReq(tokenA))
	o := installLazyPool(t, s, path)
	held, err := s.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	o.nextPhysical(t)
	open, releaseOpen, opened := nativeBarrier(t)
	closeNative, releaseClose, closing := nativeBarrier(t)
	o.d.open = func(n int32) {
		if n == 2 {
			open()
		}
	}
	o.d.close = func(n int32) {
		if n == 2 {
			closeNative()
		}
	}
	var replacementSQL atomic.Int32
	o.b.before = func(_ context.Context, q string) error {
		if o.b.opens.Load() == 2 {
			replacementSQL.Add(1)
		}
		return nil
	}
	type key struct{}
	ctx, cancel := context.WithTimeout(context.WithValue(context.Background(), key{}, "bounded"), 500*time.Millisecond)
	defer cancel()
	deadline, _ := ctx.Deadline()
	done := make(chan error, 1)
	start := time.Now()
	waits := s.db.Stats().WaitCount
	go func() { _, err := boundedDecision(s, ctx, 5*time.Second); done <- err }()
	waitPoolQueue(t, s.db, waits+1)
	discardLazyLease(t, held)
	awaitNativeEvent(t, opened, "replacement Open")
	if count := o.nextBackground(t); count != 1 {
		t.Errorf("background opener performed native work: %d", count)
	}
	actual := o.nextPhysical(t)
	got, _ := actual.Deadline()
	if !got.Equal(deadline) || actual.Value(key{}) != "bounded" {
		t.Errorf("native opening lost original borrower context: %v %v", got, actual.Value(key{}))
	}
	select {
	case err := <-done:
		if !errors.Is(err, ErrStoreBusy) {
			t.Errorf("blocked open=%v", err)
		}
	case <-time.After(1500*time.Millisecond - time.Since(start)):
		t.Error("Open blocked caller past original 500ms + 1s")
	}
	checkOwned := func() {
		if len(s.cleanup.operationGate) != 1 || s.db.Stats().InUse != 1 {
			t.Errorf("native owner released admission/lease: gate=%d pool=%+v", len(s.cleanup.operationGate), s.db.Stats())
		}
		for i := 0; i < 3; i++ {
			retry, stop := context.WithTimeout(context.Background(), 20*time.Millisecond)
			_, err := s.WithContext(retry).GetPermissionRequestWithin(relayID, tokenA, time.Second)
			stop()
			if !errors.Is(err, ErrStoreBusy) {
				t.Errorf("retained owner bypassed: %v", err)
			}
		}
		zeroStart := time.Now()
		if _, err := boundedDecision(s, context.Background(), 0); !errors.Is(err, ErrStoreBusy) || time.Since(zeroStart) > time.Second {
			t.Errorf("zero admission bypassed owner: %v after%v", err, time.Since(zeroStart))
		}
		if o.b.opens.Load() != 2 || replacementSQL.Load() != 0 {
			t.Errorf("expired owner accumulated native work: opens=%d SQL=%d", o.b.opens.Load(), replacementSQL.Load())
		}
	}
	checkOwned()
	releaseOpen()
	awaitNativeEvent(t, closing, "replacement Close")
	checkOwned()
	releaseClose()
	recovery, cancelRecovery := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelRecovery()
	conn, err := s.db.Conn(recovery)
	if err != nil {
		t.Fatal(err)
	}
	requireLazySettings(t, conn)
	_ = conn.Close()
	assertDeadlineDatabaseReusable(t, s, path, "")
	if o.b.opens.Load() != 3 {
		t.Errorf("physical replacement count=%d want3", o.b.opens.Load())
	}
}

func TestProductionLazyReplacementMultipleLegitimateWaiters(t *testing.T) {
	s, path := newRelayRow(t, "on", StateWorking)
	insertRelay(t, s, relayReq(tokenA))
	o := installLazyPool(t, s, path)
	held, err := s.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	o.nextPhysical(t)
	block, release, entered := nativeBarrier(t)
	o.d.open = func(n int32) {
		if n == 2 {
			block()
		}
	}
	type key struct{}
	ctx, cancel := context.WithTimeout(context.WithValue(context.Background(), key{}, "bounded"), 500*time.Millisecond)
	defer cancel()
	deadline, _ := ctx.Deadline()
	o.b.before = func(sqlCtx context.Context, q string) error {
		if q == "SELECT 1" {
			owner, _ := sqlCtx.Value(key{}).(string)
			if _, ok := sqlCtx.Deadline(); ok || !strings.HasPrefix(owner, "legacy") {
				t.Errorf("legacy statement inherited foreign context: %q", owner)
			}
		}
		return nil
	}
	done := make(chan error, 1)
	legacy := make(chan error, 3)
	waits := s.db.Stats().WaitCount
	go func() { _, err := boundedDecision(s, ctx, 5*time.Second); done <- err }()
	for i := 0; i < 3; i++ {
		go func(i int) {
			var one int
			err := s.db.QueryRowContext(context.WithValue(context.Background(), key{}, fmt.Sprintf("legacy%d", i)), "SELECT 1").Scan(&one)
			if err == nil && one != 1 {
				err = fmt.Errorf("legacy result %d", one)
			}
			legacy <- err
		}(i)
	}
	waitPoolQueue(t, s.db, waits+4)
	discardLazyLease(t, held)
	awaitNativeEvent(t, entered, "multiwaiter replacement Open")
	actual := o.nextPhysical(t)
	owner, _ := actual.Value(key{}).(string)
	t.Logf("actual native replacement borrower=%s", owner)
	got, bounded := actual.Deadline()
	if owner == "bounded" {
		if !bounded || !got.Equal(deadline) {
			t.Errorf("bounded native deadline=%v", got)
		}
	} else if !strings.HasPrefix(owner, "legacy") || bounded {
		t.Errorf("foreign native context owner=%q deadline=%v", owner, got)
	}
	if count := o.nextBackground(t); count != 1 {
		t.Errorf("background performed native opening: %d", count)
	}
	<-ctx.Done()
	select {
	case err := <-done:
		if !errors.Is(err, ErrStoreBusy) {
			t.Errorf("bounded waiter=%v", err)
		}
	case <-time.After(time.Second):
		t.Error("bounded waiter retained beyond caller deadline")
	}
	select {
	case err := <-legacy:
		t.Errorf("legacy native owner/waiter completed before finite release: %v", err)
	default:
	}
	release()
	for i := 0; i < 3; i++ {
		select {
		case err := <-legacy:
			if err != nil {
				t.Errorf("legitimate legacy waiter failed: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("legacy waiter did not complete")
		}
	}
	conn, err := s.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	requireLazySettings(t, conn)
	_ = conn.Close()
	assertDeadlineDatabaseReusable(t, s, path, "")
}

func TestProductionFreshLazyBorrowerMethods(t *testing.T) {
	for _, method := range []string{"exec", "query", "exec_fallback", "query_fallback", "prepare", "begin", "ping", "returned_unused", "raw_discard", "unopened_close"} {
		t.Run(method, func(t *testing.T) {
			s, path := newRelayRow(t, "on", StateWorking)
			o := installLazyPool(t, s, path)
			held, err := s.db.Conn(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			o.nextPhysical(t)
			if strings.HasSuffix(method, "_fallback") {
				o.wrap = func(c driver.Conn) driver.Conn { return struct{ driver.Conn }{c} }
			}
			leases := make(chan *sql.Conn, 1)
			errs := make(chan error, 1)
			waits := s.db.Stats().WaitCount
			go func() { c, e := s.db.Conn(context.Background()); leases <- c; errs <- e }()
			waitPoolQueue(t, s.db, waits+1)
			discardLazyLease(t, held)
			var conn *sql.Conn
			select {
			case conn = <-leases:
			case <-time.After(2 * time.Second):
				t.Fatal("fresh lazy lease not delivered")
			}
			if err := <-errs; err != nil {
				t.Fatal(err)
			}
			if count := o.nextBackground(t); count != 1 || o.b.opens.Load() != 1 {
				t.Fatalf("fresh handoff opened native resources: %d/%d", count, o.b.opens.Load())
			}
			if method == "raw_discard" {
				discardLazyLease(t, conn)
				if o.b.opens.Load() != 1 {
					t.Error("unopened Raw discard performed native I/O")
				}
				return
			}
			if method == "unopened_close" {
				_ = conn.Close()
				if err := s.db.Close(); err != nil {
					t.Fatal(err)
				}
				if o.b.opens.Load() != 1 {
					t.Error("unopened Close/Validator performed native I/O")
				}
				return
			}
			type key struct{}
			ctx, cancel := context.WithTimeout(context.WithValue(context.Background(), key{}, method), time.Second)
			defer cancel()
			switch method {
			case "exec", "exec_fallback":
				_, err = conn.ExecContext(ctx, "CREATE TABLE lazy_probe (n INTEGER)")
			case "query", "query_fallback":
				var n int
				err = conn.QueryRowContext(ctx, "SELECT 1").Scan(&n)
			case "prepare":
				var stmt *sql.Stmt
				stmt, err = conn.PrepareContext(ctx, "SELECT ?")
				if err == nil {
					var n int
					err = stmt.QueryRowContext(ctx, 1).Scan(&n)
					_ = stmt.Close()
				}
			case "begin":
				var tx *sql.Tx
				tx, err = conn.BeginTx(ctx, nil)
				if err == nil {
					err = tx.Rollback()
				}
			case "ping":
				err = conn.PingContext(ctx)
			case "returned_unused":
				_ = conn.Close()
				conn, err = s.db.Conn(ctx)
			}
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			actual := o.nextPhysical(t)
			got, _ := actual.Deadline()
			want, _ := ctx.Deadline()
			if actual.Value(key{}) != method || !got.Equal(want) {
				t.Errorf("first %s lost actual borrower context: marker=%v deadline=%v", method, actual.Value(key{}), got)
			}
			requireLazySettings(t, conn)
			if o.b.opens.Load() != 2 {
				t.Errorf("native opens=%d want2", o.b.opens.Load())
			}
		})
	}
}
