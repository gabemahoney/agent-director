package api_test

// advice_follow_findmissing_test.go: literal-follow tests for find-missing's advice (area D, b.fji).

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/manifest"
)

// TestAdviceFollow_D1_UnreportedReadPaneThenSendKeys (D1, b.kdf): "To act on it: read-pane, then send-keys with
// allow_pending (--allow-pending on the CLI); only a caller that looked should type." (get's liveness_note; find-missing
// and list say the same). Followed after find-missing noted a live pending row, found by liveness_note in get and list:
// read-pane reads its pane, and send-keys with allow_pending delivers the text and Enter into that pane.
func TestAdviceFollow_D1_UnreportedReadPaneThenSendKeys(t *testing.T) {
	t.Parallel()
	adviceAssertManifest(t, "find-missing", "", "Past its grace, a pending row whose agent runs but no hook reported "+
		"gets liveness_note unreported: read-pane, and only having looked, send-keys with allow_pending.")
	for _, rf := range []struct{ verb, field, advice string }{
		{"get", "liveness_note", "To act on it: read-pane, then send-keys with allow_pending (--allow-pending on the CLI); " +
			"only a caller that looked should type."},
		{"list", "spawns", "read-pane, and only having looked, send-keys with allow_pending (--allow-pending on the CLI)"},
		{"find-missing", "unverified_ids", "a pending row noted unreported (its agent alive, no hook reported since its " +
			"launch; find it by liveness_note in get or list) are in neither list"},
	} {
		v, _ := manifest.Lookup(rf.verb)
		i := slices.IndexFunc(v.ResultFields, func(f manifest.FieldDef) bool { return f.Name == rf.field })
		if i < 0 {
			t.Fatalf("manifest %s has no %s result field", rf.verb, rf.field)
		}
		if text := v.ResultFields[i].Description; !strings.Contains(text, rf.advice) {
			t.Errorf("%s %s text %q\nwant it to carry %q", rf.verb, rf.field, text, rf.advice)
		}
	}

	e := newKillEnv(t)
	r := e.seedRow(t, e.pendingSpec(pendingOurs))
	e.clock.Advance(fmGrace + time.Second)
	c, _ := e.client(t)
	if res, err := c.FindMissing(context.Background()); err != nil || len(res.IDs) != 0 || len(res.UnverifiedIDs) != 0 {
		t.Fatalf("FindMissing = %+v, %v; want the row in neither list", res, err)
	}
	row, err := c.Get(r.ID)
	if err != nil || row.State != store.StatePending || row.LivenessNote == nil || *row.LivenessNote != "unreported" {
		t.Fatalf("Get = %+v, %v; want pending, noted unreported", row, err)
	}
	listed, err := c.List(api.ListParams{State: []string{store.StatePending}})
	i := slices.IndexFunc(listed.Spawns, func(s api.ListRow) bool { return s.ClaudeInstanceID == r.ID })
	if err != nil || i < 0 || listed.Spawns[i].LivenessNote == nil || *listed.Spawns[i].LivenessNote != "unreported" {
		t.Fatalf("List = %+v, %v; want the row, noted unreported", listed, err)
	}

	e.setPaneTexts(r.Socket)
	pane := r.Spawn.Identity.PaneID
	if got, err := c.ReadPane(api.ReadPaneParams{ClaudeInstanceID: r.ID}); err != nil || got.Pane != paneText(r.Socket, pane) {
		t.Fatalf("ReadPane = %q, %v; want the row's pane %q", got.Pane, err, paneText(r.Socket, pane))
	}
	e.rec.Reset()
	if _, err := c.SendKeys(api.SendKeysParams{ClaudeInstanceID: r.ID, Text: skpText, AllowPending: true}); err != nil {
		t.Fatalf("SendKeys with allow_pending: %v", err)
	}
	e.assertDelivered(t, r.Socket, pane, skpText)
}
