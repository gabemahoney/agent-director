// Command ts-helper is the Go-side fixture-seeding CLI that TypeScript smoke
// tests shell out to for store/template setup.  It is a normal Go program
// under test/smoke/ts-helper/ and requires no special build tags.
//
// Contract
//
//	SUCCESS: exactly one line of JSON on stdout, empty stderr, exit 0.
//	FAILURE: message on stderr, empty stdout, exit 1.
//
// Available subcommands (use `ts-helper json-schema` for machine-readable
// result shapes):
//
//	seed-spawn           Insert one spawn row at a requested state
//	                     (--no-pre-trust records the pre-trust opt-out;
//	                     --no-launch-identity seeds a row from before the
//	                     release, with no launch token, socket or identity;
//	                     --agent seeds a row's agent, claude by default;
//	                     --no-subagent-tracking disables record capture).
//	seed-parent-child    Link an existing child spawn to an existing parent.
//	seed-permission-request  Insert an open permission request for a spawn
//	                     (--created-ago-seconds backdates it,
//	                     --hook-gone-ago-seconds records hook_gone_at).
//	seed-subagent        Insert one subagent record for an existing spawn
//	                     (--state, --close-reason, the four *-ms times, the
//	                     string fields and --confirmed pick its columns; a
//	                     column no flag names keeps apitest.SeedSubagent's
//	                     default; --subagent-id is generated if empty).
//	seed-template        Write a .toml template file.
//	seed-empty-store     Initialise a fresh SQLite store.
//	seed-row-session     Write a seeded row's own labelled session and pane
//	                     into test/fake-tmux's table for the row's socket
//	                     (--capture sets the pane's capture text;
//	                     --tables-dir defaults to $FAKE_TMUX_TABLES).
//	seed-read-surface-store  Create a store holding the fixed rows the
//	                     settle-off goldens pin status, get and list on
//	                     (apitest.SeedReadSurfaceStore; b.3ie).
//	set-settle           Write an existing row's settle columns: its settled
//	                     state and the time of its last raw state change
//	                     (apitest.SetSettle; b.3ie).
//	json-schema          Print the result shapes of every subcommand.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

func main() {
	os.Exit(dispatch(os.Args[1:], os.Stdout, os.Stderr))
}

// availableSubcmds is the ordered list used for error messages and json-schema.
var availableSubcmds = []string{
	"seed-spawn",
	"seed-parent-child",
	"seed-permission-request",
	"seed-subagent",
	"seed-template",
	"seed-empty-store",
	"seed-row-session",
	"fake-pane-owner",
	"seed-read-surface-store",
	"set-settle",
	"json-schema",
}

// dispatch is the testable entry point.  It reads args, routes to the
// correct subcommand handler, and returns an OS exit code (0 = success).
// stdout and stderr are explicit io.Writer parameters so tests can capture
// them without spawning a subprocess.
func dispatch(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printError(stderr, fmt.Errorf("no subcommand given; available: %s",
			strings.Join(availableSubcmds, ", ")))
		return 1
	}

	switch args[0] {
	case "seed-spawn":
		return cmdSeedSpawn(args[1:], stdout, stderr)
	case "seed-parent-child":
		return cmdSeedParentChild(args[1:], stdout, stderr)
	case "seed-permission-request":
		return cmdSeedPermissionRequest(args[1:], stdout, stderr)
	case "seed-subagent":
		return cmdSeedSubagent(args[1:], stdout, stderr)
	case "seed-template":
		return cmdSeedTemplate(args[1:], stdout, stderr)
	case "seed-empty-store":
		return cmdSeedEmptyStore(args[1:], stdout, stderr)
	case "seed-row-session":
		return cmdSeedRowSession(args[1:], stdout, stderr)
	case "fake-pane-owner":
		return cmdFakePaneOwner(args[1:], stdout, stderr)
	case "seed-read-surface-store":
		return cmdSeedReadSurfaceStore(args[1:], stdout, stderr)
	case "set-settle":
		return cmdSetSettle(args[1:], stdout, stderr)
	case "json-schema":
		return cmdJSONSchema(args[1:], stdout, stderr)
	default:
		printError(stderr, fmt.Errorf("unknown subcommand %q; available: %s",
			args[0], strings.Join(availableSubcmds, ", ")))
		return 1
	}
}

// ---- seed-spawn ------------------------------------------------------------

