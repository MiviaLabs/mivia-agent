package agent

import (
	"strings"
	"testing"

	sdkagentloop "github.com/MiviaLabs/mivia-ai-sdk/agentloop"
	sdkshape "github.com/MiviaLabs/mivia-ai-sdk/provider"
)

// TestSDKRepeatedToolFailureStopFailsTheTurn pins the conversion of the
// SDK's graceful StopRepeatedToolFailures stop into the host's failure-spiral
// hard error. The stop arrives with a nil error and a normal-looking Result
// whose Final is the last model turn; returning it unchanged let a turn that
// never ran its work report success with stale text. Every other graceful
// stop must stay a non-error.
func TestSDKRepeatedToolFailureStopFailsTheTurn(t *testing.T) {
	res := sdkagentloop.Result{
		Stop:       sdkagentloop.StopRepeatedToolFailures,
		Iterations: 7,
		Final:      sdkshape.Message{Role: sdkshape.RoleAssistant, Content: "partial answer"},
	}
	err := sdkRepeatedToolFailureError(res)
	if err == nil {
		t.Fatal("StopRepeatedToolFailures must fail the turn, not report success")
	}
	if !strings.Contains(err.Error(), "failure spiral") {
		t.Fatalf("error %v does not carry the failure-spiral vocabulary", err)
	}

	for _, stop := range []sdkagentloop.StopReason{
		sdkagentloop.StopNoToolCalls,
		sdkagentloop.StopEmptyResponse,
		sdkagentloop.StopMaxIterations,
		sdkagentloop.StopHookVeto,
		sdkagentloop.StopConcluded,
		sdkagentloop.StopSteered,
	} {
		res.Stop = stop
		if err := sdkRepeatedToolFailureError(res); err != nil {
			t.Fatalf("%s: unexpected error %v", stop, err)
		}
	}
}
