package qualification_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/test/claude-live/internal/qualificationfix"
	"github.com/gabemahoney/agent-director/test/claude-live/internal/signalsfix"
	q "github.com/gabemahoney/agent-director/test/claude-live/qualification"
	"github.com/gabemahoney/agent-director/test/claude-live/runner"
)

// Reuse the owned adapter's copy/index; never create a second capture loader.
func retainUnit(t *testing.T, consumer *qualificationfix.Fixture, producer *signalsfix.Fixture) q.ImportRef {
	t.Helper()
	b := producer.Read("manifest.json")
	hash := runner.Digest(b)
	relative := "raw/imports/" + hash
	for ref := range producer.Manifest.Artifacts {
		consumer.PutBytes(relative+"/"+ref, producer.Read(ref))
	}
	consumer.PutBytes(relative+"/manifest.json", b)
	m := producer.Manifest
	return q.ImportRef{Relative: relative, ManifestSHA256: hash, Phase: m.Phase, Run: m.RunID, Candidate: m.Candidate, Provider: m.ProviderVersion, Kit: m.KitSHA256}
}

// Reuse the original ci witness and import adapter for compatibility controls.
func specialCIImport(t *testing.T, phase, kind string) (*qualificationfix.Fixture, q.Capture, q.ImportRef, string) {
	t.Helper()
	f := qualificationfix.New(t, phase)
	c := f.Capture()
	ci := signalsfix.Copy(t)
	var clip struct {
		Inventory struct {
			Tree  string            `json:"tree"`
			Files map[string]string `json:"files"`
		} `json:"inventory"`
		BuildLines  []string `json:"build_lines"`
		OriginalJob string   `json:"original_job"`
		Toolchain   string   `json:"toolchain"`
	}
	b, err := os.ReadFile("testdata/recorded/ci-build-witness.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(b, &clip); err != nil {
		t.Fatal(err)
	}
	// A two-pin UNIT product descriptor, using original build facts.
	// This exercises compatibility, not full current build provenance.
	c.Product.Toolchain = clip.Toolchain
	c.Product.Files = map[string]string{}
	for name, hash := range clip.Inventory.Files {
		c.Product.Files[name] = hash
	}
	c.Product.Product, err = q.ProductDigest(c.Product)
	if err != nil {
		t.Fatal(err)
	}
	c.Manifest.KitSHA256 = runner.Digest([]byte("different unit qualification kit"))
	c.Before.Identity.Kit = c.Manifest.KitSHA256
	c.Before.Signals = q.SignalCharge{Capture: ci.Manifest.RunID, Product: c.Product.Product, Provider: ci.Manifest.ProviderVersion, Actual: int(ci.Manifest.ObservedTurns)}
	var proof runner.CandidateEvidence
	ci.Decode("raw/candidate-installed.json", &proof)
	inventory, _ := json.Marshal(clip.Inventory)
	log := []byte(clip.BuildLines[0] + "\n")
	w := q.SourceWitness{Capture: ci.Manifest.RunID, Candidate: ci.Manifest.Candidate, Kit: ci.Manifest.KitSHA256, SourceArchive: proof.Kit.SourceSHA256, Snapshot: proof.Kit.SnapshotTree, OriginalJob: clip.OriginalJob, InventorySHA256: runner.Digest(inventory), BuildLogSHA256: runner.Digest(log), Toolchain: clip.Toolchain, Platform: proof.Kit.Platform}
	wantReason := ""
	switch kind {
	case "old-plan":
		ci.Manifest.ProbePlan.Version = "obsolete-completion-order-plan"
		ci.Reindex()
		wantReason = "historical completion-order/P-a capture has no current qualification authority"
	case "altered-product-input":
		c.Product.Files["go.mod"] = runner.Digest([]byte("different pinned input"))
		c.Product.Product, _ = q.ProductDigest(c.Product)
		c.Before.Signals.Product = c.Product.Product
		wantReason = "ci product code differs despite potentially identical version stamps"
	case "wrong-actual-use":
		c.Before.Signals.Actual++
		wantReason = "ci capture/actual charge differs from persisted authority"
	case "invalid-original-witness":
		w.Snapshot = "0000000000000000000000000000000000000000"
		wantReason = "ci original-build witness identity mismatch"
	}
	c.Before.Identity.Product = c.Product.Product
	c.After.Identity = c.Before.Identity
	f.PutBytes("raw/source-witnesses/"+w.InventorySHA256, inventory)
	f.PutBytes("raw/source-witnesses/"+w.BuildLogSHA256, log)
	f.Put("raw/scheduler-evidence.json", q.SchedulerEvidence{Schema: q.Schema, SourceWitnesses: []q.SourceWitness{w}})
	ref := retainUnit(t, f, ci)
	f.Put("raw/import-index.json", []q.ImportRef{ref})
	return f, c, ref, wantReason
}

