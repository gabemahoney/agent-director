package layout

import (
	"crypto/sha256"
	"fmt"
	"path/filepath"
)

// Versioned installation names are shared by administrators and lock-free
// runtime readers. None of these helpers observes or changes an installation.
const (
	VersionsDir           = "versions"
	CurrentVersion        = "current"
	InstallContract       = "install-contract.json"
	InstallInitialization = "install-initialization.json"
	InstallLock           = "install.lock"
	PublicationsDir       = "publications"
	VersionRecord         = "version.json"
	VersionReady          = "ready"
	AdminDir              = "admin"
	AdminBinaryName       = "agent-director-admin"
	RuntimeEntryTarget    = "../current/agent-director"
	AdminEntryTarget      = "../current/agent-director-admin"
)

// VersionID encodes the exact stamp without permitting traversal or collisions
// caused by filename escaping. Records retain and check the original stamp.
func VersionID(stamp string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(stamp))) }

// VersionDir takes an installation root, not a home directory.
func VersionDir(root, stamp string) string {
	return filepath.Join(root, VersionsDir, VersionID(stamp))
}
