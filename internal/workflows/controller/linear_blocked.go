package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/blockedpath"
)

// blockedPathsFromOutput returns the write-blocklisted paths that a SUCCEEDED
// step's output admits it needs to write. Three signals are recognized:
//
//  1. blocked_paths — the agent's explicit record of host-refused writes. This
//     is the primary signal and works even without a controller-side
//     blocklist: the agent itself recorded the host refusal.
//
//  2. files_changed ∩ blocklist — a claim that the change modified a path the
//     host write policy makes unwritable for workflow agents. No agent can
//     legitimately change such a file, so a claim of one is a blocked signal.
//
//  3. review findings that DEMAND a blocked-path edit. A finding demands an
//     edit only through its required field (the review schema guarantees a
//     non-empty string), so only that field is scanned for a path token plus
//     demand verb. Evidence and reason are context, not demands: a finding
//     that merely quotes a blocklisted path (say, doc content mentioning
//     ".mivia/agents/<name>.toml") while requiring a plan correction must
//     not be treated as a demand to write that path. A mere mention of a
//     blocklisted path is not a demand and is ignored.
//
//  4. The host-measured actual diff (touchedFilesEvidence) intersected with
//     the blocklist. Signal 2 above only catches a blocklisted write the
//     agent VOLUNTEERED in its own files_changed; an agent that writes a
//     blocklisted path and omits it from that self-report (honestly or not)
//     would otherwise bypass this gate entirely - the same self-report gap
//     already confirmed for review visibility (touchedFilesEvidence), but
//     here it is the actual enforcement mechanism for a hard security
//     boundary, so ground truth is checked directly rather than trusted.
//
// Paths are deduplicated and sorted for deterministic error messages.
func (c *LinearController) blockedPathsFromOutput(ctx context.Context, output map[string]any) []string {
	var paths []string
	seen := make(map[string]bool)
	add := func(p string) {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			return
		}
		seen[p] = true
		paths = append(paths, p)
	}

	if raw, ok := output["blocked_paths"].([]any); ok {
		for _, item := range raw {
			if s, ok := item.(string); ok {
				add(s)
			}
		}
	}
	if len(c.WritePathBlocklist) > 0 {
		if raw, ok := output["files_changed"].([]any); ok {
			for _, item := range raw {
				if s, ok := item.(string); ok && blockedpath.IsBlockedPath(s, c.WritePathBlocklist) {
					add(s)
				}
			}
		}
		for _, s := range c.actualTouchedFiles(ctx) {
			if blockedpath.IsBlockedPath(s, c.WritePathBlocklist) {
				add(s)
			}
		}
		if raw, ok := output["findings"].([]any); ok {
			for _, item := range raw {
				switch f := item.(type) {
				case map[string]any:
					// The demand of a finding lives in its required field (the
					// review schema guarantees a non-empty string). Scanning
					// the whole marshaled finding would merge evidence and
					// demand onto one line: a finding whose evidence merely
					// quotes a blocklisted path (doc content mentioning
					// ".mivia/agents/<name>.toml") while required says
					// "correct the plan" would be misread as a demand to
					// write the blocked path.
					if required, ok := f["required"].(string); ok && strings.TrimSpace(required) != "" {
						for _, p := range blockedpath.PathsDemandedInText(required, c.WritePathBlocklist) {
							add(p)
						}
					}
				case string:
					for _, p := range blockedpath.PathsDemandedInText(f, c.WritePathBlocklist) {
						add(p)
					}
				}
			}
		}
	}
	sort.Strings(paths)
	return paths
}

// actualTouchedFiles decodes touchedFilesEvidence's JSON-encoded file list
// (the host-measured worktree diff vs the admitted base), or returns nil
// when no git context is wired, no base commit was admitted, or the JSON is
// empty - the same best-effort conditions touchedFilesEvidence itself uses.
func (c *LinearController) actualTouchedFiles(ctx context.Context) []string {
	raw := c.touchedFilesEvidence(ctx)
	if raw == "" {
		return nil
	}
	var files []string
	if err := json.Unmarshal([]byte(raw), &files); err != nil {
		return nil
	}
	return files
}

// blockedCause reports whether a succeeded step's output admits a write to a
// write-blocklisted path, and if so returns the durable blocked cause. The
// cause names the paths so the failure is attributable: the step is not a
// repair-loop candidate because no workflow agent can satisfy the demand.
func (c *LinearController) blockedCause(ctx context.Context, output map[string]any) (error, bool) {
	paths := c.blockedPathsFromOutput(ctx, output)
	if len(paths) == 0 {
		return nil, false
	}
	return fmt.Errorf("workflow blocked: write path(s) %s are write-blocklisted for workflow agents (host policy); the run cannot proceed - route this change through the root session or a host-owned process", strings.Join(paths, ", ")), true
}
