package spawn

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
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

// TestPreTrustWaitsForConfigLockHolder pins b.zjm: while a writer holds Claude
// Code's <file>.lock and saves under it, pre-trust waits, so both updates land.
func TestPreTrustWaitsForConfigLockHolder(t *testing.T) {
	env, path := seedConfigDir(t)
	const cwd = "/tmp/lock-holder-cwd"
	warn := capturePreTrustWarn(t)
	// The writer takes the lock as Claude Code does (mkdir) and reads the file under it.
	holdLock(t, path, time.Now())
	cfg := readClaudeJSON(t, path)

	// Pre-trust's first sleep between attempts shows it found the lock held.
	waiting := make(chan struct{})
	var once sync.Once
	savedSleep := configLockSleep
	configLockSleep = func(d time.Duration) { once.Do(func() { close(waiting) }); savedSleep(d) }
	t.Cleanup(func() { configLockSleep = savedSleep })

	done := make(chan PreTrustOutcome, 1)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); done <- PreTrust(cwd, env, false) }()
	t.Cleanup(wg.Wait)
	select {
	case got := <-done:
		t.Fatalf("PreTrust = %q (warning %q) while the lock was held; want it to wait for the holder", got, warn)
	case <-waiting:
	}

	// The writer saves its own update to what it read, then releases the lock.
	cfg["numStartups"] = 3.0
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	seedFile(t, path, string(raw))
	if err := os.Remove(path + ".lock"); err != nil {
		t.Fatalf("release lock: %v", err)
	}

	if got := <-done; got != PreTrustOK {
		t.Fatalf("PreTrust = %q (warning %q); want ok once the holder released the lock", got, warn)
	}
	got := readClaudeJSON(t, path)
	if !trusts(got, cwd) || got["numStartups"] != 3.0 || got["userID"] != "u" {
		t.Errorf("claude.json = %v; want projects[%q] trusted and the holder's numStartups 3 kept", got, cwd)
	}
	assertNoStray(t, filepath.Dir(path))
}

// TestPreTrustHeldLockFailsAfterBoundedWait pins b.zjm: a fresh lock held
// throughout makes pre-trust wait 5 s, then report failed, touching nothing.
func TestPreTrustHeldLockFailsAfterBoundedWait(t *testing.T) {
	clock := withFakeLockClock(t, 0)
	env, path := seedConfigDir(t)
	held := holdLock(t, path, clock.now)
	warn := capturePreTrustWarn(t)

	if got := PreTrust("/tmp/held-cwd", env, false); got != PreTrustFailed {
		t.Fatalf("PreTrust = %q; want failed", got)
	}
	if clock.slept != 5*time.Second {
		t.Errorf("waited %s for the held lock; want 5s", clock.slept)
	}
	assertOneFailedLine(t, warn.String(), path+".lock", "held by another process")
	assertSeedKept(t, path)
	assertLockKept(t, path, held)
}

// TestPreTrustLockStaleness pins proper-lockfile's stale rule: a lock dir last
// refreshed over 10 s ago is broken and taken; a younger one is waited on.
func TestPreTrustLockStaleness(t *testing.T) {
	const cwd = "/tmp/stale-cwd"
	cases := []struct {
		name     string
		age      time.Duration // the lock dir's mtime age when pre-trust starts
		wait     time.Duration // configLockWait
		minSlept time.Duration // fake wait before the lock was taken
	}{
		{name: "11s old lock is broken at once", age: 11 * time.Second},
		{name: "9s old lock is broken once over 10s", age: 9 * time.Second, wait: 5 * time.Second, minSlept: time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clock := withFakeLockClock(t, 0)
			saved := configLockWait
			configLockWait = tc.wait
			t.Cleanup(func() { configLockWait = saved })
			env, path := seedConfigDir(t)
			holdLock(t, path, clock.now.Add(-tc.age))
			capturePreTrustWarn(t)

			if got := PreTrust(cwd, env, false); got != PreTrustOK {
				t.Fatalf("PreTrust = %q; want ok", got)
			}
			if clock.slept < tc.minSlept {
				t.Errorf("took the lock after waiting %s; want at least %s (not before it was over 10s old)", clock.slept, tc.minSlept)
			}
			if got := readClaudeJSON(t, path); !trusts(got, cwd) || got["userID"] != "u" {
				t.Errorf("claude.json = %v; want projects[%q] trusted and userID kept", got, cwd)
			}
			assertNoStray(t, filepath.Dir(path))
		})
	}
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

			if got := PreTrust(cwd, env, false); got != PreTrustFailed {
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

// TestConfigLockReplacedLock: once another process breaks this lock as stale and
// takes its own, checkHold reports the takeover and unlock leaves theirs alone.
func TestConfigLockReplacedLock(t *testing.T) {
	_, path := seedConfigDir(t)
	l, err := lockConfig(path)
	if err != nil {
		t.Fatalf("lockConfig: %v", err)
	}
	if err := l.checkHold(); err != nil {
		t.Fatalf("checkHold on the untouched lock = %v; want nil", err)
	}
	if err := os.Remove(path + ".lock"); err != nil {
		t.Fatalf("break lock: %v", err)
	}
	// The other taker's mkdir, then proper-lockfile's mtime probe (next whole second + 5 ms).
	theirs := holdLock(t, path, time.Now().Truncate(time.Second).Add(time.Second+5*time.Millisecond))

	if err := l.checkHold(); !errors.Is(err, errConfigLockTakenOver) {
		t.Errorf("checkHold after the takeover = %v; want errConfigLockTakenOver", err)
	}
	l.unlock()
	assertLockKept(t, path, theirs)
}
