// Package toolguardfix builds synthetic files and links for Guard decisions.
// This is a leaf test-support package: no production Guard or agent state.
// Must reuse: Guard tests and sandbox transcripts use New instead of rebuilding
// decoy layouts. Callers supply a fresh temporary root and own HOME/cwd setup
// and cleanup; the fixture never consults or changes the ambient HOME or cwd.
package toolguardfix

import (
	"fmt"
	"os"
	"path/filepath"
)

// Layout names the decoys; every path and link target is beneath Root.
// Entries includes directory, file, missing and linked Secret Paths. LateLink
// is initially absent: callers may link it to LateTarget after fixing Entries.
type Layout struct {
	Root, Home, Work                 string
	SecretDir, SecretDirFile         string
	SecretFile, MissingSecret        string
	ListedLink, ResolvedSecret       string
	LateLink, LateTarget             string
	WorkLink, ParentLink, DotDotLink string
	OrdinaryFile, SafeDir, SafeFile  string
	GlobRoot, GlobSecret             string
	Entries                          []string
}

// New creates the shared decoy layout in an existing, empty, absolute temporary
// root (not a symlink). Must reuse it for decision tests and sandbox drivers;
// it returns paths/errors without testing.T, payloads, verdicts or a runner.
// On error the caller still owns cleanup of any partially constructed layout.
func New(root string) (Layout, error) {
	if !filepath.IsAbs(root) {
		return Layout{}, fmt.Errorf("toolguardfix: temporary root must be absolute")
	}
	root = filepath.Clean(root)
	info, err := os.Lstat(root)
	if err != nil {
		return Layout{}, err
	}
	if !info.IsDir() {
		return Layout{}, fmt.Errorf("toolguardfix: temporary root must be a directory, not a link")
	}
	children, err := os.ReadDir(root)
	if err != nil {
		return Layout{}, err
	}
	if len(children) != 0 {
		return Layout{}, fmt.Errorf("toolguardfix: temporary root must be empty")
	}
	f := Layout{
		Root: root, Home: filepath.Join(root, "home"), Work: filepath.Join(root, "work"),
		ResolvedSecret: filepath.Join(root, "vault"), LateTarget: filepath.Join(root, "late-target"),
		SafeDir: filepath.Join(root, "safe"), GlobRoot: filepath.Join(root, "search", "root"),
		GlobSecret: filepath.Join(root, "search", "secret"),
	}
	f.SecretDir = filepath.Join(f.Home, ".ssh")
	f.SecretDirFile = filepath.Join(f.SecretDir, "key")
	f.SecretFile = filepath.Join(f.Home, "token")
	f.MissingSecret = filepath.Join(f.Home, "not-yet", "secret")
	f.ListedLink = filepath.Join(f.Home, "protected-link")
	f.LateLink = filepath.Join(f.Home, "late-secret")
	f.WorkLink = filepath.Join(f.Work, "secret-link")
	f.ParentLink = filepath.Join(f.Work, "home-link")
	f.DotDotLink = filepath.Join(f.Work, "detour")
	f.OrdinaryFile = filepath.Join(f.Work, "ordinary.txt")
	f.SafeFile = filepath.Join(f.SafeDir, "ordinary.txt")
	f.Entries = []string{f.SecretDir, f.SecretFile, f.MissingSecret, f.ListedLink, f.LateLink, f.GlobSecret}
	for _, dir := range []string{filepath.Join(f.SecretDir, "nested"), f.Work, f.ResolvedSecret, f.LateTarget, f.SafeDir, f.GlobRoot, f.GlobSecret} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return Layout{}, err
		}
	}
	for _, file := range []string{f.SecretDirFile, f.SecretFile, f.OrdinaryFile, f.SafeFile,
		filepath.Join(f.ResolvedSecret, "decoy"), filepath.Join(f.LateTarget, "decoy"), filepath.Join(f.GlobSecret, "decoy")} {
		if err := os.WriteFile(file, []byte("synthetic toolguard decoy\n"), 0o600); err != nil {
			return Layout{}, err
		}
	}
	for _, link := range [][2]string{
		{f.ListedLink, f.ResolvedSecret},
		{f.WorkLink, "../home/.ssh"},
		{f.ParentLink, f.Home},
		{f.DotDotLink, "../home/.ssh/nested"},
	} {
		if err := os.Symlink(link[1], link[0]); err != nil {
			return Layout{}, err
		}
	}
	return f, nil
}
