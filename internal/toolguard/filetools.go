package toolguard

import (
	"io/fs"
	"path/filepath"
)

// decideFiles consumes only decoded fields and the shared resolver. Keeping the
// resolver explicit lets the common decision table inject lookup faults or the
// Darwin comparison without replacing the runner or mutating global functions.
func decideFiles(p payload, entries []string, paths pathResolver) reason {
	secrets, err := paths.secretForms(entries)
	if err != nil {
		return checkFailed
	}
	switch p.tool {
	case "Read", "Create", "Edit":
		target, _ := p.text("file_path")
		return paths.checkTarget(target, []string{p.cwd}, secrets, false)
	case "ApplyPatch":
		input, _ := p.text("input")
		targets, err := patchPaths(paths.ctx, input)
		if err != nil {
			return checkFailed
		}
		for _, target := range targets {
			if decision := paths.checkTarget(target, []string{p.cwd}, secrets, false); decision != noDecision {
				return decision
			}
		}
		return noDecision
	case "LS", "Grep", "Glob":
		field := map[string]string{"LS": "directory_path", "Grep": "path", "Glob": "folder"}[p.tool]
		root, err := paths.searchRoot(p, field)
		if err != nil {
			return checkFailed
		}
		if decision := paths.check(root.resolved, secrets, p.tool != "LS"); decision != noDecision {
			return decision
		}
		if p.tool == "Glob" {
			patterns, _ := p.strings("patterns")
			return paths.checkGlob(root, patterns, secrets)
		}
		return noDecision
	default:
		return checkFailed // only the File Tool dispatch may call this rule
	}
}

func (r pathResolver) checkTarget(target string, bases, secrets []string, ancestors bool) reason {
	forms, err := r.toolForms(target, bases)
	if err != nil {
		return checkFailed
	}
	return r.check(forms.resolved, secrets, ancestors)
}

func (r pathResolver) searchRoot(p payload, field string) (pathForms, error) {
	root, present := p.text(field)
	if !present {
		// An omitted root is cwd itself, not cwd joined onto a relative cwd.
		if !filepath.IsAbs(p.cwd) {
			return pathForms{}, fs.ErrInvalid
		}
		root = p.cwd
	}
	return r.toolForms(root, []string{p.cwd})
}
