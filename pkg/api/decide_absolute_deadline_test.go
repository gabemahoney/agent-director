package api_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/internal/trail"
	"github.com/gabemahoney/agent-director/pkg/api"
)

type parkedDecideStore struct {
	api.DecideStore
	real    *store.Store
	ctx     context.Context
	entered chan struct{}
}

func (s *parkedDecideStore) WithDecideContext(ctx context.Context) api.DecideStore {
	scoped := *s
	scoped.ctx, scoped.DecideStore = ctx, s.real.WithContext(ctx)
	return &scoped
}

func (s *parkedDecideStore) GetSpawnWithin(id string, wait time.Duration) (api.Spawn, error) {
	close(s.entered)
	<-s.ctx.Done()
	// The relative allowance was captured before parking; the real Store must
	// still see the original expired context when its phase finally starts.
	return s.DecideStore.GetSpawnWithin(id, wait)
}

func TestDecideAbsoluteContextSurvivesParkedPhase(t *testing.T) {
	t.Parallel()
	e := newRelayEnv(t, relayEnvHook)
	s := &parkedDecideStore{DecideStore: e.s, real: e.s, entered: make(chan struct{})}
	start := time.Now()
	_, err := api.Decide(s, e.view(), api.DecideParams{InstanceID: e.id, RequestToken: storefix.TestRequestTokenA, Decision: "allow", MaxWaitMs: ptr(int64(100))})
	if !errors.Is(err, api.ErrStoreBusy) || time.Since(start) > 1100*time.Millisecond {
		t.Fatalf("parked phase result=%v after %v", err, time.Since(start))
	}
	select {
	case <-s.entered:
	default:
		t.Fatal("real caller did not reach phase barrier")
	}
	if pr := e.request(t); pr.Decision != "" || pr.AttemptedDecision != "" {
		t.Errorf("expired precommit call mutated request: %+v", pr)
	}
	// caller-contract: "Retry only as a new, deliberate call with a fresh budget."
	// The parked call held no competing lock. Deliberately start a fresh operation,
	// then inspect its durable request through the public recovery verb.
	if _, err := api.Decide(e.s, e.view(), api.DecideParams{InstanceID: e.id, RequestToken: storefix.TestRequestTokenA, Decision: "allow", MaxWaitMs: ptr(int64(500))}); err != nil {
		t.Fatalf("deliberate fresh-budget decision: %v", err)
	}
	got := e.getPermission(t, e.s)
	if got.Decision == nil || *got.Decision != "allow" || got.RequestToken != storefix.TestRequestTokenA {
		t.Fatalf("public inspection after fresh-budget call=%+v", got)
	}
}

func TestClientDecideBoundsDeferredAuditAndPreservesFIFO(t *testing.T) {
	// Serial: it holds the process-wide trail singleton's mutex and reads its audit.
	e := newRelayEnv(t, relayEnvHook)
	c, err := api.New(api.Options{StorePath: e.dbPath})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	api.SetClockForTest(c, time.Now)
	api.SetProcCheckerForTest(c, e.pc)
	api.SetSelfPIDNSForTest(c, func() (string, bool) { return relayNS, true })
	mark := len(readAPITrailLines(t))
	gate := &trailEncodingGate{entered: make(chan struct{}), release: make(chan struct{})}
	var once sync.Once
	release := func() { once.Do(func() { close(gate.release) }) }
	defer release()
	holder := make(chan error, 1)
	go func() {
		holder <- trail.Emit(context.Background(), "ad.test.client_deadline_holder", map[string]any{"gate": gate})
	}()
	select {
	case <-gate.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("holder did not enter real writer")
	}
	defer func() {
		release()
		select {
		case err := <-holder:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(3 * time.Second):
			t.Error("holder did not finish")
		}
	}()
	before := trail.Default().Status().Pending
	type outcome struct {
		result api.DecideResult
		err    error
	}
	done := make(chan outcome, 1)
	start := time.Now()
	go func() {
		result, err := c.Decide(api.DecideParams{InstanceID: e.id, RequestToken: storefix.TestRequestTokenA, Decision: "allow", MaxWaitMs: ptr(int64(500))})
		done <- outcome{result, err}
	}()
	select {
	case got := <-done:
		if got.err != nil || got.result.Delivery != api.DeliveryNotConfirmed {
			t.Errorf("Client.Decide=%+v, %v", got.result, got.err)
		}
	case <-time.After(1500 * time.Millisecond):
		t.Fatal("Client.Decide deferred called event exceeded bound")
	}
	t.Logf("Client.Decide including deferred audit returned at %v while writer held", time.Since(start))
	if pr := e.request(t); pr.Decision != "allow" || pr.AttemptedDecision != "" {
		t.Errorf("independent committed request=%+v", pr)
	}
	if got := trail.Default().Status().Pending; got != before+2 {
		t.Errorf("pending audit=%d; want prior %d plus committed/called", got, before)
	}
	closed := make(chan error, 1)
	go func() { closed <- c.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Error(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Client.Close waited for blocked reporting")
	}
	if _, err := c.Decide(api.DecideParams{}); !errors.Is(err, api.ErrClientClosed) {
		t.Errorf("closed client result=%v", err)
	}
	release()
	// A same-worker sentinel acknowledges physical FIFO drain without closing the singleton.
	drain, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := trail.Emit(drain, "ad.test.client_deadline_drained", nil); err != nil {
		t.Fatal(err)
	}
	var events []string
	for _, row := range readAPITrailLines(t)[mark:] {
		if row["instance_id"] == e.id && row["request_token"] == storefix.TestRequestTokenA {
			if event, _ := row["event"].(string); event == "ad.row_mutation.committed" || event == "ad.decide.called" {
				events = append(events, event)
			}
		}
	}
	if len(events) != 2 || events[0] != "ad.row_mutation.committed" || events[1] != "ad.decide.called" {
		t.Errorf("commit/called audit ordering=%v", events)
	}
}
