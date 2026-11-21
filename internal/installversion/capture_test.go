//go:build linux || darwin

package installversion

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// b.kii / accepted F2: model Darwin returning the original invocation link.
// Publication happens across a causal barrier BEFORE its first resolution,
// or after capture but before child creation. All layout/ready bytes and
// publication operations are real; only the loader's returned path is injected.
func TestCaptureExecutableAcrossPublication(t *testing.T) {
	for _, beforeResolution := range []bool{true, false} {
		name := "after capture"
		if beforeResolution {
			name = "before resolution (Darwin model)"
		}
		t.Run(name, func(t *testing.T) {
			f := newPublicationFixture(t)
			publishOK(t, f.root, f.a)
			own := assertSelected(t, f.root, f.a.Facts.Version)
			entry := filepath.Join(f.root, "bin", "agent-director")
			entered, release := make(chan struct{}), make(chan struct{})
			t.Cleanup(func() {
				select {
				case <-release:
				default:
					close(release)
				}
			})
			type outcome struct {
				path string
				err  error
			}
			done := make(chan outcome, 1)
			go func() {
				got, err := captureExecutable(func() (string, error) { return entry, nil }, func(path string) (string, error) {
					if beforeResolution {
						close(entered)
						<-release
					}
					return filepath.EvalSymlinks(path)
				}, f.a.Facts.Version)
				done <- outcome{got, err}
			}()
			if beforeResolution {
				select {
				case <-entered:
				case <-time.After(5 * time.Second):
					t.Fatal("capture never reached resolution barrier")
				}
				publishOK(t, f.root, f.b)
				close(release)
			}
			var got outcome
			select {
			case got = <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("capture did not finish")
			}
			if got.err != nil || got.path != own.RuntimeExecutable {
				t.Fatalf("captured %q, %v; want running A %q", got.path, got.err, own.RuntimeExecutable)
			}
			if !beforeResolution {
				publishOK(t, f.root, f.b)
			}
			if resolved, err := filepath.EvalSymlinks(entry); err != nil || resolved == got.path {
				t.Fatalf("publication did not move permanent entry: %q, %v", resolved, err)
			}
			out, err := exec.Command(got.path).Output()
			if err != nil || string(out) != f.a.Facts.Version+"\n" {
				t.Fatalf("captured child executed %q, %v; want A", out, err)
			}
		})
	}
}

type cancelCaptureRead struct {
	cancel context.CancelFunc
	reads  int
}

func (r *cancelCaptureRead) Read(p []byte) (int, error) {
	r.reads++
	p[0] = 'x'
	r.cancel()
	return 1, nil
}

func (r *cancelCaptureRead) WriteTo(io.Writer) (int64, error) {
	panic("hash copy bypassed cancellation reader")
}

// CR3's component half: retained-resource hashing notices cancellation during
// a read, including readers implementing WriterTo; no extra chunks are read.
func TestInstalledDiscoveryCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	r := &cancelCaptureRead{cancel: cancel}
	_, err := io.Copy(io.Discard, contextReader{ctx: ctx, r: r})
	if !errors.Is(err, context.Canceled) || r.reads != 1 {
		t.Fatalf("hash cancellation: %v reads=%d", err, r.reads)
	}
	f := newPublicationFixture(t)
	publishOK(t, f.root, f.a)
	before := snapshot(t, f.root)
	if _, err := ListInstalledContext(ctx, f.root); !errors.Is(err, context.Canceled) {
		t.Fatalf("discovery after cancellation: %v", err)
	}
	assertSame(t, snapshot(t, f.root), before)
}

func TestCaptureExecutableBoundaries(t *testing.T) {
	for _, name := range []string{"immutable own image", "outside symlink", "outside current directory", "outside unresolved", "own image missing", "own image unready", "broken current", "entry redirected outside"} {
		t.Run(name, func(t *testing.T) {
			f := newPublicationFixture(t)
			publishOK(t, f.root, f.a)
			own := assertSelected(t, f.root, f.a.Facts.Version)
			entry, stamp, want := own.RuntimeExecutable, f.a.Facts.Version, own.RuntimeExecutable
			wantError := false
			resolve := filepath.EvalSymlinks
			switch name {
			case "outside current directory":
				// CR2: current is an ordinary build directory, with no AD layout.
				entry = filepath.Join(t.TempDir(), "current", "bin", "agent-director")
				must(t, os.MkdirAll(filepath.Dir(entry), 0700))
				must(t, os.WriteFile(entry, []byte("build"), 0700))
				want = entry
			case "outside symlink", "outside unresolved":
				entry = filepath.Join(t.TempDir(), "build-link")
				must(t, os.Symlink(f.a.Runtime, entry))
				want = f.a.Runtime
				if name == "outside unresolved" {
					resolve = func(string) (string, error) { return "", errors.New("unresolved outside") }
					wantError = true
				}
			case "own image missing":
				stamp, wantError = "absent", true
			case "own image unready":
				must(t, os.Remove(filepath.Join(own.Directory, "ready")))
				wantError = true
			case "broken current":
				entry = filepath.Join(f.root, "bin", "agent-director")
				must(t, os.Remove(filepath.Join(f.root, "current")))
				must(t, os.Symlink("missing-version", filepath.Join(f.root, "current")))
				stamp, wantError = "absent", true
			case "entry redirected outside":
				entry = filepath.Join(f.root, "bin", "agent-director")
				must(t, os.Remove(entry))
				must(t, os.Symlink(f.b.Runtime, entry))
				stamp, wantError = "absent", true
			}
			before := snapshot(t, f.root)
			got, err := captureExecutable(func() (string, error) { return entry, nil }, resolve, stamp)
			if wantError {
				if err == nil || got != "" {
					t.Fatalf("unsafe capture = %q, %v; want refusal without fallback", got, err)
				}
			} else if err != nil || got != want {
				t.Fatalf("capture = %q, %v; want %q", got, err, want)
			}
			assertSame(t, snapshot(t, f.root), before)
		})
	}
}
