package agent

import (
	"context"
	"encoding/json"
	"testing"
)

// unmarshalableParamsTool is a fakeTool whose Parameters() cannot be
// json.Marshal'd (a channel value), the only way ConvertTool's schema
// marshal can fail.
type unmarshalableParamsTool struct{ fakeTool }

func (t *unmarshalableParamsTool) Parameters() map[string]any {
	return map[string]any{"bad": make(chan int)}
}

// TestRunUnadmittedTool_ConvertToolErrorSurfaces pins RunUnadmittedTool's
// earliest guard: a tool whose schema cannot be converted must fail before
// any dispatch, admission, or turn-state mutation.
func TestRunUnadmittedTool_ConvertToolErrorSurfaces(t *testing.T) {
	badTool := &unmarshalableParamsTool{fakeTool{name: "bad-tool"}}
	turn := newSDKTurnState()
	_, err := RunUnadmittedTool(context.Background(), Options{}, turn, badTool, json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("RunUnadmittedTool accepted a tool whose schema cannot be converted")
	}
}

// TestArmExplicitCancel_NoTurnOrNoCallKeyIsANoOp pins the early return: a
// nil turn or an empty call key skips cancel registration rather than
// panicking on a nil turn dereference.
func TestArmExplicitCancel_NoTurnOrNoCallKeyIsANoOp(t *testing.T) {
	d := &dispatcherShim{}
	ctx, cancel := d.armExplicitCancel(context.Background(), "call-1")
	defer cancel()
	if ctx == nil {
		t.Fatal("armExplicitCancel returned a nil context")
	}

	d2 := &dispatcherShim{turn: newSDKTurnState()}
	ctx2, cancel2 := d2.armExplicitCancel(context.Background(), "")
	defer cancel2()
	if ctx2 == nil {
		t.Fatal("armExplicitCancel returned a nil context for an empty call key")
	}
}
