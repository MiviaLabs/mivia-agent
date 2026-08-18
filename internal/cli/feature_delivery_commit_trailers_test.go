package cli

import (
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/compiler"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
)

// TestFeatureDeliveryCommitMessageTemplateCarriesFixTrailers is the
// regression for wfr-MASR36MV6LQRBSYC's chunks c1/c2 (2026-08-18): both
// chunk deliveries picked an agent-authored "fix(scope): ..." title (legal
// per the PR title policy - a feature-delivery chunk is not always a
// "feat"), but freshDeliveryCommitSingle (internal/workflows/delivery/deliver_stage.go)
// uses the workflow's commit_message_template as the commit BODY
// unconditionally whenever it renders non-empty - the agent's own pr_summary
// is only a fallback for an EMPTY render, never a preference between the
// two. The template then in force carried no Regression/Class/Sweep
// trailers, so the repo's commit-msg hook (which requires them on any commit
// typed fix, regardless of which workflow produced it) rejected the commit
// on the first attempt every time, sending the chunk into repair with no
// branch ever pushed. This pins that the shipped template's rendered body
// carries all three trailers with non-empty content, matching has_trailer's
// check in scripts/git-hooks/commit-msg - so it passes the hook no matter
// what type the agent's title carries.
func TestFeatureDeliveryCommitMessageTemplateCarriesFixTrailers(t *testing.T) {
	root := committedWorkflowRoot(t)
	workflow, _ := loadCommittedFeatureDeliveryWorkflow(t, root)
	compiled, err := compiler.Compile(&workflow)
	if err != nil {
		t.Fatalf("compile committed feature-delivery workflow: %v", err)
	}
	policy, ok := delivery.FromCompiled(compiled)
	if !ok {
		t.Fatal("committed feature-delivery workflow has no active delivery policy")
	}
	body, err := policy.RenderCommitMessage(map[string]string{"task": "example task"})
	if err != nil {
		t.Fatalf("RenderCommitMessage: %v", err)
	}
	for _, label := range []string{"Regression:", "Class:", "Sweep:"} {
		idx := strings.Index(body, label)
		if idx < 0 {
			t.Fatalf("commit_message_template render = %q, missing required trailer %q", body, label)
		}
		rest := strings.TrimSpace(body[idx+len(label):])
		if rest == "" || strings.HasPrefix(rest, "\n") {
			t.Fatalf("commit_message_template render = %q, trailer %q has no content on its line", body, label)
		}
	}
}
