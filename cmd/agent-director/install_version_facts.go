package main

import (
	"encoding/json"
	"os"

	"github.com/gabemahoney/agent-director/internal/installfacts"
	"github.com/gabemahoney/agent-director/internal/installversion"
)

// installVersionFacts runs before any normal initialization, including hook
// stdin inspection. This private handshake is only for the same build's admin
// querying its unpublished staged runtime; it is not in the public manifest.
func installVersionFacts(args []string) (handled bool, code int) {
	if len(args) == 0 || args[0] != installversion.RuntimeFactsArgument {
		return false, 0
	}
	if len(args) != 1 {
		return true, 1
	}
	if err := json.NewEncoder(os.Stdout).Encode(installfacts.Compiled()); err != nil {
		return true, 1
	}
	return true, 0
}
