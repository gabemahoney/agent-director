package probe

import (
	"errors"
	"testing"
)

// fakeAncestorReader is a build-tag-free in-memory ancestorReader for the
// pure-logic walkAncestors cases. It models a process tree by pid, so the
// topmost/no-match/cycle/depth/vanish paths are exercised without any OS proc
// I/O or build tags.
type fakeProc struct {
	ppid      int
	startTime string
	envID     string // "" ⇒ no matching var present
	hasEnv    bool   // whether the EnvKey is present at all
	// present controls the parent()'s present flag: false models an ancestor
	// that vanished mid-walk (stat gone).
	present bool
	// idErr / parentErr force the corresponding hard reader error (abort).
	idErr     error
	parentErr error
}

type fakeAncestorReader struct {
	self  int
	procs map[int]fakeProc
}

func (f *fakeAncestorReader) selfPID() int { return f.self }

func (f *fakeAncestorReader) instanceID(pid int) (string, bool, error) {
	p, ok := f.procs[pid]
	if !ok {
		// Unknown pid: routine non-match (environ unreadable / gone).
		return "", false, nil
	}
	if p.idErr != nil {
		return "", false, p.idErr
	}
	if !p.hasEnv {
		return "", false, nil
	}
	return p.envID, true, nil
}

func (f *fakeAncestorReader) parent(pid int) (int, string, bool, error) {
	p, ok := f.procs[pid]
	if !ok {
		return 0, "", false, nil
	}
	if p.parentErr != nil {
		return 0, "", false, p.parentErr
	}
	if !p.present {
		return 0, "", false, nil
	}
	return p.ppid, p.startTime, true, nil
}

// TestWalkAncestorsTopmostMatch pins the topmost-not-nearest contract: both the
// child (self) and its parent carry the matching id, and the walk MUST return
// the TOPMOST (parent) pid + starttime. A nearest-match implementation that
// returns on the first match fails this test.
func TestWalkAncestorsTopmostMatch(t *testing.T) {
	const id = "match-me"
	const topStart = "99999999"
	r := &fakeAncestorReader{
		self: 100,
		procs: map[int]fakeProc{
			// child (self): matches, but is NOT topmost.
			100: {ppid: 200, startTime: "11111111", envID: id, hasEnv: true, present: true},
			// parent: also matches → this is the TOPMOST match.
			200: {ppid: 300, startTime: topStart, envID: id, hasEnv: true, present: true},
			// grandparent (e.g. tmux server): no matching var → terminates.
			300: {ppid: 1, startTime: "22222222", hasEnv: false, present: true},
		},
	}

	pid, start, err := walkAncestors(r, id)
	if err != nil {
		t.Fatalf("walkAncestors: unexpected error %v", err)
	}
	if pid != 200 {
		t.Errorf("pid = %d; want 200 (topmost, not nearest 100)", pid)
	}
	if start != topStart {
		t.Errorf("starttime = %q; want %q (topmost's, verbatim)", start, topStart)
	}
}

// TestWalkAncestorsNoMatch pins that a chain with no matching ancestor yields
// ErrNoMatchingAncestor and never a bogus pid.
func TestWalkAncestorsNoMatch(t *testing.T) {
	r := &fakeAncestorReader{
		self: 100,
		procs: map[int]fakeProc{
			100: {ppid: 200, startTime: "1", hasEnv: false, present: true},
			200: {ppid: 1, startTime: "2", hasEnv: false, present: true},
		},
	}

	pid, start, err := walkAncestors(r, "nobody-has-this")
	if !errors.Is(err, ErrNoMatchingAncestor) {
		t.Fatalf("err = %v; want ErrNoMatchingAncestor", err)
	}
	if pid != 0 || start != "" {
		t.Errorf("got pid=%d start=%q; want zero identity on no-match", pid, start)
	}
}

// TestWalkAncestorsMidWalkVanishNoPriorMatch pins the mid-walk contract when
// NO match was seen before the ancestor vanished: present=false ends the walk
// cleanly, and with no match recorded the result is ErrNoMatchingAncestor with
// no partial identity.
func TestWalkAncestorsMidWalkVanishNoPriorMatch(t *testing.T) {
	r := &fakeAncestorReader{
		self: 100,
		procs: map[int]fakeProc{
			// self: no match, and its parent read reports vanished.
			100: {ppid: 200, startTime: "1", hasEnv: false, present: false},
		},
	}

	pid, start, err := walkAncestors(r, "match-me")
	if !errors.Is(err, ErrNoMatchingAncestor) {
		t.Fatalf("err = %v; want ErrNoMatchingAncestor", err)
	}
	if pid != 0 || start != "" {
		t.Errorf("got pid=%d start=%q; want zero identity", pid, start)
	}
}

