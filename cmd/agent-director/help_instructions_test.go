package main_test

import (
	"github.com/gabemahoney/agent-director/internal/helptext"
	"testing"
)

func TestHelpPlainMatchesMCPInstructionRenderer(t *testing.T) {
	want := helpVerbStdout(t)
	got, err := helptext.Plain()
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("instruction bytes=%d plain CLI bytes=%d; content must match including final newline", len(got), len(want))
	}
}
