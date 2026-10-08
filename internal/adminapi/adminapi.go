// Package adminapi is agent-director-admin's door into pkg/api (b.vqr): the
// operator-only actions that pkg/api keeps out of its exported surface, so
// that the agent-director CLI, its MCP tools and the Go and TypeScript client
// libraries expose exactly the same verbs and parameters, and the off-PATH
// admin binary is the only way to run these two actions.
//
// pkg/api sets the hooks KillFinished and Delete in its init; any program
// that imports pkg/api has them set. This package imports nothing from
// pkg/api, so pkg/api can import it with no import cycle, and being under
// internal/ it cannot be imported from outside this module.
//
// Verbs and GlobalFlags are the admin binary's own verb and global-flag
// lists: its help and the generated docs/admin-reference.md come from them,
// never from pkg/api/manifest.Verbs, so nothing shown to agents names the
// admin binary or its verbs.
package adminapi

// ApprovalStatement is the first line of every help agent-director-admin
// prints (no verb, help, --help, -h and each verb's --help or -h) and the
// first content of docs/admin-reference.md.
const ApprovalStatement = "agent-director-admin is an operator tool. Do not run any of its commands without explicit approval from a human for this specific run. Agents and automated callers must not run it."

// KillResult is kill-finished's result, printed as JSON on stdout. It has the
// shape of the kill verb's result.
type KillResult struct {
	// KillSent is true exactly when a pane kill or a session kill was sent.
	KillSent bool `json:"kill_sent"`
}

// DeleteResult is delete's result, printed as JSON on stdout.
type DeleteResult struct {
	// Results maps each requested id to its outcome: "ok" when the row was
	// removed, or an err_name ("ErrSpawnNotFound", "ErrInternal"). It is never
	// nil and has one entry per distinct requested id.
	Results map[string]string `json:"results"`
}

// KillFinished is kill with the finished-row option (SR-6.5) on the client
// c, which must be a *api.Client from api.New: on a finished row (ended or
// missing) it ends the row's own old session, if that session reported in to
// the row, or, when the row's latest launch records no session of its own,
// this id's own abandoned launch once it has outlived the starting-session
// bound (b.6sa), with the kill sequence and the process wait of kill, using
// c's configured durations; a live row (pending included) gets
// ErrSpawnNotResumable with no lookup. Its one ad.kill.called trail record
// carries include_finished true. A c of any other type is an error and
// nothing runs. Set by pkg/api's init.
var KillFinished func(c any, claudeInstanceID string) (KillResult, error)

// Delete removes the rows claudeInstanceIDs from the store of the client c,
// which must be a *api.Client from api.New, bypassing every state guard. Each
// id is processed on its own; a failure on one never aborts the batch, and
// the error is nil whenever the batch ran. It touches no tmux session and no
// transcript. A c of any other type is an error and nothing runs. Set by
// pkg/api's init.
var Delete func(c any, claudeInstanceIDs []string) (DeleteResult, error)

// Verb is one agent-director-admin verb, as its help and
// docs/admin-reference.md describe it.
type Verb struct {
	// Name is the verb as typed after the binary name.
	Name string
	// Usage is the verb's full command line.
	Usage string
	// Description says what the verb does.
	Description string
	// Flags are the verb's flags, in the order help lists them.
	Flags []Flag
	// Output says what a successful run prints on stdout.
	Output string
}

// Flag is one flag of a Verb.
type Flag struct {
	// Name is the flag as typed, with its value placeholder.
	Name string
	// Description says what the flag sets.
	Description string
}

// Common texts of the verb list.
const (
	// errorsText says how every verb reports an error.
	errorsText = "An error prints one JSON envelope ({err_name, err_description}) on stderr and exits 1."
	// noStoreText says which verbs open no store.
	noStoreText = "It opens no store and loads no config."
	// sameUserText is the rule every store-backed verb follows.
	sameUserText = "Run it as the agents' user and in their tmux environment."
	// idFlagName is the instance-id flag both store-backed verbs take.
	idFlagName = "--claude-instance-id <id>"
)

// GlobalFlagsText introduces GlobalFlags in help and docs/admin-reference.md.
const GlobalFlagsText = "The global flags are agent-director's, parsed and applied the same way: give the ones agent-director's runs use, if any, " +
	"so that agent-director-admin opens the same store and reaches the same tmux. Each goes before or after the verb, as --flag value or --flag=value."

