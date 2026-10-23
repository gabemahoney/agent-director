package main

import (
	"errors"
	"flag"
	"io"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// cmdWriteFamilyTransition is a real hook writer process for restart continuity.
func cmdWriteFamilyTransition(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("write-family-transition", flag.ContinueOnError)
	fs.SetOutput(stderr)
	path := fs.String("store", "", "store path")
	id := fs.String("id", "", "existing instance")
	state := fs.String("state", "working", "new raw state")
	ms := fs.Int64("settle-ms", 0, "writer's existing settle interval")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	s, err := store.Open(*path)
	if err != nil {
		printError(stderr, err)
		return 1
	}
	defer s.Close()
	s.SetSettleMs(*ms)
	err = storefix.WithSeedPane(*path, *id, func(gate store.HookGate) error {
		_, applied, err := s.ApplyHookTransitionResult(*id, gate, *state, false, "family_restart_test", "", false)
		if err == nil && !applied.Applied {
			return errors.New("family transition was not applied")
		}
		return err
	})
	if err != nil {
		printError(stderr, err)
		return 1
	}
	if err := printResult(stdout, map[string]string{"instance_id": *id}); err != nil {
		printError(stderr, err)
		return 1
	}
	return 0
}

// ---- seed-read-surface-store -------------------------------------------------

// cmdSeedReadSurfaceStore creates the store at --store holding the fixed rows
// the settle-off goldens pin status, get and list on
// (apitest.SeedReadSurfaceStore, b.3ie), and prints their ids in seeding order
// and, for each raw state, the settled state its rows were scrambled to
// (apitest.ReadSurfaceSettled), which a read with a settle time reports.
func cmdSeedReadSurfaceStore(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("seed-read-surface-store", flag.ContinueOnError)
	fs.SetOutput(stderr)
	storePath := fs.String("store", "", "path for the new SQLite store file (required)")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *storePath == "" {
		printError(stderr, errors.New("--store is required"))
		return 1
	}
	ids, err := apitest.SeedReadSurfaceStore(*storePath)
	if err != nil {
		printError(stderr, err)
		return 1
	}
	settled := map[string]string{}
	for _, s := range []string{store.StatePending, store.StateWaiting, store.StateWorking, store.StateAskUser,
		store.StateCheckPermission, store.StateEnded, store.StateMissing} {
		settled[s] = apitest.ReadSurfaceSettled(s)
	}
	if err := printResult(stdout, map[string]any{"ids": ids, "settled": settled}); err != nil {
		printError(stderr, err)
		return 1
	}
	return 0
}

// ---- set-settle --------------------------------------------------------------

// cmdSetSettle writes an existing row's settle columns (apitest.SetSettle,
// b.3ie): --settled as its settled_state and --stamp-ms as its
// state_changed_ms, every other column kept.
func cmdSetSettle(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("set-settle", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		storePath = fs.String("store", "", "path to SQLite store file (required)")
		id        = fs.String("id", "", "instance_id of an existing spawn (required)")
		settled   = fs.String("settled", "", "the row's settled_state, the state a read with a settle time reports until its raw state has held long enough (required)")
		stampMs   = fs.Int64("stamp-ms", 0, "the row's state_changed_ms: when its raw state last changed, in Unix milliseconds")
	)
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *storePath == "" || *id == "" || *settled == "" {
		printError(stderr, errors.New("--store, --id and --settled are required"))
		return 1
	}
	if err := apitest.SetSettle(*storePath, *id, *settled, *stampMs); err != nil {
		printError(stderr, err)
		return 1
	}
	if err := printResult(stdout, map[string]string{"instance_id": *id}); err != nil {
		printError(stderr, err)
		return 1
	}
	return 0
}

// cmdSeedSurface seeds the shared precommitted display wire surface seed adds
// (apitest.SeedNeedsIntelligenceSurface, apitest.SeedIdleScreenSurface) into
// --store, for the subcommand name.
func cmdSeedSurface(name string, seed func(string) error, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	path := fs.String("store", "", "store path")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *path == "" {
		printError(stderr, errors.New("--store is required"))
		return 1
	}
	if err := seed(*path); err != nil {
		printError(stderr, err)
		return 1
	}
	if err := printResult(stdout, map[string]any{"seeded": true}); err != nil {
		printError(stderr, err)
		return 1
	}
	return 0
}
