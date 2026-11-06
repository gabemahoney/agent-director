// Command standin is a stand-in for an agent-director or agent-director-admin
// release, built by test/install-sh's move fixture. Its version result comes
// from the build's -X flags, so one source gives an "earlier release" whose
// version result lacks config_dir or names another directory, and the binary
// a test runs as an old install's agent-director: `standin serve` prints
// "ready" and then runs until its stdin closes, as a Claude Code session's MCP
// server runs until the session ends. Any other verb exits 0, printing nothing.
// Cutover-outcome cases opt into exec delegation so no stand-in process is
// left for the real administrator's live-mutator verification to refuse.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"
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
	// Scheduling cases inspect every invocation, including accidental recovery
	// probes. All other move-fixture consumers retain their original behavior.
	if path := os.Getenv("SCHED_TEST_LOG"); path != "" {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			panic(err)
		}
		fmt.Fprintln(f, os.Args[1])
		f.Close()
	}
	if os.Args[1] == "help" {
		switch os.Getenv("SCHED_TEST_HELP") {
		case "new":
			fmt.Println(`{"verbs":[{"name":"sweep"},{"name":"find-missing"}]}`)
		case "legacy":
			fmt.Println(`{"verbs":[{"name":"find-missing"}]}`)
		case "malformed":
			fmt.Println(`{"verbs":[{"name":"sweep"}]`)
		case "failed":
			fmt.Println(`{"verbs":[{"name":"sweep"}]}`)
			os.Exit(1)
		}
		return
	}
	if delegate := os.Getenv("SCHED_TEST_DELEGATE"); delegate != "" {
		if os.Getenv("SCHED_TEST_DELEGATE_EXEC") == "1" {
			if err := syscall.Exec(delegate, append([]string{delegate}, os.Args[1:]...), os.Environ()); err != nil {
				panic(err)
			}
		}
		cmd := exec.Command(delegate, os.Args[1:]...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			if e, ok := err.(*exec.ExitError); ok {
				os.Exit(e.ExitCode())
			}
			panic(err)
		}
		return
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
