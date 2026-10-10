// Package envelope is the synthetic fixture for
// TestEnvelopeErrNamesCatalogued_NegativeControl: an envelope type, a writer,
// a forwarding writer and name-returning functions, under names unlike the
// real tree's, so the scan finds them by tag and data flow alone. sites.go
// hands them err_names.
package envelope

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
)

// report is an error envelope: its Code field carries the err_name tag.
type report struct {
	Code string `json:"err_name"`
	Text string `json:"err_description"`
}

// Constants sites.go passes, declared in this file to exercise cross-file
// resolution: one in errnames.Catalog, one not.
const (
	codeCatalogued   = "ErrInvalidFlags"
	codeUncatalogued = "ErrFixtureConst"
)

// emit is a writer: its parameter code becomes the envelope's err_name.
func emit(w io.Writer, code, text string) error {
	b, err := json.Marshal(report{Code: code, Text: text})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(w, string(b))
	return err
}

// emitAndStop is a writer by forwarding its code to emit.
func emitAndStop(code, text string) error {
	if err := emit(os.Stderr, code, text); err != nil {
		return err
	}
	return io.EOF
}

// pick returns an uncatalogued literal name on one path and a dynamic one on
// the other.
func pick(err error) (string, string) {
	if err == io.EOF {
		return "ErrFixtureReturned", "end of input"
	}
	return err.Error(), err.Error()
}

// single returns an uncatalogued literal name as its only result.
func single() string {
	return "ErrFixtureSingle"
}
