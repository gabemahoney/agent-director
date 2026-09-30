package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gabemahoney/agent-director/internal/testsupport/faketmuxfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// ---- seed-row-session --------------------------------------------------------

// cmdSeedRowSession writes the row's own labelled session into test/fake-tmux's
// table for the row's recorded socket (SR-3.4, SR-3.7, SR-20.3), so a CLI
// read-pane or send-keys on the row finds it Ours and reaches its pane by id.
//
// The row's launch token, socket, session name and pane come from the store
// through apitest.ReadSpawnColumns, and the store id through
// apitest.ReadStoreID; the label and pane label values come from the fake's
// shared builders (tmuxfix.LabelValue, tmuxfix.PaneLabelValue) and the stored
// name from faketmuxfix.StoredForm, so no label text is spelled here. The
// session is added to the socket's table under the table lock
// (faketmuxfix.UpdateTable), keeping its other sessions, server and
// injections; the session id is the next free "$N". The pane is the row's
// recorded one, or apitest.TestPaneID with pid apitest.TestPanePID when the
// row records none (a finished row: the verb finds it by its @ad_pane).
// When the table has no server, the row's recorded server identity is used,
// else this process's pid and the current time (a row with no recorded
// server matches any server). It fails, writing nothing, when the row records
// no launch token or socket, or its pane is already in the table.
func cmdSeedRowSession(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("seed-row-session", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var (
		storePath = fs.String("store", "", "path to SQLite store file (required)")
		id        = fs.String("id", "", "claude_instance_id of an existing spawn (required)")
		capture   = fs.String("capture", "", "text capture-pane prints for the row's pane; empty uses the fake's default")
		tablesDir = fs.String("tables-dir", os.Getenv(faketmuxfix.EnvTables),
			"directory of the fake's tables (FAKE_TMUX_TABLES); defaults to $"+faketmuxfix.EnvTables+", else the table beside the socket")
	)
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

	res, err := seedRowSession(*storePath, *id, *capture, *tablesDir)
	if err != nil {
		printError(stderr, err)
		return 1
	}
	if err := printResult(stdout, res); err != nil {
		printError(stderr, err)
		return 1
	}
	return 0
}

// seedRowSession reads the row and writes its session; it returns the
// subcommand's result fields.
func seedRowSession(storePath, id, capture, tablesDir string) (map[string]string, error) {
	cols, err := apitest.ReadSpawnColumns(storePath, id)
	if err != nil {
		return nil, err
	}
	storeID, err := apitest.ReadStoreID(storePath)
	if err != nil {
		return nil, err
	}
	token, _ := cols.LaunchToken.(string)
	if token == "" {
		return nil, fmt.Errorf("row %s records no launch token", id)
	}
	socket, _ := cols.TmuxSocket.(string)
	if socket == "" {
		return nil, fmt.Errorf("row %s records no tmux socket", id)
	}
	name, _ := cols.TmuxSessionName.(string)
	pane := faketmuxfix.Pane{ID: apitest.TestPaneID, PID: apitest.TestPanePID, Capture: capture}
	if paneID, _ := cols.PaneID.(string); paneID != "" {
		pid, _ := cols.PanePID.(int64)
		pane.ID, pane.PID = paneID, int(pid)
	}
	pane.AdPane = tmuxfix.PaneLabelValue(token, pane.ID)

	path := faketmuxfix.TablePath(socket, tablesDir)
	var sessionID string
	err = faketmuxfix.UpdateTable(path, true, func(tb *faketmuxfix.Table, _ bool) (bool, error) {
		if owner := sessionWithPane(tb, pane.ID); owner != "" {
			return false, fmt.Errorf("pane %s of row %s is already in session %s on %s", pane.ID, id, owner, socket)
		}
		now := time.Now().Unix()
		if tb.Server == nil {
			tb.Server = rowServer(cols, now)
		}
		sessionID = nextSessionID(tb)
		tb.Socket = socket
		tb.Sessions = append(tb.Sessions, faketmuxfix.Session{
			ID: sessionID, Created: now, Name: faketmuxfix.StoredForm(name),
			Label: tmuxfix.LabelValue(token, sessionID, id, storeID),
			Panes: []faketmuxfix.Pane{pane},
		})
		return true, nil
	})
	if err != nil {
		return nil, fmt.Errorf("fake-tmux table %s: %w", path, err)
	}
	return map[string]string{"socket": socket, "session_id": sessionID, "pane_id": pane.ID, "table": path}, nil
}

// rowServer is the server a new table gets: the row's recorded identity, or
// this process's pid started at now when the row records none.
func rowServer(cols apitest.SpawnColumns, now int64) *faketmuxfix.Server {
	pid, _ := cols.TmuxServerPID.(int64)
	start, _ := cols.TmuxServerStarted.(int64)
	if pid == 0 {
		return &faketmuxfix.Server{PID: os.Getpid(), Start: now}
	}
	return &faketmuxfix.Server{PID: int(pid), Start: start}
}

// sessionWithPane returns the id of the table's session holding paneID, or
// "" when none does.
func sessionWithPane(tb *faketmuxfix.Table, paneID string) string {
	for _, s := range tb.Sessions {
		for _, p := range s.Panes {
			if p.ID == paneID {
				return s.ID
			}
		}
	}
	return ""
}

// nextSessionID returns "$N" for the table's next session number: its
// counter, raised past every session id already in the table, as the fake's
// create numbers them.
func nextSessionID(tb *faketmuxfix.Table) string {
	n := tb.NextSession
	for _, s := range tb.Sessions {
		if v, err := strconv.Atoi(strings.TrimPrefix(s.ID, "$")); err == nil && v >= n {
			n = v + 1
		}
	}
	return "$" + strconv.Itoa(n)
}
