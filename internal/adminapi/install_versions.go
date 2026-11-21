package adminapi

import (
	"context"
	"os"
	"path/filepath"

	"github.com/gabemahoney/agent-director/internal/installversion"
	"github.com/gabemahoney/agent-director/internal/layout"
)

// VersionInstallRefusal is the operator error category registered alongside
// JournalRefusal. No public Client verb returns it.
type VersionInstallRefusal = installversion.Refusal

// PublishVersion is called by the candidate's own operator binary. It opens no
// config or store. Engine also asks the staged admin for facts, so a mutable
// invocation path cannot silently supply a different administrator's bytes.
func PublishVersion(ctx context.Context, root, runtimeBinary string) (installversion.Result, error) {
	self, err := os.Executable()
	if err != nil {
		return installversion.Result{}, err
	}
	self, err = filepath.EvalSymlinks(self)
	if err != nil {
		return installversion.Result{}, err
	}
	return publishVersion(ctx, installversion.Engine{Root: root}, runtimeBinary, self)
}

// Per-call engine injection lets adapter tests observe real filesystem
// failures without production flags, environment switches or global hooks.
func publishVersion(ctx context.Context, engine installversion.Engine, runtimeBinary, adminBinary string) (installversion.Result, error) {
	return engine.Publish(ctx, runtimeBinary, adminBinary, VersionFacts())
}

func RollbackVersion(root, stamp string) (installversion.Result, error) {
	return (installversion.Engine{Root: root}).Rollback(stamp)
}

type InstalledVersions struct {
	Versions         []installversion.InstalledVersion `json:"versions"`
	PublishedVersion *string                           `json:"published_version"`
}

// ListVersions is two lock-free observations. Discovery never depends on
// current; the separately observed selection can change during enumeration.
func ListVersions(root string) (InstalledVersions, error) {
	versions, err := installversion.ListInstalled(root)
	if err != nil {
		return InstalledVersions{}, err
	}
	result := InstalledVersions{Versions: versions}
	published, err := installversion.ReadPublished(root)
	if err != nil {
		return result, err
	}
	if published != nil {
		result.PublishedVersion = &published.Version
	}
	return result, nil
}

// VersionInstallRoot uses only the invocation's effective home. Unlike a
// Client, it neither parses config.toml nor resolves a configured store path.
func VersionInstallRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Abs(layout.Dir(home))
}
