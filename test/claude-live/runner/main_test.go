package runner

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"testing"

	"github.com/gabemahoney/agent-director/internal/testsupport/sandboxguard"
)

func TestMain(m *testing.M) {
	sandboxguard.Require()
	if len(os.Args) == 2 && os.Args[1] == "runner-cancel-helper" {
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, syscall.SIGUSR1)
		fmt.Fprintln(os.Stdout, os.Getpid())
		<-ch
		os.Exit(0)
	}
	os.Exit(m.Run())
}
