package adminapi

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gabemahoney/agent-director/internal/probe"
)

// darwinVerifier is the Darwin quiescence verifier (SRD SR-2.15): the
// evidence an unprivileged user has on macOS, where there is no /proc. Every
// decision is here, with no build tag, and every source of evidence is a
// field, so tests on Linux drive it whole through fakes and real temporary
// files; journal_migration_darwin.go binds the real seams on darwin/arm64.
// It reads no process arguments or environment: the listing asks lsof for no
// command or user field, and the probe readers have no KERN_PROCARGS2 seam.
// Must use: add Darwin evidence through these seams, file-system evidence
// through fs under bounded, and bind them only in the tagged file.
type darwinVerifier struct {
	// selectUID is the kernel's own effective-uid selection
	// (probe.UIDProcessReader.PIDs).
	selectUID func(uid int) ([]int, error)
	// procs reads each process's start identity, and ppids its parent pid.
	procs probe.ProcChecker
	ppids probe.ParentPIDReader
	// listFiles is one bulk `lsof -F0pftDin` listing of uid's processes, and
	// readACL one `/bin/ls -led` of path. Each calls started with the pid of
	// every helper process it starts, at once, while the helper is unreaped.
	listFiles func(ctx context.Context, uid int, started func(pid int)) (stdout, stderr []byte, err error)
	readACL   func(ctx context.Context, path string, started func(pid int)) (stdout, stderr []byte, err error)
	fs        journalFS
	now       func() time.Time
	selfPID   int
	euid      int
}

// darwinProcess is one process under check.
type darwinProcess struct {
	// start is its first start identity and started that instant, both empty
	// when the first identity could not be read.
	start   string
	started time.Time
	files   []lsofFile
	mutator bool
}

// setStart records a usable first start identity; otherwise it stays empty.
func (p *darwinProcess) setStart(start string) {
	if started, ok := probe.ParseDarwinProcStartTime(start); ok {
		p.start, p.started = start, started
	}
}

// verify is SR-2.15's check, in its order: the paths and their access-control
// entries; the selection, with each process's first start identity; one
// listing; the exclusions; the selection and the listing reconciled; the
// store's and roots' identities; each process's files and program name; the
// second start identities. Only positive mutator evidence about a process
// refuses. Whole-inventory failures still refuse. Every file-system lookup
// runs under bounded, so a blocked one refuses
// inventory_deadline rather than hold the attempt.
func (v darwinVerifier) verify(ctx context.Context, dbPath string, roots []string) error {
	helpers := &darwinHelpers{}
	for _, path := range append([]string{dbPath}, roots...) {
		if err := bounded(ctx, func() error { return verifyJournalPathIn(v.fs, path) }); err != nil {
			return err
		}
		if err := v.checkAccessControl(ctx, path, helpers); err != nil {
			return err
		}
	}
	store := map[journalFileID]bool{}
	if err := bounded(ctx, func() error { return journalStoreIDs(v.fs, dbPath, store) }); err != nil {
		return err
	}

	selectedAt := v.now().Truncate(time.Microsecond)
	procs, err := v.selection()
	if err != nil {
		return err
	}

	stdout, stderr, err := v.listFiles(ctx, v.euid, helpers.started(v.procs))
	if ctx.Err() != nil {
		return journalRefusal("inventory_deadline")
	}
	if err != nil || len(stderr) > 0 {
		return journalRefusal("process_inventory_unreadable")
	}
	listing, err := parseLsofListing(stdout)
	if err != nil {
		return journalRefusal("process_inventory_unreadable")
	}

	starts := &darwinStartReads{procs: v.procs, selected: procs}
	excluded := v.exclusions(listing, helpers.snapshot(), starts)
	if err := reconcileDarwinListing(procs, listing, excluded, starts, selectedAt); err != nil {
		return err
	}

	evidence, err := v.evidence(ctx, dbPath, roots, store)
	if err != nil {
		return err
	}
	ordered := make([]int, 0, len(procs))
	for pid := range procs {
		ordered = append(ordered, pid)
	}
	sort.Ints(ordered)
	for _, pid := range ordered {
		if ctx.Err() != nil {
			return journalRefusal("inventory_deadline")
		}
		if err := v.judge(ctx, procs[pid], evidence); err != nil {
			return err
		}
	}
	return v.report(procs, ordered)
}

