package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/gabemahoney/claude-director/internal/api"
	"github.com/gabemahoney/claude-director/internal/config"
	"github.com/gabemahoney/claude-director/internal/spawn"
	"github.com/gabemahoney/claude-director/internal/store"
	"github.com/gabemahoney/claude-director/internal/tmux"
)

// tmuxClient is the runtime tmux client wired into the verb handlers.
// Held as a *tmux.Client (the concrete type) rather than a narrowest-
// interface alias so every verb that needs a different subset of tmux ops
// (spawn → NewSession, send-keys → SendKeys, read-pane → CapturePane) can
// pull from one shared client. Cmd-level integration tests swap behavior
// by prepending a fake-tmux binary onto PATH; no field replacement is
// required.
var tmuxClient = tmux.New()

// spawnHandlerWith implements `claude-director spawn`. Called via a closure
// from handlers() so the store + config opened by setupStoreAndCfg are
// reused rather than reopened.
func spawnHandlerWith(st *store.Store, cfg config.Config, args []string) error {
	params, err := parseSpawnFlags(args)
	if err != nil {
		return writeApiErrorAndDispatch("ErrInvalidFlags", err.Error())
	}
	result, err := api.Spawn(st, tmuxClient, cfg, params)
	if err != nil {
		name, desc := classifyError(err)
		return writeApiErrorAndDispatch(name, errMessageStartsWithName(name, desc))
	}
	return writeJSON(os.Stdout, result)
}

// parseSpawnFlags carves the argv into a SpawnParams. Stdlib `flag`
// covers most of it; the `--` separator pulls the remainder into
// ClaudeArgs verbatim.
func parseSpawnFlags(args []string) (spawn.SpawnParams, error) {
	var p spawn.SpawnParams
	var (
		labelKVs         map[string]string
		extraEnvKVs      map[string]string
		allow, deny, ask []string
	)
	fs := flag.NewFlagSet("spawn", flag.ContinueOnError)
	fs.SetOutput(io.Discard) // we surface errors via the JSON envelope
	fs.StringVar(&p.CWD, "cwd", "", "absolute / ~-prefixed cwd for the Spawn")
	fs.StringVar(&p.Template, "template", "", "named template (Epic 7)")
	fs.StringVar(&p.ClaudeInstanceID, "claude-instance-id", "", "explicit instance id (default: minted UUID4)")
	fs.StringVar(&p.RelayMode, "relay-mode", "", "on / off (default: config defaults.relay_mode)")
	fs.Var(newKVSlice(&labelKVs, "--label"), "label", "k=v (repeatable)")
	fs.Var(newKVSlice(&extraEnvKVs, "--extra-env"), "extra-env", "K=V (repeatable)")
	fs.Var(newStringSlice(&allow), "allow", "permissions.allow entry (repeatable)")
	fs.Var(newStringSlice(&deny), "deny", "permissions.deny entry (repeatable)")
	fs.Var(newStringSlice(&ask), "ask", "permissions.ask entry (repeatable)")
	if err := fs.Parse(args); err != nil {
		return p, err
	}
	p.ClaudeDirectorLabels = labelKVs
	p.ExtraEnv = extraEnvKVs
	p.Permissions = buildPermissions(allow, deny, ask)
	p.ClaudeArgs = fs.Args() // everything after `--`
	return p, nil
}

// statusHandlerWith implements `claude-director status`.
func statusHandlerWith(st *store.Store, args []string) error {
	var id string
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&id, "claude-instance-id", "", "id to inspect")
	if err := fs.Parse(args); err != nil {
		return writeApiErrorAndDispatch("ErrInvalidFlags", err.Error())
	}
	if id == "" {
		return writeApiErrorAndDispatch("ErrInvalidFlags", "--claude-instance-id is required")
	}
	res, err := api.Status(st, id)
	if err != nil {
		name, desc := classifyError(err)
		return writeApiErrorAndDispatch(name, errMessageStartsWithName(name, desc))
	}
	return writeJSON(os.Stdout, res)
}

// sendKeysHandlerWith implements `claude-director send-keys`. The store is
// re-used from setupStoreAndCfg via the closure; the tmux client is the
// shared package-level *tmux.Client which already satisfies
// api.SendKeysTmux via its SendKeys method.
func sendKeysHandlerWith(st *store.Store, args []string) error {
	params, err := parseSendKeysFlags(args)
	if err != nil {
		return writeApiErrorAndDispatch("ErrInvalidFlags", err.Error())
	}
	if _, err := api.SendKeys(st, tmuxClient, params); err != nil {
		name, desc := classifyError(err)
		return writeApiErrorAndDispatch(name, errMessageStartsWithName(name, desc))
	}
	return writeJSON(os.Stdout, struct{}{})
}

// parseSendKeysFlags carves argv into a SendKeysParams. `--text` is
// required and may contain literal `\n` / `\r` from the caller — the verb
// strips `\r` and preserves `\n` per SRD §4.3. `--no-enter` flips the
// default-true press_enter to false.
func parseSendKeysFlags(args []string) (api.SendKeysParams, error) {
	var p api.SendKeysParams
	var noEnter bool
	fs := flag.NewFlagSet("send-keys", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&p.ClaudeInstanceID, "claude-instance-id", "", "id of the Spawn to drive")
	fs.StringVar(&p.Text, "text", "", "text to type into the Spawn's input")
	fs.BoolVar(&noEnter, "no-enter", false, "do not append a trailing Enter (default: append)")
	if err := fs.Parse(args); err != nil {
		return p, err
	}
	if p.ClaudeInstanceID == "" {
		return p, fmt.Errorf("--claude-instance-id is required")
	}
	// Empty --text is allowed (a press-Enter-only call has no body); the
	// verb-layer state guard still applies.
	p.PressEnter = !noEnter
	return p, nil
}

// getHandlerWith implements `claude-director get`.
func getHandlerWith(st *store.Store, args []string) error {
	var id string
	fs := flag.NewFlagSet("get", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&id, "claude-instance-id", "", "id to fetch")
	if err := fs.Parse(args); err != nil {
		return writeApiErrorAndDispatch("ErrInvalidFlags", err.Error())
	}
	if id == "" {
		return writeApiErrorAndDispatch("ErrInvalidFlags", "--claude-instance-id is required")
	}
	res, err := api.Get(st, id)
	if err != nil {
		name, desc := classifyError(err)
		return writeApiErrorAndDispatch(name, errMessageStartsWithName(name, desc))
	}
	return writeJSON(os.Stdout, res)
}

// writeJSON marshals v and writes it to w as a single line with a trailing
// newline. Used by every verb handler that succeeds.
func writeJSON(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return writeApiErrorAndDispatch(errJSONMarshal, err.Error())
	}
	if _, err := fmt.Fprintln(w, string(b)); err != nil {
		return err
	}
	return nil
}

// writeApiErrorAndDispatch writes the SRD §13.1 envelope to stderr and
// returns errDispatch so the run() exit code is non-zero without
// re-printing.
func writeApiErrorAndDispatch(name, description string) error {
	if werr := writeError(os.Stderr, name, description); werr != nil {
		return werr
	}
	return errDispatch
}
