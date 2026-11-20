package qualification

import (
	"encoding/json"
	"path/filepath"
	"strings"

	"github.com/gabemahoney/agent-director/test/claude-live/runner"
	"github.com/gabemahoney/agent-director/test/claude-live/signals"
)

// SourceWitness is needed only for a retained pre-qualification ci kit. It
// points to that build's ORIGINAL inventory and build-log facts; a new harness
// cannot restamp old signal evidence as its own source snapshot.
type SourceWitness struct {
	Capture         string `json:"capture"`
	Candidate       string `json:"candidate"`
	Kit             string `json:"kit"`
	SourceArchive   string `json:"source_archive"`
	Snapshot        string `json:"snapshot"`
	OriginalJob     string `json:"original_job"`
	Inventory       string `json:"inventory"`
	InventorySHA256 string `json:"inventory_sha256"`
	BuildLog        string `json:"build_log"`
	BuildLogSHA256  string `json:"build_log_sha256"`
	Toolchain       string `json:"toolchain"`
	Platform        string `json:"platform"`
}

type CIResolution struct {
	Manifest runner.Manifest
	Verdict  runner.Verdict
	Relative string
	Source   ProductSource
}

func (c Capture) compatibleCI(ref ImportRef) (CIResolution, error) {
	out := CIResolution{Relative: ref.Relative}
	root, err := runner.Confined(c.Root, ref.Relative)
	if err != nil {
		return out, err
	}
	m, err := runner.ReadManifest(root)
	out.Manifest = m
	if err != nil {
		return out, err
	}
	if m.Phase != "probe" || m.ProbePlan == nil || m.ProbePlan.Version != signals.PlanVersion {
		return out, corrupt("historical completion-order/P-a capture has no current qualification authority")
	}
	if m.ProviderVersion != c.Manifest.ProviderVersion {
		return out, corrupt("ci provider version differs")
	}
	out.Verdict = signals.Analyze(root)
	if out.Verdict.Status != "pass" {
		return out, absent("compatible ci signal analyzer did not pass every revised clause")
	}
	var proof runner.CandidateEvidence
	data, err := runner.Read(root, "raw/candidate-installed.json")
	if err != nil {
		return out, err
	}
	if err = json.Unmarshal(data, &proof); err != nil {
		return out, err
	}
	sourceRef := "raw/candidate-source-installed.json"
	if _, ok := m.Artifacts[sourceRef]; ok {
		source, er := LoadProductSource(root, sourceRef)
		if er != nil {
			return out, er
		}
		b, er := runner.Read(root, sourceRef)
		if er != nil {
			return out, er
		}
		a, ok := proof.Kit.Files["qualification/source.json"]
		if !ok || a.SHA256 != runner.Digest(b) || int64(len(b)) != a.Bytes {
			return out, corrupt("ci product inputs are not its kit asset")
		}
		if er = VerifyProductKit(source, proof.Kit); er != nil {
			return out, er
		}
		out.Source = source
	} else {
		var bundle SchedulerEvidence
		if err = c.json("raw/scheduler-evidence.json", &bundle); err != nil {
			return out, absent("ci lacks original source provenance; supply the retained original build inventory/log witness")
		}
		found := false
		for _, w := range bundle.SourceWitnesses {
			if w.Capture != m.RunID {
				continue
			}
			if w.Candidate != m.Candidate || w.Kit != m.KitSHA256 || w.SourceArchive != proof.Kit.SourceSHA256 || w.Snapshot != proof.Kit.SnapshotTree || w.OriginalJob == "" || w.Platform != proof.Kit.Platform || !digestText(w.InventorySHA256, 32) || !digestText(w.BuildLogSHA256, 32) {
				return out, corrupt("ci original-build witness identity mismatch")
			}
			b, er := c.read("raw/source-witnesses/" + w.InventorySHA256)
			if er != nil {
				return out, er
			}
			if runner.Digest(b) != w.InventorySHA256 {
				return out, corrupt("ci original inventory changed")
			}
			var inventory struct {
				Tree  string            `json:"tree"`
				Files map[string]string `json:"files"`
			}
			if er = json.Unmarshal(b, &inventory); er != nil {
				return out, er
			}
			if inventory.Tree != proof.Kit.SnapshotTree {
				return out, corrupt("ci inventory belongs to another original snapshot")
			}
			log, er := c.read("raw/source-witnesses/" + w.BuildLogSHA256)
			if er != nil {
				return out, er
			}
			if runner.Digest(log) != w.BuildLogSHA256 || !strings.Contains(string(log), "KIT_BUILT "+m.KitSHA256+" "+w.Platform) {
				return out, corrupt("ci original build log does not bind its kit archive")
			}
			original := ProductSource{Schema: Schema, Algorithm: ProductAlgorithm, Platform: w.Platform, Toolchain: w.Toolchain, Snapshot: w.Snapshot, SourceArchive: w.SourceArchive, InventorySHA256: w.InventorySHA256, Files: map[string]string{}}
			for name := range c.Product.Files {
				hash, ok := inventory.Files[name]
				if !ok {
					return out, corrupt("original ci inventory lacks a current product build input")
				}
				original.Files[name] = hash
			}
			original.Product, er = ProductDigest(original)
			if er != nil {
				return out, er
			}
			out.Source = original
			found = true
			break
		}
		if !found {
			return out, absent("original ci source witness missing")
		}
	}
	if out.Source.Product != c.Product.Product {
		return out, corrupt("ci product code differs despite potentially identical version stamps")
	}
	if c.Before.Signals.Capture != m.RunID || c.Before.Signals.Actual != int(m.ObservedTurns) || c.Before.Signals.Product != out.Source.Product || c.Before.Signals.Provider != m.ProviderVersion {
		return out, corrupt("ci capture/actual charge differs from persisted authority")
	}
	return out, nil
}

