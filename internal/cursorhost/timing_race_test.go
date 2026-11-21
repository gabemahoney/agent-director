//go:build race

package cursorhost

// b.dcj: 16 serial race-built helper re-execs exceeded 5 s in the reproduction;
// 10x leaves 50 s for that work without changing any product deadline.
const testWaitScale = 10
