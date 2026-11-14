package main

import (
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// ---- seed-busy-send-claude-route ----------------------------------------------

// cmdSeedBusySendClaudeRoute records an existing Claude row's current Launch
// and published conversation covered (apitest.SeedBusySendCoverage) with
// route A at the Launch's own wake endpoint (apitest.SeedBusySendRoute at
// store.BusySendWakePath of the store and the row's launch token), as a
// covered Claude Launch records them once Claude joins the covered set (Task
// t2.xis.oo.2o). It creates no file: the Launch's own SessionStart hook does.
// It fails, writing nothing, when the row records no launch token.
func cmdSeedBusySendClaudeRoute(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("seed-busy-send-claude-route", flag.ContinueOnError)
	fs.SetOutput(stderr)
	storePath := fs.String("store", "", "path to SQLite store file (required)")
	id := fs.String("id", "", "instance_id of an existing Claude spawn (required)")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	var missing []string
	if *storePath == "" {
		missing = append(missing, "--store")
	}
	if *id == "" {
		missing = append(missing, "--id")
	}
	if len(missing) > 0 {
		printError(stderr, fmt.Errorf("%s: required", strings.Join(missing, ", ")))
		return 1
	}
	wake, err := seedBusySendClaudeRoute(*storePath, *id)
	if err != nil {
		printError(stderr, err)
		return 1
	}
	if err := printResult(stdout, map[string]string{"wake_path": wake}); err != nil {
		printError(stderr, err)
		return 1
	}
	return 0
}

// seedBusySendClaudeRoute records the coverage and route; it returns the wake
// endpoint's path.
func seedBusySendClaudeRoute(storePath, id string) (string, error) {
	cols, err := apitest.ReadSpawnColumns(storePath, id)
	if err != nil {
		return "", err
	}
	storeID, err := apitest.ReadStoreID(storePath)
	if err != nil {
		return "", err
	}
	token, _ := cols.LaunchToken.(string)
	if token == "" {
		return "", fmt.Errorf("row %s records no launch token", id)
	}
	wake, err := store.BusySendWakePath(storeID, token)
	if err != nil {
		return "", err
	}
	if err := apitest.SeedBusySendCoverage(storePath, id, store.BusySendCovered, store.BusySendCovered); err != nil {
		return "", err
	}
	if err := apitest.SeedBusySendRoute(storePath, id, string(store.BusySendRouteClaudeHooks), wake); err != nil {
		return "", err
	}
	return wake, nil
}