func cmdSeedSpawn(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("seed-spawn", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var (
		storePath          = fs.String("store", "", "path to SQLite store file (required)")
		state              = fs.String("state", "waiting", "spawn state (waiting|working|ended|check_permission|…)")
		cwd                = fs.String("cwd", "/tmp", "working directory for the spawn row")
		id                 = fs.String("id", "", "instance_id (UUID); auto-generated if empty")
		relayMode          = fs.String("relay-mode", "off", "relay_mode value (on|off); defaults to off")
		sessionID          = fs.String("session-id", "", "session_id; non-empty enables resume pre-flight")
		createStore        = fs.Bool("create-store", false, "create the store if it does not exist")
		socket             = fs.String("socket", "", "recorded tmux socket; defaults to apitest.TestSocket (a resume needs its directory to exist)")
		noPreTrust         = fs.Bool("no-pre-trust", false, "record the pre-trust opt-out on the row (apitest.WithNoPreTrust); default records pre-trust allowed")
		agent              = fs.String("agent", "", "the row's agent (claude|codex|cursor); empty keeps SeedSpawn's claude (apitest.WithAgent)")
		noSubagentTracking = fs.Bool("no-subagent-tracking", false, "disable subagent tracking on the row (apitest.WithNoSubagentTracking); default keeps tracking enabled")
		noIdentity         = fs.Bool("no-launch-identity", false, "seed a row from before the release: no launch token, socket or identity (apitest.WithNoLaunchToken); not with --socket")
	)

	if err := fs.Parse(args); err != nil {
		// flag.ContinueOnError already wrote the usage line to stderr.
		return 1
	}
	if *storePath == "" {
		printError(stderr, errors.New("--store is required"))
		return 1
	}
	if *noIdentity && *socket != "" {
		printError(stderr, errors.New("--socket and --no-launch-identity cannot be combined"))
		return 1
	}
	if *id == "" {
		*id = uuid.NewString()
	}
	var opts []apitest.SpawnOption
	if *socket != "" {
		opts = append(opts, apitest.WithTmuxSocket(*socket))
	}
	if *noIdentity {
		opts = append(opts, apitest.WithNoLaunchToken())
	}
	if *noPreTrust {
		opts = append(opts, apitest.WithNoPreTrust())
	}
	if *agent != "" {
		opts = append(opts, apitest.WithAgent(*agent))
	}

	if *noSubagentTracking {
		opts = append(opts, apitest.WithNoSubagentTracking())
	}

	instanceID, err := apitest.SeedSpawn(*storePath, *id, *state, *cwd, *relayMode, *sessionID, *createStore, opts...)
	if err != nil {
		printError(stderr, err)
		return 1
	}

	if err := printResult(stdout, map[string]string{
		"instance_id": instanceID,
	}); err != nil {
		printError(stderr, err)
		return 1
	}
	return 0
}

// ---- seed-parent-child -----------------------------------------------------

func cmdSeedParentChild(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("seed-parent-child", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var (
		storePath = fs.String("store", "", "path to SQLite store file (required)")
		parentID  = fs.String("parent-id", "", "instance_id of the parent spawn (required)")
		childID   = fs.String("child-id", "", "instance_id of the child spawn (required)")
	)

	if err := fs.Parse(args); err != nil {
		return 1
	}
	var missing []string
	if *storePath == "" {
		missing = append(missing, "--store")
	}
	if *parentID == "" {
		missing = append(missing, "--parent-id")
	}
	if *childID == "" {
		missing = append(missing, "--child-id")
	}
	if len(missing) > 0 {
		printError(stderr, fmt.Errorf("%s: required", strings.Join(missing, ", ")))
		return 1
	}

	if err := apitest.SeedParentChild(*storePath, *parentID, *childID); err != nil {
		printError(stderr, err)
		return 1
	}

	if err := printResult(stdout, map[string]string{
		"parent_id": *parentID,
		"child_id":  *childID,
	}); err != nil {
		printError(stderr, err)
		return 1
	}
	return 0
}

// ---- seed-permission-request -----------------------------------------------

func cmdSeedPermissionRequest(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("seed-permission-request", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var (
		storePath  = fs.String("store", "", "path to SQLite store file (required)")
		spawnID    = fs.String("spawn-id", "", "instance_id of an existing spawn (required)")
		toolName   = fs.String("tool", "", "tool name for the permission request (required)")
		createdAgo = fs.Int("created-ago-seconds", 0, "backdate the request's created_at this many seconds (0: now); "+
			"past the relay window it has fallen back (b.146)")
		goneAgo = fs.Int("hook-gone-ago-seconds", 0, "record hook_gone_at this many seconds ago (0: none), "+
			"as a reader that found the relay hook gone then would")
	)

	if err := fs.Parse(args); err != nil {
		return 1
	}
	var missing []string
	if *storePath == "" {
		missing = append(missing, "--store")
	}
	if *spawnID == "" {
		missing = append(missing, "--spawn-id")
	}
	if *toolName == "" {
		missing = append(missing, "--tool")
	}
	if len(missing) > 0 {
		printError(stderr, fmt.Errorf("%s: required", strings.Join(missing, ", ")))
		return 1
	}

	seed, err := apitest.SeedPermissionRequest(*storePath, *spawnID, *toolName)
	if err != nil {
		printError(stderr, err)
		return 1
	}
	if err := apitest.AgePermissionRequest(*storePath, seed.RequestID, time.Duration(*createdAgo)*time.Second,
		time.Duration(*goneAgo)*time.Second); err != nil {
		printError(stderr, err)
		return 1
	}

	if err := printResult(stdout, map[string]any{
		"request_id":    seed.RequestID,
		"request_token": seed.RequestToken,
	}); err != nil {
		printError(stderr, err)
		return 1
	}
	return 0
}

// ---- seed-subagent ---------------------------------------------------------

func cmdSeedSubagent(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("seed-subagent", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var (
		storePath    = fs.String("store", "", "path to SQLite store file (required)")
		instanceID   = fs.String("instance-id", "", "instance_id of an existing spawn (required)")
		subagentID   = fs.String("subagent-id", "", "subagent_id; auto-generated if empty")
		state        = fs.String("state", "", "state (running|stopped|lost); empty keeps SeedSubagent's running")
		closeReason  = fs.String("close-reason", "", "close_reason (stop_event|absent_from_snapshot|agent_gone); empty leaves it NULL")
		sessionID    = fs.String("session-id", "", "session_id; empty leaves it NULL")
		subagentType = fs.String("type", "", "subagent_type; empty leaves it NULL")
		description  = fs.String("description", "", "description; empty leaves it NULL")
		model        = fs.String("model", "", "model; empty leaves it NULL")
		parentType   = fs.String("parent-type", "", "parent_type (agent|subagent); needs --parent-id")
		parentID     = fs.String("parent-id", "", "parent_id; needs --parent-type")
		toolUseID    = fs.String("tool-use-id", "", "tool_use_id; empty leaves it NULL")
		runMode      = fs.String("run-mode", "", "run_mode (background|foreground); empty leaves it NULL")
		firstStarted = fs.Int64("first-started-at-ms", 0, "first_started_at, Unix milliseconds; unset leaves it NULL")
		startedAt    = fs.Int64("started-at-ms", 0, "started_at, Unix milliseconds; unset leaves it NULL")
		stoppedAt    = fs.Int64("stopped-at-ms", 0, "stopped_at, Unix milliseconds; unset leaves it NULL")
		lostAt       = fs.Int64("lost-at-ms", 0, "lost_at, Unix milliseconds; unset leaves it NULL")
		confirmed    = fs.Bool("confirmed", true, "true stores a record, false an unconfirmed observation (list-subagents never returns one)")
	)

	if err := fs.Parse(args); err != nil {
		return 1
	}
	var missing []string
	if *storePath == "" {
		missing = append(missing, "--store")
	}
	if *instanceID == "" {
		missing = append(missing, "--instance-id")
	}
	if len(missing) > 0 {
		printError(stderr, fmt.Errorf("%s: required", strings.Join(missing, ", ")))
		return 1
	}
	if (*parentType == "") != (*parentID == "") {
		printError(stderr, errors.New("--parent-type and --parent-id go together"))
		return 1
	}
	if *subagentID == "" {
		*subagentID = uuid.NewString()
	}

	given := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { given[f.Name] = true })

	opts := []apitest.SubagentOption{apitest.WithSubagentConfirmed(*confirmed)}
	strOpts := []struct {
		val string
		opt func(string) apitest.SubagentOption
	}{
		{*state, apitest.WithSubagentState},
		{*closeReason, apitest.WithSubagentCloseReason},
		{*sessionID, apitest.WithSubagentSessionID},
		{*subagentType, apitest.WithSubagentType},
		{*description, apitest.WithSubagentDescription},
		{*model, apitest.WithSubagentModel},
		{*toolUseID, apitest.WithSubagentToolUseID},
		{*runMode, apitest.WithSubagentRunMode},
	}
	for _, so := range strOpts {
		if so.val != "" {
			opts = append(opts, so.opt(so.val))
		}
	}
	if *parentType != "" {
		opts = append(opts, apitest.WithSubagentParent(*parentType, *parentID))
	}
	msOpts := []struct {
		flagName string
		val      int64
		opt      func(int64) apitest.SubagentOption
	}{
		{"first-started-at-ms", *firstStarted, apitest.WithSubagentFirstStartedAt},
		{"started-at-ms", *startedAt, apitest.WithSubagentStartedAt},
		{"stopped-at-ms", *stoppedAt, apitest.WithSubagentStoppedAt},
		{"lost-at-ms", *lostAt, apitest.WithSubagentLostAt},
	}
	for _, mo := range msOpts {
		if given[mo.flagName] {
			opts = append(opts, mo.opt(mo.val))
		}
	}

	if err := apitest.SeedSubagent(*storePath, *instanceID, *subagentID, opts...); err != nil {
		printError(stderr, err)
		return 1
	}

	if err := printResult(stdout, map[string]string{
		"instance_id": *instanceID,
		"subagent_id": *subagentID,
	}); err != nil {
		printError(stderr, err)
		return 1
	}
	return 0
}

// ---- seed-template ---------------------------------------------------------

func cmdSeedTemplate(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("seed-template", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var (
		templatesDir = fs.String("templates-dir", "", "directory to write the template into (required)")
		name         = fs.String("name", "", "template name without extension (required)")
		body         = fs.String("body", "", "TOML content for the template file (required)")
	)

	if err := fs.Parse(args); err != nil {
		return 1
	}
	var missing []string
	if *templatesDir == "" {
		missing = append(missing, "--templates-dir")
	}
	if *name == "" {
		missing = append(missing, "--name")
	}
	if *body == "" {
		missing = append(missing, "--body")
	}
	if len(missing) > 0 {
		printError(stderr, fmt.Errorf("%s: required", strings.Join(missing, ", ")))
		return 1
	}

	outPath, err := apitest.SeedTemplate(*templatesDir, *name, *body)
	if err != nil {
		printError(stderr, err)
		return 1
	}

	if err := printResult(stdout, map[string]string{
		"path": outPath,
	}); err != nil {
		printError(stderr, err)
		return 1
	}
	return 0
}

// ---- seed-empty-store ------------------------------------------------------

func cmdSeedEmptyStore(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("seed-empty-store", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var storePath = fs.String("store", "", "path for the new SQLite store file (required)")

	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *storePath == "" {
		printError(stderr, errors.New("--store is required"))
		return 1
	}

	path, err := apitest.InitStore(*storePath)
	if err != nil {
		printError(stderr, err)
		return 1
	}

	if err := printResult(stdout, map[string]string{
		"path": path,
	}); err != nil {
		printError(stderr, err)
		return 1
	}
	return 0
}

// ---- json-schema -----------------------------------------------------------

// resultSchemas maps each subcommand name to a description of its success
// result fields and their JSON types.  This is intentionally a simple
// observability aid rather than a full JSON Schema document.
var resultSchemas = map[string]map[string]string{
	"fake-pane-owner": {"pid": "number", "start": "string"},
	"seed-spawn": {
		"instance_id": "string",
	},
	"seed-parent-child": {
		"parent_id": "string",
		"child_id":  "string",
	},
	"seed-permission-request": {
		"request_id":    "number",
		"request_token": "string",
	},
	"seed-subagent": {
		"instance_id": "string",
		"subagent_id": "string",
	},
	"seed-template": {
		"path": "string",
	},
	"seed-empty-store": {
		"path": "string",
	},
	"seed-row-session": {
		"socket":     "string",
		"session_id": "string",
		"pane_id":    "string",
		"table":      "string",
	},
	"seed-read-surface-store": {
		"ids":     "array",
		"settled": "object",
	},
	"set-settle": {
		"instance_id": "string",
	},
}

func cmdJSONSchema(_ []string, stdout, stderr io.Writer) int {
	if err := printResult(stdout, resultSchemas); err != nil {
		printError(stderr, err)
		return 1
	}
	return 0
}