// TestWalkAncestorsMatchingLevelVanishedIsNonMatch pins the both-or-neither
// contract (PM ruling): an env-matching level is recorded as a match ONLY when
// its OWN parent()/stat read succeeded (present=true) yielding a non-empty
// starttime. A matching level whose stat vanished mid-walk (present=false, empty
// starttime) is a NON-match: recording it would write a non-NULL pid with a NULL
// proc_starttime — the pid-reuse-ambiguous evidence SR-6's starttime defeats.
//
// This single matching-but-vanished level with NO prior complete match below it
// must therefore resolve to ErrNoMatchingAncestor with the zero identity — never
// a pid carrying an empty starttime.
func TestWalkAncestorsMatchingLevelVanishedIsNonMatch(t *testing.T) {
	const id = "match-me"
	r := &fakeAncestorReader{
		self: 100,
		procs: map[int]fakeProc{
			// self's environ matches, but its parent()/stat read reports the
			// process vanished (present=false ⇒ startTime "" from that read).
			// Under both-or-neither this is a NON-match.
			100: {ppid: 200, startTime: "42", envID: id, hasEnv: true, present: false},
		},
	}

	pid, start, err := walkAncestors(r, id)
	if !errors.Is(err, ErrNoMatchingAncestor) {
		t.Fatalf("err = %v; want ErrNoMatchingAncestor (matching-but-vanished level is a non-match)", err)
	}
	if pid != 0 || start != "" {
		t.Errorf("got pid=%d start=%q; want zero identity (never a pid with an empty starttime)", pid, start)
	}
	// Explicit: the resolver must NEVER return a pid with an empty starttime.
	if pid != 0 && start == "" {
		t.Errorf("resolver returned pid=%d with empty starttime; both-or-neither violated", pid)
	}
}

// TestWalkAncestorsPriorMatchWinsOverVanishedHigherMatch pins the OTHER half of
// the both-or-neither contract: when a COMPLETE match exists at a lower level
// (present=true, non-empty starttime) and a HIGHER matching level then vanishes
// mid-walk (present=false), the prior complete match is what gets returned — pid
// AND its non-empty starttime — and the vanished higher level contributes
// nothing. The resolver never overwrites a complete match with a pid-without-
// starttime.
func TestWalkAncestorsPriorMatchWinsOverVanishedHigherMatch(t *testing.T) {
	const id = "match-me"
	const lowerStart = "12345678"
	r := &fakeAncestorReader{
		self: 100,
		procs: map[int]fakeProc{
			// self: COMPLETE match (present=true, non-empty starttime).
			100: {ppid: 200, startTime: lowerStart, envID: id, hasEnv: true, present: true},
			// parent: also env-matches but its stat vanished (present=false).
			// Both-or-neither ⇒ this higher level is a NON-match, so it must
			// NOT overwrite the lower complete match with a pid-only identity.
			200: {ppid: 300, startTime: "99999999", envID: id, hasEnv: true, present: false},
		},
	}

	pid, start, err := walkAncestors(r, id)
	if err != nil {
		t.Fatalf("walkAncestors: unexpected error %v", err)
	}
	if pid != 100 {
		t.Errorf("pid = %d; want 100 (the prior COMPLETE match, not the vanished higher one)", pid)
	}
	if start != lowerStart {
		t.Errorf("starttime = %q; want %q (the complete match's, never empty)", start, lowerStart)
	}
	// Explicit: never a pid with an empty starttime.
	if start == "" {
		t.Errorf("resolver returned pid=%d with empty starttime; both-or-neither violated", pid)
	}
}

// TestWalkAncestorsUnreadableEnvContinues pins that an unreadable environ
// (instanceID → "",false,nil, e.g. a foreign-uid ancestor under --pid=host) is
// a non-match that does NOT abort the walk: a matching ancestor above it is
// still found.
func TestWalkAncestorsUnreadableEnvContinues(t *testing.T) {
	const id = "match-me"
	r := &fakeAncestorReader{
		self: 100,
		procs: map[int]fakeProc{
			// self: environ unreadable (hasEnv=false) → non-match, continue.
			100: {ppid: 200, startTime: "1", hasEnv: false, present: true},
			// parent: matches → found despite the unreadable child.
			200: {ppid: 1, startTime: "77", envID: id, hasEnv: true, present: true},
		},
	}

	pid, start, err := walkAncestors(r, id)
	if err != nil {
		t.Fatalf("walkAncestors: unexpected error %v", err)
	}
	if pid != 200 || start != "77" {
		t.Errorf("got pid=%d start=%q; want pid=200 start=77", pid, start)
	}
}

