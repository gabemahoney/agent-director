package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
)

// identifiersFile is the file in the results directory that lists every
// identifier of this run, one per line, as "<kind> <value>". The busy-host
// guard (guard.sh verify --busy-host --ids-file) scans the host trail's
// appended bytes for each value: any hit means the run reached the host's
// real store. It is appended as identifiers become known, so an aborted run
// still leaves the ones it made.
const identifiersFile = "harness-ids.txt"

// Identifier kinds.
const (
	idRun      = "run_id"
	idInstance = "instance_id"
	idSocket   = "socket"
	idStore    = "store_id"
	idTmuxDir  = "tmux_tmpdir"
)

// idWriter appends identifiers, each value once.
type idWriter struct {
	mu   sync.Mutex
	w    io.Writer
	seen map[string]bool
}

// newIDWriter writes to w.
func newIDWriter(w io.Writer) *idWriter {
	return &idWriter{w: w, seen: map[string]bool{}}
}

// add appends one identifier. A value with whitespace or a line break is
// refused, since the guard reads one value per line.
func (iw *idWriter) add(kind, value string) error {
	if value == "" || strings.ContainsAny(value, " \t\r\n") {
		return fmt.Errorf("identifier %s %q: empty or contains whitespace", kind, value)
	}
	iw.mu.Lock()
	defer iw.mu.Unlock()
	if iw.seen[value] {
		return nil
	}
	iw.seen[value] = true
	_, err := fmt.Fprintf(iw.w, "%s %s\n", kind, value)
	return err
}

// openIdentifiers creates the identifiers file.
func openIdentifiers(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
}
