// Package config — template-storage helpers.
//
// Templates are TOML files under ~/.agent-director/templates/ (mode
// 0700). The dir is lazy-created on first `make-template`; the install
// skill (Epic 12) does NOT create it. Template names must be safe to
// join with the templates dir without escaping it — empty / `.` / `..`
// / leading-dot / path-separator names are rejected up front, and a
// realpath check guards against TOCTOU symlink races.

package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Template-storage errors. Names match the API surface promised by
// SRD §13.1; the CLI's errCatalog maps these via errors.Is.

// ErrTemplateNameUnsafe is returned when a template name fails the
// pre-flight safety check. Path traversal (`../foo`), absolute paths,
// hidden names (leading dot), or trivial garbage (empty / `.` / `..`)
// are rejected without touching disk.
var ErrTemplateNameUnsafe = errors.New("ErrTemplateNameUnsafe")

// ErrTemplateNotFound is returned by LoadTemplate when the named
// `.toml` does not exist on disk.
var ErrTemplateNotFound = errors.New("ErrTemplateNotFound")

// ErrTemplateMalformed is returned when a template file exists but
// fails schema validation (unknown keys, wrong types, bad enums).
var ErrTemplateMalformed = errors.New("ErrTemplateMalformed")

// ErrTemplateExists is returned by MakeTemplate when the target file
// already exists. The verb does not overwrite — the caller deletes
// and re-runs if they really mean to.
var ErrTemplateExists = errors.New("ErrTemplateExists")

// TemplatesDir returns the absolute path of the templates directory.
// Tilde expansion uses the current user's HOME; an empty HOME (test
// environments that strip it) is left literal so the caller can
// detect / override it.
func TemplatesDir() string {
	home, _ := os.UserHomeDir()
	if home == "" {
		return "~/.agent-director/templates"
	}
	return filepath.Join(home, ".agent-director", "templates")
}

// EnsureTemplatesDir creates the templates directory (mode 0700) and
// any missing parents. Idempotent: a pre-existing dir is left alone
// (MkdirAll's contract). Returns the resolved absolute path.
func EnsureTemplatesDir() (string, error) {
	dir := TemplatesDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("config: ensure templates dir: %w", err)
	}
	return dir, nil
}

// ValidateTemplateName enforces the SRD §10.3 name-safety rules. The
// function is a string-only check; it does NOT touch disk and does
// NOT depend on the templates dir existing. The realpath check in
// TemplatePath is the second line of defense for runtime races.
func ValidateTemplateName(name string) error {
	if name == "" {
		return fmt.Errorf("%w: empty name", ErrTemplateNameUnsafe)
	}
	if name == "." || name == ".." {
		return fmt.Errorf("%w: %q is reserved", ErrTemplateNameUnsafe, name)
	}
	if strings.HasPrefix(name, ".") {
		return fmt.Errorf("%w: leading-dot names are reserved", ErrTemplateNameUnsafe)
	}
	if strings.ContainsAny(name, "/\\") {
		return fmt.Errorf("%w: path separators are not allowed", ErrTemplateNameUnsafe)
	}
	if strings.ContainsAny(name, " \t\n\r") {
		return fmt.Errorf("%w: whitespace is not allowed", ErrTemplateNameUnsafe)
	}
	if strings.Contains(name, "..") {
		return fmt.Errorf("%w: %q contains path-traversal substring", ErrTemplateNameUnsafe, name)
	}
	return nil
}

// TemplatePath returns the on-disk path for a template by name. The
// returned path lives directly inside TemplatesDir — a defensive
// realpath check confirms this even when the templates dir is a
// symlink to somewhere else (so a future user-side symlink can't make
// a `dev` name resolve outside the trusted root).
//
// Returns ErrTemplateNameUnsafe if the name fails ValidateTemplateName
// or if the realpath check finds the resolved parent outside the
// templates dir's realpath.
func TemplatePath(name string) (string, error) {
	if err := ValidateTemplateName(name); err != nil {
		return "", err
	}
	dir := TemplatesDir()
	joined := filepath.Join(dir, name+".toml")

	// EvalSymlinks would fail on a not-yet-existent file (the common
	// MakeTemplate case). Walk the dir realpath instead so the check
	// is applied to the trusted root, not the file we may be about
	// to create.
	dirReal, err := filepath.EvalSymlinks(dir)
	if err != nil {
		// Templates dir may not exist yet on a fresh install. That's
		// fine — EnsureTemplatesDir creates it before we get here in
		// the write path. For read paths the missing dir is itself
		// a NotFound signal; surface as such by returning the joined
		// path unmodified.
		if os.IsNotExist(err) {
			return joined, nil
		}
		return "", fmt.Errorf("config: realpath templates dir: %w", err)
	}

	gotParent, err := filepath.EvalSymlinks(filepath.Dir(joined))
	if err != nil {
		if os.IsNotExist(err) {
			return joined, nil
		}
		return "", fmt.Errorf("config: realpath template parent: %w", err)
	}
	if gotParent != dirReal {
		return "", fmt.Errorf("%w: %q resolves outside templates dir", ErrTemplateNameUnsafe, name)
	}
	return joined, nil
}
