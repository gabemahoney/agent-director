package main

// migrate_journal_child_test.go runs the real administrator (run, dispatch,
// migrateJournalHandler, with its signal handling, adminapi and store) in a
// child of the test binary, with the fault seams a TestMigrateJournalCommitState
// row names armed first. A built binary cannot arm them.

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/gabemahoney/agent-director/internal/adminapi"
	"github.com/gabemahoney/agent-director/internal/store"
)

// TestMigrateJournalAdminChild does nothing unless AD_ADMIN_CHILD names the
// seams to arm; then it exits with run's code on the JSON argv in
// AD_ADMIN_CHILD_ARGS, or argv after -- when that variable is absent (the
// installer outcome fixture). Non-handshake rows need no fd 3 or fd 4.
// signal-after-fifo uses AD_TEST_READY/AD_TEST_GO paths instead, for the
// installer's command substitution, which preserves no extra descriptors.
// The verifier passes except at the call a
// verify-fails-<n> seam names. Fd 3 writes "ready" to the parent; fd 4
// reads its "go". It is the one child runner of the real administrator: a
// test that needs another seam adds an AD_ADMIN_CHILD value here, never a
// second runner.
func TestMigrateJournalAdminChild(t *testing.T) {
	seams := os.Getenv("AD_ADMIN_CHILD")
	if seams == "" {
		return
	}
	var args []string
	if encoded, ok := os.LookupEnv("AD_ADMIN_CHILD_ARGS"); ok {
		if err := json.Unmarshal([]byte(encoded), &args); err != nil {
			os.Exit(98)
		}
	} else {
		args = flag.Args()
	}
	var toParent, fromParent *os.File
	if seams == "signal-after-fifo" {
		var err error
		toParent, err = os.OpenFile(os.Getenv("AD_TEST_READY"), os.O_WRONLY, 0)
		if err != nil {
			os.Exit(96)
		}
		fromParent, err = os.Open(os.Getenv("AD_TEST_GO"))
		if err != nil {
			os.Exit(96)
		}
	} else {
		toParent, fromParent = os.NewFile(3, "to-parent"), os.NewFile(4, "from-parent")
	}
	ready := func() {
		if _, err := toParent.WriteString("ready\n"); err != nil {
			os.Exit(96)
		}
	}
	awaitGo := func() {
		if _, err := io.ReadFull(fromParent, make([]byte, 3)); err != nil {
			os.Exit(96)
		}
	}
	calls := 0
	var work context.Context
	adminapi.SetJournalVerifierForTest(func(ctx context.Context, _ string, _ []string) error {
		calls++
		work = ctx // the attempt's work context
		if seams == "verify-fails-"+strconv.Itoa(calls) {
			return &adminapi.JournalRefusal{Reason: "old_or_unverified_mutators", Processes: []adminapi.JournalProcess{}, Action: "stop them"}
		}
		return nil
	})
	var faults store.InputMigrationFaults
	switch {
	case seams == "step-unconfirmed":
		faults.AfterStepCommit = func(from int) error {
			if from == 14 {
				return errors.New("step commit result lost")
			}
			return nil
		}
	case seams == "exit-after-step":
		// A run killed before its envelope, just after an earlier step committed.
		faults.AfterStepCommit = func(int) error { os.Exit(2); return nil }
	case seams == "exit-after-cutover":
		// A run killed before its envelope, just after the cutover committed.
		faults.CutoverCommit = func(commit func() error) error {
			if err := commit(); err != nil {
				return err
			}
			os.Exit(2)
			return nil
		}
	case strings.HasPrefix(seams, "cutover-unconfirmed"):
		faults.CutoverCommit = func(commit func() error) error {
			if seams == "cutover-unconfirmed-committed" {
				if err := commit(); err != nil {
					return err
				}
			}
			return errors.New("commit result lost")
		}
	case seams == "signal-before":
		faults.BeforeCutoverCommit = func() {
			ready()
			<-work.Done() // ended by the handler's cancellation alone
		}
	case seams == "signal-after", seams == "signal-after-fifo":
		faults.AfterCutoverCommitBegan = func() { ready(); awaitGo() }
	case seams == "ignored":
		faults.BeforeCutoverCommit = func() {
			ready()
			awaitGo()
			if !signal.Ignored(syscall.SIGINT) || !signal.Ignored(syscall.SIGHUP) {
				os.Exit(97) // catching them undid the ignore
			}
		}
	}
	store.SetInputMigrationFaultsForTest(faults)
	os.Exit(run(args))
}
