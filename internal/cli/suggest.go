package cli

import (
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/sahilm/fuzzy"
)

const suggestWindowRows = 8

type suggestState struct {
	open           bool
	dismissedToken string
	token          string
	from, to       int
	selected       int
	commands       []SlashCommand
}

func (m *tuiModel) syncSuggest() {
	value := m.textarea.Value()
	cursor := m.textarea.LineInfo().StartColumn + m.textarea.LineInfo().ColumnOffset
	from, to, token, ok := suggestToken(value, cursor)
	if !ok {
		m.closeSuggest()
		m.suggest.dismissedToken = ""
		return
	}
	if m.suggest.dismissedToken != "" && m.suggest.dismissedToken != token {
		m.suggest.dismissedToken = ""
	}
	if m.suggest.dismissedToken == token {
		m.closeSuggest()
		return
	}
	if m.suggest.open && m.suggest.token == token {
		m.suggest.from, m.suggest.to = from, to
		return
	}
	binding := m.session.CurrentBinding()
	commands := rankSlashCommands(token, slashCommands(slashSurfaceTUI, binding.SkillRegistry))
	if len(commands) == 0 {
		m.closeSuggest()
		return
	}
	m.suggest = suggestState{open: true, token: token, from: from, to: to, commands: commands}
}

func (m *tuiModel) closeSuggest() {
	m.suggest.open = false
	m.suggest.commands = nil
	m.suggest.selected = 0
}

func (m *tuiModel) dismissSuggest() {
	m.suggest.dismissedToken = m.suggest.token
	m.closeSuggest()
}

// handleSuggestKey owns composer keys only while a suggestion popup is open.
// It is deliberately called before focus cycling and normal enter handling.
func (m *tuiModel) handleSuggestKey(key string) (bool, bool, []tea.Cmd) {
	if !m.suggest.open || m.focus != focusComposer || len(m.suggest.commands) == 0 {
		return false, false, nil
	}
	switch key {
	case "up", "ctrl+p":
		m.suggest.selected = (m.suggest.selected - 1 + len(m.suggest.commands)) % len(m.suggest.commands)
		return true, true, nil
	case "down", "ctrl+n":
		m.suggest.selected = (m.suggest.selected + 1) % len(m.suggest.commands)
		return true, true, nil
	case "tab", "enter":
		selected := m.suggest.commands[m.suggest.selected]
		commandEnd := m.suggest.from + len([]rune(selected.Name))
		applyTokenReplace(&m.textarea, m.suggest.from, m.suggest.to, selected.Name+" ")
		m.closeSuggest()
		if key == "enter" && selected.AutoExecute && suggestHasNoArguments(m.textarea.Value(), commandEnd) {
			if m.mode == modeWelcome {
				return true, true, m.handleWelcomeEnter(strings.TrimSpace(m.textarea.Value()))
			}
			return m.handleChatEnter(false)
		}
		return true, true, nil
	case "esc", "shift+tab":
		m.dismissSuggest()
		return true, true, nil
	case "pgup", "pgdown", "home", "end":
		// These keys still navigate the composer after this handler declines
		// them, so retain dismissal through the footer sync for the same token.
		m.dismissSuggest()
		return false, false, nil
	}
	return false, false, nil
}

func suggestHasNoArguments(value string, commandEnd int) bool {
	runes := []rune(value)
	commandEnd = min(max(0, commandEnd), len(runes))
	return strings.TrimSpace(string(runes[commandEnd:])) == ""
}

func suggestToken(value string, cursor int) (from, to int, token string, ok bool) {
	if strings.Contains(value, "\n") {
		return 0, 0, "", false
	}
	runes := []rune(value)
	from = 0
	for from < len(runes) && (runes[from] == ' ' || runes[from] == '\t') {
		from++
	}
	if from == len(runes) || runes[from] != '/' {
		return 0, 0, "", false
	}
	to = from
	for to < len(runes) && runes[to] != ' ' && runes[to] != '\t' {
		to++
	}
	if cursor < from || cursor > to {
		return 0, 0, "", false
	}
	return from, to, string(runes[from:to]), true
}

func rankSlashCommands(query string, commands []SlashCommand) []SlashCommand {
	if query == "/" {
		return orderBareSlashCommands(commands)
	}
	type scored struct {
		command     SlashCommand
		tier, score int
	}
	query = strings.ToLower(query)
	var matches []scored
	for _, command := range commands {
		name := strings.ToLower(command.Name)
		tier, score := 0, 0
		switch {
		case query == name:
			tier = 1
		case hasExactAlias(query, command.Aliases):
			tier = 1
		case strings.HasPrefix(name, query):
			tier = 2
		case hasAliasPrefix(query, command.Aliases):
			tier = 3
		case fuzzyScore(query, name) > 0:
			tier, score = 4, fuzzyScore(query, name)
		case command.Kind == slashKindSkill && fuzzyScore(query, strings.ToLower(command.Description)) > 0:
			tier, score = 5, fuzzyScore(query, strings.ToLower(command.Description))
		default:
			continue
		}
		matches = append(matches, scored{command: command, tier: tier, score: score})
	}
	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].tier != matches[j].tier {
			return matches[i].tier < matches[j].tier
		}
		if matches[i].command.Kind != matches[j].command.Kind {
			return matches[i].command.Kind < matches[j].command.Kind
		}
		if matches[i].command.Origin != matches[j].command.Origin {
			return matches[i].command.Origin == "project"
		}
		if matches[i].score != matches[j].score {
			return matches[i].score > matches[j].score
		}
		return matches[i].command.Name < matches[j].command.Name
	})
	out := make([]SlashCommand, len(matches))
	for i := range matches {
		out[i] = matches[i].command
	}
	return out
}

func orderBareSlashCommands(commands []SlashCommand) []SlashCommand {
	order := make(map[string]int)
	for i, name := range []string{"/help", "/clear", "/model", "/agent", "/agents", "/status", "/sessions", "/save", "/load", "/search"} {
		order[name] = i
	}
	out := append([]SlashCommand(nil), commands...)
	rank := func(name string) int {
		if index, ok := order[name]; ok {
			return index
		}
		return len(order)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		if out[i].Kind == slashKindBuiltin {
			return rank(out[i].Name) < rank(out[j].Name)
		}
		if out[i].Origin != out[j].Origin {
			return out[i].Origin == "project"
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func fuzzyScore(query, value string) int {
	matches := fuzzy.Find(query, []string{value})
	if len(matches) == 0 {
		return 0
	}
	return matches[0].Score
}

func hasExactAlias(query string, aliases []string) bool {
	for _, alias := range aliases {
		if query == strings.ToLower(alias) {
			return true
		}
	}
	return false
}

func hasAliasPrefix(query string, aliases []string) bool {
	for _, alias := range aliases {
		if strings.HasPrefix(strings.ToLower(alias), query) {
			return true
		}
	}
	return false
}

func applyTokenReplace(ta *textarea.Model, from, to int, insert string) {
	full := []rune(ta.Value())
	from = min(max(0, from), len(full))
	to = min(max(from, to), len(full))
	next := string(full[:from]) + insert + string(full[to:])
	ta.SetValue(next)
	ta.SetCursor(from + len([]rune(insert)))
}
