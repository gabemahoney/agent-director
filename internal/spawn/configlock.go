package spawn

import (
	"errors"
	"fmt"
	"io/fs"
	"math/rand"
	"os"
	"syscall"
	"time"
)

// Claude Code's lock on its global config file, taken by pre-trust around its
// read-modify-write of .claude.json (bug b.zjm). Claude Code saves that file
// under a proper-lockfile lock: the directory <file>.lock (the config path as
// given, plus ".lock"), made with mkdir, its mtime refreshed while held,
// treated as stale once its mtime is more than configLockStale old, and
// removed with rmdir on release. Taking the same lock in the same way means
// pre-trust neither loses Claude Code's updates nor has its entry lost to
// them, and agent-director's own pre-trusts on one file run one at a time.

const (
	// configLockStale is proper-lockfile's stale threshold as Claude Code
	// uses it: a lock dir whose mtime is older than this is abandoned, and
	// any taker may remove it. It is Claude Code's protocol, not
	// agent-director's choice, so it is a fixed constant and not a setting:
	// every taker of the lock must judge staleness alike.
	configLockStale = 10 * time.Second
	// configLockMaxHold bounds how long pre-trust may hold the lock before
	// it commits its write. Pre-trust does not refresh the lock dir's mtime,
	// so it must commit well inside configLockStale; half of it is
	// proper-lockfile's own refresh interval. It follows from Claude Code's
	// protocol (configLockStale), so it is a fixed constant and not a
	// setting.
	configLockMaxHold = configLockStale / 2
	// configLockFirstDelay and configLockMaxDelay shape the wait between
	// attempts on a held lock: doubling from the first, capped at the max,
	// each stretched by a random factor in [1, 2) as Claude Code's retries are.
	configLockFirstDelay = 25 * time.Millisecond
	configLockMaxDelay   = 500 * time.Millisecond
)

// configLockNow and configLockSleep are the lock's clock and its sleep
// between attempts. Held as vars so tests can drive the clock. The total wait
// for a held lock is not here: it is the launch's configured
// pre_trust.lock_wait_seconds, passed to lockConfig through PreTrust (b.kr4).
var (
	configLockNow   = time.Now
	configLockSleep = time.Sleep
)

// errConfigLockHeld is the condition behind a lock attempt that found the
// lock held by another process (Claude Code or another agent-director) and
// not stale. lockConfig retries it until its wait runs out, then returns it
// wrapped.
var errConfigLockHeld = errors.New("held by another process")

// errConfigLockTakenOver is the condition behind a held lock whose lock dir is
// gone or is no longer the one it made: another process removed it (as
// stale) and may have taken the lock for itself. checkHold returns it
// wrapped, so the caller writes nothing.
var errConfigLockTakenOver = errors.New("was taken over by another process")

// configLock is one held lock on a config file.
type configLock struct {
	dir   string      // the lock dir, <config file>.lock
	made  os.FileInfo // the lock dir as this lock made it, for unlock's check
	since time.Time   // configLockNow just before the mkdir that took it
}

// lockConfig takes Claude Code's lock on the config file at path. While the
// lock is held and not stale it waits and tries again, with backoff, for at
// most wait in total; then it returns an error matching errConfigLockHeld
// that names the lock dir and the wait. Any other failure to take the lock is
// returned at once. The caller must unlock a returned lock.
//
// wait is PreTrust's effective pre_trust.lock_wait_seconds (b.kr4), 12 s by
// default: above configLockStale, so a lock dir abandoned by a killed holder
// goes stale and is broken within the wait. A wait at or below
// configLockStale can give up on such a lock before it goes stale. A wait of
// 0 or less makes one attempt and gives up on a held lock without sleeping.
func lockConfig(path string, wait time.Duration) (*configLock, error) {
	dir := path + ".lock"
	deadline := configLockNow().Add(wait)
	delay := configLockFirstDelay
	for {
		l, err := tryLockConfig(dir)
		if !errors.Is(err, errConfigLockHeld) {
			return l, err
		}
		left := deadline.Sub(configLockNow())
		if left <= 0 {
			return nil, fmt.Errorf("lock %s %w; gave up after waiting %s", dir, err, wait)
		}
		configLockSleep(min(time.Duration(float64(delay)*(1+rand.Float64())), left))
		delay = min(2*delay, configLockMaxDelay)
	}
}