// TestWalkAncestorsCycleGuard pins the pid-cycle guard: a ppid loop terminates
// the walk rather than hanging. With no match in the loop, the result is the
// typed no-match error.
func TestWalkAncestorsCycleGuard(t *testing.T) {
	r := &fakeAncestorReader{
		self: 100,
		procs: map[int]fakeProc{
			// 100 → 200 → 100 → ... a 2-cycle.
			100: {ppid: 200, startTime: "1", hasEnv: false, present: true},
			200: {ppid: 100, startTime: "2", hasEnv: false, present: true},
		},
	}

	pid, _, err := walkAncestors(r, "match-me")
	if !errors.Is(err, ErrNoMatchingAncestor) {
		t.Fatalf("err = %v; want ErrNoMatchingAncestor (cycle must terminate)", err)
	}
	if pid != 0 {
		t.Errorf("pid = %d; want 0", pid)
	}
}

// TestWalkAncestorsCycleGuardReturnsSeenMatch pins that when a match exists
// inside a cycle, the guard still lets the walk terminate AND return that
// match rather than hanging.
func TestWalkAncestorsCycleGuardReturnsSeenMatch(t *testing.T) {
	const id = "match-me"
	r := &fakeAncestorReader{
		self: 100,
		procs: map[int]fakeProc{
			100: {ppid: 200, startTime: "1", hasEnv: false, present: true},
			200: {ppid: 100, startTime: "55", envID: id, hasEnv: true, present: true},
		},
	}

	pid, start, err := walkAncestors(r, id)
	if err != nil {
		t.Fatalf("walkAncestors: unexpected error %v", err)
	}
	if pid != 200 || start != "55" {
		t.Errorf("got pid=%d start=%q; want pid=200 start=55", pid, start)
	}
}

// TestWalkAncestorsDepthBound pins that a chain longer than maxWalkDepth
// terminates via the depth guard rather than hanging. The chain is built with
// no matching env, so the bounded walk ends in the typed no-match error.
func TestWalkAncestorsDepthBound(t *testing.T) {
	procs := make(map[int]fakeProc)
	// Build an ever-climbing chain of unique pids far longer than maxWalkDepth.
	const chainLen = maxWalkDepth + 500
	for i := 2; i < 2+chainLen; i++ {
		procs[i] = fakeProc{ppid: i + 1, startTime: "1", hasEnv: false, present: true}
	}
	r := &fakeAncestorReader{self: 2, procs: procs}

	pid, _, err := walkAncestors(r, "match-me")
	if !errors.Is(err, ErrNoMatchingAncestor) {
		t.Fatalf("err = %v; want ErrNoMatchingAncestor (depth bound must terminate)", err)
	}
	if pid != 0 {
		t.Errorf("pid = %d; want 0", pid)
	}
}

// TestWalkAncestorsHardIDError pins that a hard instanceID reader error aborts
// the walk verbatim (fail-open: the hook maps it to NULL identity).
func TestWalkAncestorsHardIDError(t *testing.T) {
	sentinel := errors.New("hard environ read failure")
	r := &fakeAncestorReader{
		self: 100,
		procs: map[int]fakeProc{
			100: {ppid: 200, startTime: "1", present: true, idErr: sentinel},
		},
	}

	pid, start, err := walkAncestors(r, "match-me")
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v; want the verbatim sentinel", err)
	}
	if pid != 0 || start != "" {
		t.Errorf("got pid=%d start=%q; want zero identity on abort", pid, start)
	}
}

// TestWalkAncestorsHardParentError pins that a hard parent() reader error
// (e.g. a malformed stat line surfacing ErrLinuxStatMalformed) aborts the walk
// verbatim.
func TestWalkAncestorsHardParentError(t *testing.T) {
	sentinel := errors.New("malformed stat")
	r := &fakeAncestorReader{
		self: 100,
		procs: map[int]fakeProc{
			100: {ppid: 200, startTime: "1", present: true, parentErr: sentinel},
		},
	}

	pid, start, err := walkAncestors(r, "match-me")
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v; want the verbatim sentinel", err)
	}
	if pid != 0 || start != "" {
		t.Errorf("got pid=%d start=%q; want zero identity on abort", pid, start)
	}
}

// TestWalkAncestorsStopsAtPID1 pins that the walk stops at the init/kernel
// boundary (pid ≤ 1) without descending into pid 1's environ.
func TestWalkAncestorsStopsAtPID1(t *testing.T) {
	const id = "match-me"
	r := &fakeAncestorReader{
		self: 100,
		procs: map[int]fakeProc{
			100: {ppid: 1, startTime: "1", hasEnv: false, present: true},
			// pid 1 DOES carry the var, but the walk must never inspect it.
			1: {ppid: 0, startTime: "boot", envID: id, hasEnv: true, present: true},
		},
	}

	pid, _, err := walkAncestors(r, id)
	if !errors.Is(err, ErrNoMatchingAncestor) {
		t.Fatalf("err = %v; want ErrNoMatchingAncestor (must not inspect pid 1)", err)
	}
	if pid != 0 {
		t.Errorf("pid = %d; want 0", pid)
	}
}
