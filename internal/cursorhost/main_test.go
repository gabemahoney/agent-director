package cursorhost

import (
	"github.com/gabemahoney/agent-director/internal/testsupport/sandboxguard"
	"os"
	"testing"
)

var cursorReportHelper func() int
var cursorTestCleanup func()

func TestMain(m *testing.M) {
	sandboxguard.Require()
	if os.Getenv("AD_TEST_CURSOR_REPORT_HELPER") == "1" && cursorReportHelper != nil {
		os.Exit(cursorReportHelper())
	}
	code := m.Run()
	if cursorTestCleanup != nil {
		cursorTestCleanup()
	}
	os.Exit(code)
}
