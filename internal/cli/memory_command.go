package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/memory"
)

// runMemory handles the memory CLI commands. Only the search subcommand
// exists today.
func runMemory(args []string) error {
	return runMemoryWithIO(args, os.Stdout, os.Stderr)
}

func runMemoryWithIO(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("memory: expected search")
	}
	subcommand := args[0]
	if subcommand != "search" {
		return fmt.Errorf("memory: unknown subcommand %q (try search)", safeCatalogText(subcommand, 80))
	}
	query, scope, limit, jsonFlag, workspaceRoot, cfgPath, err := parseMemorySearchArgs(args[1:])
	if err != nil {
		return err
	}
	root, err := chatWorkspaceRoot(workspaceRoot)
	if err != nil {
		return fmt.Errorf("memory search: %w", err)
	}
	res, err := config.Load(config.LoadOptions{
		ConfigPath:         cfgPath,
		WorkspaceRoot:      root,
		AllowMissingConfig: true,
	})
	if err != nil {
		return err
	}
	if !res.Memory.IsEnabled() {
		return fmt.Errorf("memory search: memory is disabled; set [memory] enabled = true")
	}
	store, err := openMemoryStoreReadOnly(root, res.Memory)
	if err != nil {
		return fmt.Errorf("memory search: %w", err)
	}
	defer store.Close()
	results, err := store.Search(context.Background(), memory.Query{Text: query, Scope: scope, MaxResults: limit})
	if err != nil {
		return fmt.Errorf("memory search: %w", err)
	}
	if jsonFlag {
		return writeMemorySearchJSON(stdout, results)
	}
	writeMemorySearchHuman(stdout, query, results)
	return nil
}

// parseMemorySearchArgs parses `memory search` flags and the query. The query
// is every positional token joined with spaces: the store's token-AND search
// semantics match all words. Flag validation follows parseAgentsArgs: a value
// flag refuses a missing or dash-prefixed space value (DC-9 fail-open), while
// the "=" form stays permissive so negative limits remain expressible as
// --limit=-2.
func parseMemorySearchArgs(args []string) (query string, scope memory.Scope, limit int, jsonFlag bool, workspaceRoot, configPath string, err error) {
	scope = memory.ScopeAll
	var positional []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		name, value, hasValue := cutMemoryFlag(arg)
		switch name {
		case "--workspace":
			workspaceRoot, i, err = memoryFlag(name, value, "a directory", hasValue, args, i)
			if err == nil && (strings.TrimSpace(workspaceRoot) == "" || strings.HasPrefix(workspaceRoot, "-")) {
				err = fmt.Errorf("memory search: --workspace requires a directory")
			}
		case "--config":
			configPath, i, err = memoryFlag(name, value, "a path", hasValue, args, i)
			if err == nil && (strings.TrimSpace(configPath) == "" || strings.HasPrefix(configPath, "-")) {
				err = fmt.Errorf("memory search: --config requires a path")
			}
		case "--scope":
			var raw string
			raw, i, err = memoryFlag(name, value, "a value (project, org, or all)", hasValue, args, i)
			if err == nil {
				scope, err = parseMemoryScope(raw)
			}
		case "--limit":
			var raw string
			raw, i, err = memoryFlag(name, value, "a value", hasValue, args, i)
			if err == nil {
				limit, err = parseMemoryLimit(raw)
			}
		case "--json":
			if hasValue {
				err = fmt.Errorf("memory search: unknown flag %q", safeCatalogText(arg, 80))
				break
			}
			if jsonFlag {
				err = fmt.Errorf("memory search: duplicate --json flag")
				break
			}
			jsonFlag = true
		case "":
			if strings.HasPrefix(arg, "-") {
				err = fmt.Errorf("memory search: unknown flag %q", safeCatalogText(arg, 80))
				break
			}
			positional = append(positional, arg)
		default:
			err = fmt.Errorf("memory search: unknown flag %q", safeCatalogText(arg, 80))
		}
		if err != nil {
			return "", "", 0, false, "", "", err
		}
	}
	query = strings.Join(positional, " ")
	if strings.TrimSpace(query) == "" {
		return "", "", 0, false, "", "", fmt.Errorf("memory search: missing search query")
	}
	return query, scope, limit, jsonFlag, workspaceRoot, configPath, nil
}

