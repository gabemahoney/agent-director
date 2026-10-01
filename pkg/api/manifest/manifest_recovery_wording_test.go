package manifest_test

import (
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// TestRecoveryGoDocSites pins SR-18.4's recourse (AC-DOC-03) on each resume
// error's doc comment in pkg/api/errors.go and its Client.Resume "Errors:" bullet.
func TestRecoveryGoDocSites(t *testing.T) {
	c := apitest.DescReuseRecourse(apitest.RecourseGoDoc)
	for _, name := range []string{"ErrNoSessionId", "ErrJsonlMissing", "ErrJsonlNeverWritten"} {
		t.Run(name, func(t *testing.T) {
			doc := strings.Join(strings.Fields(apiDeclDoc(t, name)), " ")
			apitest.AssertAgentTextCase(t, name+" doc comment", doc, c)
			apitest.AssertAgentTextCase(t, "(*Client).Resume Errors: "+name, goDocErrorBulletText(t, "Resume", name), c)
		})
	}
}
