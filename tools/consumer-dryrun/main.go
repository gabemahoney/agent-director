// consumer-dryrun is an external-consumer compilation check for pkg/api.
//
// It imports github.com/gabemahoney/agent-director/pkg/api via a go.mod
// replace directive that points at the working tree, exercising every public
// Client method and standalone function. This file intentionally references
// no internal/* packages — Go's visibility rules prevent external modules
// from importing internal/* anyway, so any attempt would be a compile error.
//
// Usage:
//
//	make consumer-dryrun
//	# or manually:
//	cd tools/consumer-dryrun && go build ./...
package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/gabemahoney/agent-director/pkg/api"
)

func main() {
	// Create a temporary store path for the dryrun.
	tmp, err := os.MkdirTemp("", "consumer-dryrun-*")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(tmp)

	c, err := api.New(api.Options{
		StorePath:       filepath.Join(tmp, "state.db"),
		CreateIfMissing: true,
	})
	if err != nil {
		panic(err)
	}
	defer c.Close()

	// Spawn — compile check only; will fail at runtime without tmux, which
	// is expected. The important property is that api.SpawnParams is a pkg/api
	// type and the call compiles without importing internal/*.
	_, _ = c.Spawn(api.SpawnParams{CWD: "/tmp"})

	// Status — will return ErrSpawnNotFound for unknown id; that's fine.
	_, _ = c.Status("absent-id")

	// List — exercises ListParams (pure pkg/api type).
	_, _ = c.List(api.ListParams{})

	// Get — will return ErrSpawnNotFound.
	_, _ = c.Get("absent-id")

	// Kill — will return ErrSpawnNotFound.
	_, _ = c.Kill(api.KillParams{ClaudeInstanceID: "absent-id"})

	// SendKeys — will return ErrSpawnNotFound.
	_, _ = c.SendKeys(api.SendKeysParams{ClaudeInstanceID: "absent-id"})

	// ReadPane — will return ErrSpawnNotFound.
	_, _ = c.ReadPane(api.ReadPaneParams{ClaudeInstanceID: "absent-id"})

	// Pause — will return ErrSpawnNotFound.
	_, _ = c.Pause(context.Background(), api.PauseParams{ClaudeInstanceID: "absent-id"})

	// Resume — will return ErrSpawnNotFound.
	_, _ = c.Resume(api.ResumeParams{ClaudeInstanceID: "absent-id"})

	// Decide — will return ErrInvalidDecision (decision="" is invalid).
	_, _ = c.Decide(api.DecideParams{ClaudeInstanceID: "absent-id"})

	// Delete — empty slice is a no-op.
	_, _ = c.Delete([]string{})

	// Expire — zero retentionDays overrides via nil olderThan means use default.
	_, _ = c.Expire(nil)

	// FindMissing — will succeed (empty store).
	_, _ = c.FindMissing(context.Background())

	// MakeTemplate — standalone (no Client needed).
	_, _ = api.MakeTemplate(api.MakeTemplateParams{Name: "dryrun-test"})

	// Help — standalone.
	_, _ = api.Help()

	// Version — standalone.
	_, _ = api.Version()

	// Verify exported types from aliases.go are reachable without internal imports.
	var _ api.Spawn         // = store.Spawn alias
	var _ api.PermissionRow // = store.PermissionRow alias
	var _ api.ListFilters   // = store.ListFilters alias

	// Verify re-exported error sentinels are usable for errors.Is without
	// importing internal/store or internal/tmux. These compile-checks confirm
	// that external callers can write errors.Is(err, api.ErrSpawnNotFound)
	// rather than needing internal/* package access.
	_ = errors.Is(nil, api.ErrSpawnNotFound)
	_ = errors.Is(nil, api.ErrNoOpenPermissionRequest)
	_ = errors.Is(nil, api.ErrAlreadyDecided)
	_ = errors.Is(nil, api.ErrSchemaMismatch)
	_ = errors.Is(nil, api.ErrStoreNotInitialized)
	_ = errors.Is(nil, api.ErrTmuxSessionCreate)
}
