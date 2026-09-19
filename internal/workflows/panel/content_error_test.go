package panel

import (
	"context"
	"testing"
)

func TestValidateTaskContentRejectsTamperedContent(t *testing.T) {
	work := panelTaskWithID(t, "content", "task")
	repo := newMemoryRepo(t)
	storePanelTask(t, repo, work)
	if err := ValidateTaskContent(context.Background(), repo, "run", "task", work); err != nil {
		t.Fatalf("valid content: %v", err)
	}
	work.InputDigest = "wrong"
	if err := ValidateTaskContent(context.Background(), repo, "run", "task", work); err == nil {
		t.Fatal("expected digest conflict")
	}
}
