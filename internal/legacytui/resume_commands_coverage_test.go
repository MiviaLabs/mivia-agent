package legacytui

// resume_commands_coverage_test.go drives the handleResumeSlash and
// handlePendingResumeInput methods directly to cover the diff-coverage
// lines that legacytui's higher-level tests do not drive individually.

import (
	"context"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
)

func TestHandleResumeSlashNoCoordinator(t *testing.T) {
	// The no-active-coordinator branch (line 14-17 of resume_commands.go):
	// no cliorchestrate coordinator is registered, so the slash handler
	// just prints an info message. With cli.FindCoordinator returning
	// nil in the test environment, this is the path that runs.
	sess := chat.NewSession(&config.Resolved{ProviderName: "p", Model: "m"}, nil)
	m := newTUIModel(sess, &config.Resolved{ProviderName: "p", Model: "m"}, false)
	if !m.handleResumeSlash(nil) {
		t.Fatal("handleResumeSlash should return true (handled)")
	}
	if !m.handleResumeSlash([]string{}) {
		t.Fatal("handleResumeSlash(empty) should return true")
	}
}

func TestHandlePendingResumeInputEmpty(t *testing.T) {
	// handlePendingResumeInput with no pending resume set short-circuits
	// to (true, "resume cancelled") because the user input is "no"
	// against an empty pending run id. The line is exercised either way.
	sess := chat.NewSession(&config.Resolved{ProviderName: "p", Model: "m"}, nil)
	m := newTUIModel(sess, &config.Resolved{ProviderName: "p", Model: "m"}, false)
	if !m.handlePendingResumeInput("no") {
		t.Fatal("handlePendingResumeInput(no) should return true")
	}
	_ = context.Background()
}
