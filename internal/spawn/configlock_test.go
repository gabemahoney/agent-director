package spawn

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
)

// fakeLockClock is the config lock's clock in a test: it starts at the real
// now, moves by step at each reading and by each sleep, and totals the sleeps.
type fakeLockClock struct {
	now   time.Time
	slept time.Duration
}

// withFakeLockClock runs the config lock on a fake clock for the test's life.
func withFakeLockClock(t *testing.T, step time.Duration) *fakeLockClock {
	t.Helper()
	c := &fakeLockClock{now: time.Now()}
	savedNow, savedSleep := configLockNow, configLockSleep
	configLockNow = func() time.Time { c.now = c.now.Add(step); return c.now }
	configLockSleep = func(d time.Duration) { c.now = c.now.Add(d); c.slept += d }
	t.Cleanup(func() { configLockNow, configLockSleep = savedNow, savedSleep })
	return c
}

// holdLock makes path's lock dir as another process holding it would, last
// refreshed at mtime, and returns the dir as made.
func holdLock(t *testing.T, path string, mtime time.Time) os.FileInfo {
	t.Helper()
	lock := path + ".lock"
	if err := os.Mkdir(lock, 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", lock, err)
	}
	if err := os.Chtimes(lock, mtime, mtime); err != nil {
		t.Fatalf("chtimes %s: %v", lock, err)
	}
	info, err := os.Stat(lock)
	if err != nil {
		t.Fatalf("stat %s: %v", lock, err)
	}
	return info
}

// assertSeedKept fails unless path still holds lockTestSeed byte for byte.
func assertSeedKept(t *testing.T, path string) {
	t.Helper()
	if got := mustReadFile(t, path); string(got) != lockTestSeed {
		t.Errorf("claude.json = %q; want byte-identical %q", got, lockTestSeed)
	}
}

// assertLockKept fails unless path's lock dir is still held's, unchanged.
func assertLockKept(t *testing.T, path string, held os.FileInfo) {
	t.Helper()
	cur, err := os.Stat(path + ".lock")
	if err != nil || !os.SameFile(cur, held) || !cur.ModTime().Equal(held.ModTime()) {
		t.Errorf("lock dir: %v, %v; want the holder's (mtime %s) left in place", cur, err, held.ModTime())
	}
}

// TestPreTrustHeldLockFailsAfterConfiguredWait pins b.kr4 (and b.zjm): a fresh
// lock held throughout makes pre-trust wait exactly the configured
// lock_wait_seconds, then report failed naming it, touching nothing.
func TestPreTrustHeldLockFailsAfterConfiguredWait(t *testing.T) {
	for _, secs := range []int{1, 5} {
		wait := time.Duration(secs) * time.Second
		t.Run(wait.String(), func(t *testing.T) {
			clock := withFakeLockClock(t, 0)
			env, path := seedConfigDir(t)
			held := holdLock(t, path, clock.now)
			warn := capturePreTrustWarn(t)

			if got := PreTrust("/tmp/held-cwd", env, false, config.PreTrust{LockWaitSeconds: secs}); got != PreTrustFailed {
				t.Fatalf("PreTrust = %q; want failed", got)
			}
			if clock.slept != wait {
				t.Errorf("waited %s for the held lock; want the configured %s", clock.slept, wait)
			}
			assertOneFailedLine(t, warn.String(), path+".lock", "held by another process",
				"gave up after waiting "+wait.String())
			assertSeedKept(t, path)
			assertLockKept(t, path, held)
		})
	}
}

// TestPreTrustLockStaleness pins proper-lockfile's stale rule: a lock dir last
// refreshed over 10 s ago is broken and taken; a younger one is waited on. With
// lock_wait_seconds missing (12 s), a fresh lock never refreshed goes stale
// within the wait, so pre-trust reports ok (b.kr4; the old fixed 5 s gave up).
// So it does with the largest value Load accepts, whose wait does not wrap
// to 0 or below and give up at the first attempt.
func TestPreTrustLockStaleness(t *testing.T) {
	const cwd = "/tmp/stale-cwd"
	// largest is config.MaxPreTrustLockWaitSeconds held in a variable, so its
	// int conversion happens at run time and the file builds where int is 32
	// bits (GOARCH=386), where the row using it is skipped.
	largest := config.MaxPreTrustLockWaitSeconds
	cases := []struct {
		name               string
		age                time.Duration   // the lock dir's mtime age when pre-trust starts
		cfg                config.PreTrust // the launch's [pre_trust] table
		wide               bool            // cfg needs a 64-bit int
		minSlept, maxSlept time.Duration   // fake wait before the lock was taken
	}{
		{name: "11s old lock is broken at once", age: 11 * time.Second},
		{name: "9s old lock is broken once over 10s", age: 9 * time.Second, cfg: config.PreTrust{LockWaitSeconds: 5},
			minSlept: time.Second, maxSlept: 5 * time.Second},
		{name: "fresh lock never refreshed is broken within the default wait",
			minSlept: 10 * time.Second, maxSlept: 12 * time.Second},
		{name: "fresh lock never refreshed is broken within the largest wait",
			cfg: config.PreTrust{LockWaitSeconds: int(largest)}, wide: true,
			minSlept: 10 * time.Second, maxSlept: 12 * time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.wide && strconv.IntSize < 64 {
				t.Skipf("lock_wait_seconds %d does not fit a %d-bit int", largest, strconv.IntSize)
			}
			clock := withFakeLockClock(t, 0)
			env, path := seedConfigDir(t)
			holdLock(t, path, clock.now.Add(-tc.age))
			warn := capturePreTrustWarn(t)

			if got := PreTrust(cwd, env, false, tc.cfg); got != PreTrustOK {
				t.Fatalf("PreTrust = %q (waited %s, warning %q); want ok", got, clock.slept, warn)
			}
			if clock.slept < tc.minSlept || clock.slept > tc.maxSlept {
				t.Errorf("took the lock after waiting %s; want %s to %s (not before it was over 10s old)",
					clock.slept, tc.minSlept, tc.maxSlept)
			}
			if got := readClaudeJSON(t, path); !trusts(got, cwd) || got["userID"] != "u" {
				t.Errorf("claude.json = %v; want projects[%q] trusted and userID kept", got, cwd)
			}
			assertNoStray(t, filepath.Dir(path))
		})
	}
}

