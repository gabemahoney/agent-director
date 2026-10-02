package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/probe"
)

// tmuxProgram is the tmux client the harness runs; every call names the
// private socket with -S.
const tmuxProgram = "tmux"

// harness is one run's state, shared by the cases.
type harness struct {
	cfg   config
	env   environment
	iso   isolation
	inv   *invoker
	clock clock
	procs probe.ProcChecker
	ids   *idWriter
	scr   scrubber
	res   *results
	// out is where the summary and table are printed.
	out io.Writer
	// self is this driver's own executable (real path). The generated
	// layers register it as a hook program in exec form (the RN-9
	// recorder's `record` subcommand).
	self string
}

// childEnvMap is the child environment as a map (budget capture's
// "container environment").
func (h *harness) childEnvMap() map[string]string {
	m := make(map[string]string, len(h.inv.env))
	for _, kv := range h.inv.env {
		if k, v, ok := strings.Cut(kv, "="); ok {
			m[k] = v
		}
	}
	return m
}

// agentDirectorVersion is the version verb's version.
func (h *harness) agentDirectorVersion() (string, error) {
	res, err := h.inv.agentDirectorCall("preflight", actionVersion, "version")
	if err != nil {
		return "", err
	}
	return parseAgentDirectorVersion(res.stdout)
}

// claudeVersion is `claude --version`'s output and where claude resolved
// on the children's PATH.
func (h *harness) claudeVersion() (string, string, error) {
	path, err := lookPathIn(h.cfg.claude, h.env.getenv("PATH"))
	if err != nil {
		return "", "", err
	}
	res, err := h.inv.run("preflight", actionVersion, tmuxCallTimeout, h.inv.env, []string{path, "--version"})
	if err != nil {
		return "", path, err
	}
	return strings.TrimSpace(string(res.stdout)), path, nil
}

// lookPathIn resolves name on pathList (a name with a slash is taken as
// given).
func lookPathIn(name, pathList string) (string, error) {
	if strings.Contains(name, "/") {
		return name, nil
	}
	for _, dir := range filepath.SplitList(pathList) {
		p := filepath.Join(dir, name)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
			return p, nil
		}
	}
	return "", fmt.Errorf("%s not found on PATH", name)
}

// Exit codes of the run subcommand.
const (
	exitOK      = 0
	exitFailed  = 1
	exitUsage   = 2
	exitRefused = 4
)

// runCommand is the run subcommand: preflight, seed, every selected case,
// then the table. Nothing is written before the environment checks pass.
func runCommand(args []string, stdout, stderr io.Writer) int {
	e := productionEnvironment()
	cfg, err := parseRunFlags(args, stderr, uuid.NewString)
	if err != nil {
		fmt.Fprintln(stderr, "measure-exit run:", err)
		return exitUsage
	}
	cases, err := selectCases(caseRegistry, cfg.cases, cfg.mode)
	if err != nil {
		fmt.Fprintln(stderr, "measure-exit run:", err)
		return exitUsage
	}
	sampled := anySampled(cases)
	// The environment checks run once before anything is written, and again
	// inside the preflight proper.
	if _, _, err := checkEnvironment(cfg, e, sampled); err != nil {
		return reportFailure(stderr, err)
	}
	if err := checkLocalLayer(cfg, cases); err != nil {
		return reportFailure(stderr, err)
	}
	h, closeFn, err := newHarness(cfg, e, stdout)
	if err != nil {
		return reportFailure(stderr, err)
	}
	defer closeFn()
	if err := h.start(sampled); err != nil {
		h.note(err)
		return reportFailure(stderr, err)
	}
	for _, c := range cases {
		res, err := c.run(h)
		// RN-9 scenarios and the probe fill their own sections; only the
		// sampled RN-6/RN-2 cases are rows of the cases table.
		if c.sampled {
			res.finalize()
			h.res.Cases = append(h.res.Cases, res)
		}
		if werr := h.res.writeJSON(filepath.Join(cfg.outDir, resultsFile), h.scr); werr != nil {
			return reportFailure(stderr, werr)
		}
		if err != nil {
			h.note(fmt.Errorf("case %s aborted the run: %w", c.id, err))
			return reportFailure(stderr, err)
		}
	}
	if err := h.finish(); err != nil {
		return reportFailure(stderr, err)
	}
	return exitOK
}

// reportFailure prints err and maps it to an exit code.
func reportFailure(stderr io.Writer, err error) int {
	fmt.Fprintln(stderr, err)
	var r *refusal
	if errors.As(err, &r) {
		return exitRefused
	}
	return exitFailed
}

