package spawn

import (
	"bytes"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/tmux"
)

// A31 (b.ma7): "run chmod g-w", "run chmod go-w", or "run chmod 00700"
// repairs the named unsafe directory before retrying preparation.
func TestAdviceFollow_A31_LaunchDirectoryRefusal(t *testing.T) {
	for _, tc := range []struct {
		name, relative, modeText, fix string
		mode                          os.FileMode
	}{
		{"group writable", ".config", "2775", "g-w", os.ModeSetgid | 0775},
		{"other writable", ".config", "2777", "go-w", os.ModeSetgid | 0777},
		{"private launch", ".config/agent-director/launch", "0755", "00700", 0755},
		{"setgid private launch", ".config/agent-director/launch", "2700", "00700", os.ModeSetgid | 0700},
		{"setuid private launch", ".config/agent-director/launch", "4700", "00700", os.ModeSetuid | 0700},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			path := filepath.Join(home, tc.relative)
			if err := os.MkdirAll(path, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, tc.mode); err != nil {
				t.Fatal(err)
			}
			c := laneFiles(t, "token")
			err := PrepareLaunchFiles(c, "store", "token", nil)
			detail := fmt.Sprintf("launch directory %q has mode %s; run chmod %s '%s'", path, tc.modeText, tc.fix, path)
			if !errors.Is(err, tmux.ErrTmuxSessionCreate) {
				t.Fatalf("lost refusal classification: %v", err)
			}
			if !strings.Contains(err.Error(), detail) || !strings.HasSuffix(err.Error(), "; nothing was launched or written to the store") {
				t.Errorf("refusal = %v; want detail %q and no-write assurance", err, detail)
			}
			entries, readErr := os.ReadDir(path)
			if readErr != nil || len(entries) != 0 {
				t.Fatalf("refusal wrote entries: %v, %v", entries, readErr)
			}
			var logs bytes.Buffer
			ReclaimLaunchFiles("store", "token", log.New(&logs, "", 0))
			if !strings.Contains(logs.String(), "open reclaim directory failed: "+detail) {
				t.Errorf("reclaim warning = %s; want same diagnostic %q", &logs, detail)
			}
			_, advice, found := strings.Cut(err.Error(), "; run ")
			if !found {
				t.Fatalf("missing printed chmod advice: %v", err)
			}
			advice, _, _ = strings.Cut(advice, "; nothing was launched or written to the store")
			if output, err := exec.Command("sh", "-c", advice).CombinedOutput(); err != nil {
				t.Fatalf("follow chmod advice: %s: %v", output, err)
			}
			if err := PrepareLaunchFiles(c, "store", "token", nil); err != nil {
				t.Fatalf("preparation after repair: %v", err)
			}
		})
	}
}
