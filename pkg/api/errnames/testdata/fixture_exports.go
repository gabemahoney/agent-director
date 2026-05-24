// Package fixture is a synthetic test fixture for TestScanExportedSentinels.
// This file is NOT a _test.go file so the scanner includes it when walking
// the testdata directory.
package fixture

import "errors"

// ErrExportedA and ErrExportedB are exported Err* vars — both should be
// collected by ScanExportedSentinels.
var ErrExportedA = errors.New("ErrExportedA")
var ErrExportedB = errors.New("ErrExportedB")

// notAnError does not start with "Err" — should be ignored.
var notAnError = "just a string"
