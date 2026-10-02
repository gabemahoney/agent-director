package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
)

// caseSpec is one registered case. Case files register theirs with
// registerCase from an init function; ids are stable, since results.json,
// decide and the SRD tables key on them.
type caseSpec struct {
	id     string
	family family
	title  string
	// sampled cases (RN-6, RN-2) run cfg.samples samples and are subject
	// to the 20-sample floor in real mode; RN-9 scenarios run one agent.
	sampled bool
	// modes are the run modes the case runs in.
	modes []mode
	// generated cases write the harness's own .claude/settings.local.json
	// in their agents' working directories (a raised-budget layer, the RN-9
	// recorder, the probe's recorder); a deployment local layer cannot sit
	// beside it (checkLocalLayer).
	generated bool
	// run measures the case. A returned error aborts the whole run (an
	// isolation breach such as a socket outside the private TMUX_TMPDIR);
	// a sample's failure is recorded in the caseResult instead.
	run func(h *harness) (caseResult, error)
}

// caseRegistry holds the registered cases in registration order.
var caseRegistry []caseSpec

// registerCase adds a case; a duplicate id is a programming error.
func registerCase(c caseSpec) {
	for _, have := range caseRegistry {
		if have.id == c.id {
			panic("measure-exit: duplicate case id " + c.id)
		}
	}
	caseRegistry = append(caseRegistry, c)
}

// supports reports whether the case runs in m.
func (c caseSpec) supports(m mode) bool {
	for _, have := range c.modes {
		if have == m {
			return true
		}
	}
	return false
}

// selectCases resolves -cases against the registry: the named cases in the
// given order, or every case that runs in the mode. An unknown id, a case
// that does not run in the mode, or an empty selection is a usage error.
// Real mode needs the ids named: every real-mode case is both L1's RN-6 and
// RN-2 cases and L2's RN-9 scenarios, so a default selection would start
// both runs' agents (about twice the approved sessions); the host runner
// always passes its mode's ids.
func selectCases(registry []caseSpec, ids []string, m mode) ([]caseSpec, error) {
	var out []caseSpec
	if len(ids) == 0 && m == modeReal {
		return nil, fmt.Errorf("%w: real mode needs an explicit -cases (the runner passes its mode's case ids); it never runs every registered case", errUsage)
	}
	if len(ids) == 0 {
		for _, c := range registry {
			if c.supports(m) {
				out = append(out, c)
			}
		}
	}
	for _, id := range ids {
		found := false
		for _, c := range registry {
			if c.id != id {
				continue
			}
			if !c.supports(m) {
				return nil, fmt.Errorf("%w: case %s does not run in %s mode", errUsage, id, m)
			}
			out, found = append(out, c), true
		}
		if !found {
			return nil, fmt.Errorf("%w: unknown case %q", errUsage, id)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: no case selected for %s mode", errUsage, m)
	}
	return out, nil
}

// anySampled reports whether a selected case is subject to the sample floor.
func anySampled(cases []caseSpec) bool {
	for _, c := range cases {
		if c.sampled {
			return true
		}
	}
	return false
}

// newCaseResult starts a case's result from its spec.
func newCaseResult(c caseSpec) caseResult {
	return caseResult{ID: c.id, Family: c.family, Title: c.title, Samples: []sample{}}
}

// sampleWorkDir creates a fresh harness-owned working directory for one
// agent: <workDir>/<case id>/<index>. It refuses one that already exists, so
// no deployment local layer or leftover file can sit in it.
func (h *harness) sampleWorkDir(caseID string, index int) (string, error) {
	parent := filepath.Join(h.iso.WorkDir, caseID)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return "", err
	}
	dir := filepath.Join(parent, strconv.Itoa(index))
	if err := os.Mkdir(dir, 0o700); err != nil {
		return "", fmt.Errorf("working directory %s: %w", dir, err)
	}
	return dir, nil
}

// generatedLayerPath is where a harness-generated settings layer goes: the
// agent's working directory's .claude/settings.local.json. --settings is a
// denied spawn argument, so a generated layer must be a file layer, and the
// mounted deployment copies are never edited.
func generatedLayerPath(cwd string) string {
	return filepath.Join(cwd, ".claude", "settings.local.json")
}

// writeGeneratedLayer writes doc as the harness's own local layer in cwd. A
// local layer already there (a deployment's) is refused: the generated one
// cannot sit beside it.
func writeGeneratedLayer(cwd string, doc map[string]any) error {
	path := generatedLayerPath(cwd)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("%s already exists (a deployment local layer?): the harness's generated layer cannot sit beside it", path)
	}
	if err != nil {
		return err
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// budgetsFor captures the SessionEnd budgets in force for a running agent:
// every layer it loads (agent-director's own read from its argv) and every
// source of CLAUDE_CODE_SESSIONEND_HOOKS_TIMEOUT_MS. Call it while the agent
// runs, before its measured action. generated tells whether its local layer
// is the harness's own; spawnEnv is the spawn's extra environment.
func (h *harness) budgetsFor(a agentRef, generated bool, spawnEnv map[string]string) budgetReport {
	layers := agentLayers(h.iso.Home, a.CWD, generated, agentDirectorLayer(procRoot, a.PID))
	return captureBudgets(layers, h.childEnvMap(), spawnEnv, h.iso.ClaudeCode)
}