// newHarness creates the results directory, the run log and the identifiers
// file, and the invoker. closeFn closes the files.
func newHarness(cfg config, e environment, out io.Writer) (*harness, func(), error) {
	if err := os.MkdirAll(cfg.outDir, 0o700); err != nil {
		return nil, nil, err
	}
	logf, err := openRunLog(filepath.Join(cfg.outDir, runLogFile))
	if err != nil {
		return nil, nil, err
	}
	idf, err := openIdentifiers(filepath.Join(cfg.outDir, identifiersFile))
	if err != nil {
		_ = logf.Close()
		return nil, nil, err
	}
	scr := newScrubber(e)
	self, err := os.Executable()
	if err == nil {
		self, err = filepath.EvalSymlinks(self)
	}
	if err != nil {
		_ = logf.Close()
		_ = idf.Close()
		return nil, nil, fmt.Errorf("cannot resolve the driver's own executable: %w", err)
	}
	h := &harness{
		cfg: cfg, env: e, clock: realClock{}, procs: probe.NewProcChecker(),
		ids: newIDWriter(idf), scr: scr, out: out, self: self,
	}
	h.inv = &invoker{log: newRunLog(logf, scr), exec: osExec, clock: h.clock, scr: scr,
		agentDirector: cfg.agentDirector, tmux: tmuxProgram}
	closeFn := func() {
		_ = logf.Close()
		_ = idf.Close()
	}
	return h, closeFn, nil
}

// start runs the preflight (the private TMUX_TMPDIR is made before the
// child environment that carries it), records the isolation summary in the
// results before any case, prints it, and seeds the Claude state file.
func (h *harness) start(sampled bool) error {
	startedAt := h.clock.Now()
	h.res = newResults(isolation{RunID: h.cfg.runID, Mode: h.cfg.mode}, startedAt)
	if err := h.ids.add(idRun, h.cfg.runID); err != nil {
		return err
	}
	iso, err := preflight(h.cfg, h.env, h, sampled)
	h.iso = iso
	h.res.Isolation = iso
	if iso.TmuxTmpdir != "" {
		if aerr := h.ids.add(idTmuxDir, iso.TmuxTmpdir); aerr != nil && err == nil {
			err = aerr
		}
	}
	if err != nil {
		return err
	}
	if err := os.MkdirAll(iso.WorkDir, 0o700); err != nil {
		return err
	}
	if h.cfg.mode == modeDry {
		fmt.Fprintln(h.out, "*** "+dryRunBanner+" ***")
	}
	for _, line := range iso.summaryLines() {
		fmt.Fprintln(h.out, line)
	}
	seedEnv := h.env
	if h.cfg.mode == modeDry {
		seedEnv = seedEnv.credentialFree()
	}
	seed, err := seedClaudeState(iso.Home, seedEnv, iso.ClaudeCode)
	if err != nil {
		return err
	}
	h.res.Seed = &seed
	return h.res.writeJSON(filepath.Join(h.cfg.outDir, resultsFile), h.scr)
}

// readVersions is the harness's versionReader: it builds the child
// environment around the private TMUX_TMPDIR (every later call uses it),
// then reads both versions through the logged invoker.
func (h *harness) readVersions(tmuxTmpdir string) (agentDirector, claudeCode, claudePath string, err error) {
	if h.inv.env, err = h.env.childEnv(tmuxTmpdir, nil); err != nil {
		return "", "", "", err
	}
	if h.cfg.mode == modeDry {
		// A dry run never forwards a credential, whatever the sandbox's
		// environment holds (c7); values are still scrubbed from output.
		h.inv.env = withoutCredentials(h.inv.env)
	}
	if h.cfg.mode == modeReal {
		// The preflight has refused real mode with any of these set; the
		// children never get one either way (gateway only).
		h.inv.env = realModeEnv(h.inv.env)
	}
	if agentDirector, err = h.agentDirectorVersion(); err != nil {
		return "", "", "", fmt.Errorf("agent-director version: %w", err)
	}
	if claudeCode, claudePath, err = h.claudeVersion(); err != nil {
		return agentDirector, "", claudePath, fmt.Errorf("claude --version: %w", err)
	}
	return agentDirector, claudeCode, claudePath, nil
}

// note adds a run-level note and rewrites results.json; a write failure is
// ignored here because the caller is already reporting a failure.
func (h *harness) note(err error) {
	if h.res == nil {
		return
	}
	h.res.Notes = append(h.res.Notes, err.Error())
	_ = h.res.writeJSON(filepath.Join(h.cfg.outDir, resultsFile), h.scr)
}

// finish writes the final results.json and the table (to stdout and the
// table file).
func (h *harness) finish() error {
	at := h.clock.Now().UTC()
	h.res.FinishedAt = &at
	if err := h.res.writeJSON(filepath.Join(h.cfg.outDir, resultsFile), h.scr); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(h.cfg.outDir, tableFile), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if err := h.res.writeTable(io.MultiWriter(f, h.out), h.scr); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
