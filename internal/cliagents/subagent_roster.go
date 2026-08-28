package cliagents

import (
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
)

// Roster bounds. The "# Subagents" section is appended to the root system
// prompt, so its parts are clamped to keep the assembled prompt bounded and
// provider prompt-cache friendly (the section changes only when the roster
// does).
const (
	// SubagentRosterMaxLines caps the roster entries; overflow collapses into
	// a tail line pointing at the dispatch_tasks agent enum.
	SubagentRosterMaxLines = 8
	// subagentRosterDescBytes clamps each sanitized description.
	subagentRosterDescBytes = 160
	// subagentRosterNameBytes clamps each name.
	subagentRosterNameBytes = 40
)

// SubagentRosterSection renders the "# Subagents" prompt section: one line
// per agent in registry order (file-backed agents first, then compiled
// built-ins), capped at SubagentRosterMaxLines with an overflow tail. Empty
// when there is nothing to announce. A pure function of the immutable
// registry snapshot.
func SubagentRosterSection(registry *agents.AgentRegistry) string {
	if registry == nil {
		return ""
	}
	list := registry.List()
	if len(list) == 0 {
		return ""
	}
	shown := list
	truncated := false
	if len(shown) > SubagentRosterMaxLines {
		shown = shown[:SubagentRosterMaxLines]
		truncated = true
	}
	var b strings.Builder
	b.WriteString("\n\n# Subagents\nLoaded subagents, selectable via dispatch_tasks' optional agent field:\n")
	for _, agent := range shown {
		b.WriteString("- ")
		b.WriteString(subagentRosterEntry(agent))
		b.WriteString("\n")
	}
	if truncated {
		b.WriteString("- ...and ")
		b.WriteString(strconv.Itoa(len(list) - SubagentRosterMaxLines))
		b.WriteString(" more (full roster in the dispatch_tasks agent enum)\n")
	}
	return b.String()
}

// RootSystemPromptWithRoster appends the roster section ADDITIVELY to the
// root system prompt - the compiled fallback or an operator's custom
// [chat].system_prompt alike - because the roster is environment fact, not
// user content. It lives at the prompt-assignment point, never inside
// buildAgentPrompt, so a customized prompt still gets the announcement. An
// empty section returns the prompt unchanged byte-for-byte.
func RootSystemPromptWithRoster(prompt string, registry *agents.AgentRegistry) string {
	section := SubagentRosterSection(registry)
	if section == "" {
		return prompt
	}
	if strings.TrimSpace(prompt) == "" {
		return strings.TrimPrefix(section, "\n\n")
	}
	return prompt + section
}

func subagentRosterEntry(agent agents.ResolvedAgent) string {
	name := truncateUTF8(agent.Name, subagentRosterNameBytes)
	desc := strings.TrimSpace(agents.SanitizeDescription(agent.Description))
	desc = truncateUTF8(firstSentence(desc), subagentRosterDescBytes)
	if desc == "" {
		return name
	}
	return name + ": " + desc
}

// firstSentence keeps the opening sentence of a description; roster lines are
// orientation, not documentation.
func firstSentence(s string) string {
	if i := strings.IndexByte(s, '.'); i > 0 {
		return s[:i+1]
	}
	return s
}

// truncateUTF8 cuts s to at most max bytes without splitting a rune.
func truncateUTF8(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}
