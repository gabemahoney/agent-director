// Package setup reads macrun's setup files under test/mac/. Each follows
// test/docker-epics.txt's conventions: one entry per line, lines that are
// blank or start with # ignored, fields separated by tabs. Every parser
// collects all of a file's errors rather than stopping at the first. Every
// macrun reader of a test/mac/ file uses this package.
package setup

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/gabemahoney/agent-director/tools/macrun/internal/model"
)

// line is one entry of a setup file.
type line struct {
	number int
	fields []string
}

// readLines reads the setup file at file, a slash-separated path relative to
// root, and returns its entries. It skips a line whose first non-whitespace
// character is #, and a line that is empty or holds only whitespace, as the
// Makefile's grep -vE '^[[:space:]]*(#|$)' does for test/docker-epics.txt. A
// missing file, or one under a regular file (ENOTDIR), has no entries; any
// other read fault is an error.
func readLines(root, file string) ([]line, error) {
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(file)))
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
		return nil, nil
	}
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		return nil, fmt.Errorf("read %s: %w", file, pathErr.Err)
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", file, err)
	}
	var lines []line
	for i, text := range strings.Split(string(data), "\n") {
		text = strings.TrimSuffix(text, "\r")
		trimmed := strings.TrimSpace(text)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		lines = append(lines, line{number: i + 1, fields: strings.Split(text, "\t")})
	}
	return lines, nil
}

// errorAt returns a setup error at one line of file.
func errorAt(file string, number int, format string, args ...any) model.SetupError {
	return model.SetupError{File: file, Line: number, Message: fmt.Sprintf(format, args...)}
}

// sortErrors orders errors by line, keeping the order of errors on one line.
func sortErrors(errs []model.SetupError) {
	sort.SliceStable(errs, func(i, j int) bool { return errs[i].Line < errs[j].Line })
}
