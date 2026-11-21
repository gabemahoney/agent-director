package qualification

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/gabemahoney/agent-director/test/claude-live/runner"
)

type ImportRef struct {
	Relative       string `json:"relative"`
	Phase          string `json:"phase"`
	Run            string `json:"run"`
	Candidate      string `json:"candidate"`
	Provider       string `json:"provider"`
	Kit            string `json:"kit"`
	ManifestSHA256 string `json:"manifest_sha256"`
}

// retainImports snapshots already sealed input, so offline analysis needs no
// old VM path, HOME or subprocess. Copying is not a compatibility verdict.
func (e *Exercise) retainImports() error {
	var imports []ImportRef
	for _, source := range e.Options.Imports {
		if !filepath.IsAbs(source) || filepath.Clean(source) != source {
			return errors.New("import root must be an explicit clean absolute path")
		}
		m, err := runner.ReadManifest(source)
		if err != nil {
			return err
		}
		b, err := runner.Read(source, "manifest.json")
		if err != nil {
			return err
		}
		digest := runner.Digest(b)
		relative := "raw/imports/" + digest
		if slices.ContainsFunc(imports, func(i ImportRef) bool { return i.ManifestSHA256 == digest }) {
			return errors.New("duplicate evidence import")
		}
		for name := range m.Artifacts {
			b, err := runner.Read(source, name)
			if err != nil {
				return err
			}
			if err = e.Run.Evidence.Put(relative+"/"+name, b); err != nil {
				return err
			}
		}
		b, err = runner.Read(source, "manifest.json")
		if err != nil {
			return err
		}
		if runner.Digest(b) != digest {
			return errors.New("import manifest changed during retention")
		}
		if err = e.Run.Evidence.Put(relative+"/manifest.json", b); err != nil {
			return err
		}
		imports = append(imports, ImportRef{relative, m.Phase, m.RunID, m.Candidate, m.ProviderVersion, m.KitSHA256, digest})
	}
	if err := e.Run.Evidence.JSON("raw/import-index.json", imports); err != nil {
		return err
	}
	if e.Options.Scheduler != "" {
		data, err := runner.Read(filepath.Dir(e.Options.Scheduler), filepath.Base(e.Options.Scheduler))
		if err != nil {
			return err
		}
		var proof SchedulerEvidence
		if err = json.Unmarshal(data, &proof); err != nil {
			return err
		}
		// Each raw log is copied from beside the explicitly supplied evidence
		// declaration, never fetched from a scheduler/network during a phase.
		for _, job := range proof.Jobs {
			for _, test := range job.Tests {
				data, err := runner.Read(filepath.Dir(e.Options.Scheduler), test.Log)
				if err != nil {
					return err
				}
				if runner.Digest(data) != test.SHA256 {
					return fmt.Errorf("scheduler log hash mismatch: %s", test.Log)
				}
				ref := "raw/scheduler/" + test.SHA256
				path, err := runner.Confined(e.Run.Evidence.Root, ref)
				if err != nil {
					return err
				}
				if _, err = os.Stat(path); os.IsNotExist(err) {
					if err = e.Run.Evidence.Put(ref, data); err != nil {
						return err
					}
				} else if err != nil {
					return err
				}
			}
		}
		for _, w := range proof.SourceWitnesses {
			for _, source := range []struct{ name, hash string }{{w.Inventory, w.InventorySHA256}, {w.BuildLog, w.BuildLogSHA256}} {
				data, err := runner.Read(filepath.Dir(e.Options.Scheduler), source.name)
				if err != nil {
					return err
				}
				if runner.Digest(data) != source.hash {
					return errors.New("original source witness hash mismatch")
				}
				ref := "raw/source-witnesses/" + source.hash
				path, err := runner.Confined(e.Run.Evidence.Root, ref)
				if err != nil {
					return err
				}
				if _, err = os.Stat(path); os.IsNotExist(err) {
					if err = e.Run.Evidence.Put(ref, data); err != nil {
						return err
					}
				} else if err != nil {
					return err
				}
			}
		}
		if err = e.Run.Evidence.JSON("raw/scheduler-evidence.json", proof); err != nil {
			return err
		}
	}
	// Reject incompatible inputs before the first provider prompt is reserved.
	l, err := LoadLedger(e.Ledger)
	if err != nil {
		return err
	}
	product, err := LoadProductSource(e.Run.Evidence.Root, "raw/qualification/source.json")
	if err != nil {
		return err
	}
	index, err := e.Run.Evidence.Index()
	if err != nil {
		return err
	}
	m := e.Run.Manifest
	m.Artifacts = index
	consumer := Capture{Root: e.Run.Evidence.Root, Manifest: m, Before: l, After: l, Product: product}
	for _, ref := range imports {
		if ref.Phase == "probe" {
			if _, err = consumer.compatibleCI(ref); err != nil {
				return err
			}
			continue
		}
		if _, er := consumer.loadPhaseImport(ref); er != nil {
			return er
		}
	}
	var required []string
	if m.Phase == "groups" {
		required = []string{"force-states"}
	}
	if m.Phase == "text-limits-backlog" {
		required = []string{"force-states", "typed-controls", "groups"}
	}
	for _, phase := range required {
		if !slices.ContainsFunc(imports, func(ref ImportRef) bool { return ref.Phase == phase }) {
			return fmt.Errorf("required prior phase import missing before spending turns: %s", phase)
		}
	}
	return nil
}