func (c Capture) ciResults() []runner.Result {
	refs, err := c.imports()
	if err != nil {
		return []runner.Result{result("signal-capture", evidenceStatus(err), err.Error(), "compatible revised ci recording", nil)}
	}
	for _, ref := range refs {
		if ref.Phase != "probe" {
			continue
		}
		proof, er := c.compatibleCI(ref)
		if er != nil {
			return []runner.Result{result("signal-capture", evidenceStatus(er), er.Error(), "same product/provider with revised clauses and actual ci charge", nil)}
		}
		results := []runner.Result{result("signal-capture", "pass", "compatible ci recording; separately identified kit/capture, charged once", "revised ci analyzer PASS", map[string]any{"run": proof.Manifest.RunID, "kit": proof.Manifest.KitSHA256, "candidate": proof.Manifest.Candidate, "product": proof.Source.Product, "turns": proof.Manifest.ObservedTurns}, ref.Relative+"/manifest.json")}
		for _, mapping := range []struct{ target, source string }{{"p-b", signals.PB}, {"p-c", signals.PC}, {"turn-end-premise", "turn-end-order"}, {"wake-order-point", "hook-completion-order"}} {
			found := false
			for _, r := range proof.Verdict.Results {
				if r.SignalID == mapping.source {
					r.SignalID = mapping.target
					r.Evidence = prefixRefs(ref.Relative, r.Evidence)
					results = append(results, r)
					found = true
				}
			}
			if !found {
				results = append(results, result(mapping.target, "not_completed", "revised ci clause missing", mapping.source, nil))
			}
		}
		return results
	}
	return []runner.Result{result("signal-capture", "not_completed", "no retained compatible ci capture", "revised ci source with original provenance", nil)}
}
func prefixRefs(prefix string, refs []string) []string {
	out := make([]string, 0, len(refs))
	for _, r := range refs {
		out = append(out, filepath.ToSlash(filepath.Join(prefix, r)))
	}
	return out
}

func (c Capture) importPhase(phase string) (Capture, ImportRef, error) {
	refs, err := c.imports()
	if err != nil {
		return Capture{}, ImportRef{}, err
	}
	for _, ref := range refs {
		if ref.Phase == phase {
			producer, er := c.loadPhaseImport(ref)
			return producer, ref, er
		}
	}
	return Capture{}, ImportRef{}, absent("required compatible phase import missing: %s", phase)
}
