// Command agent-director-admin is agent-director's operator tool (b.vqr). It
// runs the operator-only actions that the agent-facing agent-director
// CLI, its MCP tools and the Go and TypeScript client libraries do not offer:
// kill-finished (kill's finished-row opt-in), delete, migrate-journal and
// snapshot-store.
// install.sh installs
// it at ~/.config/agent-director/admin/agent-director-admin and never on PATH.
//
// Like cmd/agent-director it is a thin shim: it parses and applies
// agent-director's global flags (--store-path, --home, --tmux-command,
// --create-if-missing) with the same code (clisetup.ParseGlobalFlags,
// GlobalFlags.Apply), parses the verb's flags and calls through internal/adminapi.
// kill-finished and delete open the pkg/api Client with clisetup.Open;
// migrate-journal and snapshot-store intentionally bypass Client initialization.
// It prints the result as
// JSON on stdout, or one {err_name, err_description} envelope on stderr with
// exit code 1. help, --help, -h, version, --version, -v, the no-verb run and
// every verb's --help or -h open no store and load no config, and every help
// opens with adminapi.ApprovalStatement.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/gabemahoney/agent-director/internal/adminapi"
	"github.com/gabemahoney/agent-director/internal/clisetup"
	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/oldnames"
	"github.com/gabemahoney/agent-director/internal/store"
	pkgapi "github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/errnames"
)

// errorEnvelope is the JSON shape written on stderr for every error, as
// agent-director writes it.
type errorEnvelope struct {
	ErrName        string `json:"err_name"`
	ErrDescription string `json:"err_description"`
	Details        any    `json:"details,omitempty"`
}

// The err_names this binary writes itself; the verbs' own come from
// errnames.Classify.
const (
	errUnknownVerb  = "ErrUnknownVerb"
	errInvalidFlags = "ErrInvalidFlags"
	errJSONMarshal  = "ErrJSONMarshal"
)

// binaryName is how help and errors name this binary.
const binaryName = "agent-director-admin"

// idFlag is the instance-id flag of kill-finished and delete.
const idFlag = "instance-id"

// helpWidth is the column help text wraps at.
const helpWidth = 80

// errDispatch is returned by a handler that has already written its error
// envelope, so run sets exit code 1 without printing again.
var errDispatch = errors.New("dispatch error")

// handlers maps each verb of adminapi.Verbs to its handler; the store-backed
// verbs open the Client with o, the run's global-flag overrides. help's
// aliases --help and -h print the same help, and version's aliases --version
// and -v (b.fv2) the same version stamp; an alias is looked up only as the
// first argument after the global flags.
func handlers(o clisetup.Overrides) map[string]func([]string) error {
	return map[string]func([]string) error{
		"help":            helpHandler,
		"--help":          helpHandler,
		"-h":              helpHandler,
		"version":         versionHandler,
		"--version":       versionHandler,
		"-v":              versionHandler,
		"kill-finished":   func(args []string) error { return killFinishedHandler(o, args) },
		"delete":          func(args []string) error { return deleteHandler(o, args) },
		"migrate-journal": func(args []string) error { return migrateJournalHandler(o, args) },
		"snapshot-store":  func(args []string) error { return snapshotStoreHandler(o, args) },
	}
}

// The cutover must not construct a Client: even its initial schema probe needs
// the explicit exclusion boundary when an old authorization sentinel exists.
// --check-only makes only the argument checks, before any config load or
// store-path resolution, so it loads no config and opens no store.
func migrateJournalHandler(o clisetup.Overrides, args []string) error {
	var options adminapi.JournalMigrationOptions
	var checkOnly bool
	fs := flag.NewFlagSet("migrate-journal", flag.ContinueOnError)
	fs.BoolVar(&options.MaintenanceWindow, "maintenance-window", false, "operator maintains exclusion of old mutators through verification")
	fs.BoolVar(&options.RootsComplete, "roots-complete", false, "operator established all legacy input roots")
	fs.Var((*stringSlice)(&options.LegacyRoots), "legacy-root", "additional absolute legacy input root (repeatable)")
	fs.StringVar(&options.BackupDir, "backup-dir", "", "new absolute stopped-writer backup directory")
	fs.BoolVar(&checkOnly, "check-only", false, "make only the argument checks")
	if done, err := parseFlags(fs, args); done {
		return err
	}
	if checkOnly {
		if err := adminapi.CheckJournalMigration(options); err != nil {
			return writeJournalRefusal(err, nil)
		}
		return writeJSON(struct{}{})
	}
	cfg, err := config.Load(clisetup.ConfigPath)
	if err != nil {
		return writeError("ErrConfigMalformed", err.Error())
	}
	path := o.StorePath
	if path == "" {
		path, err = cfg.Store.EffectiveDbPath()
		if err != nil {
			return writeError("ErrStoreOpen", err.Error())
		}
	}
	result, err := adminapi.MigrateJournal(context.Background(), path, options)
	if err != nil {
		return writeJournalRefusal(err, &result)
	}
	return writeJSON(result)
}

