// Package installversion owns file-only version publication and lock-free
// retained-version discovery. It has no config, store, client or trail access.
package installversion

import (
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/gabemahoney/agent-director/internal/layout"
)

const FormatVersion = 1

// RuntimeFactsArgument is a private same-build handshake, not a public verb or
// a cross-version protocol. Only a copied, unpublished runtime is queried.
const RuntimeFactsArgument = "--install-version-facts"

// Facts are independently compiled into both executables of a generation.
type Facts struct {
	FormatVersion          int      `json:"format_version"`
	Version                string   `json:"version"`
	Commit                 string   `json:"commit"`
	CoveredAgents          []string `json:"covered_agents"`
	CoreVersion            string   `json:"core_version"`
	CoreDeclarationsSHA256 string   `json:"core_declarations_sha256"`
}

type FileRecord struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	Mode   uint32 `json:"mode"`
}

// VersionRecord holds the facts both staged binaries reported and agreed on.
// Files inventories the entire program payload; the record and ready marker
// are installation metadata, not executable dependencies.
type VersionRecord struct {
	Facts
	InstalledSequence uint64       `json:"installed_sequence"`
	Files             []FileRecord `json:"files"`
}

type InstalledVersion struct {
	VersionRecord
	VersionID         string `json:"version_id"`
	Directory         string `json:"directory"`
	RuntimeExecutable string `json:"runtime_executable"`
}

// Contract is anchored once. Later administrators never replace it with their
// own compiled identity, even when recovering an interrupted establishment.
type Contract struct {
	FormatVersion          int    `json:"format_version"`
	CoreVersion            string `json:"core_version"`
	CoreDeclarationsSHA256 string `json:"core_declarations_sha256"`
}

type Initialization struct {
	Contract
	InitialVersionID    string `json:"initial_version_id"`
	InitialRecordSHA256 string `json:"initial_record_sha256"`
	RuntimeTarget       string `json:"runtime_target"`
	AdminTarget         string `json:"admin_target"`
	Complete            bool   `json:"complete"`
}

func contractFor(f Facts) Contract {
	return Contract{FormatVersion, f.CoreVersion, f.CoreDeclarationsSHA256}
}

func validDigest(s string) bool {
	return len(s) == 64 && strings.Trim(s, "0123456789abcdef") == ""
}

func validSet(agents []string) bool {
	if agents == nil {
		return false
	}
	for i, agent := range agents {
		if agent == "" || strings.Trim(agent, "abcdefghijklmnopqrstuvwxyz0123456789_-") != "" || i > 0 && agents[i-1] >= agent {
			return false
		}
	}
	return true
}

func (f Facts) Validate() error {
	if f.FormatVersion != FormatVersion || f.Version == "" || !utf8.ValidString(f.Version) || strings.ContainsRune(f.Version, 0) || f.Commit == "" || f.CoreVersion == "" || !validDigest(f.CoreDeclarationsSHA256) || !validSet(f.CoveredAgents) {
		return invalid("invalid compiled version facts")
	}
	return nil
}

func (f Facts) Equal(other Facts) bool {
	return f.FormatVersion == other.FormatVersion && f.Version == other.Version && f.Commit == other.Commit && f.CoreVersion == other.CoreVersion && f.CoreDeclarationsSHA256 == other.CoreDeclarationsSHA256 && slices.Equal(f.CoveredAgents, other.CoveredAgents)
}

func (c Contract) validate() error {
	if c.FormatVersion != FormatVersion || c.CoreVersion == "" || !validDigest(c.CoreDeclarationsSHA256) {
		return invalid("invalid installation contract")
	}
	return nil
}

func (i Initialization) validate() error {
	if err := i.Contract.validate(); err != nil {
		return err
	}
	if !validDigest(i.InitialVersionID) || !validDigest(i.InitialRecordSHA256) || i.RuntimeTarget != layout.RuntimeEntryTarget || i.AdminTarget != layout.AdminEntryTarget {
		return invalid("invalid initialization evidence")
	}
	return nil
}

func (r VersionRecord) validate(id string) error {
	if err := r.Facts.Validate(); err != nil {
		return err
	}
	if !validDigest(id) || id != layout.VersionID(r.Version) || r.InstalledSequence == 0 || len(r.Files) != 2 {
		return invalid("invalid retained version record")
	}
	for n, name := range []string{layout.BinaryName, layout.AdminBinaryName} {
		f := r.Files[n]
		if f.Path != name || !validDigest(f.SHA256) || f.Size <= 0 || f.Mode > 0777 || f.Mode&0100 == 0 || f.Mode&0022 != 0 {
			return invalid("invalid retained executable inventory")
		}
	}
	return nil
}

func compatible(f Facts, anchor Contract, floor []string) error {
	if contractFor(f) != anchor {
		return problem{"core_mismatch", "candidate does not match the permanent core contract"}
	}
	for _, agent := range floor {
		if !slices.Contains(f.CoveredAgents, agent) {
			return problem{"coverage_regression", "candidate omits a previously published provider"}
		}
	}
	return nil
}

type problem struct{ reason, message string }

func (p problem) Error() string    { return p.message }
func invalid(message string) error { return problem{"layout_invalid", message} }

// Result describes the requested publication separately from any earlier
// publication recovered on this invocation. Durable concerns the requested
// selection; InitializationComplete additionally proves the permanent entries.
type Result struct {
	VersionID              string  `json:"version_id"`
	Version                string  `json:"version"`
	PublishedVersion       *string `json:"published_version"`
	PublicationState       string  `json:"publication_state"`
	Durable                bool    `json:"durable"`
	Reused                 bool    `json:"reused"`
	RecoveredVersion       *string `json:"recovered_version"`
	InitializationComplete bool    `json:"initialization_complete"`
}

type Refusal struct {
	Reason    string `json:"reason"`
	Retryable bool   `json:"retryable"`
	Result
	cause error
}

func (r *Refusal) Error() string {
	if r.Reason == "io_failure" {
		return "version installation I/O failed; rerun the same operation to complete forward recovery"
	}
	if r.cause == nil {
		return "version installation refused"
	}
	return fmt.Sprintf("version installation refused: %s", r.cause)
}
func (r *Refusal) Unwrap() error { return r.cause }

// Is identifies the administrator-only category without exporting a second
// public Client sentinel or losing the original operation's typed details.
func (r *Refusal) Is(target error) bool {
	_, ok := target.(*Refusal)
	return ok
}
