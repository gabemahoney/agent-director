package qualification

import (
	"context"

	"github.com/gabemahoney/agent-director/test/claude-live/runner"
)

// Test-only access keeps external evidence tests on the single qualification
// fixture adapter while exercising the actual private production judgments.
var (
	ForceStateForTest           = forceState
	PendingForceForTest         = pendingForce
	ForceGroupForTest           = forceGroup
	ForceGroupTextsForTest      = forceGroupTexts
	ControlledToolForTest       = controlledTool
	LongToolForTest             = longTool
	CheckCapForTest             = checkCap
	VerifyBacklogRecordsForTest = verifyBacklogRecords
	AnalyzeLimitsForTest        = analyzeLimits
	AnalyzeGroupsForTest        = analyzeGroups
	SyntheticBacklogForTest     = syntheticBacklogProof
	RequirePhysicalCaseForTest  = requirePhysicalCase
	ContractInputsForTest       = contractInputs
)

func FinalizeExerciseForTest(ctx context.Context, e *Exercise) error { return e.finalize(ctx) }

func AnalyzeTypedForTest(c Capture) []runner.Result { return analyzeTyped(c) }

func ScheduledStartsForTest(c Capture) runner.Result { return checkScheduledStarts(c) }

func AccountingForTest(c Capture) runner.Result { return checkAccounting(c) }

func CleanupForTest(c Capture) runner.Result { return checkCleanup(c) }

func ToolWindowForTest(ctx context.Context, e *Exercise, iid string, n int, between bool) (string, error) {
	return e.toolWindow(ctx, iid, n, between)
}

func WaitSightingForTest(ctx context.Context, e *Exercise, iid, text string) error {
	return e.waitSighting(ctx, iid, text)
}

func ForceStateEstablishedForTest(c Capture, state string, cmd runner.Command, before runner.Snapshot, bound bool) bool {
	return c.forceStateEstablished(state, cmd, before, bound)
}
func CompatibleCIForTest(c Capture, ref ImportRef) (CIResolution, error) {
	return c.compatibleCI(ref)
}

func RetainImportsForTest(e *Exercise) error { return e.retainImports() }

func ImportPhaseForTest(c Capture, phase string) (Capture, ImportRef, error) {
	return c.importPhase(phase)
}