func (c Capture) imports() ([]ImportRef, error) {
	var refs []ImportRef
	if err := c.json("raw/import-index.json", &refs); err != nil {
		return nil, err
	}
	for _, ref := range refs {
		if _, err := scopeRelative(c.Root, ref.Relative); err != nil {
			return nil, err
		}
		b, err := c.read(ref.Relative + "/manifest.json")
		if err != nil {
			return nil, err
		}
		if runner.Digest(b) != ref.ManifestSHA256 {
			return nil, corrupt("retained import changed")
		}
		var m runner.Manifest
		if err = json.Unmarshal(b, &m); err != nil {
			return nil, err
		}
		if m.RunID != ref.Run || m.Phase != ref.Phase || m.Candidate != ref.Candidate || m.ProviderVersion != ref.Provider || m.KitSHA256 != ref.Kit {
			return nil, corrupt("import index identity differs from retained manifest")
		}
	}
	return refs, nil
}

// loadPhaseImport requires a compatible sealed producer whose freshly derived
// whole-phase verdict passes, including independent failures and nested gaps.
// Reuse this file-only gate before operational spending and offline consumption;
// ci keeps its separate compatibleCI policy. See docs/architecture.md.
func (c Capture) loadPhaseImport(ref ImportRef) (Capture, error) {
	root, err := runner.Confined(c.Root, ref.Relative)
	if err != nil {
		return Capture{}, err
	}
	producer, err := LoadCapture(root)
	if err != nil {
		return producer, err
	}
	if err = CompatibleCapture(c, producer); err != nil {
		return producer, err
	}
	verdict := AnalyzePhase(root)
	for _, row := range verdict.Results {
		if containsFailedVerdict(row) {
			return producer, corrupt("phase import prerequisite failed: %s (%s)", ref.Phase, row.SignalID)
		}
	}
	if verdict.Status == "fail" {
		return producer, corrupt("phase import prerequisite failed: %s", ref.Phase)
	}
	if verdict.Status != "pass" || len(incompleteJudgments(verdict.Results)) != 0 {
		return producer, absent("phase import prerequisite not_completed: %s", ref.Phase)
	}
	return producer, nil
}

func CompatibleCapture(consumer, producer Capture) error {
	if !consumer.Before.Identity.Compatible(producer.Before.Identity) || consumer.Before.Identity.Kit != producer.Before.Identity.Kit || !samePriorLedger(consumer.Before.Prior, producer.Before.Prior) {
		return errors.New("cross-phase source candidate/product/provider/qualification differs")
	}
	if consumer.Before.Identity.Attempt != producer.Before.Identity.Attempt {
		// A prior attempt is reusable only through the explicit immutable chain.
		authority, known := consumer.Before.History[producer.Before.Identity.Attempt]
		if consumer.Before.Identity.Previous == "" || !known || authority.Identity != producer.Before.Identity {
			return errors.New("cross-attempt evidence lacks an explicit previous link")
		}
	}
	return nil
}
