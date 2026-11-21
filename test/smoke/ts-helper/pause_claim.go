package main

import (
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// ---- seed-pause-surface ------------------------------------------------------

// cmdSeedPauseSurface seeds --id, a covered Claude row, in --store in the
// conditional pause shape --shape (apitest.SeedPauseSurface) and prints the
// row's id and the exact err_details pause with only_if_waiting returns on it.
func cmdSeedPauseSurface(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("seed-pause-surface", flag.ContinueOnError)
	fs.SetOutput(stderr)
	storePath := fs.String("store", "", "path to SQLite store file, created when missing (required)")
	id := fs.String("id", "", "instance_id of the new row (required)")
	shape := fs.String("shape", "", "an apitest.PauseSurfaces name (required)")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *storePath == "" || *id == "" || *shape == "" {
		printError(stderr, errors.New("--store, --id and --shape are required"))
		return 1
	}
	var details map[string]any
	for _, s := range apitest.PauseSurfaces {
		if s.Name == *shape {
			details = s.Details
		}
	}
	if details == nil {
		printError(stderr, fmt.Errorf("unknown --shape %q", *shape))
		return 1
	}
	if err := apitest.SeedPauseSurface(*storePath, *id, *shape); err != nil {
		printError(stderr, err)
		return 1
	}
	if err := printResult(stdout, map[string]any{"instance_id": *id, "err_details": details}); err != nil {
		printError(stderr, err)
		return 1
	}
	return 0
}
