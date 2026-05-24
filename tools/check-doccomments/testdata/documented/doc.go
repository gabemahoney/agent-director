// Package documented is a fixture package used by check-doccomments tests.
// Every exported identifier in this package has a doc comment — the checker
// must exit 0 when run against this directory.
package documented

// ExportedConst is a fully documented constant.
const ExportedConst = 42

// ExportedVar is a fully documented variable.
var ExportedVar = "hello"

// ExportedFunc is a fully documented function.
func ExportedFunc() {}

// ExportedType is a fully documented type.
type ExportedType struct{}

// ExportedMethod is a fully documented method on ExportedType.
func (ExportedType) ExportedMethod() {}

// ExportedInterface is a fully documented interface.
type ExportedInterface interface{}

// ExportedStruct is a fully documented struct whose exported fields also carry
// doc comments, exercising the struct-field check path in check-doccomments.
type ExportedStruct struct {
	// DocumentedField is a documented exported field.
	DocumentedField string
	// DocumentedInlineField is documented via a trailing inline comment.
	DocumentedInlineField int // documented inline

	unexportedField bool // unexported fields are intentionally skipped
}

// DocumentedMethod is a fully documented method on ExportedStruct.
func (ExportedStruct) DocumentedMethod() {}
