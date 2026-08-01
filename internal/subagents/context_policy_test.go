package subagents

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
)

func TestOneShotRejectsIrreduciblePrompt(t *testing.T) {
	completer := &captureRequestCompleter{}
	h := &OneShotHandler{
		Completer: completer, Model: "model", SystemPrompt: strings.Repeat("system ", 100),
		MaxContextTokens: 1, MaxTokens: intPointer(20),
	}
	_, err := h.Invoke(context.Background(), runtime.Request{Name: "one-shot", Input: json.RawMessage(`"objective"`)})
	if !errors.Is(err, agent.ErrPromptBudgetExceeded) {
		t.Fatalf("error = %v", err)
	}
	if completer.requests != 0 {
		t.Fatalf("provider calls = %d", completer.requests)
	}
}

func TestMultiStepHasNoCheckpointCapability(t *testing.T) {
	typeOfHandler := reflect.TypeOf(MultiStepHandler{})
	if _, ok := typeOfHandler.FieldByName("CheckpointPublisher"); ok {
		t.Fatal("nested multi-step handler has a checkpoint publisher capability")
	}
	if _, ok := typeOfHandler.FieldByName("ContextStore"); ok {
		t.Fatal("nested multi-step handler has a durable context store capability")
	}
	if _, ok := typeOfHandler.FieldByName("ContextPreparationManager"); !ok {
		t.Fatal("nested multi-step handler lost its preparation-only capability")
	}
}

func intPointer(value int) *int { return &value }

var _ provider.Completer = (*captureRequestCompleter)(nil)
