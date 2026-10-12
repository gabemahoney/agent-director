// Command standin is a stand-in for an agent-director or agent-director-admin
// release, built by test/install-sh's move fixture. Its version result comes
// from the build's -X flags, so one source gives an "earlier release" whose
// version result lacks config_dir or names another directory, and the binary
// a test runs as an old install's agent-director: `standin serve` prints
// "ready" and then runs until its stdin closes, as a Claude Code session's MCP
// server runs until the session ends. Any other verb exits 0, printing nothing.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
)

// Set with -ldflags "-X main.<name>=<value>"; configDir is left out of the
// version result when empty.
var (
	version   = "0.0.0-standin"
	commit    = ""
	configDir = ""
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "standin: no verb")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "version", "--version", "-v":
		out := struct {
			Version   string `json:"version"`
			Commit    string `json:"commit"`
			ConfigDir string `json:"config_dir,omitempty"`
		}{version, commit, configDir}
		if err := json.NewEncoder(os.Stdout).Encode(out); err != nil {
			os.Exit(1)
		}
	case "serve":
		fmt.Println("ready")
		_, _ = io.Copy(io.Discard, os.Stdin)
	}
}
