package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

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
// strips `\r` and preserves `\n` per SRD §4.3 and always appends a single
// trailing Enter.
func parseSendKeysFlags(args []string) (api.SendKeysParams, error) {
	var p api.SendKeysParams
	fs := flag.NewFlagSet("send-keys", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&p.ClaudeInstanceID, "claude-instance-id", "", "id of the Spawn to drive")
	fs.StringVar(&p.Text, "text", "", "text to type into the Spawn's input")
	if err := fs.Parse(args); err != nil {
		return p, err
	}
	if p.ClaudeInstanceID == "" {
		return p, fmt.Errorf("--claude-instance-id is required")
	}
	// Empty --text is allowed (a press-Enter-only call has no body); the
	// verb-layer state guard still applies.
	return p, nil
}

// readPaneHandlerWith implements `claude-director read-pane`. The handler
// trusts the api layer for ANSI handling and default-lines fallback;
// argv parsing here is purely flag-to-params translation.
func readPaneHandlerWith(st *store.Store, args []string) error {
	params, err := parseReadPaneFlags(args)
	if err != nil {
		return writeApiErrorAndDispatch("ErrInvalidFlags", err.Error())
	}
	result, err := api.ReadPane(st, tmuxClient, params)
	if err != nil {
		name, desc := classifyError(err)
		return writeApiErrorAndDispatch(name, errMessageStartsWithName(name, desc))
	}
	return writeJSON(os.Stdout, result)
}

// parseReadPaneFlags carves argv into a ReadPaneParams. The default for
// --n-lines is the same package-level constant the verb uses, so a CLI
// caller omitting the flag and an MCP caller passing 0 land on the same
// number.
func parseReadPaneFlags(args []string) (api.ReadPaneParams, error) {
	var p api.ReadPaneParams
	fs := flag.NewFlagSet("read-pane", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&p.ClaudeInstanceID, "claude-instance-id", "", "id of the Spawn to read")
	fs.IntVar(&p.NLines, "n-lines", api.DefaultReadPaneLines, "number of trailing pane lines to return")
	fs.BoolVar(&p.ANSI, "ansi", false, "return raw bytes (escape codes preserved); default strips ANSI but preserves unicode glyphs")
	if err := fs.Parse(args); err != nil {
		return p, err
	}
	if p.ClaudeInstanceID == "" {
		return p, fmt.Errorf("--claude-instance-id is required")
	}
	return p, nil
}

// makeTemplateHandlerWith implements `claude-director make-template`.
// Flags mirror the per-call spawn surface minus the three reserved
// per-invocation params (template, claude-instance-id, tmux-session-name).
func makeTemplateHandlerWith(args []string) error {
	var (
		labelKVs    map[string]string
		extraEnvKVs map[string]string
		allow       []string
		deny        []string
		ask         []string
		claudeArgs  []string
	)
	var p api.MakeTemplateParams
	fs := flag.NewFlagSet("make-template", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&p.Name, "name", "", "template name (filename-safe; required)")
	fs.StringVar(&p.CWD, "cwd", "", "bake a default cwd")
	fs.StringVar(&p.RelayMode, "relay-mode", "", "bake a default relay_mode (on/off)")
	fs.Var(newKVSlice(&labelKVs, "--label"), "label", "k=v (repeatable)")
	fs.Var(newKVSlice(&extraEnvKVs, "--extra-env"), "extra-env", "K=V (repeatable)")
	fs.Var(newStringSlice(&allow), "allow", "permissions.allow entry (repeatable)")
	fs.Var(newStringSlice(&deny), "deny", "permissions.deny entry (repeatable)")
	fs.Var(newStringSlice(&ask), "ask", "permissions.ask entry (repeatable)")
	fs.Var(newStringSlice(&claudeArgs), "claude-args", "single claude arg (repeatable; replaces template's array wholesale at spawn time)")
	if err := fs.Parse(args); err != nil {
		return writeApiErrorAndDispatch("ErrInvalidFlags", err.Error())
	}
	if p.Name == "" {
		return writeApiErrorAndDispatch("ErrInvalidFlags", "--name is required")
	}
	p.ClaudeDirectorLabels = labelKVs
	p.ExtraEnv = extraEnvKVs
	p.ClaudeArgs = claudeArgs
	if len(allow) > 0 || len(deny) > 0 || len(ask) > 0 {
		p.Permissions = &api.MakeTemplatePermissions{Allow: allow, Deny: deny, Ask: ask}
	}
	result, err := api.MakeTemplate(p)
	if err != nil {
		name, desc := classifyError(err)
		return writeApiErrorAndDispatch(name, errMessageStartsWithName(name, desc))
	}
	return writeJSON(os.Stdout, result)
}

