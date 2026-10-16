package api_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/internal/trail"
	"github.com/gabemahoney/agent-director/pkg/api"
)

// trailEncodingGate blocks JSON encoding while the actual singleton owns its mutex.
type trailEncodingGate struct {
	entered chan struct{}
	release chan struct{}
}

func (g *trailEncodingGate) MarshalJSON() ([]byte, error) {
	close(g.entered)
	<-g.release
	return []byte(`"released"`), nil
}

// TestDecideMaxWaitWithBlockedTrail keeps reporting blocked beyond the call's bound.
func TestDecideMaxWaitWithBlockedTrail(t *testing.T) {
	// Serial: it holds the process-wide trail singleton's mutex.
	e := newRelayEnv(t, relayEnvHook)
	observer, err := store.Open(e.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer observer.Close()
	gate := &trailEncodingGate{entered: make(chan struct{}), release: make(chan struct{})}
	var once sync.Once
	release := func() { once.Do(func() { close(gate.release) }) }
	defer release()
	emitted := make(chan error, 1)
	go func() {
		emitted <- trail.Emit(context.Background(), "ad.test.deadline_holder", map[string]any{"gate": gate})
	}()
	select {
	case <-gate.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("trail encoder did not acquire the real writer mutex")
	}
	defer func() {
		release()
		select {
		case err := <-emitted:
			if err != nil {
				t.Errorf("holder Emit: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("trail holder did not finish after release")
		}
	}()

	type outcome struct {
		result api.DecideResult
		err    error
		took   time.Duration
	}
	done := make(chan outcome, 1)
	view := e.view()
	view.Now = time.Now
	start := time.Now()
	const limit = 500*time.Millisecond + time.Second
	deadline := time.NewTimer(limit)
	defer deadline.Stop()
	go func() {
		res, err := api.Decide(e.s, view, api.DecideParams{InstanceID: e.id,
			RequestToken: storefix.TestRequestTokenA, Decision: "allow", MaxWaitMs: ptr(int64(500))})
		done <- outcome{result: res, err: err, took: time.Since(start)}
	}()

	// An independent SQL connection observes the authoritative commit before release.
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	committed := false
	var got outcome
	returned := false
observe:
	for {
		select {
		case got = <-done:
			returned = true
			break observe
		case <-deadline.C:
			t.Errorf("decide has not returned after %v; want within %v while the real trail mutex remains held", time.Since(start), limit)
			break observe
		case <-ticker.C:
			if !committed {
				pr, err := observer.GetPermissionRequestWithin(e.id, storefix.TestRequestTokenA, 0)
				if err != nil {
					t.Errorf("independent verdict read: %v", err)
					break observe
				}
				if pr.Decision == "allow" {
					committed = true
					t.Logf("independent connection observed committed allow at +%v with trail mutex still held", time.Since(start))
				}
			}
		}
	}
	if !committed {
		pr, err := observer.GetPermissionRequestWithin(e.id, storefix.TestRequestTokenA, 0)
		committed = err == nil && pr.Decision == "allow"
	}
	if !committed {
		t.Error("no independently committed allow while reporting remained blocked")
	}
	release()
	if !returned {
		select {
		case got = <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("decide did not finish within cleanup bound after releasing trail mutex")
		}
	}
	t.Logf("decide finished after %v; reporting released at +%v", got.took, time.Since(start))
	if got.took > limit {
		t.Errorf("decide returned after %v; want within %v", got.took, limit)
	}
	if got.err != nil || got.result.Delivery != api.DeliveryNotConfirmed {
		t.Errorf("committed decide = %+v, %v; want not_confirmed and no error", got.result, got.err)
	}
	if pr := e.request(t); pr.Decision != "allow" || pr.AttemptedDecision != "" {
		t.Errorf("request = decision %q, attempted %q; want allow, none", pr.Decision, pr.AttemptedDecision)
	}
}
