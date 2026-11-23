//go:build linux || darwin

package inputdelivery

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// b.vxo: once a writer announces intent, repeated observers cannot touch the
// input lock or delay the writer beyond the one proof pass already admitted.
func TestVXOInputWriterPriorityOverRepeatedReaders(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "launch")
	reader := Native{Guard: Guard{ProofOnly: true}}
	first, gate, err := reader.openLocks(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if gate != nil {
		defer gate.Close()
		t.Fatal("admitted proof reader retained admission gate")
	}
	readers := []Native{reader, reader, reader}
	deferred := func() {
		t.Helper()
		for round := 0; round < 4; round++ {
			for i, observer := range readers {
				lock, intent, err := observer.openLocks(ctx, dir)
				if lock != nil {
					_ = lock.Close()
				}
				if intent != nil {
					_ = intent.Close()
				}
				if !errors.Is(err, ErrBusy) {
					t.Fatalf("reader %d round %d reached the poisoned input lock: %v; want ErrBusy", i, round, err)
				}
			}
		}
	}
	now := time.Unix(0, 0)
	waits := 0
	writer := Native{Locks: &LockPolicy{Now: func() time.Time { return now },
		Wait: func(context.Context, time.Duration) error {
			waits++
			if waits != 1 {
				t.Fatal("writer did not acquire input lock on first retry after reader release")
			}
			// A directory at the lock pathname detects any reader that reaches
			// it. The waiting writer already opened the original locked inode.
			if err := os.Rename(dir+".lock", dir+".held"); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(dir+".lock", 0700); err != nil {
				t.Fatal(err)
			}
			deferred()
			now = now.Add(time.Millisecond)
			return first.Close()
		}}}
	lock, intent, err := writer.openLocks(ctx, dir)
	if err != nil {
		t.Fatalf("writer failed after in-progress reader released: %v", err)
	}
	defer lock.Close()
	if intent == nil {
		t.Fatal("writer lost intent after admission")
	}
	defer intent.Close()
	if waits != 1 {
		t.Fatalf("writer waits = %d; want one in-progress reader drain", waits)
	}
	deferred()
}
