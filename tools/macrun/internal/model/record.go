package model

// RecordSchemaVersion is the version of a part record's JSON form. A change
// that removes or renames a field, or changes a field's meaning, raises it;
// adding a field does not.
const RecordSchemaVersion = 1

// RecordFile is the name of the part record's file in the part's output
// folder, which the part job uploads as the artifact mac-part-<part>-<attempt>.
const RecordFile = "record.json"

// The names of a record's versions, in the order a part records them.
const (
	// VersionMacOSProduct is sw_vers -productVersion.
	VersionMacOSProduct = "macos-product-version"
	// VersionMacOSBuild is sw_vers -buildVersion.
	VersionMacOSBuild = "macos-build-version"
	// VersionTmux is tmux -V.
	VersionTmux = "tmux"
	// VersionMacOSSQLite3 is macOS's own /usr/bin/sqlite3 --version.
	VersionMacOSSQLite3 = "macos-sqlite3"
	// VersionGo is go env GOVERSION.
	VersionGo = "go"
	// VersionBun is bun --version.
	VersionBun = "bun"
	// VersionBash is the first line of --version of the bash the tests use.
	VersionBash = "bash"
	// VersionStandardSQLite3 is --version of the standard SQLite build the
	// tests use.
	VersionStandardSQLite3 = "standard-sqlite3"
)

// Version is one tool's version as a part recorded it.
type Version struct {
	// Name is one of the Version* names.
	Name string `json:"name"`
	// Value is the version as the tool printed it, trimmed.
	Value string `json:"value"`
}

// Configuration names a set of settings a case runs under.
type Configuration string

// ConfigurationDefault runs a case as the runner's own user with the
// environment the part sets.
const ConfigurationDefault Configuration = "default"

// PartEnd is how a part's run of its cases ended.
type PartEnd string

// The part ends. A record whose part never reached its cases, or whose log
// lost its end, has none.
const (
	// EndFinished: the part ran each of its packages to its end.
	EndFinished PartEnd = "finished"
	// EndCancelled: a cancel stopped the part; the cases it cut off have no
	// result.
	EndCancelled PartEnd = "cancelled"
)

// PackageBuild is how one package's test binary built.
type PackageBuild struct {
	// Dir is the package dir.
	Dir string `json:"dir"`
	// Built reports whether the binary built.
	Built bool `json:"built"`
	// Output is the build's output when it failed, cut as CapOutput cuts it.
	Output string `json:"output,omitempty"`
	// OutputCut reports that Output lost its beginning to the cap.
	OutputCut bool `json:"output_cut,omitempty"`
}

// SetupFailure is the check or setup step that stopped a part before it ran
// any case.
type SetupFailure struct {
	// Step names the failed check or step.
	Step string `json:"step"`
	// Message says what failed, naming the path or tool.
	Message string `json:"message"`
}

// Record is one part's record of one attempt: what it ran and how each case
// ended. A part writes it to RecordFile and as marked log lines (WriteEvent);
// ReadLog rebuilds it from the log. Empty lists are left out of its JSON form.
type Record struct {
	// SchemaVersion is RecordSchemaVersion when the record was made.
	SchemaVersion int `json:"schema_version"`
	// Part is the part's name.
	Part string `json:"part"`
	// RunID is the GitHub run's ID.
	RunID int64 `json:"run_id"`
	// Attempt is the run attempt the part ran in.
	Attempt int `json:"attempt"`
	// Commit is the tested commit.
	Commit string `json:"commit"`
	// Versions are the tools' versions, in the order of the Version* names.
	Versions []Version `json:"versions,omitempty"`
	// Configurations are the configurations the part ran.
	Configurations []Configuration `json:"configurations,omitempty"`
	// ExpectedCases are the plan's expected cases of the part, sorted. A
	// record read from a log has none.
	ExpectedCases []CaseID `json:"expected_cases,omitempty"`
	// Packages are the part's package builds, in the order built.
	Packages []PackageBuild `json:"packages,omitempty"`
	// Cases are the results of the cases that ended, in the order they
	// ended, one per case ID.
	Cases []CaseRecord `json:"cases,omitempty"`
	// WithoutResult are the cases that started and never ended, in the order
	// they started.
	WithoutResult []CaseID `json:"without_result,omitempty"`
	// End is how the part ended, or empty when it never reached its cases or
	// its log lost the end.
	End PartEnd `json:"end,omitempty"`
	// SetupFailure is set when a check or setup step stopped the part before
	// any case; such a record has no cases.
	SetupFailure *SetupFailure `json:"setup_failure,omitempty"`
	// FromLog reports that the record was rebuilt from the part's job log, so
	// it holds no output and no expected cases.
	FromLog bool `json:"from_log,omitempty"`
}
