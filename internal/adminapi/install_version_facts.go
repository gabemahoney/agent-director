package adminapi

import (
	"github.com/gabemahoney/agent-director/internal/installfacts"
	"github.com/gabemahoney/agent-director/internal/installversion"
)

// VersionFacts reads compiled data only: no config, store, lock or filesystem.
func VersionFacts() installversion.Facts { return installfacts.Compiled() }
