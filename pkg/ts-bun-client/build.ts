import { rmSync, copyFileSync } from "fs";
import { join } from "path";

const distDir = join(import.meta.dir, "dist");

// Clean dist/ before each build
try {
  rmSync(distDir, { recursive: true, force: true });
} catch {
  // ignore if doesn't exist
}

// Step 1: Bun.build for JavaScript output. Single entrypoint at src/index.ts —
// the subprocess Client has no worker thread to bundle separately.
const result = await Bun.build({
  entrypoints: ["src/index.ts"],
  outdir: "dist",
  target: "bun",
  format: "esm",
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

// Step 3: copy version-floor.json into dist/ byte-for-byte (SR-5.3).
// The JSON is a static asset shipped in the tarball; bash consumers read it
// via `jq -r .min_binary_version < node_modules/agent-director/dist/version-floor.json`.
const floorSrc = join(import.meta.dir, "version-floor.json");
const floorDst = join(distDir, "version-floor.json");
copyFileSync(floorSrc, floorDst);

console.log("Build complete: dist/");
