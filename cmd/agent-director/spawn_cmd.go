package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	pkgapi "github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/errnames"
)

// spawnHandlerWith implements `agent-director spawn`. Called via a closure
// from handlers() so the Client constructed by setupClient is reused.
func spawnHandlerWith(client *pkgapi.Client, args []string) error {
	params, err := parseSpawnFlags(args)
	if err != nil {
		return writeApiErrorAndDispatch("ErrInvalidFlags", err.Error())
	}
	result, err := client.Spawn(params)
	if err != nil {
		name, desc := errnames.Classify(err)
		return writeApiErrorAndDispatch(name, errnames.TrimNamePrefix(name, desc))
	}
	return writeJSON(os.Stdout, result)
}

// parseSpawnFlags carves the argv into a SpawnParams. Stdlib `flag`
// covers most of it; the `--` separator pulls the remainder into
// ClaudeArgs verbatim.
func parseSpawnFlags(args []string) (pkgapi.SpawnParams, error) {
	var p pkgapi.SpawnParams
	var (
		labelKVs         map[string]string
		extraEnvKVs      map[string]string
		allow, deny, ask []string
	)
	fs := flag.NewFlagSet("spawn", flag.ContinueOnError)
	fs.SetOutput(io.Discard) // we surface errors via the JSON envelope
	fs.StringVar(&p.CWD, "cwd", "", "absolute / ~-prefixed cwd for the Spawn")
	fs.StringVar(&p.Template, "template", "", "named template stored under ~/.agent-director/templates/<name>.toml")
	fs.StringVar(&p.ClaudeInstanceID, "claude-instance-id", "", "explicit instance id (default: minted UUID4)")
	fs.StringVar(&p.TmuxSessionName, "tmux-session-name", "", "explicit tmux session name (default: <basename(cwd)>-<id[:8]>); rejects ':' '.' '#', control chars, and >64 bytes; no DB uniqueness check, name reuse across ended spawns supported")
	fs.StringVar(&p.RelayMode, "relay-mode", "", "on / off (default: config defaults.relay_mode)")
	fs.BoolVar(&p.NoPreTrust, "no-pre-trust", false, "skip pre-writing ~/.claude.json's trust key for cwd (bug b.f75); default off (pre-trust IS performed)")
	fs.Var(newKVSlice(&labelKVs, "--label"), "label", "k=v (repeatable)")
	fs.Var(newKVSlice(&extraEnvKVs, "--extra-env"), "extra-env", "K=V (repeatable)")
	fs.Var(newStringSlice(&allow), "allow", "permissions.allow entry (repeatable)")
	fs.Var(newStringSlice(&deny), "deny", "permissions.deny entry (repeatable)")
	fs.Var(newStringSlice(&ask), "ask", "permissions.ask entry (repeatable)")
	if err := fs.Parse(args); err != nil {
		return p, err
	}
	// Distinguish "--tmux-session-name omitted" from
	// "--tmux-session-name=" (explicit empty). Omitted falls through to
	// composeSessionName defaulting; explicit empty must surface
	// ErrTmuxSessionNameEmpty at Validate time.
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "tmux-session-name" {
			p.TmuxSessionNameSupplied = true
		}
	})
	p.AgentDirectorLabels = labelKVs
	p.ExtraEnv = extraEnvKVs
	p.Permissions = buildPermissions(allow, deny, ask)
	// flag.FlagSet.Args() returns a non-nil empty slice when nothing
	// follows the `--` separator. Treat "no trailing args" as "not
	// supplied" so spawn.Resolve's nil-falls-back-to-template branch
	// fires; non-empty trailing args replace the template wholesale.
	// Mirrors the TmuxSessionNameSupplied sentinel pattern above.
	if rest := fs.Args(); len(rest) > 0 {
		p.ClaudeArgs = rest
	}
	return p, nil
}

// statusHandlerWith implements `agent-director status`.
func statusHandlerWith(client *pkgapi.Client, args []string) error {
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
	res, err := client.Status(id)
	if err != nil {
		name, desc := errnames.Classify(err)
		return writeApiErrorAndDispatch(name, errnames.TrimNamePrefix(name, desc))
	}
	return writeJSON(os.Stdout, res)
}

// sendKeysHandlerWith implements `agent-director send-keys`.
func sendKeysHandlerWith(client *pkgapi.Client, args []string) error {
	params, err := parseSendKeysFlags(args)
	if err != nil {
		return writeApiErrorAndDispatch("ErrInvalidFlags", err.Error())
	}
	if _, err := client.SendKeys(params); err != nil {
		name, desc := errnames.Classify(err)
		return writeApiErrorAndDispatch(name, errnames.TrimNamePrefix(name, desc))
	}
	return writeJSON(os.Stdout, struct{}{})
}