// cutMemoryFlag splits one argument into a flag name and an "=" value. A
// non-flag argument yields an empty name so the parser treats it as a query
// token.
func cutMemoryFlag(arg string) (name, value string, hasValue bool) {
	if strings.HasPrefix(arg, "-") {
		return strings.Cut(arg, "=")
	}
	return "", "", false
}

// memoryFlag resolves one value flag in both forms ("--name value" and
// "--name=value"). The space form refuses a missing or dash-prefixed value
// instead of swallowing a following flag (DC-9), matching flagValue. The "="
// form stays permissive; callers validate dash-prefixed values when a dash
// cannot be a legal value.
func memoryFlag(name, value, what string, hasValue bool, args []string, i int) (string, int, error) {
	if !hasValue {
		if i+1 >= len(args) || strings.TrimSpace(args[i+1]) == "" || strings.HasPrefix(args[i+1], "-") {
			return "", i, fmt.Errorf("memory search: %s requires %s", name, what)
		}
		value = args[i+1]
		i++
	}
	if strings.TrimSpace(value) == "" {
		return "", i, fmt.Errorf("memory search: %s requires %s", name, what)
	}
	return value, i, nil
}

// parseMemoryScope validates the --scope value against the store's scope set.
func parseMemoryScope(value string) (memory.Scope, error) {
	scope := memory.Scope(strings.TrimSpace(value))
	switch scope {
	case memory.ScopeProject, memory.ScopeOrg, memory.ScopeAll:
		return scope, nil
	default:
		return "", fmt.Errorf("memory search: --scope must be project, org, or all, got %q", safeCatalogText(value, 40))
	}
}

// parseMemoryLimit validates the --limit value. A non-numeric value is an
// error; a numeric value <= 0 becomes 0 so the store clamps MaxResults to
// [memory] max_search_results.
func parseMemoryLimit(value string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return 0, fmt.Errorf("memory search: --limit must be a positive integer, got %q", safeCatalogText(value, 40))
	}
	if n <= 0 {
		return 0, nil
	}
	return n, nil
}

// memorySearchJSONResult is one JSON result. It mirrors the memory_search
// tool's envelope: id, scope, org, title, verdict, tags, created, summary.
// The search hit's text travels as memory.Result.Snippet and is serialized
// as the JSON "summary" field (the memory_search tool input schema names the
// same text "summary"), so --json output is directly comparable to the
// tool's result envelope.
type memorySearchJSONResult struct {
	ID      string         `json:"id"`
	Scope   memory.Scope   `json:"scope"`
	Org     string         `json:"org"`
	Title   string         `json:"title"`
	Verdict memory.Verdict `json:"verdict"`
	Tags    []string       `json:"tags"`
	Created string         `json:"created"`
	Summary string         `json:"summary"`
}

// writeMemorySearchJSON writes the results as a JSON array. An empty result
// set encodes as "[]".
func writeMemorySearchJSON(w io.Writer, results []memory.Result) error {
	out := make([]memorySearchJSONResult, 0, len(results))
	for _, r := range results {
		out = append(out, memorySearchJSONResult{
			ID: r.ID, Scope: r.Scope, Org: r.Org, Title: r.Title,
			Verdict: r.Verdict, Tags: r.Tags, Created: r.Created, Summary: r.Snippet,
		})
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		return fmt.Errorf("memory search: json encode failed: %w", err)
	}
	return nil
}

// writeMemorySearchHuman prints each result as one compact ranked block:
// title, scope, verdict, created date, tags, and the summary snippet. Zero
// results print one friendly line.
func writeMemorySearchHuman(w io.Writer, query string, results []memory.Result) {
	if len(results) == 0 {
		fmt.Fprintf(w, "no memories found for %q\n", query)
		return
	}
	fmt.Fprintf(w, "memory search results (%d) for %q:\n", len(results), query)
	for i, r := range results {
		fmt.Fprintf(w, "%d. %s\n", i+1, r.Title)
		fmt.Fprintf(w, "   scope: %s verdict: %s created: %s\n", r.Scope, r.Verdict, r.Created)
		fmt.Fprintf(w, "   tags: %s\n", formatMemoryTags(r.Tags))
		fmt.Fprintf(w, "   %s\n", r.Snippet)
	}
}

// formatMemoryTags renders the tag list for the human output.
func formatMemoryTags(tags []string) string {
	if len(tags) == 0 {
		return "(none)"
	}
	return strings.Join(tags, ", ")
}
