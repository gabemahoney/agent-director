package toolguard

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
)

// pathResolver owns the Guard's two resolution orders and component comparison.
// Must reuse: every path rule, including Execute's Secret Path spellings, uses
// this owner with the original decision context. It reads metadata/links only;
// no resolved name survives a decision. The function fields and foldCase are
// private seams for lookup failures, causal cancellation and Darwin comparison.
type pathResolver struct {
	ctx      context.Context
	lstat    func(string) (os.FileInfo, error)
	readlink func(string) (string, error)
	foldCase bool
}

func newPathResolver(ctx context.Context) pathResolver {
	return pathResolver{ctx: ctx, lstat: os.Lstat, readlink: os.Readlink, foldCase: runtime.GOOS == "darwin"}
}

// pathForms retains the uncleaned candidates as well as both resolved orders.
// A Glob suffix needs the original Search Root too: cleaning or resolving that
// root before appending ".." could erase one interpretation of the whole path.
type pathForms struct {
	absolute []string
	resolved []string
}

// secretForms retains each entry literally for Execute, alongside both resolved
// forms. Recompute it for each applicable call, even for initially absent links.
func (r pathResolver) secretForms(entries []string) ([]string, error) {
	var forms []string
	for _, entry := range entries {
		resolved, err := r.resolve(entry)
		if err != nil {
			return nil, err
		}
		forms = append(forms, entry)
		forms = append(forms, resolved...)
	}
	return forms, nil
}

// toolForms makes only relative paths depend on bases. Tilde paths need both
// their literal relative interpretation and HOME; neither replaces the other.
func (r pathResolver) toolForms(path string, bases []string) (pathForms, error) {
	var result pathForms
	if strings.IndexByte(path, 0) >= 0 {
		return result, fs.ErrInvalid
	}
	if filepath.IsAbs(path) {
		result.absolute = []string{path}
	} else {
		if len(bases) == 0 {
			return result, fs.ErrInvalid
		}
		for _, base := range bases {
			if !filepath.IsAbs(base) || strings.IndexByte(base, 0) >= 0 {
				return result, fs.ErrInvalid
			}
			result.absolute = append(result.absolute, rawJoin(base, path))
		}
		if path == "~" || strings.HasPrefix(path, "~/") {
			home := os.Getenv("HOME")
			if !filepath.IsAbs(home) || strings.IndexByte(home, 0) >= 0 {
				return result, fs.ErrInvalid
			}
			result.absolute = append(result.absolute, home+path[1:])
		}
	}
	for _, absolute := range result.absolute {
		resolved, err := r.resolve(absolute)
		if err != nil {
			return pathForms{}, err
		}
		result.resolved = append(result.resolved, resolved...)
	}
	return result, nil
}

// rawJoin deliberately preserves dot components until both orders see them.
func rawJoin(base, suffix string) string { return base + "/" + suffix }

func (r pathResolver) resolve(absolute string) ([]string, error) {
	if !filepath.IsAbs(absolute) || strings.IndexByte(absolute, 0) >= 0 {
		return nil, fs.ErrInvalid
	}
	lexical, err := r.walk(filepath.Clean(absolute))
	if err != nil {
		return nil, err
	}
	kernel, err := r.walk(absolute)
	if err != nil {
		return nil, err
	}
	if lexical == kernel {
		return []string{lexical}, nil
	}
	return []string{lexical, kernel}, nil
}

// maxPathLinks bounds traversal independently of the deadline, as an OS path
// lookup does. A loop or longer chain is a failure, never a missing prefix.
const maxPathLinks = 40

// walk resolves links before consuming the next component, including "..".
// Only ENOENT ends the existing prefix. In particular a file before "/.." or
// a trailing slash is ENOTDIR, even though lexical cleaning could hide it.
func (r pathResolver) walk(absolute string) (string, error) {
	prefix, pending := "/", absolute[1:]
	directory, links := true, 0
	for {
		if err := r.ctx.Err(); err != nil {
			return "", err
		}
		if !directory {
			return "", syscall.ENOTDIR
		}
		component, rest, more := strings.Cut(pending, "/")
		switch component {
		case "", ".":
		case "..":
			prefix = filepath.Dir(prefix)
		default:
			next := strings.TrimSuffix(prefix, "/") + "/" + component
			info, err := r.lstat(next)
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					return filepath.Clean(rawJoin(next, rest)), nil
				}
				return "", err
			}
			if info.Mode()&os.ModeSymlink != 0 {
				links++
				if links > maxPathLinks {
					return "", syscall.ELOOP
				}
				target, err := r.readlink(next)
				if err != nil {
					return "", err
				}
				if target == "" || strings.IndexByte(target, 0) >= 0 {
					return "", fs.ErrInvalid
				}
				pending = target
				if filepath.IsAbs(target) {
					prefix, pending = "/", target[1:]
				}
				if more {
					pending += "/" + rest
				}
				continue
			}
			prefix, directory = next, info.IsDir()
		}
		if !more {
			return prefix, nil
		}
		pending = rest
	}
}

// within compares whole components in either direction (swap arguments for
// ancestry). Clean only here: resolution has already retained both orders.
func within(path, root string, foldCase bool) bool {
	path, root = filepath.Clean(path), filepath.Clean(root)
	if root == "/" {
		return filepath.IsAbs(path)
	}
	pathParts, rootParts := strings.Split(path, "/"), strings.Split(root, "/")
	if len(pathParts) < len(rootParts) {
		return false
	}
	for i, part := range rootParts {
		if pathParts[i] != part && !(foldCase && strings.EqualFold(pathParts[i], part)) {
			return false
		}
	}
	return true
}

func (r pathResolver) check(forms, secrets []string, ancestors bool) reason {
	for _, form := range forms {
		for _, secret := range secrets {
			if r.ctx.Err() != nil {
				return checkFailed
			}
			if within(form, secret, r.foldCase) || (ancestors && within(secret, form, r.foldCase)) {
				return secretPath
			}
		}
	}
	return noDecision
}
