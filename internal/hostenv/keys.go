// Package hostenv owns credentials that must never be inherited by helper
// processes. Launch policies and helper environment filters must reuse Names
// and IsCredential rather than defining their own lists.
package hostenv

import "slices"

// OpenRouterAPIKey is the host credential used by the corresponding launch policy.
const OpenRouterAPIKey = "OPENROUTER_API_KEY"

var credentials = []string{OpenRouterAPIKey}

// Names returns the host-only credential names, never their values.
func Names() []string { return slices.Clone(credentials) }

// IsCredential reports whether name must be excluded from helper environments.
func IsCredential(name string) bool { return slices.Contains(credentials, name) }
