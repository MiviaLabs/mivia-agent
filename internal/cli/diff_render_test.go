package cli

import (
	"strings"
	"testing"
)

func TestHistoryToolExpand_RendersDiffChanges(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")
	block := ChatBlock{Kind: ChatBlockTool, ToolName: "search_replace", Text: "updated x.txt (+1 −1)\n--- a/x.txt\n+++ b/x.txt\n@@ -1 +1 @@\n-old\n+new", Collapsed: false}
	out := strings.Join(renderOneChatBlock(block, "model", 80, false), "\n")
	if !strings.Contains(out, "-old") || !strings.Contains(out, "+new") {
		t.Fatalf("diff changes missing: %q", out)
	}
}
