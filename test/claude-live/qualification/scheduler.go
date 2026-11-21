package qualification

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/gabemahoney/agent-director/test/claude-live/runner"
)

type SchedulerEvidence struct {
	Schema          int             `json:"schema"`
	Jobs            []SchedulerJob  `json:"jobs"`
	SourceWitnesses []SourceWitness `json:"source_witnesses,omitempty"`
}
type SchedulerJob struct {
	ID          string          `json:"id"`
	Candidate   string          `json:"candidate"`
	Product     string          `json:"product"`
	Platform    string          `json:"platform"`
	Status      string          `json:"status"`
	Environment json.RawMessage `json:"environment"`
	Tests       []SchedulerTest `json:"tests"`
}
type SchedulerTest struct {
	Coverage string     `json:"coverage"`
	Package  string     `json:"package"`
	Name     string     `json:"name"`
	Case     string     `json:"case,omitempty"`
	Log      string     `json:"log"`
	SHA256   string     `json:"sha256"`
	Format   string     `json:"format"`
	Reason   string     `json:"reason"`
	Kind     string     `json:"kind"`
	Subject  *DialogKey `json:"subject,omitempty"`
	Source   string     `json:"source"`
}

type SchedulerResolution struct {
	Job      string `json:"job"`
	Test     string `json:"test"`
	Case     string `json:"case"`
	Reason   string `json:"reason"`
	Evidence string `json:"evidence"`
}

// ValidateSchedulerTest requires the exact candidate and an actual pass record
// in the hashed primary log. A suite heading or a claimed boolean is no proof.
func ValidateSchedulerTest(identity Identity, job SchedulerJob, test SchedulerTest, log []byte) error {
	if job.ID == "" || job.Candidate != identity.Candidate || job.Product != identity.Product || job.Status != "succeeded" || (job.Platform != "linux/amd64" && job.Platform != "linux/arm64") || len(job.Environment) == 0 {
		return errors.New("scheduler job is absent, nonpassing or from another candidate")
	}
	if test.Coverage == "" || test.Name == "" || test.Reason == "" || test.Source == "" || !digestText(test.SHA256, 32) || runner.Digest(log) != test.SHA256 {
		return errors.New("incomplete or corrupt exact scheduler test/case evidence")
	}
	if !slices.Contains([]string{"deterministic_seam", "native_unavailable", "upgrade", "interface_parity", "named_premise"}, test.Kind) {
		return errors.New("scheduler fallback has no admissible reason; budget omissions never resolve coverage")
	}
	if test.Format != "go-test-json" && test.Format != "docker-case-json" {
		return errors.New("unknown scheduler log format")
	}
	if strings.HasPrefix(test.Coverage, "uncovered/") {
		parts := strings.Split(test.Coverage, "/")
		if len(parts) < 2 {
			return errors.New("uncovered coverage lacks kind")
		}
		kind := parts[1]
		if len(parts) == 2 {
			// The candidate's registered Docker group currently has this one
			// concrete kind/case. Other kinds stay incomplete until their own
			// integration case exists; this case can never be relabelled.
			if kind != "codex" || test.Name != "busy-send-uncovered" || test.Format != "docker-case-json" || test.Case != "t2.75s.bs.un" || test.Source != "tickets/testplans/b.75s/t1.75s.bs/t2.75s.bs.un/t2.75s.bs.un.md" {
				return errors.New("unavailable kind requires its own registered busy-send-uncovered Docker case")
			}
		} else {
			operation := parts[2]
			if test.Format != "go-test-json" || operation != "request-token" || test.Case != kind+"/send-keys_request_token" || test.Name != "TestBusySendUncoveredRefusalMatrix" {
				return errors.New("uncovered Go fallback must name the exact kind/operation subcase")
			}
		}
	}
	if test.Coverage == "prd/Seamless upgrades" && (test.Kind != "upgrade" || test.Format != "docker-case-json" || test.Name != "epic-12-install" || test.Case != "t2.75s.f7.la" || test.Source != "tickets/testplans/b.75s/t1.75s.f7/t2.75s.f7.la/t2.75s.f7.la.md") {
		return errors.New("seamless upgrades require the registered E7 LaterInstallUnderLoad Docker case")
	}
	var environment map[string]any
	if json.Unmarshal(job.Environment, &environment) != nil {
		return errors.New("invalid scheduler Environment Record")
	}
	arch, _ := environment["dpkg_architecture"].(string)
	if arch != strings.TrimPrefix(job.Platform, "linux/") {
		return errors.New("scheduler Environment Record architecture differs")
	}
	if strings.HasPrefix(test.Coverage, "dialog/") && (test.Subject == nil || test.Subject.ID() != test.Coverage || test.Case == "") {
		return errors.New("Dialog fallback must name its exact combination and case")
	}
	scanner := bufio.NewScanner(bytes.NewReader(log))
	scanner.Buffer(make([]byte, 4096), runner.MaxArtifactBytes)
	passes, failed := 0, false
	wanted := test.Name
	if test.Case != "" && test.Format == "go-test-json" {
		wanted += "/" + test.Case
	}
	for scanner.Scan() {
		var row map[string]any
		if json.Unmarshal(scanner.Bytes(), &row) != nil {
			continue
		}
		if test.Format == "go-test-json" {
			if row["Package"] != test.Package || row["Test"] != wanted {
				continue
			}
			if row["Action"] == "pass" {
				passes++
			}
			failed = failed || row["Action"] == "fail"
		} else if test.Format == "docker-case-json" {
			if test.Case == "" || row["case"] != test.Case {
				continue
			}
			if row["status"] == "pass" {
				passes++
			}
			failed = failed || row["status"] == "fail"
		} else {
			return errors.New("unknown scheduler log format")
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if passes != 1 || failed {
		return fmt.Errorf("exact scheduler test/case has %d passes or a failure", passes)
	}
	return nil
}

func (c Capture) scheduler(coverage string, expectedTests ...string) (SchedulerResolution, error) {
	var proof SchedulerEvidence
	if err := c.json("raw/scheduler-evidence.json", &proof); err != nil {
		return SchedulerResolution{}, err
	}
	if proof.Schema != Schema {
		return SchedulerResolution{}, corrupt("unknown scheduler evidence schema")
	}
	for _, job := range proof.Jobs {
		for _, test := range job.Tests {
			if test.Coverage != coverage {
				continue
			}
			if len(expectedTests) > 0 && !slices.Contains(expectedTests, test.Name) {
				return SchedulerResolution{}, corrupt("scheduler citation does not name the required applicable test")
			}
			data, err := c.read("raw/scheduler/" + test.SHA256)
			if err != nil {
				return SchedulerResolution{}, err
			}
			if err = ValidateSchedulerTest(c.Before.Identity, job, test, data); err != nil {
				return SchedulerResolution{}, err
			}
			return SchedulerResolution{job.ID, test.Package + "/" + test.Name, test.Case, test.Reason, "raw/scheduler/" + test.SHA256}, nil
		}
	}
	return SchedulerResolution{}, absent("exact final-candidate scheduler citation missing for %s", coverage)
}

func fallbackResult(c Capture, id string, tests ...string) runner.Result {
	proof, err := c.scheduler(id, tests...)
	if err != nil {
		return result(id, evidenceStatus(err), err.Error(), "exact final-candidate test/case and reason", nil)
	}
	return result(id, "pass", "scheduler-covered deterministic/native-unavailable case; no live PASS claimed", proof.Test, proof, proof.Evidence)
}
