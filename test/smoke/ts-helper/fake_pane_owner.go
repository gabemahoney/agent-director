package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/gabemahoney/agent-director/internal/probe"
	"github.com/gabemahoney/agent-director/internal/testsupport/faketmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// cmdFakePaneOwner binds fake creates to the test's independently live owner.
func cmdFakePaneOwner(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("fake-pane-owner", flag.ContinueOnError)
	flags.SetOutput(stderr)
	pid := flags.Int("pid", 0, "controlled long-lived owner PID")
	storePath := flags.String("store", "", "optional store for recorded identity verification")
	id := flags.String("id", "", "spawn whose recorded identity must match")
	if err := flags.Parse(args); err != nil {
		return 1
	}
	start, alive, known := probe.NewProcChecker().StartTime(*pid)
	if !known || !alive || start == "" {
		printError(stderr, fmt.Errorf("owner %d not independently alive: known=%v alive=%v start=%q", *pid, known, alive, start))
		return 1
	}
	if *storePath != "" || *id != "" {
		row, err := apitest.ReadSpawnColumns(*storePath, *id)
		if err != nil {
			printError(stderr, err)
			return 1
		}
		if row.PanePID != int64(*pid) || row.PaneStarttime != start {
			printError(stderr, fmt.Errorf("recorded pane identity=%v/%v, owner=%d/%s", row.PanePID, row.PaneStarttime, *pid, start))
			return 1
		}
	} else {
		socket, err := tmux.ResolveSocket(true)
		if err != nil {
			printError(stderr, err)
			return 1
		}
		path := faketmuxfix.TablePath(socket, os.Getenv(faketmuxfix.EnvTables))
		if err := faketmuxfix.UpdateTable(path, true, func(tb *faketmuxfix.Table, _ bool) (bool, error) {
			tb.Socket, tb.NewPanePID = socket, *pid
			return true, nil
		}); err != nil {
			printError(stderr, err)
			return 1
		}
	}
	if err := printResult(stdout, map[string]any{"pid": *pid, "start": start}); err != nil {
		printError(stderr, err)
		return 1
	}
	return 0
}
