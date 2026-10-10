package main

import (
	"errors"
	"flag"
	"io"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

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
		id        = fs.String("id", "", "claude_instance_id of an existing spawn (required)")
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
	if err := printResult(stdout, map[string]string{"claude_instance_id": *id}); err != nil {
		printError(stderr, err)
		return 1
	}
	return 0
}
