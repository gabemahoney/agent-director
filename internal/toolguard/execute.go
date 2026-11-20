package toolguard

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// decideExecute checks literal spellings, not shell syntax or carried fields.
// Secret forms, case comparison and cancellation belong to the same resolver
// as File Tools; no command target is opened or resolved.
func decideExecute(p payload, entries []string, paths pathResolver) reason {
	home := os.Getenv("HOME")
	if !filepath.IsAbs(home) || strings.IndexByte(home, 0) >= 0 {
		return checkFailed
	}
	secrets, err := paths.secretForms(entries)
	if err != nil {
		return checkFailed
	}
	command, _ := p.text("command")
	for _, secret := range secrets {
		spellings := []string{secret}
		if within(secret, home, paths.foldCase) {
			// Count components instead of using filepath.Rel: the latter's
			// case-sensitive base comparison would lose Darwin HOME spellings.
			base := strings.Split(filepath.Clean(home), "/")
			parts := strings.Split(filepath.Clean(secret), "/")
			if filepath.Clean(home) == "/" {
				base = base[:1]
			}
			rel := strings.Join(parts[len(base):], "/")
			// Retain a listed suffix literally as well (e.g. HOME/./entry).
			prefix := strings.TrimRight(home, "/") + "/"
			rels := []string{rel}
			if strings.HasPrefix(secret, prefix) {
				rels = append(rels, strings.TrimPrefix(secret, prefix))
			}
			for _, relative := range rels {
				for _, prefix := range []string{"~/", "$HOME/", "${HOME}/"} {
					spellings = append(spellings, prefix+relative)
				}
			}
		}
		for _, spelling := range spellings {
			if paths.ctx.Err() != nil {
				return checkFailed
			}
			// SR-6.4 excludes slash only at the start boundary. A trailing
			// slash already supplies the end boundary, including the root entry.
			pattern := `(^|[^\pL\p{Nd}._/-])` + regexp.QuoteMeta(spelling)
			if !strings.HasSuffix(spelling, "/") {
				pattern += `($|[^\pL\p{Nd}._-])`
			}
			if paths.foldCase {
				pattern = "(?i)" + pattern
			}
			name, err := regexp.Compile(pattern)
			if err != nil {
				return checkFailed
			}
			if name.MatchString(command) {
				return secretPath
			}
		}
	}
	return noDecision
}
