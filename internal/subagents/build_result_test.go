package subagents

// The structured-success envelope carries model-produced output. Output that
// cannot be encoded must fail the task rather than return a half-built payload.

import (
	"testing"
	"time"
)

func TestBuildResultStructuredReportsAnUnencodableOutput(t *testing.T) {
	payload, err := buildResultStructured(make(chan int), 2, time.Second, 1)
	if err == nil {
		t.Fatalf("an unencodable output produced a payload: %s", payload)
	}
	if payload != nil {
		t.Fatalf("a failed encode still returned %s", payload)
	}
}

func TestBuildResultStructuredEnvelope(t *testing.T) {
	payload, err := buildResultStructured(map[string]any{"ok": true}, 4, 1500*time.Millisecond, 3)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"schema":"ok"`, `"status":"completed"`, `"steps":2`, `"step_count":3`} {
		if !contains(string(payload), want) {
			t.Fatalf("payload %s missing %s", payload, want)
		}
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