// writeJournalRefusal writes both migrate-journal runs' ErrJournalMigrationRefused
// envelope and returns errDispatch. A refusal carries details.refusal, and
// details.result when the run attempted the migration (result non-nil); any
// other error carries the result alone.
func writeJournalRefusal(err error, result *store.InputMigrationResult) error {
	var refusal *adminapi.JournalRefusal
	var details any
	if result != nil {
		details = *result
	}
	if errors.As(err, &refusal) {
		details = struct {
			Refusal *adminapi.JournalRefusal    `json:"refusal"`
			Result  *store.InputMigrationResult `json:"result,omitempty"`
		}{refusal, result}
	}
	_ = json.NewEncoder(os.Stderr).Encode(errorEnvelope{ErrName: "ErrJournalMigrationRefused", ErrDescription: err.Error(), Details: details})
	return errDispatch
}

func main() {
	os.Exit(run(os.Args[1:]))
}

// run dispatches args (global flags, the verb and its arguments) and returns
// the exit code: 0 on success, 1 after an error envelope.
func run(args []string) int {
	if err := dispatch(args); err != nil {
		if !errors.Is(err, errDispatch) {
			fmt.Fprintln(os.Stderr, err)
		}
		return 1
	}
	return 0
}

// dispatch parses and applies the global flags as agent-director does
// (--home replaces HOME before any config load), then runs the verb's
// handler on the remaining arguments. The no-verb run, which counts a run
// with only global flags, prints help. A malformed global flag is
// ErrInvalidFlags.
func dispatch(args []string) error {
	g, args, err := clisetup.ParseGlobalFlags(args)
	if err != nil {
		return writeError(errInvalidFlags, err.Error())
	}
	o, err := g.Apply()
	if err != nil {
		return writeError(errInvalidFlags, err.Error())
	}
	if len(args) == 0 {
		args = []string{"help"}
	}
	handler, ok := handlers(o)[args[0]]
	if !ok {
		return writeError(errUnknownVerb, fmt.Sprintf("unknown verb %q; try '%s help'", args[0], binaryName))
	}
	return handler(args[1:])
}

// helpHandler prints the help of the global flags and every verb. Its
// arguments are ignored.
func helpHandler([]string) error {
	var b strings.Builder
	b.WriteString(adminapi.ApprovalStatement + "\n\n")
	b.WriteString("Usage: " + binaryName + " [global flags] <verb> [flags]\n\n")
	b.WriteString("Global flags:\n")
	writeWrapped(&b, adminapi.GlobalFlagsText, "    ")
	writeFlags(&b, adminapi.GlobalFlags)
	for _, v := range adminapi.Verbs {
		b.WriteString("\n")
		writeVerbHelp(&b, v)
	}
	_, err := io.WriteString(os.Stdout, b.String())
	return err
}

// versionHandler prints the build-time version stamp as JSON, as
// agent-director version does. pkg/api.Version never errors today; a failure
// is named as runOnClient names a verb's (errnames.Classify: its Catalog
// name, else ErrInternal), never ErrJSONMarshal, which is only for
// writeJSON's json.Marshal (b.3jc).
func versionHandler(args []string) error {
	fs := flag.NewFlagSet("version", flag.ContinueOnError)
	if done, err := parseFlags(fs, args); done {
		return err
	}
	res, err := pkgapi.Version()
	if err != nil {
		name, desc := errnames.Classify(err)
		return writeError(name, errnames.TrimNamePrefix(name, desc))
	}
	return writeJSON(res)
}

// killFinishedHandler runs kill's finished-row opt-in on one row of the store
// o selects.
func killFinishedHandler(o clisetup.Overrides, args []string) error {
	var id string
	fs := flag.NewFlagSet("kill-finished", flag.ContinueOnError)
	fs.StringVar(&id, idFlag, "", "id of the finished row")
	if done, err := parseFlags(fs, args); done {
		return err
	}
	if id == "" {
		return writeError(errInvalidFlags, "--"+idFlag+" is required")
	}
	return runOnClient(o, func(c *pkgapi.Client) (any, error) { return adminapi.KillFinished(c, id) })
}

// deleteHandler removes the named rows from the store o selects, reporting
// each row's outcome.
func deleteHandler(o clisetup.Overrides, args []string) error {
	var ids []string
	fs := flag.NewFlagSet("delete", flag.ContinueOnError)
	fs.Var((*stringSlice)(&ids), idFlag, "id of a row to remove (repeatable; ≥1 required)")
	if done, err := parseFlags(fs, args); done {
		return err
	}
	if len(ids) == 0 {
		return writeError(errInvalidFlags, "--"+idFlag+" is required (≥1)")
	}
	return runOnClient(o, func(c *pkgapi.Client) (any, error) { return adminapi.Delete(c, ids) })
}