func TestQualificationSpecialCIImport(t *testing.T) {
	for _, kind := range []string{"original-witness-separate-kit", "old-plan", "altered-product-input", "wrong-actual-use", "invalid-original-witness"} {
		t.Run(kind, func(t *testing.T) {
			_, c, ref, wantReason := specialCIImport(t, "groups", kind)
			got, err := q.CompatibleCIForTest(c, ref)
			if wantReason != "" {
				if err == nil || err.Error() != wantReason {
					t.Fatalf("ci semantic join: %v want %q", err, wantReason)
				}
				return
			}
			if err != nil || got.Verdict.Status != "pass" || got.Manifest.KitSHA256 == c.Manifest.KitSHA256 || got.Manifest.Candidate != ref.Candidate || got.Manifest.ProviderVersion != ref.Provider || got.Source.Product != c.Product.Product {
				t.Fatalf("retained ci identity/product: %+v %v", got, err)
			}
		})
	}
}

func TestQualificationGroupsRetainedForce(t *testing.T) {
	for _, kind := range []string{"missing-force", "substituted-phase"} {
		t.Run(kind, func(t *testing.T) {
			f := qualificationfix.New(t, "groups")
			c := f.Capture()
			var refs []q.ImportRef
			if kind != "missing-force" {
				phase := "force-states"
				if kind == "substituted-phase" {
					phase = "typed-controls"
				}
				pf, producer := unitPhysicalCapture(t, phase, "wait_turn", false)
				putOwnedForceCommand(pf, &producer, "F3-F4-force", api.ForceResult{ForceID: "unit-force", Outcome: "handed_over", Reason: "admitted"}, 8, 35)
				sealUnitCapture(t, pf, producer)
				refs = append(refs, retainUnit(t, f, pf.Fixture))
			}
			f.Put("raw/import-index.json", refs)
			f.Put("raw/group-plans.json", []q.GroupPlan{})
			rows := q.AnalyzeGroupsForTest(c)
			got := namedResult(t, rows, "force-group-import")
			if got.Status != "not_completed" || got.Reason != "required compatible phase import missing: force-states" {
				t.Fatalf("partial/absent producer satisfied force import: %+v", got)
			}
		})
	}
}

