package cli

import (
	"fmt"
	"io"
	"os"
	"strings"
)

// completionShells lists the shell targets `mivia completion` supports.
var completionShells = []string{"bash", "zsh", "fish"}

// completionCommands lists the top-level mivia commands. Keep it in sync with
// the Execute switch in root.go.
var completionCommands = []string{
	"chat", "config", "doctor", "agents", "memory", "workflows", "workflow",
	"worktree", "version", "help", "completion", "setup", "login", "logout",
}

// runCompletion prints a static completion script for the requested shell.
func runCompletion(args []string) error {
	return runCompletionWithIO(args, os.Stdout)
}

func runCompletionWithIO(args []string, stdout io.Writer) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: mivia completion <shell> (%s)", strings.Join(completionShells, "|"))
	}
	var script string
	switch args[0] {
	case "bash":
		script = bashCompletionScript()
	case "zsh":
		script = zshCompletionScript()
	case "fish":
		script = fishCompletionScript()
	default:
		return fmt.Errorf("completion: unsupported shell %q (%s)", args[0], strings.Join(completionShells, "|"))
	}
	_, err := io.WriteString(stdout, script)
	return err
}

// bashCompletionScript returns the bash completion definition.
func bashCompletionScript() string {
	commands := strings.Join(completionCommands, " ")
	return fmt.Sprintf(`# bash completion for mivia
_mivia_completion() {
    local cur
    cur="${COMP_WORDS[COMP_CWORD]}"
    if [ "${COMP_CWORD}" -eq 1 ]; then
        COMPREPLY=( $(compgen -W "%s" -- "${cur}") )
        return 0
    fi
    case "${COMP_WORDS[1]}" in
        config)     COMPREPLY=( $(compgen -W "show" -- "${cur}") ) ;;
        doctor)     COMPREPLY=( $(compgen -W "--json --config --workspace" -- "${cur}") ) ;;
        agents)     COMPREPLY=( $(compgen -W "list explain" -- "${cur}") ) ;;
        memory)     COMPREPLY=( $(compgen -W "search" -- "${cur}") ) ;;
        workflows)  COMPREPLY=( $(compgen -W "list show validate explain" -- "${cur}") ) ;;
        workflow)   COMPREPLY=( $(compgen -W "run runs deliver resume" -- "${cur}") ) ;;
        worktree)   COMPREPLY=( $(compgen -W "create list remove" -- "${cur}") ) ;;
        completion) COMPREPLY=( $(compgen -W "bash zsh fish" -- "${cur}") ) ;;
    esac
    return 0
}
complete -F _mivia_completion mivia
`, commands)
}

// zshCompletionScript returns the zsh completion definition.
func zshCompletionScript() string {
	commands := strings.Join(completionCommands, " ")
	return fmt.Sprintf(`#compdef mivia
# zsh completion for mivia
_mivia_completion() {
    local -a commands
    commands=(%s)
    if (( CURRENT == 2 )); then
        _describe 'command' commands
        return
    fi
    case "${words[2]}" in
        config)     _values 'subcommand' show ;;
        doctor)     _values 'flag' --json --config --workspace ;;
        agents)     _values 'subcommand' list explain ;;
        memory)     _values 'subcommand' search ;;
        workflows)  _values 'subcommand' list show validate explain ;;
        workflow)   _values 'subcommand' run runs deliver resume ;;
        worktree)   _values 'subcommand' create list remove ;;
        completion) _values 'shell' bash zsh fish ;;
    esac
}
compdef _mivia_completion mivia
`, commands)
}

// fishCompletionScript returns the fish completion definition.
func fishCompletionScript() string {
	commands := strings.Join(completionCommands, " ")
	return fmt.Sprintf(`# fish completion for mivia
complete -c mivia -f -n "__fish_use_subcommand" -a "%s"
complete -c mivia -f -n "__fish_seen_subcommand_from config" -a "show"
complete -c mivia -f -n "__fish_seen_subcommand_from doctor" -a "--json --config --workspace"
complete -c mivia -f -n "__fish_seen_subcommand_from agents" -a "list explain"
complete -c mivia -f -n "__fish_seen_subcommand_from memory" -a "search"
complete -c mivia -f -n "__fish_seen_subcommand_from workflows" -a "list show validate explain"
complete -c mivia -f -n "__fish_seen_subcommand_from workflow" -a "run runs deliver resume"
complete -c mivia -f -n "__fish_seen_subcommand_from worktree" -a "create list remove"
complete -c mivia -f -n "__fish_seen_subcommand_from completion" -a "bash zsh fish"
`, commands)
}
