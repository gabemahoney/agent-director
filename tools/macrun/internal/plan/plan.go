// Package plan computes a Mac Run's plan from the tested tree alone. The
// plan is a pure function of the tree, with no clock, environment, host
// platform or map order in it, so the plan job on Linux and the part and
// verdict jobs on the Mac each compute the same plan from the same commit.
package plan

import (
	"errors"
	"fmt"
	"go/build"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"syscall"

	"github.com/gabemahoney/agent-director/tools/macrun/internal/golist"
	"github.com/gabemahoney/agent-director/tools/macrun/internal/model"
	"github.com/gabemahoney/agent-director/tools/macrun/internal/setup"
)

// Compute returns the plan of the tree at root. A tree without the Mac
// Tests, or with an invalid setup, is a plan status, not an error; the error
// return is only for a fault reading the tree.
func Compute(root string) (model.Plan, error) {
	p := model.Plan{SchemaVersion: model.PlanSchemaVersion, Coverage: coverage()}

	hasMacTests, err := hasMacTests(root)
	if err != nil {
		return model.Plan{}, err
	}
	if !hasMacTests {
		p.Status = model.StatusNoMacTests
		p.Reason = fmt.Sprintf("The tree has no %s, so it doesn't have the Mac Tests and no part is planned.", setup.PartsFile)
		return p, nil
	}

	parts, partErrs, err := setup.ReadParts(root)
	if err != nil {
		return model.Plan{}, err
	}
	_, markErrs, err := setup.ReadExpectedFailures(root)
	if err != nil {
		return model.Plan{}, err
	}

	l := newLister(root)
	selectorErrs, err := validateSelectors(l, parts)
	if err != nil {
		return model.Plan{}, err
	}
	partErrs = append(partErrs, selectorErrs...)
	sort.SliceStable(partErrs, func(i, j int) bool { return partErrs[i].Line < partErrs[j].Line })
	if errs := append(partErrs, markErrs...); len(errs) > 0 {
		p.Status = model.StatusInvalidSetup
		p.Reason = "The Mac Run setup under test/mac/ is invalid, so no part is planned; fix every listed error."
		p.Errors = errs
		return p, nil
	}

	for _, part := range parts {
		planned, err := expectedCases(l, part)
		if err != nil {
			return model.Plan{}, err
		}
		p.Parts = append(p.Parts, planned)
	}
	p.Status = model.StatusPlanned
	return p, nil
}

// coverage returns each suite's coverage. A suite is full only when its last
// part selects rest, which no suite can do yet, so both are partial.
func coverage() model.Coverage {
	return model.Coverage{Go: model.CoveragePartial, Testplan: model.CoveragePartial}
}

// hasMacTests reports whether the tree at root has the Mac Tests: whether
// setup.PartsFile is a regular file there. A path through a regular file
// (ENOTDIR) is as absent as a missing one.
func hasMacTests(root string) (bool, error) {
	info, err := os.Stat(filepath.Join(root, filepath.FromSlash(setup.PartsFile)))
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check %s: %w", setup.PartsFile, err)
	}
	return info.Mode().IsRegular(), nil
}

// lister lists packages of one tree under the darwin and linux builds,
// listing each package once per build.
type lister struct {
	root   string
	darwin build.Context
	linux  build.Context
	cache  map[listKey]golist.Listing
}

type listKey struct {
	goos string
	dir  string
}

func newLister(root string) *lister {
	return &lister{
		root:   root,
		darwin: golist.DarwinContext(),
		linux:  golist.LinuxContext(),
		cache:  map[listKey]golist.Listing{},
	}
}

func (l *lister) list(ctx build.Context, dir string) (golist.Listing, error) {
	key := listKey{goos: ctx.GOOS, dir: dir}
	if listing, ok := l.cache[key]; ok {
		return listing, nil
	}
	listing, err := golist.List(ctx, l.root, dir)
	if err != nil {
		return golist.Listing{}, err
	}
	l.cache[key] = listing
	return listing, nil
}

// owner is the part that first selected a top-level test or Example.
type owner struct {
	part string
	line int
}

type caseKey struct {
	dir  string
	name string
}

