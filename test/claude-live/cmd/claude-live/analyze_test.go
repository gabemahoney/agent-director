package main

import (
	"bytes"
	"encoding/json"
	"github.com/gabemahoney/agent-director/test/claude-live/internal/signalsfix"
	"github.com/gabemahoney/agent-director/test/claude-live/runner"
	"github.com/gabemahoney/agent-director/test/claude-live/signals"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestClaudeLiveAnalyzeCLI(t *testing.T) {
	for _, kind := range []string{"passing", "changed-file-event", "missing-continuation-exercise", "missing-manifest"} {
		t.Run(kind, func(t *testing.T) {
			f := signalsfix.Copy(t)
			want := 0
			if kind == "changed-file-event" {
				f.Hooks("FileChanged", 0, func(r runner.Row) { r["event"] = "rename" })
				f.Reindex()
				want = 1
			}
			if kind == "missing-continuation-exercise" {
				var cases []runner.Case
				for _, x := range f.Manifest.Cases {
					if x.ID != "wake-stop-continuation" {
						cases = append(cases, x)
					}
				}
				f.Manifest.Cases = cases
				f.Reindex()
				want = 1
			}
			if kind == "missing-manifest" {
				if e := os.Remove(filepath.Join(f.Root, "manifest.json")); e != nil {
					t.Fatal(e)
				}
				want = 1
			}
			output := filepath.Join(t.TempDir(), "verdict.json")
			var out, errout bytes.Buffer
			got := run([]string{"analyze", "--evidence", f.Root, "--out", output}, &out, &errout)
			if got != want || errout.Len() != 0 {
				t.Fatalf("exit=%d want=%d stderr=%s stdout=%s", got, want, errout.String(), out.String())
			}
			b, e := os.ReadFile(output)
			if e != nil {
				t.Fatal(e)
			}
			var v runner.Verdict
			if e = json.Unmarshal(b, &v); e != nil {
				t.Fatal(e)
			}
			callable := signals.Analyze(f.Root)
			expected, _ := json.Marshal(callable)
			var other runner.Verdict
			if e = json.Unmarshal(expected, &other); e != nil {
				t.Fatal(e)
			}
			if !reflect.DeepEqual(v, other) || out.String() != signals.Format(callable) {
				t.Fatal("callable, verdict file and printed result differ")
			}
			if !strings.Contains(out.String(), "steer-wake-order: named, unmeasured") || len(v.NamedPremises) != 1 {
				t.Fatal("R-a absent")
			}
			if kind == "changed-file-event" {
				found := false
				for _, r := range v.Results {
					if r.SignalID == "file-changed" && r.Status == "fail" {
						found = true
					}
				}
				if !found {
					t.Fatal("missing named failure")
				}
			}
			if kind == "missing-continuation-exercise" && v.Status != "not_completed" {
				t.Fatalf("incomplete exercise changed status: %s", v.Status)
			}
			before := append([]byte(nil), b...)
			out.Reset()
			errout.Reset()
			if run([]string{"analyze", "--evidence", f.Root, "--out", output}, &out, &errout) != 1 {
				t.Fatal("existing verdict overwritten")
			}
			after, _ := os.ReadFile(output)
			if !bytes.Equal(before, after) {
				t.Fatal("existing verdict changed")
			}
		})
	}
}