// Partial units test their own selected facts. PM2-1 below separately gates
// whole sealed producers; no partial unit is promoted to a passing import.
func TestQualificationLimitsProducerFacts(t *testing.T) {
	for _, kind := range []string{"compatible-cap-and-drain", "missing-F1", "missing-F2", "missing-typed-cap", "missing-O1-start", "missing-O2-whole"} {
		t.Run(kind, func(t *testing.T) {
			var capsVerdict runner.Verdict
			var drain runner.Result
			for _, phase := range []string{"force-states", "typed-controls", "groups"} {
				mode := "wait_turn"
				if phase == "typed-controls" {
					mode = "steer_turn"
				}
				pf, p := unitPhysicalCapture(t, phase, mode, phase == "groups")
				if phase == "force-states" {
					for i, id := range []string{"F1", "F2"} {
						if kind == "missing-"+id {
							continue
						}
						x := p.Manifest.Cases[i]
						x.ID = id
						p.Manifest.Cases = append(p.Manifest.Cases, x)
					}
				}
				if phase == "typed-controls" && kind != "missing-typed-cap" {
					x := p.Manifest.Cases[0]
					x.ID = "typed-long-tool-ad-input"
					p.Manifest.Cases = append(p.Manifest.Cases, x)
				}
				if phase == "groups" && kind == "missing-O1-start" {
					p.After.Charges = p.After.Charges[1:]
				}
				if phase == "groups" && kind == "missing-O2-whole" {
					o := p.Owners[len(p.Owners)-1]
					rows := pf.Rows(o.TranscriptRef)
					var journal bytes.Buffer
					enc := json.NewEncoder(&journal)
					for _, row := range rows {
						if runner.Text(row, "type") == "api-request" && runner.Text(row, "id") == "unit-request-1" {
							continue
						}
						if err := enc.Encode(row); err != nil {
							t.Fatal(err)
						}
					}
					pf.PutBytes(o.TranscriptRef, journal.Bytes())
				}
				sealUnitCapture(t, pf, p)
				loaded, err := q.LoadCapture(pf.Root)
				if err != nil {
					t.Fatal(err)
				}
				switch phase {
				case "force-states":
					for _, id := range []string{"F1", "F2"} {
						capsVerdict.Results = append(capsVerdict.Results, q.CheckCapForTest(loaded, id, "wait_turn"))
					}
				case "typed-controls":
					capsVerdict.Results = append(capsVerdict.Results, q.CheckCapForTest(loaded, "typed-long-tool-ad-input", "steer_turn"))
				case "groups":
					drain = namedResult(t, q.AnalyzeGroupsForTest(loaded), "ordinary-group")
				}
			}
			capsVerdict.Aggregate()
			wantCaps, wantDrain := "pass", "pass"
			if kind == "missing-F1" || kind == "missing-F2" || kind == "missing-typed-cap" {
				wantCaps = "not_completed"
			}
			if kind == "missing-O1-start" {
				wantDrain = "fail"
			}
			if kind == "missing-O2-whole" {
				wantDrain = "not_completed"
			}
			if capsVerdict.Status != wantCaps || drain.Status != wantDrain {
				t.Fatalf("imported joins: caps=%+v drain=%+v want %s/%s", capsVerdict, drain, wantCaps, wantDrain)
			}
		})
	}
}

