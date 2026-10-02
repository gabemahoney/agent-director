package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// The exec-form version probe (RN-9; t3.h98.w4.yu.sm): does this
// container's Claude Code run a hook registered with `command` + `args`?
// The host runner builds one measurement image per Claude Code version and
// runs the driver in probe mode in each, with no network and only a dummy
// credential (L0). It probes the deployed version (2.1.280) explicitly
// first and says whether that version runs exec-form hooks, then bisects
// between 2.1.120 (ignores args) and 2.1.285 (runs them); decide merges the
// per-version results.
//
// One agent is spawned through agent-director with a recorder script
// registered on SessionStart in exec form with one marker argument. A
// version that runs exec form passes the argument; one that ignores args
// runs `command` through /bin/sh with none (as 2.1.120 does, which is also
// why agent-director's own hooks then write no_exec_form). The dry run uses
// two tiny stubs, one that keeps args and one that drops them.
//
// Case id (stable): probe.exec-form.

// probeCaseID is the probe's case id.
const probeCaseID = "probe.exec-form"

// probeArgMarker is the one argument the probe's recorder is registered
// with; receiving it proves the version ran the hook in exec form.
const probeArgMarker = "mx-exec-form-arg"

// Probe results (probeVersion.Result).
const (
	probeArgsReceived    = "args_received"
	probeArgsNotReceived = "args_not_received"
	probeDidNotStart     = "did_not_start"
)

// probeRecorderScript is the probe's recorder: it appends its argument
// count, first argument and parent pid to the file named in it, reads and
// discards the payload, and exits 0 with no output. %s is the file.
const probeRecorderScript = `#!/bin/sh
# measure-exit exec-form probe recorder: argument count, first argument and
# parent pid only; the payload is read and discarded.
printf 'argc=%%s arg1=%%s ppid=%%s\n' "$#" "${1:-}" "$PPID" >> '%s'
exec cat >/dev/null
`

func init() {
	spec := caseSpec{id: probeCaseID, family: familyRN9, title: "exec-form version probe",
		modes: []mode{modeProbe, modeDry}, generated: true}
	spec.run = func(h *harness) (caseResult, error) {
		v, err := h.runProbe(spec)
		h.res.Probe = &probeSection{Versions: []probeVersion{v}}
		if v.Result == probeArgsReceived {
			h.res.Probe.Minimum = v.Version
		}
		return newCaseResult(spec), err
	}
	registerCase(spec)
}

// runProbe spawns the probe's one agent and waits (stepTimeout) for its
// recorder's first line. It then ends the agent's pane (a teardown, not a
// measurement).
func (h *harness) runProbe(spec caseSpec) (probeVersion, error) {
	v := probeVersion{Version: h.iso.ClaudeCode}
	if p, ok := parseVersion(h.iso.ClaudeCode); ok {
		v.Version = fmt.Sprintf("%d.%d.%d", p[0], p[1], p[2])
	}
	out := filepath.Join(h.cfg.outDir, "probe-recorder.txt")
	if strings.ContainsAny(out, "'\n") {
		return v, fmt.Errorf("results path %q cannot be quoted in the probe's recorder", out)
	}
	scriptDir := filepath.Join(h.iso.WorkDir, "probe-recorder")
	if err := os.MkdirAll(scriptDir, 0o700); err != nil {
		return v, err
	}
	script := filepath.Join(scriptDir, "record.sh")
	if err := os.WriteFile(script, []byte(fmt.Sprintf(probeRecorderScript, out)), 0o700); err != nil {
		return v, err
	}
	layer := map[string]any{"hooks": map[string]any{"SessionStart": []any{map[string]any{"hooks": []any{
		map[string]any{"type": "command", "command": script, "args": []string{probeArgMarker}},
	}}}}}
	dir, err := h.prepareAgentDir(spec.id, 0, layer)
	if err != nil {
		return v, err
	}
	a, err := h.spawnAgent(spec.id, spawnSpec{CWD: dir})
	if err != nil {
		var r *refusal
		if errors.As(err, &r) {
			return v, err
		}
		v.Result, v.Note = probeDidNotStart, "spawn: "+err.Error()
		return v, nil
	}
	line, waitErr := h.waitProbeLine(out)
	v.Result, v.Note = classifyProbeLine(line)
	if waitErr != nil {
		v.Result, v.Note = probeDidNotStart, waitErr.Error()
	}
	if reasons, err := ignoredHookReasons(h.trailPath(), a.InstanceID); err == nil && len(reasons) > 0 {
		v.Note = strings.TrimPrefix(v.Note+"; agent-director's own hooks ignored: "+strings.Join(reasons, ", "), "; ")
	}
	if err := h.identify(spec.id, &a); err == nil {
		_, _ = h.inv.tmuxCall(spec.id, actionTeardown, a.Socket, "kill-pane", "-t", a.PaneID)
	}
	return v, nil
}

// waitProbeLine waits up to stepTimeout for the recorder's first line.
func (h *harness) waitProbeLine(path string) (string, error) {
	deadline := h.clock.Now().Add(h.cfg.stepTimeout)
	for {
		if b, err := os.ReadFile(path); err == nil {
			if line, _, ok := strings.Cut(string(b), "\n"); ok {
				return line, nil
			}
		}
		if !h.clock.Now().Before(deadline) {
			return "", fmt.Errorf("no SessionStart hook ran within %s (Claude Code may not reach its prompt offline)", h.cfg.stepTimeout)
		}
		h.clock.Sleep(rn9Poll)
	}
}

// classifyProbeLine reads one recorder line: the marker as the first
// argument means args received; anything else, not received.
func classifyProbeLine(line string) (result, note string) {
	fields := map[string]string{}
	for _, f := range strings.Fields(line) {
		if k, val, ok := strings.Cut(f, "="); ok {
			fields[k] = val
		}
	}
	argc, _ := strconv.Atoi(fields["argc"])
	if argc >= 1 && fields["arg1"] == probeArgMarker {
		return probeArgsReceived, fmt.Sprintf("recorder got %d argument(s), parent pid %s", argc, fields["ppid"])
	}
	return probeArgsNotReceived, fmt.Sprintf("recorder got %d argument(s), parent pid %s", argc, fields["ppid"])
}