// TestPreTrustReadsUnderTheLock pins b.zjm's read under the lock: the holder
// updates .claude.json and releases the lock during pre-trust's first wait,
// and pre-trust's write keeps that update.
func TestPreTrustReadsUnderTheLock(t *testing.T) {
	const cwd = "/tmp/reread-cwd"
	clock := withFakeLockClock(t, 0)
	env, path := seedConfigDir(t)
	holdLock(t, path, clock.now)
	fakeSleep, released := configLockSleep, false
	configLockSleep = func(d time.Duration) {
		if !released {
			released = true
			seedFile(t, path, `{"projects":{},"userID":"u","numStartups":3}`)
			if err := os.Remove(path + ".lock"); err != nil {
				t.Fatalf("release lock: %v", err)
			}
		}
		fakeSleep(d)
	}

	if got := PreTrust(cwd, env, false, config.PreTrust{}); got != PreTrustOK {
		t.Fatalf("PreTrust = %q; want ok", got)
	}
	if got := readClaudeJSON(t, path); !released || !trusts(got, cwd) || got["numStartups"] != float64(3) {
		t.Errorf("claude.json = %v (holder released: %v); want projects[%q] trusted and numStartups 3 kept", got, released, cwd)
	}
	assertNoStray(t, filepath.Dir(path))
}

// TestPreTrustWritesNothingUnderLostLock pins b.zjm's check before the write:
// a lock held over 5 s (never refreshed) or taken over makes pre-trust write nothing.
func TestPreTrustWritesNothingUnderLostLock(t *testing.T) {
	const cwd = "/tmp/lost-lock-cwd"
	cases := []struct {
		name     string
		step     time.Duration // fake clock move per reading; 6 s holds the lock too long
		takeover string        // what another process does to the lock dir just before the write
		reason   string        // text the failed line must carry
	}{
		{name: "held too long", step: 6 * time.Second, reason: "stale limit"},
		{name: "lock dir removed", takeover: "remove", reason: "was taken over by another process"},
		{name: "lock dir replaced", takeover: "replace", reason: "was taken over by another process"},
		{name: "held too long and replaced reports the hold", step: 6 * time.Second, takeover: "replace", reason: "stale limit"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withFakeLockClock(t, tc.step)
			env, path := seedConfigDir(t)
			warn := capturePreTrustWarn(t)
			var theirs os.FileInfo
			saved := preTrustBeforeCommit
			preTrustBeforeCommit = func() {
				if tc.takeover == "" {
					return
				}
				mine, err := os.Stat(path + ".lock")
				if err != nil {
					t.Fatalf("stat own lock: %v", err)
				}
				if err := os.Remove(path + ".lock"); err != nil {
					t.Fatalf("break lock: %v", err)
				}
				if tc.takeover == "replace" {
					theirs = holdLock(t, path, mine.ModTime().Add(time.Second))
				}
			}
			t.Cleanup(func() { preTrustBeforeCommit = saved })

			if got := PreTrust(cwd, env, false, config.PreTrust{}); got != PreTrustFailed {
				t.Fatalf("PreTrust = %q; want failed", got)
			}
			assertOneFailedLine(t, warn.String(), path+".lock", tc.reason)
			if w := warn.String(); strings.Contains(w, "stale limit") && strings.Contains(w, "taken over") {
				t.Errorf("warning = %q; want one reason, not both", w)
			}
			assertSeedKept(t, path)
			if theirs != nil {
				assertLockKept(t, path, theirs)
				assertNoStray(t, filepath.Dir(path), ".claude.json.lock")
			} else {
				assertNoStray(t, filepath.Dir(path))
			}
		})
	}
}