// selection is the kernel's selection of the verifier's effective uid, with
// each process's first start identity; a process already gone is skipped. A
// selection that fails, lacks the verifier itself, or includes pid 0, which
// only a root selection does, refuses, so running as root refuses.
func (v darwinVerifier) selection() (map[int]*darwinProcess, error) {
	pids, err := v.selectUID(v.euid)
	if err != nil || !slices.Contains(pids, v.selfPID) || slices.Contains(pids, 0) {
		return nil, journalRefusal("process_inventory_unreadable")
	}
	procs := map[int]*darwinProcess{}
	for _, pid := range pids {
		if pid == v.selfPID || procs[pid] != nil {
			continue
		}
		start, alive, known := v.procs.StartTime(pid)
		if known && !alive {
			continue // exited; a zombie answers gone
		}
		p := &darwinProcess{}
		if known {
			p.setStart(start)
		}
		procs[pid] = p
	}
	return procs, nil
}

// report reads each process's second start identity, after all evidence, and
// refuses with every process still blocking, sorted by pid. A process gone
// since is skipped. A positive mutator finding survives a changed or
// unreadable start identity; no other process is reported.
// Only pid, first start identity, role and action are reported (SR-2.11).
func (v darwinVerifier) report(procs map[int]*darwinProcess, ordered []int) error {
	var blockers []JournalProcess
	for _, pid := range ordered {
		p := procs[pid]
		_, alive, known := v.procs.StartTime(pid)
		if known && !alive {
			continue
		}
		if p.mutator {
			blockers = append(blockers, JournalProcess{PID: pid, Starttime: p.start, Role: "agent_director_mutator", Action: "stop_and_restart_after_cutover"})
		}
	}
	if len(blockers) > 0 {
		return journalRefusal("old_or_unverified_mutators", blockers...)
	}
	return nil
}

// checkAccessControl refuses an existing path whose access-control entries
// allow another principal or cannot be read (SR-2.15 "Limits"), because
// Darwin's access-control lists bypass the mode bits verifyJournalPathIn
// reads. An absent path has no entries and passes, as on Linux.
func (v darwinVerifier) checkAccessControl(ctx context.Context, path string, helpers *darwinHelpers) error {
	absent := false
	if err := bounded(ctx, func() error {
		_, err := v.fs.Lstat(path)
		if journalLookupAbsent(err) {
			absent = true
			return nil
		}
		if err != nil {
			return journalRefusal("legacy_root_unreadable")
		}
		return nil
	}); err != nil || absent {
		return err
	}
	stdout, stderr, err := v.readACL(ctx, path, helpers.started(v.procs))
	if ctx.Err() != nil {
		return journalRefusal("inventory_deadline")
	}
	if err != nil || len(stderr) > 0 || !journalACLPrivate(stdout) {
		return journalRefusal("legacy_root_access_unestablished")
	}
	return nil
}

// darwinHelpers are the helper processes the verifier started, each with the
// confirmed start identity read as it started. An unreadable or already-gone
// helper grants no exclusion; anything it would exclude stays under check.
type darwinHelpers struct {
	mu     sync.Mutex
	starts map[int]string
}

func (h *darwinHelpers) started(procs probe.ProcChecker) func(pid int) {
	return func(pid int) {
		start, alive, known := procs.StartTime(pid)
		if _, ok := probe.ParseDarwinProcStartTime(start); !known || !alive || !ok {
			return
		}
		h.mu.Lock()
		defer h.mu.Unlock()
		if h.starts == nil {
			h.starts = map[int]string{}
		}
		h.starts[pid] = start
	}
}

func (h *darwinHelpers) snapshot() map[int]string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return maps.Clone(h.starts)
}

