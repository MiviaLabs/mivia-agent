package tools

// Every tool that returns file CONTENT refuses a secret-like path: read_file,
// list_dir, grep, write, edit, delete, run_command, and list_symbols all call
// isSecretPath. go_to_definition and find_symbol_context also return file
// content - read live from disk at the symbol's span - and did not.
//
// The gap was in the shared registration site, which forwarded the configured
// patterns to list_symbols only, so the other two could not have enforced the
// policy even if they had tried.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/codeintel"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

type fixedDefinitionResolver struct{ def codeintel.Definition }

func (f fixedDefinitionResolver) Definition(context.Context, string) (codeintel.Definition, error) {
	return f.def, nil
}

func (f fixedDefinitionResolver) References(context.Context, string, []codeintel.Role, int) (codeintel.Result, error) {
	return codeintel.Result{Symbol: f.def.Symbol}, nil
}

// secretDefinition is a symbol whose declaration lives in a file the operator
// declared secret-like.
func secretDefinition() codeintel.Definition {
	return codeintel.Definition{
		Symbol: "APIKey",
		Kind:   codeintel.KindConst,
		Path:   "secrets/creds.go",
		Source: "const APIKey = \"sk-live-not-a-real-key\"",
	}
}

func TestGoToDefinitionRefusesSecretPath(t *testing.T) {
	ws := &workspace.Root{Abs: t.TempDir()}
	tool := &goToDefinitionTool{
		ws:                 ws,
		resolver:           fixedDefinitionResolver{def: secretDefinition()},
		maxBytes:           100_000,
		secretPathPatterns: []string{"secrets/"},
	}
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"symbol":"APIKey"}`))
	if err == nil && strings.Contains(out, "sk-live-not-a-real-key") {
		t.Fatalf("go_to_definition returned the contents of a secret-like path: %s", out)
	}
}

func TestFindSymbolContextRefusesSecretPath(t *testing.T) {
	ws := &workspace.Root{Abs: t.TempDir()}
	tool := &findSymbolContextTool{
		ws:                 ws,
		resolver:           fixedDefinitionResolver{def: secretDefinition()},
		maxBytes:           100_000,
		secretPathPatterns: []string{"secrets/"},
	}
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"symbol":"APIKey"}`))
	if err == nil && strings.Contains(out, "sk-live-not-a-real-key") {
		t.Fatalf("find_symbol_context returned the contents of a secret-like path: %s", out)
	}
}

// The registration site is the root cause: it forwarded the policy to one of
// the three code-nav tools. A tool built without the patterns cannot enforce
// them, so parity is asserted at construction.
func TestCodeNavToolsAllReceiveSecretPathPolicy(t *testing.T) {
	ws := &workspace.Root{Abs: t.TempDir()}
	var registered []Tool
	registerCodeNavTools(func(tool Tool) { registered = append(registered, tool) },
		DefaultOptions{}, ws, []string{"secrets/"}, []string{"secrets/ok.go"})

	for _, tool := range registered {
		switch typed := tool.(type) {
		case *goToDefinitionTool:
			if len(typed.secretPathPatterns) == 0 {
				t.Error("go_to_definition was built without the secret-path patterns")
			}
		case *findSymbolContextTool:
			if len(typed.secretPathPatterns) == 0 {
				t.Error("find_symbol_context was built without the secret-path patterns")
			}
		case *listSymbolsTool:
			if len(typed.secretPathPatterns) == 0 {
				t.Error("list_symbols was built without the secret-path patterns")
			}
		}
	}
	if len(registered) == 0 {
		t.Fatal("no code-nav tools registered")
	}
}