// tryLockConfig makes one attempt at the lock dir, as proper-lockfile's
// acquire does: mkdir; on EEXIST, a lock dir whose mtime is more than
// configLockStale old is removed with rmdir and the mkdir tried once more, as
// it is when the lock dir vanished before its mtime could be read. A lock
// still held after that returns errConfigLockHeld.
//
// Breaking a stale lock this way can race, as it can in proper-lockfile: a
// taker that read the stale mtime may rmdir the fresh lock dir another taker
// has just made in its place, and both then hold the lock. checkHold's
// ownership check before the write narrows that window but cannot close it.
func tryLockConfig(dir string) (*configLock, error) {
	since := configLockNow()
	err := os.Mkdir(dir, 0o700)
	if errors.Is(err, fs.ErrExist) {
		info, serr := os.Stat(dir)
		switch {
		case errors.Is(serr, fs.ErrNotExist):
			err = os.Mkdir(dir, 0o700)
		case serr != nil:
			return nil, fmt.Errorf("check lock: %w", serr)
		case configLockNow().Sub(info.ModTime()) <= configLockStale:
			return nil, errConfigLockHeld
		default:
			if rerr := syscall.Rmdir(dir); rerr != nil && !errors.Is(rerr, fs.ErrNotExist) {
				return nil, fmt.Errorf("remove stale lock %s: %w", dir, rerr)
			}
			err = os.Mkdir(dir, 0o700)
		}
		if errors.Is(err, fs.ErrExist) {
			return nil, errConfigLockHeld
		}
	}
	if err != nil {
		return nil, fmt.Errorf("take lock: %w", err)
	}
	made, err := os.Stat(dir)
	if err != nil {
		_ = syscall.Rmdir(dir)
		return nil, fmt.Errorf("check lock: %w", err)
	}
	return &configLock{dir: dir, made: made, since: since}, nil
}

// checkHold is the caller's last check before it writes under the lock. It
// returns an error, and the caller must write nothing, when the lock has been
// held longer than configLockMaxHold (past that, another process may soon
// judge it stale), or when the lock dir is no longer the one this lock made
// (an error matching errConfigLockTakenOver; see owned). The ownership check
// is the cheap compromise check proper-lockfile makes on each refresh. It
// narrows, but does not close, the window in which two processes both hold
// the lock: one that takes it over after this check still races the write.
func (l *configLock) checkHold() error {
	if held := configLockNow().Sub(l.since); held > configLockMaxHold {
		return fmt.Errorf("held lock %s for %s, too close to its %s stale limit, so wrote nothing", l.dir, held, configLockStale)
	}
	if err := l.owned(); err != nil {
		return fmt.Errorf("%w, so wrote nothing", err)
	}
	return nil
}

// owned returns nil while the lock dir is still the one this lock made: the
// same directory with the same mtime. A lock dir that is gone, or is another
// directory or carries another mtime, returns an error matching
// errConfigLockTakenOver: another process removed this lock, as stale, and
// may have made its own. Any other failure to stat the lock dir is returned
// with context.
func (l *configLock) owned() error {
	cur, err := os.Stat(l.dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("lock %s %w", l.dir, errConfigLockTakenOver)
	case err != nil:
		return fmt.Errorf("check lock: %w", err)
	case !os.SameFile(cur, l.made) || !cur.ModTime().Equal(l.made.ModTime()):
		return fmt.Errorf("lock %s %w", l.dir, errConfigLockTakenOver)
	}
	return nil
}

// unlock releases the lock with rmdir, but only while the lock dir is still
// the one this lock made (owned): a holder stalled past configLockStale may
// have had its lock removed as stale and replaced by another process's,
// which unlock must leave alone. An rmdir failure is ignored: the entry is
// already written or abandoned, and a lock dir left behind goes stale and is
// removed by the next taker.
func (l *configLock) unlock() {
	if l.owned() != nil {
		return
	}
	_ = syscall.Rmdir(l.dir)
}
