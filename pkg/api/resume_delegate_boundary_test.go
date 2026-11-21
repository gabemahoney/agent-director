package api_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/resumedelegate"
	"github.com/gabemahoney/agent-director/pkg/api"
)

// b.kii / ip addendum CR1: unusable success replies from a started recipient
// are unknown outcomes, including legal JSON objects ordinary Unmarshal accepts.
// Reuse dj's existing sender fixture; this file is solely ip-owned.
func TestResumeDelegationInvalidSuccess(t *testing.T) {
	// Serial: newDelegateEnv changes HOME and observes the shared trail.
	for _, body := range []string{
		`{}`, `{"instance_id":%q}`, `{"pre_trust":"skipped"}`,
		`{"instance_id":null,"pre_trust":"skipped"}`,
		`{"instance_id":%q,"pre_trust":null}`,
		`{"instance_id":"different-row","pre_trust":"skipped"}`,
		`{"instance_id":%q,"pre_trust":"invented"}`,
	} {
		t.Run(body, func(t *testing.T) {
			e := newDelegateEnv(t)
			e.covering(t)
			body = strings.ReplaceAll(body, "%q", fmt.Sprintf("%q", e.row.ID))
			e.run(t, func(resumedelegate.Request) ([]byte, bool, error) {
				return []byte(`{"response_format":1,"result":` + body + `}`), true, nil
			})
			check := e.untouched(t)
			_, err := e.c.Resume(api.ResumeParams{InstanceID: e.row.ID})
			assertOneName(t, err, "ErrInternal")
			if err == nil || err.Error() != "delegated resume outcome is unknown; a Launch may have been created; check the agent with get before taking further action" || api.ErrDetails(err) != nil || len(e.calls) != 1 {
				t.Fatalf("unusable success: err=%v details=%+v calls=%d", err, api.ErrDetails(err), len(e.calls))
			}
			check()
		})
	}
}
