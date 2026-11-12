package installsh_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// b.td5: every documented release install fetches that release's installer.
// Reuse b.5i3's upgradeNetworkInstall rule for --from-release commands.
func TestReleaseInstallerDocs(t *testing.T) {
	fetch := regexp.MustCompile(`https://raw\.githubusercontent\.com/gabemahoney/agent-director/([^/\s]+)/skills/install-agent-director/install\.sh`)
	blocks := regexp.MustCompile("(?ms)^[ \\t]*```[^\\n]*\\n(.*?)^[ \\t]*```")
	download := regexp.MustCompile(`https://github\.com/gabemahoney/agent-director/releases/download/([^/\s]+)`)
	tag := regexp.MustCompile(`^(<tag>|v[0-9]+\.[0-9]+\.[0-9]+([-+][0-9A-Za-z.-]+)?)$`)
	for _, file := range []string{"README.md", "docs/install.md", "docs/architecture.md", "skills/install-agent-director/SKILL.md"} {
		t.Run(file, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("..", "..", file))
			if err != nil {
				t.Fatal(err)
			}
			seen := 0
			for _, block := range blocks.FindAllStringSubmatch(string(data), -1) {
				for _, line := range strings.Split(block[1], "\n") {
					m := fetch.FindStringSubmatch(line)
					if m == nil {
						continue
					}
					seen++
					command := strings.TrimSpace(line)
					if strings.Contains(command, "--from-release") {
						upgradeNetworkInstall(t, command+" ")
					} else {
						release := download.FindStringSubmatch(block[1])
						if !tag.MatchString(m[1]) || release == nil || release[1] != m[1] {
							t.Errorf("installer ref %q must match its binaries' releases/download/<tag> URL, never main: %s", m[1], block[1])
						}
					}
				}
			}
			if total := len(fetch.FindAll(data, -1)); seen == 0 || seen != total {
				t.Errorf("checked %d installer fetches in command blocks, want every fetch (%d) and at least one", seen, total)
			}
		})
	}
}
