package cli

import (
	"bytes"
	"strings"
	"testing"
)

func runCompletionCapture(t *testing.T, args []string) (string, error) {
	t.Helper()
	var buf bytes.Buffer
	err := runCompletionWithIO(args, &buf)
	return buf.String(), err
}

func TestCompletionCommandsIncludesLoginLogout(t *testing.T) {
	for _, want := range []string{"login", "logout"} {
		found := false
		for _, cmd := range completionCommands {
			if cmd == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("completionCommands is missing %q (must stay in sync with the Execute switch in root.go)", want)
		}
	}
}

func TestCompletionCommandsIncludesMemory(t *testing.T) {
	for _, want := range []string{"memory"} {
		found := false
		for _, cmd := range completionCommands {
			if cmd == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("completionCommands is missing %q (must stay in sync with the Execute switch in root.go)", want)
		}
	}
}

func TestCompletionMemorySubcommandOffersSearch(t *testing.T) {
	for _, shell := range []string{"bash", "zsh", "fish"} {
		out, err := runCompletionCapture(t, []string{shell})
		if err != nil {
			t.Fatalf("completion %s error = %v", shell, err)
		}
		if !strings.Contains(out, "memory") || !strings.Contains(out, "search") {
			t.Fatalf("%s script lacks a memory -> search subcommand entry: %q", shell, out)
		}
	}
}

func TestCompletionBashScript(t *testing.T) {
	out, err := runCompletionCapture(t, []string{"bash"})
	if err != nil {
		t.Fatalf("completion bash error = %v", err)
	}
	if !strings.Contains(out, "complete -F _mivia_completion mivia") {
		t.Fatalf("bash script lacks the complete directive: %q", out)
	}
	for _, cmd := range completionCommands {
		if !strings.Contains(out, cmd) {
			t.Fatalf("bash script lacks command %q", cmd)
		}
	}
}

func TestCompletionZshScript(t *testing.T) {
	out, err := runCompletionCapture(t, []string{"zsh"})
	if err != nil {
		t.Fatalf("completion zsh error = %v", err)
	}
	if !strings.Contains(out, "#compdef mivia") {
		t.Fatalf("zsh script lacks the compdef header: %q", out)
	}
	if !strings.Contains(out, "compdef _mivia_completion mivia") {
		t.Fatalf("zsh script lacks the compdef line: %q", out)
	}
}

func TestCompletionFishScript(t *testing.T) {
	out, err := runCompletionCapture(t, []string{"fish"})
	if err != nil {
		t.Fatalf("completion fish error = %v", err)
	}
	if !strings.Contains(out, "complete -c mivia") {
		t.Fatalf("fish script lacks the complete directive: %q", out)
	}
}

func TestCompletionRequiresAShell(t *testing.T) {
	if _, err := runCompletionCapture(t, nil); err == nil {
		t.Fatal("completion with no shell returned nil error")
	} else if !strings.Contains(err.Error(), "usage") {
		t.Fatalf("completion no-shell error = %v, want usage line", err)
	}
}

func TestCompletionRejectsUnknownShell(t *testing.T) {
	if _, err := runCompletionCapture(t, []string{"powershell"}); err == nil {
		t.Fatal("completion with an unknown shell returned nil error")
	} else if !strings.Contains(err.Error(), "unsupported shell") {
		t.Fatalf("completion unknown-shell error = %v, want unsupported-shell message", err)
	}
}

func TestCompletionRejectsExtraArgs(t *testing.T) {
	if _, err := runCompletionCapture(t, []string{"bash", "extra"}); err == nil {
		t.Fatal("completion with extra args returned nil error")
	}
}
