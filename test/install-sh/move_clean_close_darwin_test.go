package installsh_test

// SR-9.1's b.zsj case on the Mac (darwin only): the move of an old store in
// WAL mode that its last connection closed cleanly, so it has no -wal and no
// -shm, which Apple's sqlite3 cannot open read-only.

import (
	"path/filepath"
	"testing"
)

// TestMoveAfterCleanClose (b.zsj): install.sh moves an old install whose WAL
// store was closed cleanly. Nothing opens the old store with SQLite before
// install.sh does, because that would leave it b.up4's -wal and -shm.
func TestMoveAfterCleanClose(t *testing.T) {
	h := newMoveHome(t)
	moveRichOld(t, h, withMoveClosedStore())
	before := moveMacSnapshotCopy(t, h)
	c := newMoveMacCase(t, h, "upgrade after a clean close (b.zsj)")
	moveMacCheckClosedStore(t, filepath.Join(h.Old, "state.db"))
	res := moveMustExit0(t, c.Install())
	// No ended-3: that row lives only in a store with a -wal.
	moveCheckSuccessful(t, h, res, before, false, "ended-1", "ended-2", "missing-1")
}
