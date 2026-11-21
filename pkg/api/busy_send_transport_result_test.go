package api_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/busysend"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api"
)

// TestBusySendTransportResult (SR-5.1 to SR-5.3; xe-repair-2.md F4): the
// transport an issuing job's completion records reaches get-delivery's
// compatibility outcome: a partial write is partial_input_unconfirmed and an
// uncertain whole prompt submission_unconfirmed, neither resend-safe, and the
// route's acceptance that makes the Result Final keeps it, after a reopen
// too. A transport fact recorded only after the Result is Final changes
// nothing get-delivery shows.
func TestBusySendTransportResult(t *testing.T) {
	t.Parallel()
	e := newKillEnv(t)
	c, _ := e.client(t)
	for _, tc := range []struct {
		name          string
		output        store.BusySendOutput
		acceptedFirst bool
		outcome       string
	}{
		{"partial", store.BusySendOutputPartial, false, "partial_input_unconfirmed"},
		{"uncertain", store.BusySendOutputUncertain, false, "submission_unconfirmed"},
		{"partial after Final", store.BusySendOutputPartial, true, "submission_unconfirmed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			session := uuid.NewString()
			r := e.seedRow(t, killRowSpec{SessionID: session})
			seedBusyCovered(t, e, r.ID)
			busyWake(t, e, r.ID)
			busyAdmit(t, c, api.SendKeysParams{InstanceID: r.ID, Text: tc.name, ConfirmedDelivery: ptr(true), CallerUUID: ptr(busyCaller),
				DeliveryRequestID: tc.name})
			dest := busyDestination(t, e, r.ID, session)
			accept := func(p busysend.Prepared) {
				if _, err := e.st.RecordBusySendEvidence(context.Background(), store.BusySendPromptEvidence{HandoverID: p.HandoverID,
					PromptIndex: p.PromptIndex, LaunchID: p.Destination.LaunchID, ConnectionGeneration: p.Destination.ConnectionGeneration,
					Order: store.BusySendOrderEmissionComplete, UnprovenBoundary: true, At: e.clock.Now()}); err != nil {
					t.Error(err)
				}
			}
			route := &busyRoute{dest: dest, out: busysend.Disposition{Output: tc.output}}
			if tc.acceptedFirst {
				route.before = accept
			}
			deliverBusy(t, e, r.ID, route)
			lookup := func(c *api.Client) api.DeliveryResult {
				t.Helper()
				got, err := c.GetDelivery(api.GetDeliveryParams{CallerUUID: busyCaller, DeliveryRequestID: tc.name})
				if err != nil || got.Result == nil {
					t.Fatalf("GetDelivery = %+v, %v", got, err)
				}
				return *got.Result
			}
			token := dest.LaunchToken
			issued := api.DeliveryResult{CallerUUID: busyCaller, RequestID: tc.name, Class: "sent_unconfirmed", Reason: "handover_in_progress",
				RetryWith: "none", Wait: &api.DeliveryWait{Reason: "handover_in_progress", Since: e.clock.Now()}, InstanceID: r.ID,
				LaunchToken: &token, SessionID: session, Outcome: tc.outcome}
			final := issued
			final.Final, final.Reason, final.Wait = true, "handed_over_not_proven", nil
			if !tc.acceptedFirst {
				if got := lookup(c); !reflect.DeepEqual(got, issued) {
					t.Errorf("Result after the %s completion = %+v; want %+v", tc.output, got, issued)
				}
				accept(route.issued[0])
			}
			reopened, _ := e.client(t)
			for _, c := range []*api.Client{c, reopened} {
				if got := lookup(c); !reflect.DeepEqual(got, final) {
					t.Errorf("Final Result = %+v; want %+v", got, final)
				}
			}
		})
	}
}