// parseFlags parses args into fs, whose name is the verb. done is true when
// the verb must stop with err: after printing the verb's help for --help or
// -h, or after an ErrInvalidFlags envelope for a bad flag or any positional
// argument.
func parseFlags(fs *flag.FlagSet, args []string) (done bool, err error) {
	fs.SetOutput(io.Discard)
	switch perr := fs.Parse(args); {
	case errors.Is(perr, flag.ErrHelp):
		return true, printVerbHelp(fs.Name())
	case perr != nil:
		if text, ok := oldnames.RefuseFlag(fs, perr); ok {
			return true, writeError(errInvalidFlags, text)
		}
		return true, writeError(errInvalidFlags, perr.Error())
	case fs.NArg() > 0:
		return true, writeError(errInvalidFlags, fmt.Sprintf("unexpected argument %q", fs.Arg(0)))
	}
	return false, nil
}

// printVerbHelp prints the help of the verb name.
func printVerbHelp(name string) error {
	v, ok := adminapi.Lookup(name)
	if !ok {
		return fmt.Errorf("%s: no help for verb %q", binaryName, name)
	}
	var b strings.Builder
	b.WriteString(adminapi.ApprovalStatement + "\n\n")
	writeVerbHelp(&b, v)
	names := make([]string, 0, len(adminapi.GlobalFlags))
	for _, f := range adminapi.GlobalFlags {
		names = append(names, strings.Fields(f.Name)[0])
	}
	writeWrapped(&b, "Global flags ("+strings.Join(names, ", ")+"): see '"+binaryName+" help'.", "    ")
	_, err := io.WriteString(os.Stdout, b.String())
	return err
}

// writeVerbHelp writes v's usage line, then its description, flags and
// output, wrapped and indented.
func writeVerbHelp(b *strings.Builder, v adminapi.Verb) {
	b.WriteString(v.Usage + "\n")
	writeWrapped(b, v.Description, "    ")
	writeFlags(b, v.Flags)
	writeWrapped(b, "Prints: "+v.Output, "    ")
}

// writeFlags writes each flag's name, then its description, wrapped and
// indented.
func writeFlags(b *strings.Builder, flags []adminapi.Flag) {
	for _, f := range flags {
		b.WriteString("    " + f.Name + "\n")
		writeWrapped(b, f.Description, "        ")
	}
}

// writeWrapped writes text as lines of at most helpWidth columns, each
// starting with indent; a word longer than a line gets a line of its own.
func writeWrapped(b *strings.Builder, text, indent string) {
	line := indent
	for _, word := range strings.Fields(text) {
		if line != indent && len(line)+1+len(word) > helpWidth {
			b.WriteString(line + "\n")
			line = indent
		}
		if line != indent {
			line += " "
		}
		line += word
	}
	if line != indent {
		b.WriteString(line + "\n")
	}
}

// runOnClient opens the Client as agent-director does, with the global-flag
// overrides o, runs verb on it and prints its result, or the error envelope
// of the open or the verb.
func runOnClient(o clisetup.Overrides, verb func(*pkgapi.Client) (any, error)) error {
	client, _, err := clisetup.Open(o)
	if err != nil {
		name := "ErrStoreOpen"
		var oe *clisetup.OpenError
		if errors.As(err, &oe) {
			name = oe.Name
		}
		return writeError(name, err.Error())
	}
	defer client.Close()
	res, err := verb(client)
	if err != nil {
		name, desc := errnames.Classify(err)
		return writeError(name, errnames.TrimNamePrefix(name, desc))
	}
	return writeJSON(res)
}

// writeJSON writes v as one JSON line on stdout.
func writeJSON(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return writeError(errJSONMarshal, err.Error())
	}
	_, err = fmt.Fprintln(os.Stdout, string(b))
	return err
}

// writeError writes one error envelope on stderr and returns errDispatch, or
// the write's own error.
func writeError(name, desc string) error {
	b, err := json.Marshal(errorEnvelope{ErrName: name, ErrDescription: desc})
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintln(os.Stderr, string(b)); err != nil {
		return err
	}
	return errDispatch
}

// stringSlice is a repeatable string flag.
type stringSlice []string

// String returns the values joined by commas.
func (s *stringSlice) String() string {
	if s == nil {
		return ""
	}
	return strings.Join(*s, ",")
}

// Set appends one value.
func (s *stringSlice) Set(v string) error {
	*s = append(*s, v)
	return nil
}
