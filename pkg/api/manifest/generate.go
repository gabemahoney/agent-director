//go:build ignore

// generate.go produces pkg/api/manifest/surface.json from the canonical
// internal/api/manifest.Verbs slice.
//
// Run via:
//
//	go generate ./pkg/api/manifest/...
//	make surface-json
//
// The output schema is locked for downstream consumption:
//
//	{
//	  "version": 1,
//	  "verbs": [ { "name", "description", "callable", "handle_free",
//	               "params": [{...nullable/allow_empty/allowed_values...}],
//	               "result_fields": [{...}], "error_names": [...] }, ... ]
//	}
//
// allowed_values is emitted as JSON null for non-enum fields (not []).
// 2-space indent, trailing newline, deterministic across runs.

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/gabemahoney/agent-director/internal/api/manifest"
)

// surface is the top-level shape of surface.json.
type surface struct {
	Version int          `json:"version"`
	Verbs   []verbOutput `json:"verbs"`
}

// verbOutput is the per-verb shape.
type verbOutput struct {
	Name         string        `json:"name"`
	Description  string        `json:"description"`
	Callable     bool          `json:"callable"`
	HandleFree   bool          `json:"handle_free"`
	Params       []paramOutput `json:"params"`
	ResultFields []fieldOutput `json:"result_fields"`
	ErrorNames   []string      `json:"error_names"`
}

// paramOutput is the per-parameter shape. AllowedValues is *[]string so
// nil marshals as JSON null rather than [].
type paramOutput struct {
	Name          string   `json:"name"`
	Type          string   `json:"type"`
	Description   string   `json:"description"`
	Required      bool     `json:"required"`
	Nullable      bool     `json:"nullable"`
	AllowEmpty    bool     `json:"allow_empty"`
	AllowedValues *[]string `json:"allowed_values"`
}

// fieldOutput is the per-result-field shape. AllowedValues is *[]string so
// nil marshals as JSON null rather than [].
type fieldOutput struct {
	Name          string   `json:"name"`
	Type          string   `json:"type"`
	Description   string   `json:"description"`
	Nullable      bool     `json:"nullable"`
	AllowEmpty    bool     `json:"allow_empty"`
	AllowedValues *[]string `json:"allowed_values"`
}

func main() {
	s := surface{
		Version: 1,
		Verbs:   make([]verbOutput, 0, len(manifest.Verbs)),
	}

	for _, v := range manifest.Verbs {
		params := make([]paramOutput, 0, len(v.Params))
		for _, p := range v.Params {
			po := paramOutput{
				Name:        p.Name,
				Type:        p.Type,
				Description: p.Description,
				Required:    p.Required,
				Nullable:    p.Nullable,
				AllowEmpty:  p.AllowEmpty,
				// nil slice → null in JSON; non-nil slice → [...] in JSON.
				AllowedValues: nilOrPtr(p.AllowedValues),
			}
			params = append(params, po)
		}

		fields := make([]fieldOutput, 0, len(v.ResultFields))
		for _, f := range v.ResultFields {
			fo := fieldOutput{
				Name:          f.Name,
				Type:          f.Type,
				Description:   f.Description,
				Nullable:      f.Nullable,
				AllowEmpty:    f.AllowEmpty,
				AllowedValues: nilOrPtr(f.AllowedValues),
			}
			fields = append(fields, fo)
		}

		errNames := v.ErrorNames
		if errNames == nil {
			errNames = []string{}
		}

		s.Verbs = append(s.Verbs, verbOutput{
			Name:         v.Name,
			Description:  v.Description,
			Callable:     v.Callable,
			HandleFree:   v.HandleFree,
			Params:       params,
			ResultFields: fields,
			ErrorNames:   errNames,
		})
	}

	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "generate: marshal: %v\n", err)
		os.Exit(1)
	}
	// Append trailing newline.
	data = append(data, '\n')

	// Write to pkg/api/manifest/surface.json, relative to the module root.
	// __FILE__ resolution: this generator lives in pkg/api/manifest/; go up
	// three levels to reach the module root, then back down to the target.
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		fmt.Fprintln(os.Stderr, "generate: cannot determine source file path")
		os.Exit(1)
	}
	// thisFile = .../pkg/api/manifest/generate.go
	// dir      = .../pkg/api/manifest/
	dir := filepath.Dir(thisFile)
	outPath := filepath.Join(dir, "surface.json")

	if err := os.WriteFile(outPath, data, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "generate: write %s: %v\n", outPath, err)
		os.Exit(1)
	}
	fmt.Printf("wrote %s (%d bytes)\n", outPath, len(data))
}

// nilOrPtr converts a nil []string to a nil *[]string (→ JSON null) and a
// non-nil []string to a non-nil *[]string (→ JSON array). This preserves the
// "allowed_values: null for non-enum, [...] for enum" contract.
func nilOrPtr(s []string) *[]string {
	if s == nil {
		return nil
	}
	return &s
}