// parseSendKeysFlags carves argv into a SendKeysParams. `--text` is
// required and may contain literal `\n` / `\r` from the caller — the verb
// strips `\r` and preserves `\n` per SRD §4.3 and always appends a single
// trailing Enter.
func parseSendKeysFlags(args []string) (pkgapi.SendKeysParams, error) {
	var p pkgapi.SendKeysParams
	fs := flag.NewFlagSet("send-keys", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&p.ClaudeInstanceID, "claude-instance-id", "", "id of the Spawn to drive")
	fs.StringVar(&p.Text, "text", "", "text to type into the Spawn's input")
	fs.BoolVar(&p.AllowPending, "allow-pending", false, "allow send-keys on a pending Spawn (pre-SessionStart use case); ended/missing still rejected")
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

// readPaneHandlerWith implements `agent-director read-pane`. The handler
// trusts the api layer for ANSI handling and default-lines fallback;
// argv parsing here is purely flag-to-params translation.
func readPaneHandlerWith(client *pkgapi.Client, args []string) error {
	params, err := parseReadPaneFlags(args)
	if err != nil {
		return writeApiErrorAndDispatch("ErrInvalidFlags", err.Error())
	}
	result, err := client.ReadPane(params)
	if err != nil {
		name, desc := errnames.Classify(err)
		return writeApiErrorAndDispatch(name, errnames.TrimNamePrefix(name, desc))
	}
	return writeJSON(os.Stdout, result)
}

// parseReadPaneFlags carves argv into a ReadPaneParams. The default for
// --n-lines is the same package-level constant the verb uses, so a CLI
// caller omitting the flag and an MCP caller passing 0 land on the same
// number.
func parseReadPaneFlags(args []string) (pkgapi.ReadPaneParams, error) {
	var p pkgapi.ReadPaneParams
	fs := flag.NewFlagSet("read-pane", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&p.ClaudeInstanceID, "claude-instance-id", "", "id of the Spawn to read")
	fs.IntVar(&p.NLines, "n-lines", pkgapi.DefaultReadPaneLines, "number of trailing pane lines to return")
	fs.BoolVar(&p.ANSI, "ansi", false, "return raw bytes (escape codes preserved); default strips ANSI but preserves unicode glyphs")
	fs.BoolVar(&p.AllowPending, "allow-pending", false, "accepted for surface symmetry with send-keys; read-pane has no state guard so this flag has no effect")
	if err := fs.Parse(args); err != nil {
		return p, err
	}
	if p.ClaudeInstanceID == "" {
		return p, fmt.Errorf("--claude-instance-id is required")
	}
	return p, nil
}

// makeTemplateHandlerWith implements `agent-director make-template`.
// Flags mirror the per-call spawn surface minus the three reserved
// per-invocation params (template, claude-instance-id, tmux-session-name).
func makeTemplateHandlerWith(client *pkgapi.Client, args []string) error {
	var (
		labelKVs    map[string]string
		extraEnvKVs map[string]string
		allow       []string
		deny        []string
		ask         []string
		claudeArgs  []string
	)
	var p pkgapi.MakeTemplateParams
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
	fs.BoolVar(&p.Overwrite, "overwrite", false, "replace any existing template atomically (default false preserves O_EXCL create-only)")
	if err := fs.Parse(args); err != nil {
		return writeApiErrorAndDispatch("ErrInvalidFlags", err.Error())
	}
	if p.Name == "" {
		return writeApiErrorAndDispatch("ErrInvalidFlags", "--name is required")
	}
	p.AgentDirectorLabels = labelKVs
	p.ExtraEnv = extraEnvKVs
	p.ClaudeArgs = claudeArgs
	if len(allow) > 0 || len(deny) > 0 || len(ask) > 0 {
		p.Permissions = &pkgapi.MakeTemplatePermissions{Allow: allow, Deny: deny, Ask: ask}
	}
	result, err := client.MakeTemplate(p)
	if err != nil {
		name, desc := errnames.Classify(err)
		return writeApiErrorAndDispatch(name, errnames.TrimNamePrefix(name, desc))
	}
	return writeJSON(os.Stdout, result)
}

// listHandlerWith implements `agent-director list`. Each filter flag
// corresponds 1:1 with a ListParams field; the API layer enforces the
// label key=value form.
func listHandlerWith(client *pkgapi.Client, args []string) error {
	var (
		stateRaw string
		labels   []string
	)
	var p pkgapi.ListParams
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&stateRaw, "state", "", "comma-separated states to filter (e.g. waiting,working)")
	fs.Var(newStringSlice(&labels), "label", "label k=v filter (repeatable; multiple AND together)")
	fs.StringVar(&p.Parent, "parent", "", "filter by parent_id exact match")
	fs.StringVar(&p.Cwd, "cwd", "", "filter by canonicalized cwd exact match")
	fs.StringVar(&p.TmuxSessionName, "tmux-session-name", "", "filter by tmux session name exact match")
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
	result, err := client.List(p)
	if err != nil {
		name, desc := errnames.Classify(err)
		return writeApiErrorAndDispatch(name, errnames.TrimNamePrefix(name, desc))
	}
	return writeJSON(os.Stdout, result)
}

