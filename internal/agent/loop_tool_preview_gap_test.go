package agent

import (
	"strings"
	"testing"
)

func TestStructurallyReduceDispatchOutputFallbackPaths(t *testing.T) {
	// 1. Invalid JSON -> false
	if _, ok := structurallyReduceDispatchOutput("not json", 1000); ok {
		t.Fatal("expected false for invalid JSON")
	}

	// 2. JSON is not an array or map with tasks/task_results -> rows == 0 -> false (line 325)
	if _, ok := structurallyReduceDispatchOutput("123", 1000); ok {
		t.Fatal("expected false for primitive number")
	}
	if _, ok := structurallyReduceDispatchOutput(`{"unrelated": true}`, 1000); ok {
		t.Fatal("expected false for object without tasks")
	}

	// 3. Encoded exceeds maxBytes -> false (line 295)
	if _, ok := structurallyReduceDispatchOutput(`[{"task_id": "long-id-12345", "status": "completed"}]`, 5); ok {
		t.Fatal("expected false when encoded exceeds maxBytes")
	}

	// 4. reduceDispatchRows with non-map elements and empty map elements (lines 339-340, 349-350)
	raw := `[123, "string-task", {"unknown_key": "val"}]`
	res, ok := structurallyReduceDispatchOutput(raw, 1000)
	if !ok {
		t.Fatalf("expected true, got false")
	}
	if !strings.Contains(res, "123") || !strings.Contains(res, "string-task") || !strings.Contains(res, "unknown_key") {
		t.Fatalf("expected unreduced elements preserved, got %s", res)
	}
}

func TestShrinkJSONPreviewExceedsMaxBytes(t *testing.T) {
	// A document whose keys are so large that even at leafBudget=0 it exceeds maxBytes (line 384)
	doc := `{"very_long_key_name_that_exceeds_budget": "a"}`
	if _, ok := shrinkJSONPreview(doc, 10); ok {
		t.Fatal("expected shrinkJSONPreview to fail on tight maxBytes")
	}
}

func TestShrinkRunesEdge(t *testing.T) {
	// shrinkRunes with budget < rune count (line 438 return s fallback if loop finishes)
	res := shrinkRunes("hello world", 3)
	if res != "hel…" {
		t.Fatalf("expected hel…, got %q", res)
	}
}
