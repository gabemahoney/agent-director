package store

import (
	"context"
	"testing"
)

// ObservePromptRowsForTest observes actual COUNT rows and transaction statements.
func ObservePromptRowsForTest(t *testing.T, s *Store, path string, before func(context.Context, string) error, after func(context.Context, string, error)) func() {
	b, _ := installNativeBoundary(t, s, path)
	b.before, b.afterNext = before, after
	return func() { b.stopObservingAfterCleanup(t, s) }
}
