package store_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
	"github.com/gabemahoney/agent-director/pkg/api/errnames"
)

// F10 follows unconfirmed COMMIT's "read the request before retrying" through public GetPermission.
func TestAdviceFollow_F10_UnconfirmedCommitInspectBeforeRetry(t *testing.T) {
	t.Parallel()
	hook := store.ProcessIdentity{PID: 4321, Starttime: "777", PIDNamespace: "pid:[4026531836]"}
	now := time.Now()
	s, path := apitest.SeedDecideFixtureAgent(t, "on", store.AgentClaude, store.RelayRequest{
		RequestToken: storefix.TestRequestTokenA, ToolName: "Bash", ToolInput: `{}`, Hook: hook, SettledAt: now.Add(time.Hour),
	})
	pc := procfix.New()
	pc.Set(hook.PID, procfix.Alive(hook.Starttime))
	v := api.RelayView{Procs: pc, PIDNamespace: func() (string, bool) { return hook.PIDNamespace, true }, Now: func() time.Time { return now }, Window: time.Hour}
	var commitAttempts, verdictUpdates atomic.Int32
	store.ObserveDecisionBoundaryForTest(t, s, path, func(_ context.Context, q string) error {
		if q == "COMMIT" {
			commitAttempts.Add(1)
		}
		if strings.Contains(q, "UPDATE permission_requests") && strings.Contains(q, "decision") {
			verdictUpdates.Add(1)
		}
		return nil
	}, func(ctx context.Context) error {
		<-ctx.Done() // SQLite has committed; only its response is uncertain.
		return ctx.Err()
	})
	budget := int64(100)
	_, err := api.Decide(s, v, api.DecideParams{InstanceID: "id-d-1", RequestToken: storefix.TestRequestTokenA, Decision: "allow", MaxWaitMs: &budget})
	name, description := errnames.Classify(err)
	if name != "ErrInternal" || errors.Is(err, api.ErrStoreBusy) || !strings.Contains(description, "commit outcome unconfirmed; read the request before retrying") {
		t.Fatalf("actual public failure=%s %q; want uncertain COMMIT inspection advice", name, description)
	}
	// Follow the advice before considering another decision, through a fresh public reader.
	reader, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	inspection := func() api.GetPermissionResult {
		t.Helper()
		got, err := api.GetPermission(reader, v, api.GetPermissionParams{RequestToken: storefix.TestRequestTokenA})
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	got := inspection()
	if got.RequestToken != storefix.TestRequestTokenA || got.Decision == nil || *got.Decision != "allow" || got.DecidedAt == nil || got.Delivery != api.DeliveryNotConfirmed {
		t.Fatalf("durable public inspection=%+v; want committed allow with unconfirmed delivery", got)
	}
	// A committed allow needs observation, never replay. Poll once more without deciding.
	if after := inspection(); !reflect.DeepEqual(got, after) {
		t.Errorf("inspection changed durable request: before=%+v after=%+v", got, after)
	}
	if commitAttempts.Load() != 1 || verdictUpdates.Load() != 1 {
		t.Errorf("recovery replayed verdict: COMMIT attempts=%d verdict updates=%d", commitAttempts.Load(), verdictUpdates.Load())
	}
}