// darwinStartReads is each listed process's start identity, read once: the
// first one for a selected process, otherwise read when first asked.
type darwinStartReads struct {
	procs    probe.ProcChecker
	selected map[int]*darwinProcess
	read     map[int]darwinStartRead
}

// darwinStartRead is one start identity: gone (not alive), or still under
// check, with started usable only when the identity was read and parses.
type darwinStartRead struct {
	start         string
	started       time.Time
	alive, usable bool
}

func (s *darwinStartReads) of(pid int) darwinStartRead {
	if p := s.selected[pid]; p != nil {
		return darwinStartRead{start: p.start, started: p.started, alive: true, usable: p.start != ""}
	}
	if r, ok := s.read[pid]; ok {
		return r
	}
	start, alive, known := s.procs.StartTime(pid)
	// A process not known to be gone stays under check, even without a start.
	r := darwinStartRead{start: start, alive: alive || !known}
	if known && alive {
		r.started, r.usable = probe.ParseDarwinProcStartTime(start)
	}
	if s.read == nil {
		s.read = map[int]darwinStartRead{}
	}
	s.read[pid] = r
	return r
}

// exclusions is the listed processes left out of the check: the verifier
// itself; each helper it started, while that pid still has the helper's start
// identity, so a pid reused since stays under check; and every listed process
// whose parent is one of those and whose start is not earlier than its
// parent's, repeated until nothing changes, so grandchildren go too.
func (v darwinVerifier) exclusions(listing map[int][]lsofFile, helpers map[int]string, starts *darwinStartReads) map[int]bool {
	excluded := map[int]bool{v.selfPID: true}
	parents := map[int]time.Time{}
	if start, alive, known := v.procs.StartTime(v.selfPID); known && alive {
		if started, ok := probe.ParseDarwinProcStartTime(start); ok {
			parents[v.selfPID] = started
		}
	}
	for pid, helperStart := range helpers {
		start, alive, known := v.procs.StartTime(pid)
		if started, ok := probe.ParseDarwinProcStartTime(start); known && alive && ok && start == helperStart {
			excluded[pid] = true
			parents[pid] = started
		}
	}
	ppids := map[int]int{}
	for changed := true; changed; {
		changed = false
		for pid := range listing {
			if excluded[pid] {
				continue
			}
			ppid, read := ppids[pid]
			if !read {
				ppid, _ = v.ppids.PPID(pid)
				ppids[pid] = ppid
			}
			parentStarted, isParent := parents[ppid]
			if !isParent {
				continue
			}
			if r := starts.of(pid); r.usable && !r.started.Before(parentStarted) {
				excluded[pid] = true
				parents[pid] = r.started
				changed = true
			}
		}
	}
	return excluded
}

// reconcileDarwinListing matches the selection with the listing. A selected
// live process the listing lacks has no files to judge. A listed process the
// selection lacks is skipped once gone and otherwise checked, except when
// its usable start predates the selection, which should have found it.
// Start identities are compared at microsecond precision.
func reconcileDarwinListing(procs map[int]*darwinProcess, listing map[int][]lsofFile, excluded map[int]bool, starts *darwinStartReads, selectedAt time.Time) error {
	for pid, p := range procs {
		if excluded[pid] {
			delete(procs, pid)
		} else {
			p.files = listing[pid]
		}
	}
	for pid, files := range listing {
		if excluded[pid] || procs[pid] != nil {
			continue
		}
		r := starts.of(pid)
		if !r.alive {
			continue
		}
		p := &darwinProcess{files: files}
		if r.usable {
			if r.started.Before(selectedAt) {
				return journalRefusal("process_inventory_unreadable")
			}
			p.start, p.started = r.start, r.started
		}
		procs[pid] = p
	}
	return nil
}

// darwinEvidence is what a process's files are judged against: the store's
// identities, every identity a root walk reached, and the roots.
type darwinEvidence struct {
	store, walked map[journalFileID]bool
	storeForms    []string
	roots         []darwinRoot
}

