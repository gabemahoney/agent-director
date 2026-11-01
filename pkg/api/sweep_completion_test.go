package api

import (
	"context"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
)

// b.x9o cb consumes the outcomes whose classification 2e already pins.
// Latest adapter coverage and cancellation govern completeness, not success.
func TestSweepCompletionConsumesOutcomes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name               string
		outcomes           []activeCheckOutcome
		completion, reason string
	}{
		{"all attempted but unsuccessful", []activeCheckOutcome{{Adapter: "liveness", Result: "unreadable"}, {Adapter: "display", Result: "unconfirmed"}}, "complete", ""},
		{"attempt replaced initial placeholder", []activeCheckOutcome{{Adapter: "display", Result: "unattempted"}, {Adapter: "display", Result: "unchanged"}}, "complete", ""},
		{"inapplicable replaced placeholder", []activeCheckOutcome{{Adapter: "liveness", Result: "unattempted"}, {Adapter: "liveness", Result: "inapplicable"}}, "complete", ""},
		{"unattempted", []activeCheckOutcome{{Adapter: "display", Result: "unattempted"}}, "incomplete", "unattempted"},
		{"unsupported", []activeCheckOutcome{{Adapter: "display", Result: "unavailable"}}, "incomplete", "unsupported"},
		{"remaining", []activeCheckOutcome{{Adapter: "display", Result: "unattempted"}, activeOutcome("remaining", false, context.Canceled)}, "incomplete", "deadline"},
		{"cancelled attempt", []activeCheckOutcome{activeOutcome("display", false, context.DeadlineExceeded)}, "incomplete", "deadline"},
		{"skipped busy", []activeCheckOutcome{{Adapter: "display", Result: "unattempted", Err: store.ErrStoreBusy}}, "incomplete", "busy"},
		{"stale with unattempted", []activeCheckOutcome{{Adapter: "refresh", Result: "stale"}, {Adapter: "display", Result: "unattempted"}}, "incomplete", "stale_observation"},
		{"unconfirmed with unattempted", []activeCheckOutcome{{Adapter: "cursor_commit", Result: "unconfirmed"}, {Adapter: "display", Result: "unattempted"}}, "incomplete", "commit_unconfirmed"},
		{"last specific cause", []activeCheckOutcome{{Adapter: "refresh", Result: "stale"}, {Adapter: "permission_read", Result: "absent"}, {Adapter: "display", Result: "unattempted"}}, "incomplete", "no_evidence"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, reason := sweepCompletion(tc.outcomes)
			if got != tc.completion || (reason == nil) != (tc.reason == "") || reason != nil && *reason != tc.reason {
				t.Errorf("completion = %s/%v; want %s/%s", got, reason, tc.completion, tc.reason)
			}
		})
	}
}
