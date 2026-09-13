package cliautomations

// buildService's post-store failure branches and installAutomationHooks'
// own arms. These run after the run store is already open, so each one has
// to release what it acquired: a leaked database handle here would outlive
// every `automations run` invocation that hit a hook fault.

import (
	"errors"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/clichat"
)

// TestInstallAutomationHooksWithoutASeamIsANoOp pins the unwired case: a
// binary that never installed the hook seam must get a usable no-op release
// rather than a nil func the caller would panic on.
func TestInstallAutomationHooksWithoutASeamIsANoOp(t *testing.T) {
	prev := clichat.InstallHookSessionFunc
	clichat.InstallHookSessionFunc = nil
	t.Cleanup(func() { clichat.InstallHookSessionFunc = prev })

	release, err := installAutomationHooks(t.TempDir())
	if err != nil {
		t.Fatalf("installAutomationHooks with no seam: %v", err)
	}
	if release == nil {
		t.Fatal("installAutomationHooks returned a nil release with no seam wired")
	}
	release() // must not panic
}

// TestInstallAutomationHooksTreatsANilReleaseAsANoOp covers the seam
// returning success with no releaser: the caller still defers release(), so
// a nil there would panic at the end of every run.
func TestInstallAutomationHooksTreatsANilReleaseAsANoOp(t *testing.T) {
	prev := clichat.InstallHookSessionFunc
	clichat.InstallHookSessionFunc = func(root string, interactive, quiet bool) (func(), error) {
		return nil, nil
	}
	t.Cleanup(func() { clichat.InstallHookSessionFunc = prev })

	release, err := installAutomationHooks(t.TempDir())
	if err != nil {
		t.Fatalf("installAutomationHooks with a nil release: %v", err)
	}
	if release == nil {
		t.Fatal("installAutomationHooks passed a nil release through to the caller")
	}
	release()
}

// TestInstallAutomationHooksWrapsASeamFailure pins the error wrap: a hook
// install fault must name this package so the operator can tell it from a
// config or store failure.
func TestInstallAutomationHooksWrapsASeamFailure(t *testing.T) {
	sentinel := errors.New("hook install refused")
	prev := clichat.InstallHookSessionFunc
	clichat.InstallHookSessionFunc = func(root string, interactive, quiet bool) (func(), error) {
		return nil, sentinel
	}
	t.Cleanup(func() { clichat.InstallHookSessionFunc = prev })

	release, err := installAutomationHooks(t.TempDir())
	if err == nil {
		t.Fatal("installAutomationHooks succeeded with a failing seam, want an error")
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("error = %v, want it to wrap the seam failure", err)
	}
	if !strings.Contains(err.Error(), "cliautomations: install hooks") {
		t.Fatalf("error = %v, want it wrapped with this package's prefix", err)
	}
	if release != nil {
		t.Fatal("a failed hook install returned a release func")
	}
}

// TestBuildServiceReleasesTheStoreWhenHooksFail is the leak contract: the
// run store is already open by the time hooks are installed, so a hook
// failure must close it on the way out. The assertion is indirect but
// real: a second buildService against the same root succeeds only if the
// first one actually released its handle.
func TestBuildServiceReleasesTheStoreWhenHooksFail(t *testing.T) {
	root := writeAutomationsFixture(t, "hook-failure-fixture")

	sentinel := errors.New("hook install refused")
	prev := clichat.InstallHookSessionFunc
	clichat.InstallHookSessionFunc = func(string, bool, bool) (func(), error) {
		return nil, sentinel
	}

	_, _, _, err := buildService(root, "")
	if err == nil {
		t.Fatal("buildService succeeded with a failing hook install, want an error")
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("buildService error = %v, want it to wrap the hook failure", err)
	}

	// Restore the seam and build for real against the SAME root: this only
	// works if the failed attempt closed the store it opened.
	clichat.InstallHookSessionFunc = prev
	svc, _, cleanup, err := buildService(root, "")
	if err != nil {
		t.Fatalf("buildService after a hook failure: %v, want the store to have been released", err)
	}
	if svc == nil {
		t.Fatal("buildService returned a nil service on the success path")
	}
	if cleanup != nil {
		cleanup()
	}
}
