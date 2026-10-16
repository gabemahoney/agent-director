# pc.01 offline fixture correction

Base revision: `7b6c2e20a9c73b82148833b8bef52dde9e2c255b` on `feature/b.ftd`.
Task: E3 Task3 `t2.jf1.r8.a8`, case `t2.jf1.pc.01`. No ticket status changes or commits.

Changed files:

- `test/Dockerfile`
- `test/driver/cursor-pane-controls.sh`
- `tickets/testplans/b.jf1/t1.jf1.pc/t1.jf1.pc.md`
- `tickets/testplans/b.jf1/t1.jf1.pc/t2.jf1.pc.01/t2.jf1.pc.01.md`

The image now installs frozen build dependencies, runs the canonical Bun build
(including tsc declaration emission), packs the real public tarball, and installs
that tarball into a baked consumer. Any peer/transitive resolution is image-build
work. Runtime pc.01 invokes no package manager: it checks a complete build-input
SHA256 manifest against the mounted source and copies the installed consumer into
the case's private directory. JavaScript/declarations must exist, and existing
public-client composition assertions and tarball/binary hash evidence remain.
The source manifest is also exported as raw evidence.

Canonical build imports `pkg/api/errnames/catalog.json` outside the TS package;
the image therefore preserves the repository-relative layout and includes this
catalogue in provenance. The narrow check exposed this second defect in the old
package-only staging before the full suite was started.

## Verification receipts

All commands below ran from the repository root. Log names are relative to this
handoff directory. No testplan group, collector, integration helper, real Cursor,
credential operation, or product binary was run directly by this worker. The full
sandbox target ran its normal test coverage. No Docker test image was built.

1. `make sandbox SANDBOX_FLAGS='--network=none' CMD='bash tickets/handoff/b.ftd/Plans/b.jf1/e3-task3/pc01-offline-fix/verify-package.sh'`
   exited 2 before script execution: Docker rejects simultaneous host/none network
   options. Receipt: `package-verification.log`.
2. `make sandbox _SANDBOX_NET=--network=none CMD='bash tickets/handoff/b.ftd/Plans/b.jf1/e3-task3/pc01-offline-fix/verify-package.sh'`
   exited 2 before script execution: the global override also invalidates the
   sandbox image build cache and apt cannot run offline. Receipt:
   `package-verification-offline.log`. No network enabling was requested.
3. `make sandbox CMD='bash tickets/handoff/b.ftd/Plans/b.jf1/e3-task3/pc01-offline-fix/verify-package.sh'`
   first exited 2 because canonical build could not resolve the unstaged Go
   catalogue (`package-verification-default-sandbox.log`). After staging the
   catalogue, the same command exited 0 (`package-verification-final.log`).
4. `make sandbox --eval='sandbox: private _SANDBOX_NET=--network=none' CMD='bash tickets/handoff/b.ftd/Plans/b.jf1/e3-task3/pc01-offline-fix/verify-package.sh'`
   exited 0. The private target override leaves cached sandbox provisioning
   unchanged and runs the verification container with only `--network=none`.
   Receipt: `package-verification-network-none.log`. It used already provisioned
   build dependencies, ran canonical build/declaration emission and pack, installed
   the local tarball, imported the public Client from a copied private consumer,
   rejected changed and newly added source, and checked shell syntax. This was a
   narrow packaging check, not execution of pc.01 or the Docker image build.
5. `make test-sandbox` ran exactly once after fixes settled and exited 2.
   Receipt: `test-sandbox.log`. Go: 67 packages passed, 13 had no test files,
   1 failed. Bun: **430 pass, 0 fail**, 1,662 expectations across 57 files.
   The unrelated Go failure was `TestDecideKeepsDurableOrigin/cursor`,
   `pkg/api/decide_delivery_test.go:595`:
   `store: commit outcome unconfirmed; read the request before retrying: context deadline exceeded`.
   The subtest took 9.29s; pkg/api failed after 144.226s. No out-of-scope
   product/test edits or second full-suite run were made.
6. `git diff --check` passed after the tracked changes.

## Remaining work

Scheduler verification is pending. The historical 16/17 group and 4/5
cursor-pane-controls result is not superseded by this packaging check. The root
must commit the correction and run the exact committed-image gate externally,
with runtime networking disabled and normal raw receipt retention. The unrelated
Go deadline failure also remains for the caller to assess. No new scheduler
packet was generated. No questions for the caller.
