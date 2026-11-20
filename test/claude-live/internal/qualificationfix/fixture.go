// Package qualificationfix adapts ci's immutable capture for narrow offline
// qualification judgments. Added authority/product metadata is synthetic unit
// data; this fixture can never qualify today's candidate or a live phase.
// Future qualification analyzer/CLI tests must reuse this adapter; see
// docs/architecture.md, "Claude qualification recordings (reusable test
// fixture)", for the reuse instructions.
package qualificationfix

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/test/claude-live/internal/signalsfix"
	q "github.com/gabemahoney/agent-director/test/claude-live/qualification"
	"github.com/gabemahoney/agent-director/test/claude-live/runner"
)

type Fixture struct {
	*signalsfix.Fixture
	Ledger q.Ledger
	t      testing.TB
}

// New supplies the shared synthetic adaptation. Future qualification analyzer
// and CLI tests must reuse it under docs/architecture.md's fixture rules.
func New(t testing.TB, phase string) *Fixture {
	t.Helper()
	f := &Fixture{Fixture: signalsfix.Copy(t), t: t}
	f.Manifest.Phase = phase
	f.Manifest.TurnBudget = 0
	var installed runner.CandidateEvidence
	f.Decode("raw/candidate-installed.json", &installed)
	p := q.ProductSource{Schema: q.Schema, Algorithm: q.ProductAlgorithm, Toolchain: "unit-adaptation", Platform: installed.Kit.Platform,
		Snapshot: installed.Kit.SnapshotTree, SourceArchive: installed.Kit.SourceSHA256, InventorySHA256: runner.Digest([]byte("synthetic inventory")),
		Files: map[string]string{"go.mod": runner.Digest([]byte("unit pins")), "go.sum": runner.Digest([]byte("unit sums"))}}
	var err error
	p.Product, err = q.ProductDigest(p)
	if err != nil {
		t.Fatal(err)
	}
	f.Put("raw/qualification/source.json", p)
	b := f.Read("raw/qualification/source.json")
	installed.Kit.Files["qualification/source.json"] = runner.Artifact{SHA256: runner.Digest(b), Bytes: int64(len(b))}
	f.Put("raw/candidate-installed.json", installed)
	f.Ledger, err = q.NewLedger(q.Identity{Run: f.Manifest.RunID, Attempt: "unit-adaptation", Candidate: f.Manifest.Candidate, Product: p.Product,
		Provider: f.Manifest.ProviderVersion, Kit: f.Manifest.KitSHA256}, "full", q.SignalCharge{})
	if err != nil {
		t.Fatal(err)
	}
	f.Ledger.Selected = []string{phase}
	f.Put("raw/qualification-ledger-before.json", f.Ledger)
	f.Put("raw/qualification-ledger-after.json", f.Ledger)
	f.Put("raw/qualification-observations.json", []q.Observation{})
	for i, owner := range f.Manifest.Owned {
		prefix := owner.InstanceID + "-" + owner.LaunchToken
		for target, source := range map[string]string{
			"raw/final-" + prefix + "-store.json": f.Store(i),
			"raw/" + prefix + "-transcript.jsonl": f.Transcript(i),
			"raw/" + prefix + "-trail.jsonl":      "raw/" + owner.InstanceID + "-trail.jsonl",
		} {
			f.PutBytes(target, f.Read(source))
		}
	}
	f.Reindex()
	return f
}

func (f *Fixture) PutBytes(ref string, data []byte) {
	f.t.Helper()
	f.Write(ref, data)
	f.Manifest.Artifacts[ref] = runner.Artifact{SHA256: runner.Digest(data), Bytes: int64(len(data))}
}
func (f *Fixture) Put(ref string, value any) {
	f.t.Helper()
	b, err := json.Marshal(value)
	if err != nil {
		f.t.Fatal(err)
	}
	f.PutBytes(ref, append(b, '\n'))
}
func (f *Fixture) Capture() q.Capture {
	f.t.Helper()
	f.Reindex()
	c, err := q.LoadCapture(f.Root)
	if err != nil {
		f.t.Fatal(err)
	}
	// Unit observations added after loading share the same indexed evidence.
	c.Manifest.Artifacts = f.Manifest.Artifacts
	return c
}

// Packing selects the three actually sighted emissions; the terminal reserved
// tag exercise remains in the original immutable fixture, not this judgment.
func (f *Fixture) Packing() q.PackingEvidence {
	f.t.Helper()
	c := f.Capture()
	o := c.Owners[0]
	in := q.PackingEvidence{Owner: o.Owner, Snapshot: o.Snapshot, Transcript: o.Transcript, Trail: o.Trail, References: []string{o.SnapshotRef, o.TranscriptRef}}
	remove := map[string]bool{}
	var prompts []runner.Row
	for _, p := range in.Snapshot.Tables["prompts"] {
		if runner.Number(p, "order_at_ms") == 0 {
			remove[runner.Text(p, "handover_id")] = true
		} else {
			prompts = append(prompts, p)
		}
	}
	in.Snapshot.Tables["prompts"] = prompts
	for _, table := range []string{"handovers", "members"} {
		var rows []runner.Row
		for _, row := range in.Snapshot.Tables[table] {
			if !remove[runner.Text(row, "handover_id")] {
				rows = append(rows, row)
			}
		}
		in.Snapshot.Tables[table] = rows
	}
	for _, h := range c.Hooks {
		if runner.Text(h.Payload, "session_id") != o.Owner.SessionID || runner.Text(h.Payload, "agent_id") != "" {
			continue
		}
		copy := runner.Row{}
		for k, v := range h.Payload {
			copy[k] = v
		}
		copy["_received_ms"] = h.Receipt.Received.UnixMilli()
		in.Hooks = append(in.Hooks, copy)
	}
	for ref := range f.Manifest.Artifacts {
		if !strings.HasPrefix(ref, "raw/order-observations-") && !strings.HasSuffix(ref, "-store.json") {
			continue
		}
		var s runner.Snapshot
		f.Decode(ref, &s)
		if s.InstanceID == o.Owner.InstanceID {
			in.History = append(in.History, s)
		}
	}
	return in
}

func (f *Fixture) Path(ref string) string { return filepath.Join(f.Root, filepath.FromSlash(ref)) }
