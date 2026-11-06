//go:build !linux

package adminapi

import "context"

func verifyJournalQuiescence(context.Context, string, []string) error {
	return journalUnsupportedHost()
}
