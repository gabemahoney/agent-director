// Package installfacts binds file-only installation records to this binary's
// compiled declarations. Keep this adapter out of the lock-free reader, so
// runtime discovery never acquires a dependency on store initialization.
package installfacts

import (
	"slices"

	"github.com/gabemahoney/agent-director/internal/installversion"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/version"
)

func Compiled() installversion.Facts {
	core, digest := store.BusySendCoreContractIdentity()
	agents := store.BusySendCoveredAgents()
	slices.Sort(agents)
	agents = slices.Compact(agents)
	if agents == nil {
		agents = []string{}
	}
	return installversion.Facts{
		FormatVersion: installversion.FormatVersion, Version: version.Version, Commit: version.Commit,
		CoveredAgents: agents, CoreVersion: core, CoreDeclarationsSHA256: digest,
	}
}