// PM2-1: file-only admission must judge the whole sealed producer before the
// phase dispatcher can spend a consumer start. These are labelled unit facts.
func TestQualificationProducerImportGate(t *testing.T) {
	for _, kind := range []string{"force-workload-incomplete", "typed-partial-with-cap", "independent-accounting-failure", "nested-incomplete", "passing-producer"} {
		t.Run(kind, func(t *testing.T) {
			var pf *qualificationfix.Fixture
			var producer q.Capture
			consumerPhase, want := "force-states", ""
			switch kind {
			case "force-workload-incomplete", "typed-partial-with-cap":
				phase, mode := "force-states", "wait_turn"
				consumerPhase = "groups"
				if kind == "typed-partial-with-cap" {
					phase, mode, consumerPhase = "typed-controls", "steer_turn", "text-limits-backlog"
				}
				pf, producer = unitPhysicalCapture(t, phase, mode, false)
				producer.Manifest.Started = evidenceTime(-1)
				producer.Manifest.Deadline = producer.Manifest.Started.Add(runner.PhaseLimit)
				// Identity command bytes stay recorded; only their unit-envelope
				// times move alongside this synthetic physical schedule.
				for ref, cmd := range producer.Commands {
					if cmd.Name == "claude-version" || cmd.Name == "verify-agent-director" || cmd.Name == "verify-agent-director-admin" {
						cmd.Started = producer.Manifest.Started.Add(time.Microsecond)
						cmd.Ended = cmd.Started.Add(time.Microsecond)
						producer.Commands[ref] = cmd
					}
				}
				producer.Manifest.ObservedTurns = int64(len(producer.After.Charges))
				for i := range producer.After.Charges {
					charge := &producer.After.Charges[i]
					ref := charge.Prompt[strings.LastIndex(charge.Prompt, "@")+1:]
					charge.Evidence = []string{ref}
					for _, h := range producer.Hooks {
						if h.Reference == ref {
							charge.Prompt = runner.Text(h.Payload, "prompt_id") + "@" + ref
						}
					}
				}
				if phase == "force-states" {
					putOwnedForceCommand(pf, &producer, "F3-F4-force", api.ForceResult{ForceID: "unit-force", Outcome: "handed_over", Reason: "admitted"}, 8, 35)
				} else {
					producer.Manifest.Cases[0].ID = "typed-long-tool-ad-input"
				}
				producer.Manifest.Problems = []runner.Result{{SignalID: "workload/missing-next-tool", Status: "not_completed", Reason: "missing-next-tool: unit producer stopped before the remaining measurements", Evidence: []string{"manifest.json"}}}
				want = "not_completed"
			default:
				// A zero-new-turn signals phase reuses the real passing ci corpus
				// and its original-build witness, never a manufactured live run.
				pf, producer, _, _ = specialCIImport(t, "signals", "original-witness-separate-kit")
				producer.Manifest.ObservedTurns = 0
				producer.After = producer.Before
				pf.Put("raw/qualification/source.json", producer.Product)
				var installed runner.CandidateEvidence
				pf.Decode("raw/candidate-installed.json", &installed)
				data := pf.Read("raw/qualification/source.json")
				installed.KitSHA256 = producer.Manifest.KitSHA256
				installed.Kit.Files["qualification/source.json"] = runner.Artifact{SHA256: runner.Digest(data), Bytes: int64(len(data))}
				pf.Put("raw/candidate-installed.json", installed)
				if kind == "independent-accounting-failure" {
					producer.Manifest.ObservedTurns = 1 // selected ci clauses remain unchanged
					want = "failed"
				}
				if kind == "nested-incomplete" {
					producer.Manifest.Problems = []runner.Result{{SignalID: "unit/combined", Status: "pass", Observed: []runner.Result{{SignalID: "unit/missing-child", Status: "not_completed", Reason: "independent unit child lacks evidence", Evidence: []string{"manifest.json"}}}}}
					want = "not_completed"
				}
			}
			for i := range producer.Manifest.Owned {
				producer.Manifest.Owned[i].StoppedAt = producer.Manifest.Finished.Add(-time.Millisecond)
			}
			if producer.Manifest.Phase != "signals" {
				doc, caps := q.ContractInputsForTest(t)
				pf.PutBytes("raw/caller-contract.md", doc)
				pf.Put("raw/capabilities.json", caps)
			}
			sealUnitCapture(t, pf, producer)
			producer, err := q.LoadCapture(pf.Root)
			if err != nil {
				t.Fatalf("producer must be sealed and loadable before verdict gating: %v", err)
			}
			v := q.AnalyzePhase(pf.Root)
			if kind == "force-workload-incomplete" || kind == "typed-partial-with-cap" {
				if v.Status != "not_completed" || namedResult(t, v.Results, "workload/missing-next-tool").Status != "not_completed" {
					t.Fatalf("producer must be workload-incomplete, not corrupt/failed: %+v", v)
				}
				for _, id := range []string{"accounting", "cleanup", "contract"} {
					if got := namedResult(t, v.Results, id); got.Status != "pass" {
						t.Fatalf("workload producer prerequisite %s: %+v", id, got)
					}
				}
				if kind == "typed-partial-with-cap" && q.CheckCapForTest(producer, "typed-long-tool-ad-input", "steer_turn").Status != "pass" {
					t.Fatal("partial typed producer lost the valid selected long-tool cap")
				}
			} else {
				if namedResult(t, v.Results, "signal-capture").Status != "pass" {
					t.Fatal("selected ci facts must stay passing independently of producer failure/gap")
				}
				status := "pass"
				if kind == "independent-accounting-failure" {
					status = "fail"
					if namedResult(t, v.Results, "accounting").Reason != "raw physical parent starts and observed counter differ" {
						t.Fatal("independent failure must reach accounting, not another guard")
					}
				}
				if v.Status != status {
					t.Fatalf("producer status=%s want %s: %+v", v.Status, status, v.Results)
				}
			}
			for _, boundary := range []string{"operational", "offline"} {
				t.Run(boundary, func(t *testing.T) {
					f := qualificationfix.New(t, consumerPhase)
					consumer := f.Capture()
					consumer.Before.Identity = producer.Before.Identity
					consumer.Before.Prior = producer.Before.Prior
					consumer.Product = producer.Product
					consumer.Manifest.KitSHA256 = producer.Manifest.KitSHA256
					if err := q.CompatibleCapture(consumer, producer); err != nil {
						t.Fatalf("producer must be identity-compatible: %v", err)
					}
					var err error
					if boundary == "offline" {
						ref := retainUnit(t, f, pf.Fixture)
						f.Put("raw/import-index.json", []q.ImportRef{ref})
						_, _, err = q.ImportPhaseForTest(consumer, producer.Manifest.Phase)
						if err != nil && (consumerPhase == "groups" || consumerPhase == "text-limits-backlog") {
							if consumerPhase == "groups" {
								f.Put("raw/group-plans.json", []q.GroupPlan{})
							}
							rows := q.AnalyzeGroupsForTest(consumer)
							id := "force-group-import"
							if consumerPhase == "text-limits-backlog" {
								rows, id = q.AnalyzeLimitsForTest(consumer), "ad-caps"
							}
							got := namedResult(t, rows, id)
							data, marshalErr := json.Marshal(got)
							if marshalErr != nil || got.Status != "not_completed" || !strings.Contains(string(data), err.Error()) {
								t.Fatalf("offline consumer lost its nonpassing import judgment: %+v; %v", got, marshalErr)
							}
						}
					} else {
						evidence, er := runner.NewEvidence(filepath.Join(t.TempDir(), "consumer"))
						if er != nil {
							t.Fatal(er)
						}
						if er = evidence.JSON("ledger.json", consumer.Before); er != nil {
							t.Fatal(er)
						}
						if er = evidence.JSON("raw/qualification/source.json", producer.Product); er != nil {
							t.Fatal(er)
						}
						r := &runner.Run{Evidence: evidence, Manifest: consumer.Manifest}
						r.Manifest.Owned, r.Manifest.Cases = nil, nil
						r.Manifest.ObservedTurns = 0
						calls := 0
						r.Commands = &runner.Commands{Evidence: evidence, Now: time.Now, Execute: func(context.Context, []string, []string, io.Writer, io.Writer) (int, error) { calls++; return 1, nil }}
						e := q.NewExercise(r, filepath.Join(evidence.Root, "ledger.json"))
						e.Options.Imports = []string{pf.Root}
						// Only the typed case needs additional required phase names.
						// They follow it, so its own verdict must be the rejection.
						if consumerPhase == "text-limits-backlog" {
							for _, phase := range []string{"force-states", "groups"} {
								other := qualificationfix.New(t, phase)
								e.Options.Imports = append(e.Options.Imports, other.Root)
							}
						}
						before := append([]byte(nil), mustReadImportFile(t, e.Ledger)...)
						err = q.RetainImportsForTest(e)
						if calls != 0 || len(r.Manifest.Owned) != 0 || len(r.Manifest.Cases) != 0 || r.Manifest.ObservedTurns != 0 || !bytes.Equal(before, mustReadImportFile(t, e.Ledger)) {
							t.Fatal("import gate spent a consumer spawn/reservation/input")
						}
						if _, er = runner.ReadManifest(filepath.Join(evidence.Root, "raw/imports", runner.Digest(pf.Read("manifest.json")))); er != nil {
							t.Fatalf("rejected producer must stay retained for diagnosis: %v", er)
						}
					}
					if want == "" {
						if err != nil {
							t.Fatalf("passing producer rejected: %v", err)
						}
					} else if err == nil || !strings.Contains(err.Error(), "phase import prerequisite "+want+": "+producer.Manifest.Phase) {
						t.Fatalf("%s admitted %s or rejected for another reason: %v; want phase prerequisite %s", boundary, kind, err, want)
					}
				})
			}
		})
	}
}

func mustReadImportFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
