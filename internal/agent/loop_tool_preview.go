package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/MiviaLabs/mivia-agent/internal/redact"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// Operator-facing previews of tool input and output.
//
// Everything here is bounded and redacted: these strings fan out to every
// EventBus sink and log, which is a different audience, a different trust
// boundary, and a different size budget from the model-visible bodies the rest
// of the loop deals with.

func redactToolInput(raw string) string { return redactToolInputForTool("", raw) }

// redactToolInputForTool is redactToolInput with a per-tool preview cap,
// mirroring redactToolOutputForTool below. dispatch_tasks gets the wider cap
// for the identical reason that function documents: its input is the
// model-authored task list, and the operator-facing UI's live per-task
// fan-out (internal/ui/screen/conversation/events.go's dispatchTaskIDs)
// re-parses that same preview as JSON - a cut mid-object silently breaks the
// parse and collapses a multi-task batch back into one aggregate row.
func redactToolInputForTool(name, raw string) string {
	redacted := redactedToolInput(raw)
	if name == "dispatch_tasks" {
		// A byte cut cannot work here whatever the budget: the consumer
		// re-parses this string as JSON, so any cut that lands mid-object
		// yields nothing at all. Reduce the STRUCTURE instead - keep the
		// identifying fields, drop the prompt bodies that make the payload
		// large - so the preview stays both small and parseable no matter
		// how long the task prompts are.
		if preview, ok := dispatchTasksPreview(redacted); ok {
			return preview
		}
		// Unparseable input (a malformed call, or a redaction that replaced
		// the whole body): fall back to the byte cut. No worse than before.
		return truncatePreview(redacted, editToolPreviewMaxBytes)
	}
	return truncatePreview(redacted, 256)
}

// dispatchTasksPreview rewrites a dispatch_tasks argument object down to the
// fields the operator surface actually reads - each task's id and whichever
// key names its agent - and drops everything else, above all the prompts.
//
// It exists because the preview has a SECOND consumer beyond display:
// internal/ui/screen/conversation/events.go re-parses it to fan a batch out
// into one row per task. A real multi-task dispatch runs to tens of
// kilobytes of prompts, so the previous byte cap (8 KiB, itself already
// widened once for this reason) cut mid-object, the parse returned nothing,
// and a six-task batch rendered as a single row with no tasks in it while
// six subagents ran. Reducing the structure removes the size dependency
// rather than moving its threshold.
//
// ok is false when raw is not a JSON object with a non-empty tasks array;
// the caller then keeps the old byte-cut behavior.
func dispatchTasksPreview(raw string) (string, bool) {
	var root map[string]any
	if err := json.Unmarshal([]byte(raw), &root); err != nil {
		return "", false
	}
	rawTasks, ok := root["tasks"].([]any)
	if !ok || len(rawTasks) == 0 {
		return "", false
	}
	tasks := make([]any, 0, len(rawTasks))
	for _, rt := range rawTasks {
		task, ok := rt.(map[string]any)
		if !ok {
			tasks = append(tasks, map[string]any{})
			continue
		}
		kept := map[string]any{}
		for _, key := range dispatchPreviewKeys {
			if v, present := task[key]; present {
				if s, isString := v.(string); isString && s != "" {
					kept[key] = s
				}
			}
		}
		tasks = append(tasks, kept)
	}
	out := map[string]any{"tasks": tasks}
	if wait, ok := root["wait"].(string); ok && wait != "" {
		out["wait"] = wait
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		return "", false
	}
	// Still bounded: a pathological batch (very many tasks, or very long
	// ids) falls back rather than breaking this file's size contract. That
	// costs the per-task rows for that batch alone, exactly as today.
	if len(encoded) > editToolPreviewMaxBytes {
		return "", false
	}
	return string(encoded), true
}

// dispatchPreviewKeys are the per-task fields the operator surface reads:
// the task id, and every key extractAgentDisplayName consults to label a
// row. Nothing else is carried - a prompt is the payload, not an identity.
var dispatchPreviewKeys = []string{"id", "agent", "subagent", "role", "type", "skill", "workflow", "name"}

// redactedToolInput is the redacted arguments with NO preview cap: the body
// Event.InputBody carries for chat-sync, which bounds and marks the cut
// itself. Every operator preview is a prefix of this, so nothing reaches the
// wider field that the preview's redaction would have hidden.
func redactedToolInput(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return "{}"
	}
	// Default: operator-visible args passed through the workspace redaction
	// policy. With no policy configured that policy redacts nothing - see
	// .agents/rules/10-security-privacy.md. RedactToolArgs opts into the
	// stricter whole-field elision below; it is a separate control from the
	// patterns and stays meaningful when no policy is set.
	if !tools.RedactToolArgs() {
		return redact.Text(raw)
	}
	var value any
	if json.Unmarshal([]byte(raw), &value) != nil {
		return redact.Text(raw)
	}
	return encodeRedactedPreview(value, raw)
}

