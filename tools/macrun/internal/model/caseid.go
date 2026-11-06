package model

import (
	"fmt"
	"regexp"
	"strings"
)

// CaseID names one case of a Mac Run. It has one of three forms:
//
//	go:<package dir>:<test name>   a Go test or subtest, named as go test -json names it
//	go:<package dir>               a Go package's own result
//	testplan:<slug>:<t2 id>        a testplan case
//
// A package dir is slash-separated and relative to the module root, with no
// "./" and no trailing slash.
type CaseID string

const (
	goPrefix       = "go:"
	testplanPrefix = "testplan:"
)

// slugPattern matches a test/docker-epics.txt slug.
var slugPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// GoTestCase returns the case ID of the Go test, subtest or Example named
// test in the package at dir.
func GoTestCase(dir, test string) CaseID {
	return CaseID(goPrefix + dir + ":" + test)
}

// GoPackageCase returns the case ID of the own result of the Go package at
// dir.
func GoPackageCase(dir string) CaseID {
	return CaseID(goPrefix + dir)
}

// ParseCaseID checks that s has one of CaseID's three forms and returns it as
// a CaseID. The error says what is wrong and which forms are accepted.
func ParseCaseID(s string) (CaseID, error) {
	if s == "" {
		return "", fmt.Errorf("case ID is empty; %s", caseIDForms)
	}
	if strings.ContainsAny(s, "\t\r\n") {
		return "", fmt.Errorf("case ID contains a tab or line break; %s", caseIDForms)
	}
	switch {
	case strings.HasPrefix(s, goPrefix):
		dir, test, hasTest := strings.Cut(strings.TrimPrefix(s, goPrefix), ":")
		if reason := caseIDDirProblem(dir); reason != "" {
			return "", fmt.Errorf("case ID %q: package dir %s; %s", s, reason, caseIDForms)
		}
		if hasTest && test == "" {
			return "", fmt.Errorf("case ID %q has an empty test name; %s", s, caseIDForms)
		}
	case strings.HasPrefix(s, testplanPrefix):
		slug, id, ok := strings.Cut(strings.TrimPrefix(s, testplanPrefix), ":")
		if !ok || !slugPattern.MatchString(slug) || id == "" || strings.ContainsAny(id, ": ") {
			return "", fmt.Errorf("case ID %q is not of the form testplan:<slug>:<t2 id>; %s", s, caseIDForms)
		}
	default:
		return "", fmt.Errorf("case ID %q has no go: or testplan: prefix; %s", s, caseIDForms)
	}
	return CaseID(s), nil
}

const caseIDForms = "use go:<package dir>:<test name>, go:<package dir> or testplan:<slug>:<t2 id>"

// caseIDDirProblem returns why dir is not a package dir of a case ID, or ""
// when it is one.
func caseIDDirProblem(dir string) string {
	switch {
	case dir == "":
		return "is empty"
	case strings.HasPrefix(dir, "/"):
		return "is absolute"
	case strings.HasSuffix(dir, "/"):
		return "ends in /"
	case strings.Contains(dir, " "):
		return "contains a space"
	}
	for _, elem := range strings.Split(dir, "/") {
		if elem == "" || elem == "." || elem == ".." {
			return "is not a clean relative path"
		}
	}
	return ""
}
