package api

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/installversion"
	"github.com/gabemahoney/agent-director/internal/layout"
	"github.com/gabemahoney/agent-director/internal/resumedelegate"
)

// CR4: the sender drains the whole producer stream but retains at most the
// same full-wire MaxBytes the encoder and decoder accept, across write chunks.
func TestResumeDelegateCollectorWireLimit(t *testing.T) {
	t.Parallel()
	for _, extra := range []int{0, 1} {
		var output cappedResumeOutput
		input := bytes.Repeat([]byte("x"), resumedelegate.MaxBytes+extra)
		for _, chunk := range [][]byte{input[:17], input[17 : len(input)-1], input[len(input)-1:]} {
			if n, err := output.Write(chunk); err != nil || n != len(chunk) {
				t.Fatalf("did not drain producer: %d,%v", n, err)
			}
		}
		if output.Len() != resumedelegate.MaxBytes || output.overflow != (extra != 0) {
			t.Fatalf("extra %d: retained %d overflow=%v", extra, output.Len(), output.overflow)
		}
	}
}

// CR3: discovery must receive the already-captured absolute deadline before
// any covering child exists. A stalled discovery consumes that allowance;
// it cannot give transport a new budget or become no-covering-version advice.
func TestResumeDelegationStalledDiscoveryDeadline(t *testing.T) {
	t.Parallel()
	deadline := time.Now().Add(250 * time.Millisecond)
	entered, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	discoveries, runs := 0, 0
	c := &Client{home: t.TempDir()}
	c.resumeVersions = func(ctx context.Context, root string) ([]installversion.InstalledVersion, error) {
		discoveries++
		if got, ok := ctx.Deadline(); !ok || !got.Equal(deadline) || root != layout.Dir(c.home) {
			return nil, errors.New("discovery lost original deadline or root")
		}
		close(entered)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-release:
			return nil, errors.New("test released stalled discovery")
		}
	}
	c.resumeRunner = func(context.Context, string, resumedelegate.Request, time.Duration) ([]byte, bool, error) {
		runs++
		return nil, false, errors.New("unexpected child")
	}
	done := make(chan error, 1)
	go func() {
		_, err := c.delegateResume(ResumeParams{InstanceID: "row"}, "codex", resumeCall{}, deadline)
		done <- err
	}()
	select {
	case <-entered:
	case err := <-done:
		t.Fatalf("never entered bounded discovery: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("discovery did not start")
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "no delegate started") || ErrDetails(err) != nil || discoveries != 1 || runs != 0 {
			t.Fatalf("stalled discovery: %v, discoveries=%d children=%d", err, discoveries, runs)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("discovery outlived original deadline")
	}
}
