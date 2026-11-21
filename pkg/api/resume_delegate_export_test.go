package api

import (
	"context"
	"time"

	"github.com/gabemahoney/agent-director/internal/resumedelegate"
)

// SetResumeDelegateRunnerForTest replaces only the process transport on c.
func SetResumeDelegateRunnerForTest(c *Client, run func(context.Context, string, resumedelegate.Request, time.Duration) ([]byte, bool, error)) {
	c.resumeRunner = run
}

// CapturedResumeDelegateForTest captures the original caller once, exposing the
// existing deadline argument for tests of already-spent call allowances.
func CapturedResumeDelegateForTest(c *Client, deadline time.Time) func(ResumeParams, string) (ResumeResult, error) {
	call := c.newResumeCall()
	return func(p ResumeParams, provider string) (ResumeResult, error) {
		return c.delegateResume(p, provider, call, deadline)
	}
}
