package main

import (
	"fmt"
	"os"

	"github.com/gabemahoney/agent-director/internal/trail"
)

// trailPathHandler implements `agent-director trail-path`. It resolves the
// trail file path via trail.Path() and prints it to stdout, one line, no
// JSON envelope.
//
// Deliberate exception to the manifest-driven JSON-envelope pattern: the
// output is a raw path string followed by \n so callers can use shell
// ergonomics like `cd $(agent-director trail-path | xargs dirname)` without
// piping through jq. This is documented in the verb's manifest entry.
//
// Called from run() before setupClient so the verb works even when
// state.db is missing or corrupted — trail-path is a recovery aid
// (SR-A-6, t3.4uk.ou.zv.9y).
func trailPathHandler(_ []string) error {
	if _, err := fmt.Fprintln(os.Stdout, trail.Path()); err != nil {
		return err
	}
	return nil
}
