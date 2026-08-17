package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/blockedpath"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/verifier"
)

// blockedPathsFromOutput returns the write-blocklisted paths that a SUCCEEDED
// step's output admits it needs to write. Four signals are recognized:
//
//  1. blocked_paths — the agent's own record of host-refused writes.
//  2. files_changed ∩ blocklist — a self-reported claim of touching an
//     unwritable path.
//  3. review findings that DEMAND a blocked-path edit — scanned only in the
//     required field (evidence/reason are context, not demands, so a finding
//     that merely quotes a blocklisted path is not treated as a demand).
//  4. The host-measured actual diff (touchedFilesEvidence) intersected with
//     the blocklist — ground truth, checked directly rather than trusted,
//     since an agent that writes a blocklisted path and omits it from its
//     own files_changed self-report would otherwise bypass this hard
//     security boundary entirely.
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

// gateFailurePathRe extracts path-like tokens from an evidence gate's
// Check.Failures lines (e.g. "NOTE comment block: internal/foo/bar.go
// L5-30 (...)"). Deliberately loose - it over-matches non-path tokens like
// "L5-30", which IsBlockedPath then filters out because they never share a
// prefix with a blocklist entry.
var gateFailurePathRe = regexp.MustCompile(`[A-Za-z0-9_.\-]+(?:/[A-Za-z0-9_.\-]+)+`)

// blockedPathsFromGateFailures returns the write-blocklisted paths named in a
// FAILED evidence gate's own Check.Failures lines, before any repair step is
// dispatched. This is the pre-repair analogue of blockedPathsFromOutput: that
// function only catches a repair AGENT's output after it already spent an
// attempt; this one catches the underlying gate failure naming an unwritable
// file up front, so a run whose only fix requires editing a blocklisted path
// never enters the repair loop at all - it fails immediately with the file
// list, instead of burning a repair attempt that was never going to be
// admitted.
func (c *LinearController) blockedPathsFromGateFailures(result verifier.Result) []string {
	if len(c.WritePathBlocklist) == 0 {
		return nil
	}
	var paths []string
	seen := make(map[string]bool)
	for _, check := range result.Checks {
		if check.Status != "failed" {
			continue
		}
		for _, line := range check.Failures {
			for _, token := range gateFailurePathRe.FindAllString(line, -1) {
				if !blockedpath.IsBlockedPath(token, c.WritePathBlocklist) || seen[token] {
					continue
				}
				seen[token] = true
				paths = append(paths, token)
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
