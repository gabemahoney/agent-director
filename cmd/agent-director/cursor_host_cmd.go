package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/cursorhost"
	"github.com/gabemahoney/agent-director/internal/spawn"
	"github.com/gabemahoney/agent-director/internal/store"
)

// hookCursorVerifier wires independent kernel and tmux evidence at the CLI
// boundary. Hook checks the immediate parent and registered connection before
// calling this verifier; neither check can substitute for the other.
func hookCursorVerifier(cfg config.Config) func(*store.Store, store.CursorRuntime, string, bool) error {
	return func(st *store.Store, b store.CursorRuntime, path string, ended bool) error {
		if err := cursorhost.VerifyRuntime(b, ended); err != nil {
			return err
		}
		if err := cursorhost.VerifyTranscriptPath(path); err != nil {
			return err
		}
		row, err := st.GetSpawn(b.InstanceID)
		if err != nil {
			return err
		}
		return cursorhost.VerifyPane(st, row, cfg)
	}
}

func hookCursorRotationVerifier(cfg config.Config) func(*store.Store, store.CursorRuntime, string, store.Spawn) error {
	return func(st *store.Store, b store.CursorRuntime, path string, row store.Spawn) error {
		if err := cursorhost.VerifyRuntime(b, false); err != nil {
			return err
		}
		if err := cursorhost.VerifyTranscriptPath(path); err != nil {
			return err
		}
		return cursorhost.VerifyPane(st, row, cfg)
	}
}

// cursor-host is internal and dispatched before public client setup. It opens
// an existing store only after parsing, and proves pane ownership before any
// runtime launch or ACP admission.
func cursorHostHandler(args []string) error {
	fs := flag.NewFlagSet("cursor-host", flag.ContinueOnError)
	var program, encoded, prompt string
	fs.StringVar(&program, "runtime", "", "selected Cursor executable (internal)")
	fs.StringVar(&encoded, "runtime-args", "[]", "validated Cursor model arguments (internal)")
	fs.StringVar(&prompt, "initial-prompt", "", "fresh initial prompt (internal)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if len(fs.Args()) != 0 || program == "" {
		return fmt.Errorf("invalid cursor-host launch")
	}
	var runtimeArgs []string
	if err := json.Unmarshal([]byte(encoded), &runtimeArgs); err != nil {
		return fmt.Errorf("invalid cursor-host arguments")
	}
	checked, initial, err := spawn.CursorArgs(runtimeArgs)
	if err != nil || initial != "" {
		return fmt.Errorf("invalid cursor-host runtime arguments")
	}
	if err = cursorhost.Run(cursorhost.Options{Runtime: program, RuntimeArgs: checked, InitialPrompt: prompt}, os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "Cursor gateway:", err)
		return errDispatch
	}
	return nil
}
