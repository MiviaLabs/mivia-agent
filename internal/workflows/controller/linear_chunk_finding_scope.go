package controller

import (
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/blockedpath"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// Chunk finding scope: a mechanical backstop for chunk-mode review loops.
//
// Live finding (three stacked-delivery runs, 2026-08-15): a review gate
// graded one chunk's diff against the WHOLE task and demanded the sibling
// chunks' packages every round. implement cannot fix that demand - the
// chunk-scope guard refuses every write outside the declared slice - so the
// findings repeated byte for byte and the zero-progress convergence gate
// killed the runs. The review template tells the reviewer not to raise
// such findings; this filter enforces that contract in the host, so a
// reviewer that ignores the instruction cannot wedge the loop.

// chunkTokenClass classifies one demanded path token against the chunk's
// declared files. The class decides the drop rule: in-scope tokens and
// foreign tokens keep a finding; only pure sibling demands drop it.
type chunkTokenClass int

const (
	// chunkTokenInScope: the token is a declared file or an ancestor
	// directory of one. The demand is fixable inside the chunk, so the
	// finding stays. Ancestors count as in scope on purpose: a demand on a
	// parent directory may include the declared file.
	chunkTokenInScope chunkTokenClass = iota
	// chunkTokenSibling: the token sits beside or inside the declared
	// files' directory tree but is not declared. The chunk-scope guard
	// refuses writes to it, so a finding that demands ONLY such tokens is
	// not fixable in this run.
	chunkTokenSibling
	// chunkTokenForeign: the token shares no directory with the declared
	// files. Reference-style demands ("match the conventions used in
	// internal/errors/errors.go") name foreign files while the fix belongs
	// to the chunk, so a foreign token never decides a drop.
	chunkTokenForeign
)

// applyChunkFindingScope filters sibling-chunk-only findings from one
// review output before route selection and writes the filtered shape back
// to the result, so routing, persistence, downstream bindings, and the
// zero-progress gate all read what the engine acted on. The re-marshal
// only rewrites the string fields this filter touched; the review gate
// schema carries no numeric fields, so the any-typed round trip stays
// lossless.
func (c *LinearController) applyChunkFindingScope(step definition.Step, attempt workflowledger.StepAttempt, result *AgentStepResult, outMap map[string]any) {
	dropped := c.enforceChunkFindingScope(step, outMap)
	if len(dropped) == 0 {
		return
	}
	c.emitChunkScopeDrop(step.ID, attempt.AttemptNo, dropped)
	if raw, err := json.Marshal(outMap); err == nil {
		result.Output = raw
		result.ValidatedOutput = outMap
	}
}

// panelChunkScopeFilter returns the member-report filter for one panel
// synthesis, or nil when the run is not an armed chunk-mode review. The
// filter applies the STRICTER panel drop rule: panel findings carry no
// required field (only title and description prose), so only
// directory-shaped sibling tokens (missing packages, the live incident
// class) may drop, and a base-name mention of a declared file keeps the
// finding. The builder applies the returned dropped-id set, records it in
// the envelope, and neutralizes a findings-less changes_requested verdict.
func (c *LinearController) panelChunkScopeFilter(stepID string, attemptNo int) func(memberID string, report *PanelMemberReport) []string {
	declared, armed := c.chunkScopeDeclared()
	if !armed {
		return nil
	}
	return func(memberID string, report *PanelMemberReport) []string {
		var dropped []string
		for _, f := range report.Findings {
			if c.panelFindingDemandsSiblingChunkOnly(f.Title+" "+f.Description, declared) {
				dropped = append(dropped, f.ID)
			}
		}
		if len(dropped) > 0 {
			c.emitChunkScopeDrop(stepID, attemptNo, dropped)
		}
		return dropped
	}
}

// maxChunkScopeDropDetailRunes bounds the drop event's detail. Finding ids
// are model-authored strings, so the list is capped, never dumped whole.
const maxChunkScopeDropDetailRunes = 200

// emitChunkScopeDrop publishes one chunk_scope_dropped progress event
// naming the step, the attempt, and the dropped finding ids.
func (c *LinearController) emitChunkScopeDrop(stepID string, attemptNo int, dropped []string) {
	sort.Strings(dropped)
	detail := fmt.Sprintf("dropped %d out-of-chunk-scope finding(s): %s", len(dropped), strings.Join(dropped, ", "))
	if runes := []rune(detail); len(runes) > maxChunkScopeDropDetailRunes {
		detail = string(runes[:maxChunkScopeDropDetailRunes]) + " ..."
	}
	c.emitProgress(ProgressEvent{
		Kind: ProgressChunkScopeDropped, StepID: stepID, AttemptNo: attemptNo, Detail: detail,
	})
}

// chunkScopeDeclared resolves the chunk's declared-file set and reports
// whether the finding-scope filter is armed. Both filter surfaces (the
// agent_gate output filter and the panel member-report filter) arm on the
// same predicate, so the two cannot drift: a stacking workflow with hard
// lines, in chunk mode, with a decodable chunk_plan that declares at least
// one file.
func (c *LinearController) chunkScopeDeclared() (map[string]bool, bool) {
	if c.Workflow == nil || c.Workflow.Stacking == nil || c.Workflow.Stacking.HardLines <= 0 {
		return nil, false
	}
	mode, err := validateStackingReservedInputs(c.Inputs, c.Workflow.Stacking)
	if err != nil || mode != "chunk" {
		return nil, false
	}
	raw, ok := c.Inputs["chunk_plan"].(string)
	if !ok {
		return nil, false
	}
	declared := chunkDeclaredFiles(raw)
	if len(declared) == 0 {
		return nil, false
	}
	return declared, true
}

// enforceChunkFindingScope drops findings that demand only sibling-chunk
// work from a chunk-mode review output, and returns the dropped finding
// ids. When no finding remains, the verdict flips to approved: within the
// chunk's declared scope the delivery satisfied the review. The output map
// is mutated in place because the caller persists it: routing, downstream
// bindings, and the zero-progress gate must all read the filtered shape.
// Runs that are not chunk-mode reviews of a stacking workflow with hard
// lines are returned untouched.
func (c *LinearController) enforceChunkFindingScope(step definition.Step, output map[string]any) []string {
	if step.Kind != "agent_gate" {
		return nil
	}
	if verdict, _ := output["verdict"].(string); verdict != "changes_requested" {
		return nil
	}
	declared, armed := c.chunkScopeDeclared()
	if !armed {
		return nil
	}
	items, ok := output["findings"].([]any)
	if !ok || len(items) == 0 {
		return nil
	}
	kept := make([]any, 0, len(items))
	var dropped []string
	for _, item := range items {
		finding, isObject := item.(map[string]any)
		if !isObject {
			kept = append(kept, item)
			continue
		}
		id, _ := finding["id"].(string)
		required, hasRequired := finding["required"].(string)
		if !hasRequired || strings.TrimSpace(required) == "" || !c.findingDemandsSiblingChunkOnly(required, declared) {
			kept = append(kept, item)
			continue
		}
		dropped = append(dropped, id)
	}
	if len(dropped) == 0 {
		return nil
	}
	sort.Strings(dropped)
	output["findings"] = kept
	if len(kept) == 0 {
		output["verdict"] = "approved"
	}
	return dropped
}

// findingDemandsSiblingChunkOnly reports whether a finding's required text
// demands only work the chunk cannot do: at least one sibling token, no
// in-scope token, and no foreign token. A finding that demands a
// write-blocklisted path is never dropped: the blocked-path check must keep
// its chance to fail the run with the honest cause.
func (c *LinearController) findingDemandsSiblingChunkOnly(required string, declared map[string]bool) bool {
	dir, file, only := c.siblingDemandBreakdown(required, declared)
	return only && dir+file > 0
}

// panelFindingDemandsSiblingChunkOnly is the stricter rule for panel
// findings. Panel reports carry no required field - only title and
// description prose, which describes context as often as it demands work -
// so only DIRECTORY-shaped sibling tokens (missing packages) may drop, and
// any sibling FILE token keeps the finding: prose that discusses a sibling
// file is evidence, not a demand for missing work.
func (c *LinearController) panelFindingDemandsSiblingChunkOnly(text string, declared map[string]bool) bool {
	dir, file, only := c.siblingDemandBreakdown(text, declared)
	return only && dir > 0 && file == 0
}

// siblingDemandBreakdown classifies the path tokens of one demand text
// against the declared files. only reports that EVERY token is a sibling
// demand (at least one sibling token, no in-scope token, no foreign token,
// no declared path or base name named in prose, no write-blocklisted
// demand); dir and file count the sibling tokens by shape (a last segment
// without an extension reads as a directory, the missing-package shape).
func (c *LinearController) siblingDemandBreakdown(text string, declared map[string]bool) (dir, file int, only bool) {
	if len(c.WritePathBlocklist) > 0 && len(blockedpath.PathsDemandedInText(text, c.WritePathBlocklist)) > 0 {
		return 0, 0, false
	}
	// A declared file named in prose - by full path or by bare base name -
	// is an in-scope demand. Base names matter because review prose names
	// files without paths; such words carry no slash, so the path tokenizer
	// skips them by design and an exact word match is the only signal.
	bases := declaredBaseNames(declared)
	for _, field := range strings.Fields(text) {
		word := demandedTokenText(field)
		if declared[word] || bases[word] {
			return 0, 0, false
		}
	}
	tokens := demandedPathTokens(text)
	if len(tokens) == 0 {
		return 0, 0, false
	}
	for _, token := range tokens {
		if classifyChunkToken(token, declared) != chunkTokenSibling {
			return 0, 0, false
		}
		if path.Ext(token) == "" {
			dir++
		} else {
			file++
		}
	}
	return dir, file, true
}

// declaredBaseNames maps the base names of the declared files, for the
// prose word match in siblingDemandBreakdown.
func declaredBaseNames(declared map[string]bool) map[string]bool {
	bases := make(map[string]bool, len(declared))
	for f := range declared {
		bases[path.Base(f)] = true
	}
	return bases
}

// chunkDeclaredFiles decodes the chunk_plan input into its normalized
// declared-file set. The normalization mirrors the delivery-time
// chunk-scope guard: a slash-less top-level file (Makefile, go.mod) is a
// declared entry the implement step may write; entries that are empty
// after normalization do not count, and a plan with no usable entry
// declares no slice (the guard enforces nothing, so the filter stays off
// too).
func chunkDeclaredFiles(raw string) map[string]bool {
	var plan struct {
		Files []string `json:"files"`
	}
	if err := json.Unmarshal([]byte(raw), &plan); err != nil {
		return nil
	}
	declared := make(map[string]bool, len(plan.Files))
	for _, f := range plan.Files {
		if n := normalizeDeclaredFile(f); n != "" {
			declared[n] = true
		}
	}
	return declared
}

// normalizeDeclaredFile canonicalizes one declared plan entry to the
// repo-relative slash form: cleaned, without a leading slash. Slash-less
// names stay. An empty or dot result is "".
func normalizeDeclaredFile(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	p = path.Clean(p)
	p = strings.TrimPrefix(p, "/")
	if p == "." || p == "" {
		return ""
	}
	return p
}

// demandedPathTokens extracts normalized repo-path tokens from a finding's
// required text. Only the required field is scanned, the same boundary the
// blocked-path check uses: evidence and reason are context, not demands.
// External paths (import hosts like github.com, URLs, dot-rooted paths
// such as .mivia) are skipped; they are never the chunk's own files.
func demandedPathTokens(required string) []string {
	var tokens []string
	seen := make(map[string]bool)
	for _, field := range strings.Fields(required) {
		token := normalizeChunkPath(demandedTokenText(field))
		if token == "" || seen[token] || strings.Contains(field, "://") {
			continue
		}
		first := token[:strings.Index(token, "/")]
		if strings.Contains(first, ".") {
			continue
		}
		seen[token] = true
		tokens = append(tokens, token)
	}
	return tokens
}

// lineSuffix matches the trailing anchors prose glues onto file paths: a
// :line or :line-line span, a GitHub-style #L anchor, a :word label
// ("runeutil.go:Fix"), or a possessive 's ("runeutil.go's").
var lineSuffix = regexp.MustCompile(`(:[0-9]+(-[0-9]+)?|:[A-Za-z][A-Za-z0-9_-]*|#L[0-9]+|'s)$`)

// tokenPunctuation is the prose wrapping around a quoted path.
const tokenPunctuation = "`'\"()[]{}<>"

// demandedTokenText strips one whitespace-separated field down to its path
// text: surrounding punctuation, then trailing glued anchors, then
// punctuation again.
func demandedTokenText(field string) string {
	text := strings.Trim(field, tokenPunctuation+",.;:!?")
	text = lineSuffix.ReplaceAllString(text, "")
	return strings.Trim(text, tokenPunctuation+",.;:!?")
}

// normalizeChunkPath canonicalizes a demanded path token to the
// repo-relative slash form: cleaned, without a leading slash. An empty,
// dot, or slash-less result is "" - a slash-less word is not a path token.
func normalizeChunkPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" || !strings.Contains(p, "/") {
		return ""
	}
	p = path.Clean(p)
	p = strings.TrimPrefix(p, "/")
	if p == "." || p == "" || !strings.Contains(p, "/") {
		return ""
	}
	return p
}

// classifyChunkToken classifies one normalized token against the declared
// file set.
func classifyChunkToken(token string, declared map[string]bool) chunkTokenClass {
	parent := path.Dir(token)
	for f := range declared {
		if f == token || strings.HasPrefix(f, token+"/") {
			return chunkTokenInScope
		}
		dir := path.Dir(f)
		if parent == dir || strings.HasPrefix(dir, parent+"/") || strings.HasPrefix(parent, dir+"/") {
			return chunkTokenSibling
		}
	}
	return chunkTokenForeign
}
