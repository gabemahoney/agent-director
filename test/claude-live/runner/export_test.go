package runner

// The external qualification regression keeps the real constructor/clock while
// using the same private-path seam as the runner's own scheduler fixtures.
var NewRunAtForTest = newRunAt

// Reuse the runner's private paths, clock and command seam for a real native
// qualification startup callback in the external runner test binary.
var RunnerFixtureForTest = runnerFixture