// encodeRedactedPreview redacts a decoded argument tree and re-encodes it,
// falling back to the scrubbed raw text when the tree cannot be encoded.
//
// Nothing json.Unmarshal produces is unencodable, and redaction only ever
// substitutes strings, so that fallback has no known trigger from
// redactToolInput. It lives here, rather than as a branch at the call site, so
// the claim is testable rather than asserted: a value this package could never
// build in production can still be handed to this function directly. The
// fallback is deliberately the same one the undecodable-input path takes -
// a preview that says nothing about the call is worse than a scrubbed one.
func encodeRedactedPreview(value any, raw string) string {
	encoded, err := json.Marshal(redactJSONValue(value))
	if err != nil {
		return redact.Text(raw)
	}
	return string(encoded)
}

const redactJSONMaxDepth = 64

// redactJSONValue prepares a decoded tool-argument value for the opt-in
// preview: file bodies are reduced to a byte count, then the workspace policy
// elides values by key name and scrubs the remaining string leaves. Scrubbing
// the leaves keeps opt-in mode a superset of the default path, since key-name
// elision alone misses a credential embedded in an innocuously named field
// ("command", "args").
//
// Key names and patterns come from the policy, never from here. The content
// elision does not: it is preview-size control rather than credential
// redaction - it keeps a whole file body out of every EventBus sink - so it
// applies whether or not a workspace configured any patterns.
func redactJSONValue(value any) any {
	return redact.JSONValue(elideContentPreviews(value, 0))
}

// elideContentPreviews replaces a string value under a "content" key with its
// size. depth stops at redactJSONMaxDepth so deeply nested or crafted input
// cannot overflow the stack.
func elideContentPreviews(value any, depth int) any {
	if depth > redactJSONMaxDepth {
		return value
	}
	switch current := value.(type) {
	case map[string]any:
		for key, nested := range current {
			if strings.ToLower(key) == "content" {
				if text, ok := nested.(string); ok {
					current[key] = fmt.Sprintf("[content %d bytes]", len(text))
					continue
				}
			}
			current[key] = elideContentPreviews(nested, depth+1)
		}
	case []any:
		for i, nested := range current {
			current[i] = elideContentPreviews(nested, depth+1)
		}
	}
	return value
}

const defaultToolPreviewMaxBytes = 512
const editToolPreviewMaxBytes = 8192

func redactToolOutput(output string) string { return redactToolOutputForTool("", output) }

func redactToolOutputForTool(name, output string) string {
	maxBytes := defaultToolPreviewMaxBytes
	switch name {
	case "write_file", "search_replace", "multi_edit",
		// Structured JSON results: a 512-byte cut lands mid-string, which
		// breaks the operator UI's JSON parse and forces a raw-envelope
		// dump instead of a formatted preview (internal/ui/render).
		"ledger_read", "read_output", "dispatch_tasks":
		maxBytes = editToolPreviewMaxBytes
	}
	return truncatePreview(redactedToolOutput(output), maxBytes)
}

// redactedToolOutput is the redacted result with NO preview cap: the body
// Event.OutputBody carries for chat-sync. See redactedToolInput.
func redactedToolOutput(output string) string {
	return redact.Text(output)
}

func truncatePreview(value string, maxBytes int) string {
	if maxBytes <= 0 || len(value) <= maxBytes {
		return value
	}
	value = value[:maxBytes]
	// Back off across the rune at the CUT BOUNDARY only (DC-6). Validating the
	// whole prefix (utf8.ValidString) trims all the way back to the first
	// invalid byte ANYWHERE in value - one stray byte in a tool's output
	// amputates the whole preview and reports it as an ordinary budget cut. It
	// is also O(n^2). DecodeLastRuneInString reports (RuneError, 1) for a byte
	// that cannot start a rune or an incomplete trailing sequence; a real
	// U+FFFD decodes with size 3 and is kept. Mirrors chatsync.truncateString.
	for len(value) > 0 {
		r, size := utf8.DecodeLastRuneInString(value)
		if r != utf8.RuneError || size > 1 {
			break
		}
		value = value[:len(value)-1]
	}
	return value
}