// evidence reads the store's and roots' identities after the listing. The
// store's join those read before the selection.
func (v darwinVerifier) evidence(ctx context.Context, dbPath string, roots []string, store map[journalFileID]bool) (*darwinEvidence, error) {
	if err := bounded(ctx, func() error { return journalStoreIDs(v.fs, dbPath, store) }); err != nil {
		return nil, err
	}
	evidence := &darwinEvidence{store: store, walked: map[journalFileID]bool{}}
	for _, path := range []string{dbPath, dbPath + "-wal", dbPath + "-shm"} {
		if err := bounded(ctx, func() error {
			ancestor, _, rest, err := journalDeepestExisting(v.fs, path)
			if err != nil {
				return journalRefusal("legacy_root_unreadable")
			}
			forms, err := journalPathForms(v.fs, path, ancestor, rest)
			evidence.storeForms = append(evidence.storeForms, forms...)
			return err
		}); err != nil {
			return nil, err
		}
	}
	for _, root := range roots {
		var r darwinRoot
		if err := bounded(ctx, func() (err error) { r, err = journalRootOf(ctx, v.fs, root, evidence.walked); return err }); err != nil {
			return nil, err
		}
		evidence.roots = append(evidence.roots, r)
	}
	return evidence, nil
}

// journalStoreIDs adds the identities of the database, -wal and -shm that
// exist. The verifier reads them before the selection and again after the
// listing, so a file recreated in between still matches.
func journalStoreIDs(fsys journalFS, dbPath string, ids map[journalFileID]bool) error {
	for _, path := range []string{dbPath, dbPath + "-wal", dbPath + "-shm"} {
		st, err := fsys.Lstat(path)
		if journalLookupAbsent(err) {
			continue
		}
		if err != nil {
			return journalRefusal("legacy_root_unreadable")
		}
		ids[st.id] = true
	}
	return nil
}

// judge tests the first txt entry's program name without inspecting its
// image, and classifies listed files by descriptor kind (SR-2.15). Incomplete
// entries use only gated, identity-confirmed paths; other missing evidence
// adds no finding.
func (v darwinVerifier) judge(ctx context.Context, p *darwinProcess, evidence *darwinEvidence) error {
	programSeen := false
	text := map[journalFileID]bool{}
	for i := range p.files {
		f := &p.files[i]
		class := lsofFDClassOf(f.fd)
		if class == lsofFDText && !programSeen {
			programSeen = true
			if strings.Contains(filepath.Base(f.name), "agent-director") {
				p.mutator = true
				return nil
			}
		}
		kind := lsofKindOf(*f)
		if class == lsofFDDirectory || kind == lsofKindNonVnode {
			continue
		}
		fullIdentity := kind == lsofKindVnode && class != lsofFDInfoError && class != lsofFDUnknown && f.hasDev && f.hasIno
		if fullIdentity {
			if class == lsofFDText {
				if text[f.id] {
					continue
				}
				text[f.id] = true
			}
			if evidence.store[f.id] || evidence.walked[f.id] {
				p.mutator = true
				return nil
			}
		} else if matches, err := v.storeAtName(ctx, f.name, evidence); err != nil {
			return err
		} else if matches {
			p.mutator = true
			return nil
		}
		under, err := v.underRoot(ctx, f.name, evidence)
		if asJournalRefusal(err) {
			return err
		}
		if err == nil && under {
			p.mutator = true
			return nil
		}
	}
	return nil
}

// storeAtName tests an incomplete entry only when its absolute listed name
// matches an unescaped store form. A failed lookup adds no finding; a
// deadline still refuses the attempt.
func (v darwinVerifier) storeAtName(ctx context.Context, name string, evidence *darwinEvidence) (bool, error) {
	if !filepath.IsAbs(name) || !slices.ContainsFunc(evidence.storeForms, func(form string) bool {
		return !strings.ContainsAny(form, `\^`) && strings.EqualFold(name, form)
	}) {
		return false, nil
	}
	var matches bool
	if err := bounded(ctx, func() error {
		st, err := v.fs.Lstat(name)
		matches = err == nil && evidence.store[st.id]
		return nil
	}); err != nil {
		return false, err
	}
	return matches, nil
}

