// Command macrun plans, runs and judges Mac Runs: runs of the Mac Tests on
// GitHub-hosted macOS runners, set up by the files under test/mac/. It is
// the only tool that reads those files, apart from the Mac workflows.
//
// Usage:
//
//	macrun <subcommand> [flags]
//
// A missing or unknown subcommand prints usage to stderr and exits 2.
//
// # macrun plan
//
//	go run ./tools/macrun plan [-root dir] [-json file] [-github-output file]
//
// plan reads the tested tree and plans the Mac Run. A tree has the Mac Tests
// exactly when it holds test/mac/parts.txt; without it, plan plans no parts
// and says why. Otherwise it validates test/mac/parts.txt and
// test/mac/expected-failures.txt, and either plans each part with its
// expected cases (the darwin/arm64 build's top-level tests and Examples the
// part selects, and each package's own result) and each suite's coverage, or
// plans no parts and lists every setup error. The plan depends on the tree
// alone, so the part and verdict jobs compute it again from the same commit
// rather than receive it.
//
// Flags:
//
//	-root dir            root of the tested tree (default: the current
//	                     directory, the checkout root)
//	-json file           write the full plan as JSON to file
//	-github-output file  append two $GITHUB_OUTPUT lines to file:
//	                     parts=<compact JSON array of part names, in file
//	                     order> and planned=true|false
//
// stdout gets a short summary for the job log: the status, the reason, each
// error as <file>:<line>: <message> (<file>: <message> when it concerns the
// whole file), each part with its count of expected cases, and the coverage.
//
// plan exits 0 whatever the plan's status (planned, no-mac-tests or
// invalid-setup), so the outcome lives in the plan rather than the job's
// conclusion. It exits 1, with the error on stderr, only when the tree can't
// be read or an output can't be written, and 2 on a usage error.
package main
