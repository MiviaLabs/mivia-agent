package cli

import (
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/version"
)

// captureStdout redirects os.Stdout for the duration of a test and returns a
// reader for what was written. The caller must invoke the returned cleanup
// function to read the captured output and restore os.Stdout.
func captureStdout(t *testing.T) func() string {
	t.Helper()
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	original := os.Stdout
	os.Stdout = write
	captured := make(chan string, 1)
	go func() {
		data, _ := io.ReadAll(read)
		captured <- string(data)
	}()
	var once bool
	var result string
	return func() string {
		if !once {
			once = true
			os.Stdout = original
			_ = write.Close()
			result = <-captured
		}
		return result
	}
}

// TestExecuteVersionHumanOutput: `mivia version` prints human output unchanged.
func TestExecuteVersionHumanOutput(t *testing.T) {
	done := captureStdout(t)
	defer done()
	err := Execute([]string{"version"})
	stdout := done()
	if err != nil {
		t.Fatalf("Execute([version]) error = %v", err)
	}
	want := version.String() + "\n"
	if stdout != want {
		t.Fatalf("stdout = %q, want %q", stdout, want)
	}
}

// TestExecuteVersionJSONOutput: `mivia version --json` prints valid JSON.
func TestExecuteVersionJSONOutput(t *testing.T) {
	done := captureStdout(t)
	defer done()
	err := Execute([]string{"version", "--json"})
	stdout := done()
	if err != nil {
		t.Fatalf("Execute([version, --json]) error = %v", err)
	}
	stdout = strings.TrimSpace(stdout)
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(stdout), &m); err != nil {
		t.Fatalf("stdout not valid JSON: %v: %q", err, stdout)
	}
	if m["binary"] != "mivia" {
		t.Fatalf("binary = %v, want mivia", m["binary"])
	}
	if m["version"] != version.Version {
		t.Fatalf("version = %v, want %q", m["version"], version.Version)
	}
}

// TestExecuteVersionJSONOnly: same as JSONOutput but explicit nil-error check.
func TestExecuteVersionJSONOnly(t *testing.T) {
	done := captureStdout(t)
	defer done()
	err := Execute([]string{"version", "--json"})
	stdout := done()
	if err != nil {
		t.Fatalf("Execute([version, --json]) error = %v", err)
	}
	stdout = strings.TrimSpace(stdout)
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(stdout), &m); err != nil {
		t.Fatalf("stdout not valid JSON: %v: %q", err, stdout)
	}
}

// TestExecuteVersionUnknownArg: `mivia version extra` returns an error.
func TestExecuteVersionUnknownArg(t *testing.T) {
	err := Execute([]string{"version", "extra"})
	if err == nil {
		t.Fatal("Execute([version, extra]) returned nil error")
	}
	if !strings.Contains(err.Error(), "unknown argument") {
		t.Fatalf("error = %v, want contains 'unknown argument'", err)
	}
}

// TestExecuteVersionJSONExtraArg: `mivia version --json extra` returns an error.
func TestExecuteVersionJSONExtraArg(t *testing.T) {
	err := Execute([]string{"version", "--json", "extra"})
	if err == nil {
		t.Fatal("Execute([version, --json, extra]) returned nil error")
	}
	if !strings.Contains(err.Error(), "unexpected arguments after --json") {
		t.Fatalf("error = %v, want contains 'unexpected arguments after --json'", err)
	}
}

// TestExecuteVersionJSONMultipleExtra: `mivia version --json a b` returns an error.
func TestExecuteVersionJSONMultipleExtra(t *testing.T) {
	err := Execute([]string{"version", "--json", "a", "b"})
	if err == nil {
		t.Fatal("Execute([version, --json, a, b]) returned nil error")
	}
	if !strings.Contains(err.Error(), "unexpected arguments after --json") {
		t.Fatalf("error = %v, want contains 'unexpected arguments after --json'", err)
	}
}

// TestExecuteVersionDashVNoJSON: `--version --json` prints human output (ignores extra).
func TestExecuteVersionDashVNoJSON(t *testing.T) {
	done := captureStdout(t)
	defer done()
	err := Execute([]string{"--version", "--json"})
	stdout := done()
	if err != nil {
		t.Fatalf("Execute([--version, --json]) error = %v", err)
	}
	want := version.String() + "\n"
	if stdout != want {
		t.Fatalf("stdout = %q, want %q", stdout, want)
	}
}

// TestExecuteVersionDashVLowerNoJSON: `-V --json` prints human output (ignores extra).
func TestExecuteVersionDashVLowerNoJSON(t *testing.T) {
	done := captureStdout(t)
	defer done()
	err := Execute([]string{"-V", "--json"})
	stdout := done()
	if err != nil {
		t.Fatalf("Execute([-V, --json]) error = %v", err)
	}
	want := version.String() + "\n"
	if stdout != want {
		t.Fatalf("stdout = %q, want %q", stdout, want)
	}
}

// TestExecuteCompletionBash: `mivia completion bash` prints the bash script.
func TestExecuteCompletionBash(t *testing.T) {
	done := captureStdout(t)
	defer done()
	if err := Execute([]string{"completion", "bash"}); err != nil {
		t.Fatalf("Execute([completion, bash]) error = %v", err)
	}
	if out := done(); !strings.Contains(out, "complete -F _mivia_completion mivia") {
		t.Fatalf("Execute completion output lacks the bash directive:\n%s", out)
	}
}

// TestExecuteCompletionNoArgs: `mivia completion` returns a usage error.
func TestExecuteCompletionNoArgs(t *testing.T) {
	err := Execute([]string{"completion"})
	if err == nil || !strings.Contains(err.Error(), "usage") {
		t.Fatalf("Execute([completion]) error = %v, want usage line", err)
	}
}

// TestUsageTextDocumentsLoginLogout: usageText() documents the login/logout
// commands, matching the existing precedent of asserting command lines are
// present in the help body.
func TestUsageTextDocumentsLoginLogout(t *testing.T) {
	text := usageText()
	if !strings.Contains(text, "login") {
		t.Fatalf("usageText() missing login command:\n%s", text)
	}
	if !strings.Contains(text, "logout") {
		t.Fatalf("usageText() missing logout command:\n%s", text)
	}
}

// TestExecuteLoginDispatchesToRunLogin: `mivia login` (no --email) reaches
// runLogin and fails fast on parseLoginArgs's pre-network validation,
// proving dispatch without a real network call or filesystem touch.
func TestExecuteLoginDispatchesToRunLogin(t *testing.T) {
	err := Execute([]string{"login"})
	if err == nil {
		t.Fatal("Execute([login]) returned nil error")
	}
	if !strings.Contains(err.Error(), "--email is required") {
		t.Fatalf("error = %v, want contains '--email is required'", err)
	}
}

// TestExecuteLogoutDispatchesToRunLogout: `mivia logout --bogus` reaches
// runLogout and fails fast on parseLogoutArgs's unknown-flag check, proving
// dispatch without touching a real ~/.mivia session file.
func TestExecuteLogoutDispatchesToRunLogout(t *testing.T) {
	err := Execute([]string{"logout", "--bogus"})
	if err == nil {
		t.Fatal("Execute([logout, --bogus]) returned nil error")
	}
	if !strings.Contains(err.Error(), "unknown flag") {
		t.Fatalf("error = %v, want contains 'unknown flag'", err)
	}
}