// underRoot is the ancestor test for an entry the walk did not find. It runs
// only when the name, compared case-insensitively, is under a form of an
// existing root, so no lookup reaches an unrelated folder. The entry then
// lies under that root when the root's identity is among the name's deepest
// existing ancestor and that ancestor's parents.
func (v darwinVerifier) underRoot(ctx context.Context, name string, evidence *darwinEvidence) (bool, error) {
	var gated []journalFileID
	for _, r := range evidence.roots {
		if r.exists && slices.ContainsFunc(r.forms, func(form string) bool { return journalUnderForm(name, form) }) {
			gated = append(gated, r.id)
		}
	}
	if len(gated) == 0 {
		return false, nil
	}
	var chain []journalFileID
	if err := bounded(ctx, func() (err error) { chain, err = journalAncestorIDs(v.fs, name); return err }); err != nil {
		return false, err
	}
	return slices.ContainsFunc(chain, func(id journalFileID) bool { return slices.Contains(gated, id) }), nil
}

// asJournalRefusal tells a refusal of the whole run from a lookup error that
// judges one entry.
func asJournalRefusal(err error) bool {
	var refusal *JournalRefusal
	return errors.As(err, &refusal)
}

// bounded runs call in a goroutine and returns its error, or refuses
// inventory_deadline when ctx ends first (SR-2.15: these lookups run under
// the attempt's deadline, as the listing does). A lookup on an unreachable
// mount, or an open held by an unanswered privacy consent prompt, can block
// in the kernel; the abandoned goroutine ends with the process, which exits
// after writing its refusal. A signal ends ctx too, and MigrateJournal then
// reports interrupted.
func bounded(ctx context.Context, call func() error) error {
	if ctx.Err() != nil {
		return journalRefusal("inventory_deadline")
	}
	done := make(chan error, 1)
	go func() { done <- call() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return journalRefusal("inventory_deadline")
	}
}

// journalHelperStdoutCap and journalHelperStderrCap bound what a helper may
// write; more is an error. Any stderr byte refuses anyway.
const (
	journalHelperStdoutCap = 64 << 20
	journalHelperStderrCap = 1 << 20
)

// journalHelperWaitDelay is how long a helper's output may stay open once it
// is killed or has exited.
const journalHelperWaitDelay = 3 * time.Second

var errJournalHelperOutput = errors.New("helper output exceeds its cap")

// journalCappedBuffer collects a helper's output up to limit bytes.
type journalCappedBuffer struct {
	buf   bytes.Buffer
	limit int
}

func (b *journalCappedBuffer) Write(p []byte) (int, error) {
	if b.buf.Len()+len(p) > b.limit {
		return 0, errJournalHelperOutput
	}
	return b.buf.Write(p)
}

// runFixedHelper runs the tool at the absolute path, never one found through
// PATH, with only LC_ALL=C in its environment and stdin from the null device,
// in its own process group. started gets its pid right after it starts. The
// end of ctx kills the whole group, so a hung lsof cannot outlive the
// attempt, and Wait gives its output journalHelperWaitDelay to close. Must
// use: the verifier runs every external tool through it.
func runFixedHelper(ctx context.Context, path string, args []string, started func(pid int)) (stdout, stderr []byte, err error) {
	if !filepath.IsAbs(path) {
		return nil, nil, fmt.Errorf("helper %q is not an absolute path", path)
	}
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = []string{"LC_ALL=C"}
	// A nil Stdin reads from the null device.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = journalHelperWaitDelay
	out := &journalCappedBuffer{limit: journalHelperStdoutCap}
	errOut := &journalCappedBuffer{limit: journalHelperStderrCap}
	cmd.Stdout, cmd.Stderr = out, errOut
	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}
	started(cmd.Process.Pid)
	err = cmd.Wait()
	return out.buf.Bytes(), errOut.buf.Bytes(), err
}
