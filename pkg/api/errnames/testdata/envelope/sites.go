package envelope

import "os"

// sites hands writers.go's envelope and writers, and envelope literals of its
// own, one err_name by each path the scan resolves. The Fixture names and ErrUnknownTool (excluded only in
// internal/mcp) must be reported; ErrInvalidFlags and ErrUnknownVerb are
// catalogued; the os.Getenv names are dynamic and never resolved.
func sites(err error) {
	_ = emit(os.Stderr, "ErrFixtureLiteral", "uncatalogued literal")
	_ = emit(os.Stderr, "ErrUnknownVerb", "catalogued literal")
	_ = emit(os.Stderr, "ErrUnknownTool", "MCP's name outside internal/mcp")
	_ = emitAndStop(codeUncatalogued, "uncatalogued constant, forwarded")
	_ = emitAndStop(codeCatalogued, "catalogued constant, forwarded")

	code := "ErrFixtureLocal"
	if err != nil {
		code = os.Getenv("DYNAMIC")
	}
	_ = emit(os.Stderr, code, "local assigned a literal, then a dynamic value")

	picked, text := pick(err)
	_ = emitAndStop(picked, text)
	_ = emit(os.Stderr, single(), "one-result function")

	_ = report{Code: "ErrFixtureKeyed", Text: "keyed envelope literal"}
	_ = report{"ErrFixturePositional", "positional envelope literal"}
	_ = struct {
		Name string `json:"err_name"`
	}{Name: "ErrFixtureAnonymous"}
	_ = map[string]string{"err_name": "ErrFixtureMap", "err_description": "map envelope literal"}

	_ = emit(os.Stderr, os.Getenv("DYNAMIC"), "dynamic")
}