// listHandlerWith implements `claude-director list`. Each filter flag
// corresponds 1:1 with a ListParams field; the API layer enforces the
// label key=value form.
func listHandlerWith(st *store.Store, args []string) error {
	var (
		stateRaw string
		labels   []string
	)
	var p api.ListParams
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&stateRaw, "state", "", "comma-separated states to filter (e.g. waiting,working)")
	fs.Var(newStringSlice(&labels), "label", "label k=v filter (repeatable; multiple AND together)")
	fs.StringVar(&p.Parent, "parent", "", "filter by parent_id exact match")
	fs.StringVar(&p.Cwd, "cwd", "", "filter by canonicalized cwd exact match")
	fs.IntVar(&p.Limit, "limit", 0, "cap result count (0 = no cap)")
	if err := fs.Parse(args); err != nil {
		return writeApiErrorAndDispatch("ErrInvalidFlags", err.Error())
	}
	if stateRaw != "" {
		for _, s := range strings.Split(stateRaw, ",") {
			s = strings.TrimSpace(s)
			if s != "" {
				p.State = append(p.State, s)
			}
		}
	}
	p.Labels = labels
	result, err := api.List(st, p)
	if err != nil {
		name, desc := classifyError(err)
		return writeApiErrorAndDispatch(name, errMessageStartsWithName(name, desc))
	}
	return writeJSON(os.Stdout, result)
}

// pauseHandlerWith implements `claude-director pause`. The verb's
// timeout is configurable but the polling cadence is fixed in the API
// layer; the CLI is intentionally a thin flag-to-params translator.
//
// ctx is rooted at context.Background() — the CLI process is short-
// lived and an OS signal terminates it directly. The MCP server (Epic
// 11) will wire request-scoped cancellation here.
func pauseHandlerWith(st *store.Store, cfg config.Config, args []string) error {
	var p api.PauseParams
	fs := flag.NewFlagSet("pause", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&p.ClaudeInstanceID, "claude-instance-id", "", "id of the Spawn to pause")
	if err := fs.Parse(args); err != nil {
		return writeApiErrorAndDispatch("ErrInvalidFlags", err.Error())
	}
	if p.ClaudeInstanceID == "" {
		return writeApiErrorAndDispatch("ErrInvalidFlags", "--claude-instance-id is required")
	}
	result, err := api.Pause(context.Background(), st, tmuxClient, cfg.Pause, p)
	if err != nil {
		name, desc := classifyError(err)
		return writeApiErrorAndDispatch(name, errMessageStartsWithName(name, desc))
	}
	return writeJSON(os.Stdout, result)
}

// killHandlerWith implements `claude-director kill`. The verb is
// idempotent on terminal states and swallows tmux failures (see
// api.Kill); the CLI surface is therefore intentionally narrow.
func killHandlerWith(st *store.Store, args []string) error {
	var p api.KillParams
	fs := flag.NewFlagSet("kill", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&p.ClaudeInstanceID, "claude-instance-id", "", "id of the Spawn to kill")
	if err := fs.Parse(args); err != nil {
		return writeApiErrorAndDispatch("ErrInvalidFlags", err.Error())
	}
	if p.ClaudeInstanceID == "" {
		return writeApiErrorAndDispatch("ErrInvalidFlags", "--claude-instance-id is required")
	}
	result, err := api.Kill(st, tmuxClient, p)
	if err != nil {
		name, desc := classifyError(err)
		return writeApiErrorAndDispatch(name, errMessageStartsWithName(name, desc))
	}
	return writeJSON(os.Stdout, result)
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
