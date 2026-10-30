package main

import (
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"path/filepath"

	"github.com/gabemahoney/agent-director/internal/testsupport/writefailfix"
)

// cmdWriteFailure bridges shell fixtures to the canonical trigger installer.
// mode=rw opens only an existing SQLite file, including legacy schemas, without
// Store.Open, migrations, schema seeding or a production fault-injection verb.
func cmdWriteFailure(command string, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(stderr)
	path := fs.String("store", "", "existing temporary SQLite store (required)")
	kindName := fs.String("kind", "", "input-attempt|input-text-intent|input-current|input-authority")
	id := fs.String("id", "", "instance id; input-authority uses native_input_authority")
	token := fs.String("handle", "", "opaque handle from install-write-failure")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	fail := func(err error) int { printError(stderr, err); return 1 }
	if *path == "" || fs.NArg() != 0 {
		return fail(errors.New("--store is required and positional arguments are not accepted"))
	}
	var kind writefailfix.Kind
	var handle writefailfix.Handle
	if command == "install-write-failure" {
		kinds := map[string]writefailfix.Kind{
			"input-attempt":     writefailfix.InputAttemptInsert,
			"input-text-intent": writefailfix.InputTextIntent,
			"input-current":     writefailfix.InputCurrent,
			"input-authority":   writefailfix.InputAuthority,
		}
		kind = kinds[*kindName]
		if kind == 0 || *id == "" || *token != "" {
			return fail(errors.New("install requires a supported --kind and --id, without --handle"))
		}
	} else {
		var err error
		handle, err = writefailfix.ParseHandle(*token)
		if err != nil || *kindName != "" || *id != "" {
			return fail(errors.New("remove requires a valid --handle, without --kind or --id"))
		}
	}
	abs, err := filepath.Abs(*path)
	if err != nil {
		return fail(err)
	}
	dsn := url.URL{Scheme: "file", Path: abs, RawQuery: "mode=rw&_pragma=busy_timeout(5000)"}
	db, err := sql.Open("sqlite", dsn.String())
	if err != nil {
		return fail(err)
	}
	defer db.Close()
	if command == "install-write-failure" {
		handle, err = writefailfix.Install(db, kind, *id)
	} else {
		err = handle.Remove(db)
	}
	if err != nil {
		return fail(fmt.Errorf("%s: %w", command, err))
	}
	if err := printResult(stdout, map[string]string{"handle": handle.String()}); err != nil {
		return fail(err)
	}
	return 0
}