// GlobalFlags are agent-director-admin's global flags, the ones agent-director
// takes (internal/clisetup.ParseGlobalFlags parses them for both binaries), in
// the order help lists them.
var GlobalFlags = []Flag{
	{Name: "--store-path <path>", Description: "Open the store at <path> instead of the configured one ([store] db_path in ~/.agent-director/config.toml, by default ~/.agent-director/state.db)."},
	{Name: "--home <dir>", Description: "Use <dir> as HOME for this run, so the config, the store and every other ~/ path resolve under it."},
	{Name: "--tmux-command <path>", Description: "Run <path> as tmux instead of the tmux on PATH."},
}

// Verbs is agent-director-admin's verb list, in help order.
var Verbs = []Verb{
	{
		Name:  "kill-finished",
		Usage: "agent-director-admin kill-finished " + idFlagName,
		Description: "End a finished row's (ended or missing) own old session, after a human has looked at it. " +
			"It ends the session only if it reported in to the row: the row records the agent's process id and the session was created, in whole seconds, before the row finished. " +
			"It ends the agent's pane and the row's labelled session, waits for the agent process (or, when the process cannot be checked, looks the session up once more) and succeeds once the agent process is gone. " +
			"It also ends this id's own abandoned launch, which resume and spawn --reuse-finished refuse: when the row records a launch token but no tmux server or pane of that launch, the sessions carrying an earlier launch's label of the row's id in this store, once the youngest has run for at least the starting-session bound; " +
			"such a launch never reports in, so its agent process is the process of the pane that launch created, never one the row records. " +
			"The row's state and every other field stay unchanged, so the conversation stays resumable. " +
			"With no session of the row's current launch found, it acts as kill does on a live row. " +
			"A live row (pending included) is refused with ErrSpawnNotResumable, with no lookup; an unknown id is ErrSpawnNotFound. " +
			"These refusals send no kill and change nothing: ErrInternal (unusable recorded tmux session name), ErrTmuxUnresponsive (still stopping or still starting: wait and run it again; or tmux did not answer usably), ErrTmuxSessionConflict (never reported in, or conflicting labels), ErrTmuxNotAvailable. " +
			"ErrTmuxKillFailed means the agent process still runs: retry later. After a sent kill, ErrTmuxUnresponsive or ErrTmuxNotAvailable means the kill may or may not have taken effect: check again later. " +
			"\"A finished row's own old session\" under \"Operator actions\" in the agent-director README describes every case. " +
			"Its ad.kill.called trail record carries include_finished true. " + sameUserText + " " + errorsText,
		Flags: []Flag{
			{Name: idFlagName, Description: "Id of the finished row. Required."},
		},
		Output: "{\"kill_sent\": true} or {\"kill_sent\": false}: kill_sent is true exactly when a pane or session kill was sent.",
	},
	{
		Name:  "delete",
		Usage: "agent-director-admin delete " + idFlagName + " [" + idFlagName + "...]",
		Description: "Remove rows by id, bypassing every guard: the human's repair tool for a row nothing else removes, such as a row whose recorded tmux session name cannot be used, which resume and reuse refuse and expire keeps. " +
			"It also removes each row's permission requests and session history and clears it as the parent of any other row. " +
			"It touches no tmux session and no transcript: on a live row the agent keeps running, untracked. " +
			"Never remove a row after a kill that did not succeed. " +
			"Each id is processed on its own; a failure on one never aborts the batch. " + sameUserText + " " + errorsText,
		Flags: []Flag{
			{Name: idFlagName, Description: "Id of a row to remove. Repeatable; at least one is required."},
		},
		Output: "{\"results\": {...}}, mapping each requested id to \"ok\" when the row was removed, \"ErrSpawnNotFound\" when no row has the id, or \"ErrInternal\" on any other store failure.",
	},
	{
		Name:  "help",
		Usage: "agent-director-admin help",
		Description: "Print the help of every verb and the global flags. agent-director-admin with no verb, --help and -h print it too, and every verb's --help or -h prints that verb's help. " +
			noStoreText,
		Output: "Plain text, opening with the human-approval statement.",
	},
	{
		Name:  "version",
		Usage: "agent-director-admin version",
		Description: "Print the build-time version stamp, as agent-director version does; install.sh installs the two binaries only when their stamps (version and commit) are the same. " +
			noStoreText + " " + errorsText,
		Output: "{\"version\": \"...\", \"commit\": \"...\"}: the version stamp and the full git SHA the binary was built from.",
	},
}

// Lookup returns the Verb named name, and false when there is none.
func Lookup(name string) (Verb, bool) {
	for _, v := range Verbs {
		if v.Name == name {
			return v, true
		}
	}
	return Verb{}, false
}
