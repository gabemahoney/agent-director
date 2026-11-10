package adminapi

import (
	"context"

	"github.com/gabemahoney/agent-director/internal/store"
)

// SnapshotStore captures an untouched source for installer preflight. This
// operator-only entry runs in its own process without a Client or config load;
// the caller owns the private destination and removes it after reading.
// Untouched preflight must use this wrapper; see store.ReadSnapshot's constraints.
func SnapshotStore(ctx context.Context, source, directory string, busyTimeoutMs int) (string, error) {
	return store.ReadSnapshot(ctx, source, directory, busyTimeoutMs)
}
