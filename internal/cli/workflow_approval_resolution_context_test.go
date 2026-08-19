package cli

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// TestOpenWorkflowResolutionContextWorkspaceOpenError pins the workspace.Open
// error return of openWorkflowResolutionContext: a root that names a regular
// file (not a directory) makes workspace.Open fail, and the function must
// surface that error with every other return value nil/zero rather than a
// partially-populated result.
func TestOpenWorkflowResolutionContextWorkspaceOpenError(t *testing.T) {
	notADir := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(notADir, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	release, repo, store, closeFn, err := openWorkflowResolutionContext(notADir, "", "wfr-test")
	if err == nil {
		t.Fatal("expected an error when root names a regular file")
	}
	if release != nil || repo != nil || store != nil || closeFn != nil {
		t.Fatalf("expected all other returns nil, got release!=nil:%v repo!=nil:%v store!=nil:%v closeFn!=nil:%v", release != nil, repo != nil, store != nil, closeFn != nil)
	}
}

// TestOpenWorkflowResolutionContextConfigLoadError pins the config.Load error
// return: a configPath pointing at malformed TOML makes config.Load fail
// after workspace.Open succeeds.
func TestOpenWorkflowResolutionContextConfigLoadError(t *testing.T) {
	root := t.TempDir()
	badConfig := filepath.Join(t.TempDir(), "bad.toml")
	if err := os.WriteFile(badConfig, []byte("this is not [ valid toml"), 0o600); err != nil {
		t.Fatal(err)
	}

	release, repo, store, closeFn, err := openWorkflowResolutionContext(root, badConfig, "wfr-test")
	if err == nil {
		t.Fatal("expected an error when configPath names malformed TOML")
	}
	if release != nil || repo != nil || store != nil || closeFn != nil {
		t.Fatalf("expected all other returns nil, got release!=nil:%v repo!=nil:%v store!=nil:%v closeFn!=nil:%v", release != nil, repo != nil, store != nil, closeFn != nil)
	}
}

// workflowApprovalTestBlockedStoreConfig writes a config file whose
// [subagents] store_path resolves under a regular file (not a directory),
// so os.MkdirAll for the store's parent fails and openWorkflowStore returns
// an error, while workspace.Open and config.Load themselves both succeed
// (the store_path is an explicit config value, unrelated to root/.mivia, so
// this does not also break the config file / MCP-config lookups that live
// under root/.mivia).
func workflowApprovalTestBlockedStoreConfig(t *testing.T) string {
	t.Helper()
	blocker := filepath.Join(t.TempDir(), "blocked")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	storePath := filepath.Join(blocker, "sub", "context.db")
	configPath := filepath.Join(t.TempDir(), "mivia.toml")
	// A config file that IS found (unlike AllowMissingConfig's empty-path
	// case) goes through full provider validation, so this needs a minimal
	// valid [providers.deepseek] catalog entry alongside the blocked
	// store_path - otherwise config.Load fails earlier on "models must be
	// non-empty" and this test would exercise the config-error return
	// instead of openWorkflowStore's.
	content := "[subagents]\nstore_backend = \"sqlite\"\nstore_path = " + strconv.Quote(storePath) + "\n" +
		"\n[provider]\nname = \"deepseek\"\n" +
		"\n[providers.deepseek]\nmodels = [{ name = \"deepseek-v4-pro\", context_window_tokens = 128000 }]\ndefault_model = \"deepseek-v4-pro\"\n"
	if err := os.WriteFile(configPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return configPath
}

// workflowApprovalTestIsolatedConfigPath returns an explicit path to a
// minimal but VALID config file (a working default-provider entry).
// Passing "" as configPath instead makes config.Load fall back to its own
// ambient search (MIVIA_CONFIG, cwd's .mivia/mivia.toml, then the user's
// ~/.mivia/mivia.toml) - config.Load requires a resolvable default provider
// with a non-empty model list regardless of AllowMissingConfig (resolveProvider
// has no way to see that the file was merely missing vs. present-and-empty),
// so these tests were silently depending on whichever of those ambient
// sources happens to exist and be valid on the machine running them (same
// isolation-debt class fixed for the chat tests; see "fix(cli): isolate chat
// tests from the ambient main repo config"). A bare empty file does NOT fix
// this - an empty-but-found file still fails the same "models must be
// non-empty" check - so this writes a minimal working deepseek entry
// instead, giving these tests a provider config independent of the host
// environment.
func workflowApprovalTestIsolatedConfigPath(t *testing.T) string {
	t.Helper()
	configPath := filepath.Join(t.TempDir(), "isolated.toml")
	content := "[provider]\nname = \"deepseek\"\n" +
		"\n[providers.deepseek]\nmodels = [{ name = \"deepseek-v4-pro\", context_window_tokens = 128000 }]\ndefault_model = \"deepseek-v4-pro\"\n"
	if err := os.WriteFile(configPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return configPath
}

// TestOpenWorkflowResolutionContextOpenStoreError pins the openWorkflowStore
// error return: config.Load succeeds with an explicit store_path that cannot
// be created, so config.Load and workspace.Open both succeed but
// openWorkflowStore fails.
func TestOpenWorkflowResolutionContextOpenStoreError(t *testing.T) {
	root := t.TempDir()
	configPath := workflowApprovalTestBlockedStoreConfig(t)

	release, repo, store, closeFn, err := openWorkflowResolutionContext(root, configPath, "wfr-test")
	if err == nil {
		t.Fatal("expected an error when the store directory cannot be created")
	}
	if release != nil || repo != nil || store != nil || closeFn != nil {
		t.Fatalf("expected all other returns nil, got release!=nil:%v repo!=nil:%v store!=nil:%v closeFn!=nil:%v", release != nil, repo != nil, store != nil, closeFn != nil)
	}
}

// TestOpenWorkflowResolutionContextBoundedWorkspaceOpenError mirrors
// TestOpenWorkflowResolutionContextWorkspaceOpenError for the bounded variant.
func TestOpenWorkflowResolutionContextBoundedWorkspaceOpenError(t *testing.T) {
	notADir := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(notADir, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	release, repo, store, closeFn, err := openWorkflowResolutionContextBounded(context.Background(), notADir, "", "wfr-test", time.Second)
	if err == nil {
		t.Fatal("expected an error when root names a regular file")
	}
	if release != nil || repo != nil || store != nil || closeFn != nil {
		t.Fatalf("expected all other returns nil, got release!=nil:%v repo!=nil:%v store!=nil:%v closeFn!=nil:%v", release != nil, repo != nil, store != nil, closeFn != nil)
	}
}

// TestOpenWorkflowResolutionContextBoundedConfigLoadError mirrors
// TestOpenWorkflowResolutionContextConfigLoadError for the bounded variant.
func TestOpenWorkflowResolutionContextBoundedConfigLoadError(t *testing.T) {
	root := t.TempDir()
	badConfig := filepath.Join(t.TempDir(), "bad.toml")
	if err := os.WriteFile(badConfig, []byte("this is not [ valid toml"), 0o600); err != nil {
		t.Fatal(err)
	}

	release, repo, store, closeFn, err := openWorkflowResolutionContextBounded(context.Background(), root, badConfig, "wfr-test", time.Second)
	if err == nil {
		t.Fatal("expected an error when configPath names malformed TOML")
	}
	if release != nil || repo != nil || store != nil || closeFn != nil {
		t.Fatalf("expected all other returns nil, got release!=nil:%v repo!=nil:%v store!=nil:%v closeFn!=nil:%v", release != nil, repo != nil, store != nil, closeFn != nil)
	}
}

// TestOpenWorkflowResolutionContextBoundedOpenStoreError mirrors
// TestOpenWorkflowResolutionContextOpenStoreError for the bounded variant.
func TestOpenWorkflowResolutionContextBoundedOpenStoreError(t *testing.T) {
	root := t.TempDir()
	configPath := workflowApprovalTestBlockedStoreConfig(t)

	release, repo, store, closeFn, err := openWorkflowResolutionContextBounded(context.Background(), root, configPath, "wfr-test", time.Second)
	if err == nil {
		t.Fatal("expected an error when the store directory cannot be created")
	}
	if release != nil || repo != nil || store != nil || closeFn != nil {
		t.Fatalf("expected all other returns nil, got release!=nil:%v repo!=nil:%v store!=nil:%v closeFn!=nil:%v", release != nil, repo != nil, store != nil, closeFn != nil)
	}
}

// TestOpenWorkflowResolutionContextBoundedLockAcquireError pins the bounded
// lock-acquire failure branch: workspace.Open, config.Load, and
// openWorkflowStore all succeed, but the execution lock is already held by
// another holder for the same runID, so the bounded acquire times out and
// the function must close the freshly opened store (via closeFn) before
// returning the error.
func TestOpenWorkflowResolutionContextBoundedLockAcquireError(t *testing.T) {
	root := t.TempDir()
	const runID = "wfr-locked"

	// Prime the store/lock paths once via a first successful open, then hold
	// the execution lock externally so the second, bounded open must fail.
	// An explicit isolated config path avoids config.Load's ambient search
	// (see workflowApprovalTestIsolatedConfigPath).
	release1, _, _, closeFn1, err := openWorkflowResolutionContextBounded(context.Background(), root, workflowApprovalTestIsolatedConfigPath(t), runID, time.Second)
	if err != nil {
		t.Fatalf("priming open failed: %v", err)
	}
	release1()
	closeFn1()

	storePath := workflowApprovalTestStorePath(t, root)
	holdRelease, err := acquireWorkflowExecutionLock(storePath, runID)
	if err != nil {
		t.Fatalf("acquire lock to hold it: %v", err)
	}
	defer holdRelease()

	release2, repo2, store2, closeFn2, err := openWorkflowResolutionContextBounded(context.Background(), root, workflowApprovalTestIsolatedConfigPath(t), runID, 250*time.Millisecond)
	if err == nil {
		t.Fatal("expected the bounded lock acquire to time out while the lock is held")
	}
	if release2 != nil || repo2 != nil || store2 != nil || closeFn2 != nil {
		t.Fatalf("expected all other returns nil, got release!=nil:%v repo!=nil:%v store!=nil:%v closeFn!=nil:%v", release2 != nil, repo2 != nil, store2 != nil, closeFn2 != nil)
	}
}

func workflowApprovalTestStorePath(t *testing.T, root string) string {
	t.Helper()
	abs, err := filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	return workspace.ContextStorePath(abs)
}
