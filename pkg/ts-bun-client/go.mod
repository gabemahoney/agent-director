// Not Go code: pkg/ts-bun-client is the bun/TypeScript client. This file only
// marks a module boundary, so the root module's ./... patterns (go build, test,
// vet, list) never walk this tree, whose dist/ and node_modules/ bun rewrites
// while those walks run (b.jct). The module path is deliberately outside the
// root module's path, so code that finds the repo root by its go.mod module
// line never stops here.
module agent-director.invalid/ts-bun-client

go 1.22
