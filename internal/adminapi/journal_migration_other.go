//go:build !linux && !(darwin && arm64)

package adminapi

import "context"

// verifyJournalQuiescence on a non-Linux build other than darwin/arm64, such
// as darwin/amd64, has no evidence basis: its host is outside the supported
// install hosts, so it refuses with process_inventory_unsupported.
func verifyJournalQuiescence(context.Context, string, []string) error {
	return journalUnsupportedHost()
}
