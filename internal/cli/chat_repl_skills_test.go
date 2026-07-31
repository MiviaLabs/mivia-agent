package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
)

func TestAttachSessionDispatcherBindsSkillsWithoutTools(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".mivia", "skills", "review")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: review\n---\nReview carefully."), 0o600); err != nil {
		t.Fatal(err)
	}
	sess := chat.NewSession(&config.Resolved{Model: "test-model"}, welcomeStubCompleter{})
	cleanup, err := attachSessionDispatcher(sess, root, "test-model", config.DefaultSubagentConfig, &agentSessionState{AllowProjectSkills: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if sess.Dispatcher != nil {
		t.Fatal("--no-tools session unexpectedly created a dispatcher")
	}
	registry := sess.CurrentBinding().SkillRegistry
	if registry == nil {
		t.Fatal("skill registry missing without tools")
	}
	if _, ok := registry.Get("review"); !ok {
		t.Fatal("project skill missing without tools")
	}
}
