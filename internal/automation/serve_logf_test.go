package automation

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestServeTickLogsRunOnceErrorMessage covers serveTick's own err!=nil
// branch of the RunOnce dispatch switch WITH a non-nil logf (the
// "scheduled run failed" log line, lines 267-269 of serve.go): drop
// automation_runs (as TestServeCallsCloseLastRunEvenWhenRunOnceFails
// does) so startRun's createRun fails for a real, enabled, due
// automation - unlike a disabled automation, whose deadline refresh
// would prune it before m.due(now) ever saw it.
func TestServeTickLogsRunOnceErrorMessage(t *testing.T) {
	root := t.TempDir()
	db := newTestDB(t)
	seedServeAutomation(t, root, "logs-run-once-error")
	spawn := &orderTrackingSpawner{conv: newRecordingConversation()}
	svc, err := New(root, db, spawn, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := dropAutomationRunsTable(t, db); err != nil {
		t.Fatalf("drop automation_runs table: %v", err)
	}
	now := time.Now().UTC()
	m := newDeadlineMap()
	m.deadlines["logs-run-once-error"] = now.Add(-time.Second)

	var logged string
	logf := func(format string, args ...any) { logged = fmt.Sprintf(format, args...) }

	svc.serveTick(context.Background(), m, logf)

	if !strings.Contains(logged, "scheduled run failed") {
		t.Fatalf("serveTick against a failing RunOnce logged %q, want it to contain %q", logged, "scheduled run failed")
	}
}
