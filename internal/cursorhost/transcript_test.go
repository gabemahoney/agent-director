//go:build linux

package cursorhost

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
)

func transcriptBinding() store.CursorRuntime {
	return store.CursorRuntime{InstanceID: "synthetic-row", Token: "synthetic-launch", Life: 2, Connection: "synthetic-connection", SessionID: "synthetic-session"}
}
func newTestTranscript(t *testing.T) *transcript {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	tr, err := openTranscript(home, transcriptBinding())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tr.file.Close() })
	return tr
}
func TestTranscriptOrderedAttributionAndReopen(t *testing.T) {
	t.Setenv("CURSOR_API_KEY", "synthetic-auth-secret")
	t.Setenv("AGENT_DIRECTOR_LABEL_TEAM", "synthetic-raw-launch-label")
	tr := newTestTranscript(t)
	for _, kind := range []string{"user_input", "update", "authorization_denied", "turn_outcome"} {
		if err := tr.record(kind, map[string]any{"text": "多行\n🦋"}); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(tr.path)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"synthetic-auth-secret", "synthetic-raw-launch-label", "CURSOR_API_KEY"} {
		if bytes.Contains(raw, []byte(secret)) {
			t.Fatal("environment secret/raw label leaked into transcript metadata")
		}
	}
	if err := tr.file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := VerifyTranscriptPath(tr.path); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(tr.path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var kinds []string
	s := bufio.NewScanner(f)
	for s.Scan() {
		var rec map[string]any
		if err = json.Unmarshal(s.Bytes(), &rec); err != nil {
			t.Fatal(err)
		}
		kinds = append(kinds, rec["kind"].(string))
		if rec["sequence"] != float64(len(kinds)) || rec["instance_id"] != "synthetic-row" || rec["life_number"] != float64(2) || rec["connection_id"] != "synthetic-connection" || rec["session_id"] != "synthetic-session" {
			t.Fatalf("wrong attribution: %s", s.Bytes())
		}
	}
	if err = s.Err(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(kinds, []string{"launch", "user_input", "update", "authorization_denied", "turn_outcome"}) {
		t.Fatalf("record order: %v", kinds)
	}
	if st, err := os.Stat(tr.path); err != nil || st.Mode().Perm() != 0600 {
		t.Fatalf("protection %v %v", st, err)
	}
}
func TestTranscriptRefusesUnsafePaths(t *testing.T) {
	for _, tc := range []string{"relative home", "traversal", "symlink ancestor", "symlink transcript dir", "public transcript dir"} {
		t.Run(tc, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			base := filepath.Join(home, ".config", "agent-director")
			switch tc {
			case "relative home":
				home = "relative"
			case "traversal":
				home += "/../escape"
			case "symlink ancestor":
				if err := os.Symlink(t.TempDir(), filepath.Join(home, ".config")); err != nil {
					t.Fatal(err)
				}
			case "symlink transcript dir", "public transcript dir":
				if err := os.MkdirAll(base, 0700); err != nil {
					t.Fatal(err)
				}
				p := filepath.Join(base, "cursor-transcripts")
				if tc == "symlink transcript dir" {
					if err := os.Symlink(t.TempDir(), p); err != nil {
						t.Fatal(err)
					}
				} else {
					if err := os.Mkdir(p, 0755); err != nil {
						t.Fatal(err)
					}
				}
			}
			if tr, err := openTranscript(home, transcriptBinding()); err == nil {
				tr.file.Close()
				t.Fatal("unsafe transcript accepted")
			}
		})
	}
}
func TestTranscriptAppendFailureAndReplacement(t *testing.T) {
	for _, tc := range []string{"closed writer", "replacement", "symlink", "hardlink", "mode"} {
		t.Run(tc, func(t *testing.T) {
			tr := newTestTranscript(t)
			switch tc {
			case "closed writer":
				tr.file.Close()
			case "replacement":
				if err := os.Rename(tr.path, tr.path+".old"); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(tr.path, []byte("replacement\n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Remove(tr.path); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(t.TempDir(), "other"), tr.path); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				if err := os.Link(tr.path, tr.path+".copy"); err != nil {
					t.Fatal(err)
				}
			case "mode":
				if err := os.Chmod(tr.path, 0644); err != nil {
					t.Fatal(err)
				}
			}
			if err := tr.record("user_input", map[string]string{"text": "must not append"}); err == nil {
				t.Fatal("unsafe append succeeded")
			}
		})
	}
}
