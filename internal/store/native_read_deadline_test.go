package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// This unchanged regression also runs with the exact reconstructed pre-repair source overlay.
func TestDecisionNativeReadCannotOutliveCaller(t *testing.T) {
	s, path := newRelayRow(t, "on", StateWorking)
	insertRelay(t, s, relayReq(tokenA))
	b := installDeadlineBoundary(t, s, path)
	lease, err := s.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_ = lease.Close()
	entered, release := make(chan struct{}), make(chan struct{})
	timer := time.AfterFunc(2*time.Second, func() { close(release) })
	defer func() {
		if timer.Stop() {
			close(release)
		}
	}()
	b.before = func(_ context.Context, q string) error {
		if strings.Contains(q, "SELECT") {
			close(entered)
			<-release
		}
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = s.WithContext(ctx).GetPermissionRequestWithin(relayID, tokenA, 5*time.Second)
	if elapsed := time.Since(start); elapsed > 1500*time.Millisecond {
		t.Errorf("uncooperative native query returned after %v; want within original 500ms + 1s", elapsed)
	}
	if !errors.Is(err, ErrStoreBusy) {
		t.Errorf("expired read=%v want Busy", err)
	}
	select {
	case <-entered:
	default:
		t.Error("native query boundary not reached")
	}
	if timer.Stop() {
		close(release)
	}
	b.stopObservingAfterCleanup(t, s)
	assertDeadlineDatabaseReusable(t, s, path, "")
}
