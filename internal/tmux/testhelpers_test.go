package tmux

import "os"

// readFileAtTestData returns the contents of a file in this package's
// directory. The function exists so the structural-grep test can read a
// sibling .go file without callers having to handle path resolution.
func readFileAtTestData(name string) ([]byte, error) {
	return os.ReadFile(name)
}
