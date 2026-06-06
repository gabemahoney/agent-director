//go:build helper

// Command ts-helper is the Go-side fixture-seeding CLI that TypeScript smoke
// tests shell out to for store/template setup.  It is compiled exclusively
// with the `helper` build tag so the production agent-director binary never
// includes any of this code.
//
// Contract
//
//	SUCCESS: exactly one line of JSON on stdout, empty stderr, exit 0.
//	FAILURE: message on stderr, empty stdout, exit 1.
//
// Available subcommands (use `ts-helper json-schema` for machine-readable
// result shapes):
//
//	seed-spawn           Insert one spawn row at a requested state.
//	seed-parent-child    Link an existing child spawn to an existing parent.
//	seed-permission-request  Insert an open permission request for a spawn.
//	seed-template        Write a .toml template file.
//	seed-empty-store     Initialise a fresh SQLite store.
//	json-schema          Print the result shapes of every subcommand.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

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
	"seed-template",
	"seed-empty-store",
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
	case "seed-template":
		return cmdSeedTemplate(args[1:], stdout, stderr)
	case "seed-empty-store":
		return cmdSeedEmptyStore(args[1:], stdout, stderr)
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
		storePath   = fs.String("store", "", "path to SQLite store file (required)")
		state       = fs.String("state", "waiting", "spawn state (waiting|working|ended|check_permission|…)")
		cwd         = fs.String("cwd", "/tmp", "working directory for the spawn row")
		id          = fs.String("id", "", "claude_instance_id (UUID); auto-generated if empty")
		relayMode   = fs.String("relay-mode", "off", "relay_mode value (on|off); defaults to off")
		sessionID   = fs.String("session-id", "", "claude_session_id; non-empty enables resume pre-flight")
		createStore = fs.Bool("create-store", false, "create the store if it does not exist")
	)

	if err := fs.Parse(args); err != nil {
		// flag.ContinueOnError already wrote the usage line to stderr.
		return 1
	}
	if *storePath == "" {
		printError(stderr, errors.New("--store is required"))
		return 1
	}
	if *id == "" {
		*id = uuid.NewString()
	}

	instanceID, err := apitest.SeedSpawn(*storePath, *id, *state, *cwd, *relayMode, *sessionID, *createStore)
	if err != nil {
		printError(stderr, err)
		return 1
	}

	if err := printResult(stdout, map[string]string{
		"claude_instance_id": instanceID,
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
		parentID  = fs.String("parent-id", "", "claude_instance_id of the parent spawn (required)")
		childID   = fs.String("child-id", "", "claude_instance_id of the child spawn (required)")
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
		storePath = fs.String("store", "", "path to SQLite store file (required)")
		spawnID   = fs.String("spawn-id", "", "claude_instance_id of an existing spawn (required)")
		toolName  = fs.String("tool", "", "tool name for the permission request (required)")
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

	if err := printResult(stdout, map[string]any{
		"request_id":    seed.RequestID,
		"request_token": seed.RequestToken,
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
	"seed-spawn": {
		"claude_instance_id": "string",
	},
	"seed-parent-child": {
		"parent_id": "string",
		"child_id":  "string",
	},
	"seed-permission-request": {
		"request_id":    "number",
		"request_token": "string",
	},
	"seed-template": {
		"path": "string",
	},
	"seed-empty-store": {
		"path": "string",
	},
}

func cmdJSONSchema(_ []string, stdout, stderr io.Writer) int {
	if err := printResult(stdout, resultSchemas); err != nil {
		printError(stderr, err)
		return 1
	}
	return 0
}
