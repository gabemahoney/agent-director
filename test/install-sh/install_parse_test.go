package installsh_test

// SR-2.16: a truncated piped installer changes nothing before its complete parse.
import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallParseBeforeFirstChange(t *testing.T) {
	script, err := os.ReadFile(filepath.Join("..", "..", "skills", "install-agent-director", "install.sh"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(script)
	open, close := strings.Index(s, "\n{\n"), strings.LastIndex(s, "\n} ; exit\n")
	if open < 0 || close < open {
		t.Fatal("install.sh is missing its enclosing brace group ({ / } ; exit)")
	}
	middle := len(s) / 2
	middle += strings.IndexByte(s[middle:], '\n') + 1
	for _, tc := range []struct {
		name string
		cut  int
	}{{"after_open", open + 3}, {"middle", middle}, {"before_close", close + 1}} {
		t.Run(tc.name, func(t *testing.T) {
			h := newMoveHome(t)
			h.BuildOld()
			h.Flags = append(h.Flags, "--journal-maintenance-window", "--journal-backup", filepath.Join(h.Root, "backup"))
			cut := filepath.Join(h.Root, "truncated-install.sh")
			h.Write(cut, s[:tc.cut], 0o600)
			before := h.PhysicalSnapshot()
			tmp := *h
			tmp.Home = h.Tmp
			tmpBefore := tmp.PhysicalSnapshot()
			res := schedRunArgv(t, h, cut, true, schedArgv(h))
			if res.Code != 2 || !strings.Contains(res.Stderr, "syntax error: unexpected end of file") || strings.Contains(res.Stderr, "install.sh:") || res.Stdout != "" {
				t.Errorf("truncated script: %+v", res)
			}
			if diff := moveDiff(before, h.PhysicalSnapshot()); diff != "" {
				t.Errorf("HOME changed before complete parse:\n%s", diff)
			}
			if diff := moveDiff(tmpBefore, tmp.PhysicalSnapshot()); diff != "" {
				t.Errorf("TMPDIR changed before complete parse:\n%s", diff)
			}
		})
	}
}
