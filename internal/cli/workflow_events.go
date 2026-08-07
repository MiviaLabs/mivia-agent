package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// executeWorkflowEvents prints the run's durable audit trail (wf_* events)
// in sequence order, paged. limit/offset of 0 mean defaults in the ledger.
func executeWorkflowEvents(runID, root, configPath string, limit, offset int, stdout, stderr io.Writer) error {
	repo, closeFn, err := openWorkflowReportContext(root, configPath)
	if err != nil {
		return err
	}
	defer closeFn()
	ctx := context.Background()
	events, err := repo.ListEvents(ctx, runID, limit, offset)
	if err != nil {
		if errors.Is(err, workflowledger.ErrNotFound) {
			return fmt.Errorf("workflow run %q not found", runID)
		}
		return err
	}
	for _, ev := range events {
		fmt.Fprintf(stdout, "%d %s %s %s\n", ev.Sequence, ev.CreatedAt.UTC().Format(time.RFC3339), ev.Kind, ev.Summary)
	}
	if len(events) == 0 {
		fmt.Fprintln(stdout, "no events")
		return nil
	}
	if limit > 0 && len(events) == limit {
		fmt.Fprintf(stdout, "showing %d of at least %d events; pass --limit/--offset to page\n", len(events), offset+len(events))
	}
	return nil
}

// parseWorkflowIntFlag parses an integer flag value from args after the named
// flag, e.g. --limit 50. It returns def when the flag is absent.
func parseWorkflowIntFlag(args []string, name string, def int) (int, error) {
	for i := 0; i < len(args); i++ {
		if strings.HasPrefix(args[i], name+"=") {
			v, err := strconv.Atoi(strings.TrimPrefix(args[i], name+"="))
			if err != nil {
				return 0, fmt.Errorf("%s requires an integer value", name)
			}
			if v < 0 {
				return 0, fmt.Errorf("%s must be >= 0", name)
			}
			return v, nil
		}
		if args[i] != name || i+1 >= len(args) {
			continue
		}
		v, err := strconv.Atoi(args[i+1])
		if err != nil {
			return 0, fmt.Errorf("%s requires an integer value", name)
		}
		if v < 0 {
			return 0, fmt.Errorf("%s must be >= 0", name)
		}
		return v, nil
	}
	return def, nil
}

// parseWorkflowStringFlag returns the value of the named string flag plus the
// remaining positional arguments, e.g. --actor alice run-id approval-id.
func parseWorkflowStringFlag(args []string, name string) (string, []string, error) {
	out := make([]string, 0, len(args))
	value := ""
	for i := 0; i < len(args); i++ {
		if args[i] == name {
			if i+1 >= len(args) {
				return "", nil, fmt.Errorf("%s requires a value", name)
			}
			value = args[i+1]
			i++
			continue
		}
		if strings.HasPrefix(args[i], name+"=") {
			value = strings.TrimPrefix(args[i], name+"=")
			continue
		}
		out = append(out, args[i])
	}
	return value, out, nil
}
