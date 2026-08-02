package events

import (
	"testing"
)

func TestNewTokenUsageEventValid(t *testing.T) {
	ev, err := NewTokenUsageEvent("deepseek", "deepseek-v4-pro", 100, 50, 96, 1.04)
	if err != nil {
		t.Fatal(err)
	}
	if ev.Provider != "deepseek" || ev.Model != "deepseek-v4-pro" {
		t.Fatalf("fields = %+v", ev)
	}
	if ev.InputTokens != 100 || ev.OutputTokens != 50 || ev.EstimatedTokens != 96 {
		t.Fatalf("counts = %+v", ev)
	}
	if ev.CalibrationRatio != 1.04 {
		t.Fatalf("ratio = %f", ev.CalibrationRatio)
	}
}

func TestNewTokenUsageEventRejectsEmptyProvider(t *testing.T) {
	_, err := NewTokenUsageEvent("", "model", 100, 50, 96, 1.0)
	if err == nil {
		t.Fatal("empty provider should fail validation")
	}
}

func TestNewTokenUsageEventRejectsWhitespaceProvider(t *testing.T) {
	_, err := NewTokenUsageEvent("  ", "model", 100, 50, 96, 1.0)
	if err == nil {
		t.Fatal("whitespace provider should fail validation")
	}
}

func TestNewTokenUsageEventRejectsOversizedProvider(t *testing.T) {
	_, err := NewTokenUsageEvent(string(make([]byte, 65)), "model", 100, 50, 96, 1.0)
	if err == nil {
		t.Fatal("oversized provider should fail validation")
	}
}

func TestNewTokenUsageEventRejectsEmptyModel(t *testing.T) {
	_, err := NewTokenUsageEvent("provider", "", 100, 50, 96, 1.0)
	if err == nil {
		t.Fatal("empty model should fail validation")
	}
}

func TestNewTokenUsageEventRejectsNegativeTokenCounts(t *testing.T) {
	_, err := NewTokenUsageEvent("provider", "model", -1, 50, 96, 1.0)
	if err == nil {
		t.Fatal("negative input tokens should fail validation")
	}
	_, err = NewTokenUsageEvent("provider", "model", 100, -1, 96, 1.0)
	if err == nil {
		t.Fatal("negative output tokens should fail validation")
	}
	_, err = NewTokenUsageEvent("provider", "model", 100, 50, -1, 1.0)
	if err == nil {
		t.Fatal("negative estimated tokens should fail validation")
	}
}

func TestNewTokenUsageEventRejectsNegativeCalibrationRatio(t *testing.T) {
	_, err := NewTokenUsageEvent("provider", "model", 100, 50, 96, -1.0)
	if err == nil {
		t.Fatal("negative calibration ratio should fail validation")
	}
}

func TestNewTokenUsageEventRejectsUnsealed(t *testing.T) {
	ev := TokenUsageEvent{Provider: "p", Model: "m"}
	err := ev.Validate()
	if err == nil {
		t.Fatal("unsealed event should fail validation")
	}
}
