//go:build linux || darwin

package main_test

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/installversion"
)

func TestVersionAdminPrivateFactsIgnoresHookStdin(t *testing.T) {
	home := poisonedHome(t)
	before := dirEntries(t, home)
	reader, writer, err := os.Pipe()
	versionMust(t, err)
	defer reader.Close()
	defer writer.Close() // Remains open and empty until the query exits.
	env := append(fakeTmuxEnv(t, home), "HOME="+home, "AGENT_DIRECTOR_INSTANCE_ID=private-facts-must-not-report")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, mainPath, "--install-version-facts")
	cmd.Env = env
	cmd.Stdin = reader
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	versionMust(t, cmd.Run())
	if stderr.Len() != 0 {
		t.Fatalf("private query stderr=%q", stderr.String())
	}
	var facts installversion.Facts
	versionJSON(t, stdout.String(), &facts)
	if !facts.Equal(versionFactsOf(t, adminPath, home, false)) {
		t.Fatalf("private facts=%+v", facts)
	}
	if got := dirEntries(t, home); len(got) != len(before) || got["config.toml"] != before["config.toml"] || got["state.db"] != before["state.db"] {
		t.Fatalf("private query changed runtime resources: %v", got)
	}
	assertInvocationKinds(t, home)
}
