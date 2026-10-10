package main

import (
	"errors"
	"flag"
	"io"

	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// ---- seed-read-surface-store -------------------------------------------------

// cmdSeedReadSurfaceStore creates the store at --store holding the fixed rows
// the settle-off goldens pin status, get and list on
// (apitest.SeedReadSurfaceStore, b.3ie), and prints their ids in seeding order.
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
	if err := printResult(stdout, map[string]any{"ids": ids}); err != nil {
		printError(stderr, err)
		return 1
	}
	return 0
}
