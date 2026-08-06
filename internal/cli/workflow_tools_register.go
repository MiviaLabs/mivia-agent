package cli

import (
	"context"
	"encoding/json"

	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/agenttools"
)

func init() {
	// Install the Phase 7 workflow tool builder so NewDefaultRegistry registers
	// the seven tools when a workspace has .mivia/workflows/.
	tools.SetWorkflowToolsBuilder(buildWorkflowToolsForRegistry)
}

// buildWorkflowToolsForRegistry constructs the seven workflow tools for the
// default registry. Pre-built opts.WorkflowTools win when the session already
// wired a Service (chat path).
func buildWorkflowToolsForRegistry(opts tools.DefaultOptions) []tools.Tool {
	if len(opts.WorkflowTools) > 0 {
		return opts.WorkflowTools
	}
	root := ""
	if opts.Workspace != nil {
		root = opts.Workspace.Abs
	}
	if !agenttools.HasWorkflows(root) {
		return nil
	}
	svc := workflowToolService(root, nil)
	if svc == nil {
		return nil
	}
	return wrapWorkflowTools(svc)
}

func wrapWorkflowTools(svc *agenttools.Service) []tools.Tool {
	if svc == nil {
		return nil
	}
	out := make([]tools.Tool, 0, 7)
	for _, inner := range agenttools.Tools(svc) {
		out = append(out, &workflowRegistryTool{inner: inner})
	}
	return out
}

// workflowRegistryTool adapts agenttools.Tool to tools.Tool.
type workflowRegistryTool struct {
	inner agenttools.Tool
}

func (t *workflowRegistryTool) Name() string               { return t.inner.Name() }
func (t *workflowRegistryTool) Description() string        { return t.inner.Description() }
func (t *workflowRegistryTool) Parameters() map[string]any { return t.inner.Parameters() }
func (t *workflowRegistryTool) ResultBudgetBytes() int     { return t.inner.ResultBudgetBytes() }
func (t *workflowRegistryTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	return t.inner.Execute(ctx, args)
}

func (t *workflowRegistryTool) Capability(args json.RawMessage) tools.Capability {
	_ = args
	class := tools.ExecutionRead
	if t.inner.Class() == "write" {
		class = tools.ExecutionWrite
	}
	return tools.Capability{Class: class, ResourceKey: "workflow"}
}