// validateSelectors checks every selector against the darwin and linux
// builds. It returns an error, at the selector's line, for a selector whose
// package is in neither build, for one that selects no top-level test or
// Example in either build, for each selected test or Example that only the
// linux build compiles, and for a test or Example a second part selects. A
// linux-only test would vanish from a Mac Run without a result, and no
// Not-on-Mac List exists yet to leave it out by name.
func validateSelectors(l *lister, parts []setup.Part) ([]model.SetupError, error) {
	var errs []model.SetupError
	owners := map[caseKey]owner{}
	for _, part := range parts {
		for _, sel := range part.Selectors {
			darwin, err := l.list(l.darwin, sel.Dir)
			if err != nil {
				return nil, err
			}
			linux, err := l.list(l.linux, sel.Dir)
			if err != nil {
				return nil, err
			}
			if !darwin.InBuild && !linux.InBuild {
				errs = append(errs, setupError(part.Line,
					"selector %q: %s is not a Go package of the tested source in its linux or darwin build", sel.Text, sel.Dir))
				continue
			}
			selected := selectedNames(sel, darwin.Cases, linux.Cases)
			if len(selected) == 0 {
				errs = append(errs, setupError(part.Line,
					"selector %q selects no top-level test or Example in the linux or darwin build of %s", sel.Text, sel.Dir))
				continue
			}
			inDarwin := map[string]bool{}
			for _, name := range darwin.Cases {
				inDarwin[name] = true
			}
			for _, name := range selected {
				if !inDarwin[name] {
					errs = append(errs, setupError(part.Line,
						"selector %q selects %s in %s, which only the linux build compiles, so it would vanish from a Mac Run; make it build for darwin or leave it out of the selection",
						sel.Text, name, sel.Dir))
				}
				key := caseKey{dir: sel.Dir, name: name}
				first, ok := owners[key]
				switch {
				case !ok:
					owners[key] = owner{part: part.Name, line: part.Line}
				case first.part != part.Name:
					errs = append(errs, setupError(part.Line,
						"%s in %s is selected by part %q (line %d) and part %q (line %d); select each test in one part",
						name, sel.Dir, first.part, first.line, part.Name, part.Line))
				}
			}
		}
	}
	return errs, nil
}

func setupError(line int, format string, args ...any) model.SetupError {
	return model.SetupError{File: setup.PartsFile, Line: line, Message: fmt.Sprintf(format, args...)}
}

// selectedNames returns the names, from the union of lists, that sel
// selects, sorted and each once.
func selectedNames(sel setup.Selector, lists ...[]string) []string {
	seen := map[string]bool{}
	var names []string
	for _, list := range lists {
		for _, name := range list {
			if !seen[name] && sel.Selects(name) {
				seen[name] = true
				names = append(names, name)
			}
		}
	}
	sort.Strings(names)
	return names
}

// expectedCases plans one part from the darwin build: each package it picks
// at least one darwin test or Example from, the names it picks there, and
// its expected cases, which are those names and each package's own result.
func expectedCases(l *lister, part setup.Part) (model.Part, error) {
	picked := map[string][]setup.Selector{}
	var dirs []string
	for _, sel := range part.Selectors {
		if _, ok := picked[sel.Dir]; !ok {
			dirs = append(dirs, sel.Dir)
		}
		picked[sel.Dir] = append(picked[sel.Dir], sel)
	}
	sort.Strings(dirs)

	planned := model.Part{Name: part.Name, Suite: part.Suite, Selection: part.Selection}
	for _, dir := range dirs {
		darwin, err := l.list(l.darwin, dir)
		if err != nil {
			return model.Part{}, err
		}
		var tests []string
		for _, name := range darwin.Cases {
			for _, sel := range picked[dir] {
				if sel.Selects(name) {
					tests = append(tests, name)
					break
				}
			}
		}
		if len(tests) == 0 {
			continue
		}
		planned.Packages = append(planned.Packages, model.Package{Dir: dir, Tests: tests})
		planned.ExpectedCases = append(planned.ExpectedCases, model.GoPackageCase(dir))
		for _, name := range tests {
			planned.ExpectedCases = append(planned.ExpectedCases, model.GoTestCase(dir, name))
		}
	}
	sort.Slice(planned.ExpectedCases, func(i, j int) bool { return planned.ExpectedCases[i] < planned.ExpectedCases[j] })
	return planned, nil
}
