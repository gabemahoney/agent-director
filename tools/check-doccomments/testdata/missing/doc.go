// Package missing is a fixture package used by check-doccomments tests.
// Several exported identifiers intentionally lack doc comments so the checker
// can verify it emits the correct diagnostics.
package missing

import "errors"

// DocumentedConst is documented — must NOT appear in check output.
const DocumentedConst = 1

const UndocumentedConst = 2

var UndocumentedVar = "missing doc"

// DocumentedFunc is documented — must NOT appear in check output.
func DocumentedFunc() {}

func UndocumentedFunc() {}

// DocumentedType is documented — must NOT appear in check output.
type DocumentedType struct{}

type UndocumentedType struct{}

// DocumentedStructUndocumentedField is documented at the type level, but its
// exported field is intentionally undocumented so the checker flags the field.
type DocumentedStructUndocumentedField struct {
	UndocumentedField string
}

var ErrUndocumented = errors.New("undocumented")
