package main

import (
	"context"
	"flag"

	"github.com/gabemahoney/agent-director/internal/adminapi"
	"github.com/gabemahoney/agent-director/internal/clisetup"
	"github.com/gabemahoney/agent-director/internal/store"
)

// snapshotStoreHandler deliberately creates no Client and loads no config.
// Opening the source through SQLite would defeat the installer's untouched
// preflight guarantee even with a readonly SQLite connection.
func snapshotStoreHandler(o clisetup.Overrides, args []string) error {
	var directory string
	var busyTimeoutMs int
	fs := flag.NewFlagSet("snapshot-store", flag.ContinueOnError)
	fs.StringVar(&directory, "directory", "", "existing empty private directory for the readonly snapshot")
	fs.IntVar(&busyTimeoutMs, "busy-timeout-ms", store.DefaultBusyTimeoutMs, "maximum snapshot/lock wait in milliseconds")
	if done, err := parseFlags(fs, args); done {
		return err
	}
	if o.StorePath == "" || directory == "" {
		return writeError(errInvalidFlags, "snapshot-store requires explicit --store-path and --directory")
	}
	path, err := adminapi.SnapshotStore(context.Background(), o.StorePath, directory, busyTimeoutMs)
	if err != nil {
		return writeError("ErrStoreOpen", err.Error())
	}
	return writeJSON(struct {
		Path string `json:"path"`
	}{path})
}
