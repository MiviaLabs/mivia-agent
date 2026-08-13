package localengine_test

import (
	"os"
	"path/filepath"
	"testing"
)

// initGitRepo makes root a real git repository with one commit on its
// default branch, so Start's base-identity resolution (ensureRunWorktree or
// its resolveLocalIdentity fallback) has a real ref/commit to admit. Start
// now fails closed when neither can resolve a real base (see startNew), so
// every workspace root a test admits a run against must be a real repo.
func initGitRepo(t *testing.T, root string) {
	t.Helper()
	runGitT(t, root, "init", "-q", "-b", "main")
	runGitT(t, root, "config", "user.email", "test@example.com")
	runGitT(t, root, "config", "user.name", "Test")
	runGitT(t, root, "commit", "-q", "--allow-empty", "-m", "init")
}

func writeTwoStepWorkspace(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	initGitRepo(t, root)
	wfRoot := filepath.Join(root, ".mivia", "workflows")
	if err := os.MkdirAll(wfRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	body := `version = 1
name = "two-step"
initial_step = "one"

[inputs.task]
type = "string"
required = true
max_bytes = 100

[[steps]]
id = "one"
kind = "agent"
agent = "one"
on_failure = "failure"

[[steps]]
id = "two"
kind = "agent"
agent = "two"
on_failure = "failure"

[[transitions]]
from = "one"
to = "two"
[transitions.match]
status = "succeeded"

[[transitions]]
from = "two"
to = "success"
[transitions.match]
status = "succeeded"
`
	if err := os.WriteFile(filepath.Join(wfRoot, "two-step.toml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

func writeDeliveryWorkspace(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	initGitRepo(t, root)
	// The workflow below declares [delivery], so admission resolves the
	// delivery origin URL (resolveOriginURL) and needs a real origin remote.
	originDir := filepath.Join(t.TempDir(), "origin.git")
	runGitT(t, filepath.Dir(originDir), "init", "-q", "--bare", filepath.Base(originDir))
	runGitT(t, root, "remote", "add", "origin", originDir)
	wfRoot := filepath.Join(root, ".mivia", "workflows")
	if err := os.MkdirAll(wfRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	body := `version = 1
name = "deliver-me"
initial_step = "one"

[inputs.task]
type = "string"
required = true
max_bytes = 100

[delivery]
kind = "pull_request"
mode = "draft"
provider = "github"
base = "main"

[[steps]]
id = "one"
kind = "agent"
agent = "one"
on_failure = "failure"

[[transitions]]
from = "one"
to = "success"
[transitions.match]
status = "succeeded"
`
	if err := os.WriteFile(filepath.Join(wfRoot, "deliver-me.toml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}
