package clichat

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"golang.org/x/sys/unix"
)

// openTestPTY opens a pty pair and returns both ends as *os.File, opened
// O_RDWR so the slave works as both a readable stdin and a writable stdout
// for term.IsTerminal checks - mirroring diffcov2_test.go's withPtyStdin,
// but returning the files instead of swapping the global os.Stdin, since
// ensureChatAPIKey takes stdin/stdout as explicit parameters. Skips (not
// fails) when the host has no /dev/ptmx, matching withPtyStdin's own
// portability posture.
func openTestPTY(t *testing.T) (master, slave *os.File) {
	t.Helper()
	m, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("cannot open /dev/ptmx: %v", err)
	}
	if err := unix.IoctlSetPointerInt(m, unix.TIOCSPTLCK, 0); err != nil {
		_ = unix.Close(m)
		t.Skipf("unlockpt failed: %v", err)
	}
	ptsN, err := unix.IoctlGetInt(m, unix.TIOCGPTN)
	if err != nil {
		_ = unix.Close(m)
		t.Skipf("pts number failed: %v", err)
	}
	s, err := unix.Open("/dev/pts/"+itoa(ptsN), unix.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		_ = unix.Close(m)
		t.Skipf("cannot open pty slave: %v", err)
	}
	master = os.NewFile(uintptr(m), "pty-master")
	slave = os.NewFile(uintptr(s), "pty-slave")
	t.Cleanup(func() {
		_ = slave.Close()
		_ = master.Close()
	})
	return master, slave
}

// TestEnsureChatAPIKeyKeyAlreadySetSkipsPrompt pins the no-op fast path:
// when Resolved already carries a usable key, ensureChatAPIKey must not
// touch stdin/stdout at all (a nil stdin/stdout would panic if it did).
func TestEnsureChatAPIKeyKeyAlreadySetSkipsPrompt(t *testing.T) {
	res := &config.Resolved{ProviderName: "openrouter", APIKeyEnv: "OPENROUTER_API_KEY", APIKeySet: true, APIKey: "already-set"}
	got, err := ensureChatAPIKey(res, config.LoadOptions{}, nil, nil)
	if err != nil {
		t.Fatalf("ensureChatAPIKey: %v", err)
	}
	if got != res {
		t.Fatalf("expected the same Resolved back unchanged")
	}
}

// TestEnsureChatAPIKeyNonInteractiveFailsClosed pins the non-interactive
// (scripted, -p one-shot, piped, CI) branch: no key, no TTY, must return an
// actionable error immediately rather than blocking on stdin.
func TestEnsureChatAPIKeyNonInteractiveFailsClosed(t *testing.T) {
	stdinReader, stdinWriter, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	defer stdinReader.Close()
	defer stdinWriter.Close()
	var stdout bytes.Buffer

	res := &config.Resolved{ProviderName: "openrouter", APIKeyEnv: "OPENROUTER_API_KEY"}
	done := make(chan struct{})
	var got *config.Resolved
	var gotErr error
	go func() {
		got, gotErr = ensureChatAPIKey(res, config.LoadOptions{}, &stdout, stdinReader)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("ensureChatAPIKey blocked on non-interactive stdin instead of failing closed")
	}
	if got != nil {
		t.Fatalf("got a non-nil Resolved on the error path: %+v", got)
	}
	if gotErr == nil || !strings.Contains(gotErr.Error(), "mivia setup") || !strings.Contains(gotErr.Error(), "OPENROUTER_API_KEY") {
		t.Fatalf("err = %v, want an actionable error naming mivia setup and OPENROUTER_API_KEY", gotErr)
	}
}

// TestEnsureChatAPIKeyInteractivePromptsAndWrites pins the interactive path:
// stdin and stdout both a TTY, no key set - it must prompt (matching mivia
// setup's own prompt), write the entered key to the user env file, and
// return a re-resolved config carrying that key so the caller never has to
// restart mivia chat a second time.
func TestEnsureChatAPIKeyInteractivePromptsAndWrites(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("MIVIA_CONFIG", "")

	cfgPath := config.UserConfigPath()
	if err := os.MkdirAll(cfgPath[:len(cfgPath)-len("/mivia.toml")], 0o700); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(cfgPath, []byte(config.DefaultUserConfigTOML), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	master, slave := openTestPTY(t)

	loadOpts := config.LoadOptions{AllowMissingConfig: true}
	res, err := config.Load(loadOpts)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if res.APIKeySet {
		t.Fatal("test setup invariant broken: key should not be set yet")
	}

	go func() {
		_, _ = master.Write([]byte("sk-test-key\n"))
	}()

	done := make(chan struct{})
	var got *config.Resolved
	var gotErr error
	go func() {
		got, gotErr = ensureChatAPIKey(res, loadOpts, slave, slave)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("ensureChatAPIKey did not return within the timeout")
	}
	if gotErr != nil {
		t.Fatalf("ensureChatAPIKey: %v", gotErr)
	}
	if got == nil || !got.APIKeySet || got.APIKey != "sk-test-key" {
		t.Fatalf("got = %+v, want a re-resolved config carrying the freshly written key", got)
	}

	envData, err := os.ReadFile(config.UserEnvPath())
	if err != nil {
		t.Fatalf("read env file: %v", err)
	}
	if !strings.Contains(string(envData), "OPENROUTER_API_KEY=sk-test-key") {
		t.Fatalf("env file content = %q, want it to contain the written key", envData)
	}
}