// pauseHandlerWith implements `agent-director pause`. The verb's
// timeout is configurable but the polling cadence is fixed in the API
// layer; the CLI is intentionally a thin flag-to-params translator.
//
// ctx is rooted at context.Background() — the CLI process is short-
// lived and an OS signal terminates it directly. The MCP server (Epic
// 11) will wire request-scoped cancellation here.
func pauseHandlerWith(client *pkgapi.Client, args []string) error {
	var p pkgapi.PauseParams
	fs := flag.NewFlagSet("pause", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&p.ClaudeInstanceID, "claude-instance-id", "", "id of the Spawn to pause")
	if err := fs.Parse(args); err != nil {
		return writeApiErrorAndDispatch("ErrInvalidFlags", err.Error())
	}
	if p.ClaudeInstanceID == "" {
		return writeApiErrorAndDispatch("ErrInvalidFlags", "--claude-instance-id is required")
	}
	result, err := client.Pause(context.Background(), p)
	if err != nil {
		name, desc := errnames.Classify(err)
		return writeApiErrorAndDispatch(name, errnames.TrimNamePrefix(name, desc))
	}
	return writeJSON(os.Stdout, result)
}

// decideHandlerWith implements `agent-director decide`. The handler
// rejects empty flags up front; the API layer guards the
// allow|deny enum (ErrInvalidDecision) as defense in depth for MCP
// callers that bypass the CLI flag parser.
func decideHandlerWith(client *pkgapi.Client, args []string) error {
	var p pkgapi.DecideParams
	fs := flag.NewFlagSet("decide", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&p.ClaudeInstanceID, "claude-instance-id", "", "id of the Spawn awaiting a decision")
	fs.StringVar(&p.RequestToken, "request-token", "", "UUIDv4 token identifying the specific permission request to decide")
	fs.StringVar(&p.Decision, "decision", "", "allow or deny")
	fs.StringVar(&p.Reason, "reason", "", "optional message surfaced to Claude")
	if err := fs.Parse(args); err != nil {
		return writeApiErrorAndDispatch("ErrInvalidFlags", err.Error())
	}
	if p.ClaudeInstanceID == "" {
		return writeApiErrorAndDispatch("ErrInvalidFlags", "--claude-instance-id is required")
	}
	if p.RequestToken == "" {
		return writeApiErrorAndDispatch("ErrInvalidFlags", "--request-token is required")
	}
	if p.Decision == "" {
		return writeApiErrorAndDispatch("ErrInvalidFlags", "--decision is required (allow|deny)")
	}
	result, err := client.Decide(p)
	if err != nil {
		name, desc := errnames.Classify(err)
		return writeApiErrorAndDispatch(name, errnames.TrimNamePrefix(name, desc))
	}
	return writeJSON(os.Stdout, result)
}

// resumeHandlerWith implements `agent-director resume`. The verb
// reads the spawn-time row out of the store and restarts claude via
// tmux with `--resume <session_id>`. Same id, fresh tmux session.
func resumeHandlerWith(client *pkgapi.Client, args []string) error {
	var p pkgapi.ResumeParams
	fs := flag.NewFlagSet("resume", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&p.ClaudeInstanceID, "claude-instance-id", "", "id of the terminated Spawn to resurrect")
	if err := fs.Parse(args); err != nil {
		return writeApiErrorAndDispatch("ErrInvalidFlags", err.Error())
	}
	if p.ClaudeInstanceID == "" {
		return writeApiErrorAndDispatch("ErrInvalidFlags", "--claude-instance-id is required")
	}
	result, err := client.Resume(p)
	if err != nil {
		name, desc := errnames.Classify(err)
		return writeApiErrorAndDispatch(name, errnames.TrimNamePrefix(name, desc))
	}
	return writeJSON(os.Stdout, result)
}

// killHandlerWith implements `agent-director kill`. The verb is
// idempotent on terminal states and swallows tmux failures at the
// verb surface (see api.Kill); a swallowed failure is logged at WARN
// to the configured error log so an interactive operator can see it.
func killHandlerWith(client *pkgapi.Client, args []string) error {
	var p pkgapi.KillParams
	fs := flag.NewFlagSet("kill", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&p.ClaudeInstanceID, "claude-instance-id", "", "id of the Spawn to kill")
	if err := fs.Parse(args); err != nil {
		return writeApiErrorAndDispatch("ErrInvalidFlags", err.Error())
	}
	if p.ClaudeInstanceID == "" {
		return writeApiErrorAndDispatch("ErrInvalidFlags", "--claude-instance-id is required")
	}
	result, err := client.Kill(p)
	if err != nil {
		name, desc := errnames.Classify(err)
		return writeApiErrorAndDispatch(name, errnames.TrimNamePrefix(name, desc))
	}
	return writeJSON(os.Stdout, result)
}

// getHandlerWith implements `agent-director get`.
func getHandlerWith(client *pkgapi.Client, args []string) error {
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
	res, err := client.Get(id)
	if err != nil {
		name, desc := errnames.Classify(err)
		return writeApiErrorAndDispatch(name, errnames.TrimNamePrefix(name, desc))
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
