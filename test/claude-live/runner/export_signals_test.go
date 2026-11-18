package runner

// Test-only access to the existing isolated setup; production scope is unchanged.
var (
	ScopedRunnerForSignalsTest = scopedRunner
	LifecycleKitForSignalsTest = lifecycleKit
	PutFixtureForSignalsTest   = putFixture
)
