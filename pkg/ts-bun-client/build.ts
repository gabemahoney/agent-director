import { rmSync } from "fs";
import { join } from "path";

const distDir = join(import.meta.dir, "dist");

// Clean dist/ before each build
try {
  rmSync(distDir, { recursive: true, force: true });
} catch {
  // ignore if doesn't exist
}

// Step 1: Bun.build for JavaScript output.
//
// Two entrypoints. src/index.ts is the package entry. src/internal/worker.ts
// is a SECOND entrypoint because workerProxy.ts spawns it via
// `new Worker(new URL(..., import.meta.url))` — the bundler does not
// follow that URL reference, so without the explicit entry the worker
// file would never be emitted and the published tarball would ship
// only dist/index.js, causing a ModuleNotFound at runtime on first
// verb call (see fix(npm): missing worker for v0.4.4).
//
// Bun auto-detects the project root as the longest common subpath of
// the entrypoints ("src/"), so the outputs land at:
//   dist/index.js
//   dist/internal/worker.js
const result = await Bun.build({
  entrypoints: ["src/index.ts", "src/internal/worker.ts"],
  outdir: "dist",
  target: "bun",
  format: "esm",
  external: ["bun:ffi"],
  splitting: false,
});

if (!result.success) {
  console.error("Bun.build failed:");
  for (const log of result.logs) {
    console.error(log);
  }
  process.exit(1);
}

// Step 2: tsc for declaration emission
const tsc = Bun.spawn(["tsc", "--emitDeclarationOnly"], {
  stdout: "inherit",
  stderr: "inherit",
});

const exitCode = await tsc.exited;
if (exitCode !== 0) {
  console.error(`tsc --emitDeclarationOnly exited with code ${exitCode}`);
  process.exit(exitCode);
}

console.log("Build complete: dist/");
