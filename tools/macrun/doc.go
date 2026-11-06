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
//
// # macrun part
//
//	macrun part -part name -run-id id -attempt n -commit sha -out dir
//	    -bash path -sqlite3 path -runner-environment value
//	    [-root dir] [-homebrew prefix] [-work dir]
//
// part runs one Go part of a Mac Run on the Mac. It recomputes the plan from
// the checkout and checks, in order: the plan holds the part; the checkout's
// HEAD is the tested commit; the runner is GitHub-hosted and none of
// ~/.config/agent-director, ~/.agent-director, ~/.claude and ~/.codex exists;
// the tools are as the tests need them; and every tool's version can be
// read. A failed check or step runs no case: part records it and exits 1.
// Otherwise it builds each of the part's packages' test binaries for
// darwin/arm64 as go test -c does, one at a time, and runs it through go tool
// test2json with exactly the plan's tests selected, from the package's
// directory, with this environment: macrun's own, with PATH set to
// /usr/bin:/bin:/usr/sbin:/sbin, then the directories of the go and bun found
// on macrun's PATH, then <homebrew>/bin and <homebrew>/sbin;
// AGENT_DIRECTOR_TEST_BASH and AGENT_DIRECTOR_TEST_SQLITE3 set to -bash and
// -sqlite3; and AGENT_DIRECTOR_TEST_BINARY removed.
//
// Flags:
//
//	-part name                 the part's name, as test/mac/parts.txt names it
//	-run-id id                 the run's ID (github.run_id)
//	-attempt n                 the run attempt (github.run_attempt)
//	-commit sha                the tested commit
//	-out dir                   output folder, which gets record.json
//	-bash path                 absolute path of a bash 4.4 or later
//	-sqlite3 path              absolute path of the standard SQLite build
//	-runner-environment value  runner.environment
//	-root dir                  root of the checkout (default: the current
//	                           directory)
//	-homebrew prefix           Homebrew's prefix (default: /opt/homebrew)
//	-work dir                  folder for the test binaries (default: a new
//	                           temporary folder, removed at the end)
//
// part writes the part's record to <out>/record.json, and prints to stdout,
// for the job log, one marked line (@macrun-event and a JSON object) when the
// part starts, when each package builds, and when each case starts and ends,
// and how the part ended, with the test output between, indented, inside a
// ::stop-commands:: block; or, when a check or setup step fails, one
// setup-failed line naming it. SIGINT or SIGTERM, which GitHub sends on a
// cancel or a job timeout, stops the running tests and ends the part
// cancelled.
//
// part exits 0 when it ran its cases to their end, whatever their results,
// which are the verdict's to judge; 1 on a setup failure, a cancel or another
// fault, such as one preparing its folders or writing the record; and 2 on a
// usage error.
//
// # macrun verdict
//
//	macrun verdict -repo owner/name -run-id id -attempt n -out dir
//	    [-root dir] [-api-url url] [-server-url url] [-summary file]
//
// verdict judges one attempt of a Mac Run. It reads the run through the
// GitHub REST API with the run's own token, read from GH_TOKEN and from
// nowhere else. It checks that the attempt tested the checkout's HEAD,
// recomputes the plan and reads test/mac/expected-failures.txt from the
// checkout, and reads whether the repository is public. For each planned
// part it finds the job named "part <name>" and reads the part's record
// from the artifact mac-part-<name>-<the job's run attempt>, or, failing
// that, from the marked lines of the job's log. A job step that failed
// before the step named "Run cases" is the part's failed setup step. The
// Mac workflow must name its part jobs and that step so.
//
// Flags:
//
//	-repo owner/name   the repository (github.repository)
//	-run-id id         the run's ID (github.run_id)
//	-attempt n         the judged attempt (github.run_attempt)
//	-out dir           output folder, which gets verdict.json and verdict.md
//	-root dir          root of the checkout of the tested commit (default:
//	                   the current directory)
//	-api-url url       GitHub's REST API URL (default: https://api.github.com)
//	-server-url url    GitHub's web URL, for links (default:
//	                   https://github.com)
//	-summary file      append the Markdown summary to file
//	                   ($GITHUB_STEP_SUMMARY), its long lists cut to fit
//	                   GitHub's limit
//
// verdict writes <out>/verdict.json, the verdict with every part's case
// results, and <out>/verdict.md, the whole Markdown summary, and prints the
// verdict, each incompleteness reason and each failed case to stdout.
//
// verdict exits 0 when the verdict is passed; 1 when it is failed or
// incomplete, when a fault writing its outputs stops it, or when it can't
// judge: a fault reading the checkout's HEAD, the attempt, its jobs or the
// repository, or an attempt that tested another commit than the checkout's
// HEAD, none of which writes a verdict; and 2 on a usage error, including an
// empty or unset GH_TOKEN.
package main
