package tools

// Branch coverage for list_dir's refusal, cancellation, truncation and
// unreadable-entry paths. The walk internals are exercised directly: a
// filesystem cannot be made to fail on demand without racing, while
// treeWalkState, emitDir and emitFile take exactly the inputs those branches
// describe.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

func listDirToolFor(t *testing.T, dir string) *listDirTool {
	t.Helper()
	ws, err := workspace.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return &listDirTool{ws: ws, maxBytes: 256 << 10}
}

// stubDirEntry is an os.DirEntry whose Info() fails on demand - the race
// emitFile handles (entry vanished, or stat refused) has no reproducible
// filesystem equivalent.
type stubDirEntry struct {
	name    string
	infoErr error
}

func (s stubDirEntry) Name() string               { return s.name }
func (s stubDirEntry) IsDir() bool                { return false }
func (s stubDirEntry) Type() fs.FileMode          { return 0 }
func (s stubDirEntry) Info() (fs.FileInfo, error) { return nil, s.infoErr }

// countdownContext reports no error for the first `alive` Err() calls and
// cancellation after, so a walk can be cancelled at a chosen point.
type countdownContext struct {
	context.Context
	alive int
	calls int
}

func (c *countdownContext) Err() error {
	c.calls++
	if c.calls > c.alive {
		return context.Canceled
	}
	return nil
}

func TestListDirRefusesBadRequestsBeforeReading(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{"a.txt": "x"})
	tool := listDirToolFor(t, dir)

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := tool.Execute(canceled, json.RawMessage(`{"path":"."}`)); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Execute = %v, want context.Canceled", err)
	}
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"path":`)); err == nil {
		t.Fatal("malformed arguments accepted")
	}
	for _, depth := range []int{-1, listDirMaxDepth + 1} {
		args := fmt.Sprintf(`{"path":".","depth":%d}`, depth)
		_, err := tool.Execute(context.Background(), json.RawMessage(args))
		if err == nil || !strings.Contains(err.Error(), "depth must be between") {
			t.Fatalf("depth %d = %v, want a range refusal", depth, err)
		}
	}
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"path":"../.."}`)); err == nil {
		t.Fatal("path outside the workspace accepted")
	}
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"path":"missing"}`)); err == nil {
		t.Fatal("missing directory accepted on the flat path")
	}
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"path":"missing","depth":3}`)); err == nil {
		t.Fatal("missing directory accepted on the tree path")
	}
}

func TestListDirRefusesSecretDirectories(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{".env.d/": ""})
	tool := listDirToolFor(t, dir)
	tool.secretPathPatterns = []string{".env"}

	_, err := tool.Execute(context.Background(), json.RawMessage(`{"path":".env.d"}`))
	if err == nil || !strings.Contains(err.Error(), "secret-like") {
		t.Fatalf("err = %v, want a secret-path refusal", err)
	}
}

func TestListDirReportsEmptyDirectories(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	tool := listDirToolFor(t, dir)

	out, err := tool.Execute(context.Background(), json.RawMessage(`{"path":"empty","depth":2}`))
	if err != nil {
		t.Fatal(err)
	}
	if out != "(empty)" {
		t.Fatalf("tree of an empty directory = %q, want %q", out, "(empty)")
	}
}

func TestListDirDefaultsToTheWorkspaceRoot(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{"top/inner.txt": "x"})
	tool := listDirToolFor(t, dir)

	out, err := tool.Execute(context.Background(), json.RawMessage(`{"depth":2}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "inner.txt") {
		t.Fatalf("omitted path did not list the workspace root:\n%s", out)
	}
}

func TestListDirTreeStopsOnCancellation(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{"a/b.txt": "x", "c.txt": "y"})
	tool := listDirToolFor(t, dir)

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := tool.formatTree(canceled, dir, 3, true, ignoreView{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("formatTree = %v, want context.Canceled", err)
	}

	// Cancelled after the walk started: the per-entry check inside the loop.
	midLoop := &countdownContext{Context: context.Background(), alive: 1}
	if err := tool.walkTree(midLoop, &treeWalkState{}, dir, ".", 1, 3); !errors.Is(err, context.Canceled) {
		t.Fatalf("mid-loop walk = %v, want context.Canceled", err)
	}

	// Cancelled inside a subdirectory: the error propagates back through emitDir.
	midChild := &countdownContext{Context: context.Background(), alive: 2}
	if err := tool.walkTree(midChild, &treeWalkState{}, dir, ".", 1, 3); !errors.Is(err, context.Canceled) {
		t.Fatalf("child walk = %v, want context.Canceled", err)
	}
}

func TestWalkTreeReportsUnreadableDirectories(t *testing.T) {
	dir := t.TempDir()
	tool := listDirToolFor(t, dir)
	if err := tool.walkTree(context.Background(), &treeWalkState{}, filepath.Join(dir, "missing"), "missing", 1, 2); err == nil {
		t.Fatal("walk of a missing directory returned no error")
	}
	st := &treeWalkState{stop: true}
	if err := tool.walkTree(context.Background(), st, dir, ".", 1, 2); err != nil {
		t.Fatalf("stopped walk = %v, want nil", err)
	}
}

func TestEmitDirCountsWhatItCouldNotEmit(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{"node_modules/x.txt": "x", ".env.d/y.txt": "y", "plain/z.txt": "z"})
	tool := listDirToolFor(t, dir)
	ctx := context.Background()

	// A stopped walk still counts the collapsed secret and ignored directories
	// it encountered but could not emit.
	stopped := func() *treeWalkState {
		return &treeWalkState{stop: true, secretsPat: []string{".env"}, view: ignoreView{patterns: []string{"node_modules"}}}
	}
	secret := stopped()
	if err := tool.emitDir(ctx, secret, dir, ".env.d", ".env.d", "", 1, 3); err != nil {
		t.Fatal(err)
	}
	if secret.moreEncountered != 1 {
		t.Fatalf("unemitted secret dir moreEncountered = %d, want 1", secret.moreEncountered)
	}
	ignored := stopped()
	if err := tool.emitDir(ctx, ignored, dir, "node_modules", "node_modules", "", 1, 3); err != nil {
		t.Fatal(err)
	}
	if ignored.moreEncountered != 1 {
		t.Fatalf("unemitted ignored dir moreEncountered = %d, want 1", ignored.moreEncountered)
	}

	// A plain directory line that does not fit is counted, not descended into.
	full := &treeWalkState{entryCap: 1, emitted: 1}
	if err := tool.emitDir(ctx, full, dir, "plain", "plain", "", 1, 3); err != nil {
		t.Fatal(err)
	}
	if full.moreEncountered != 1 || strings.Contains(full.b.String(), "z.txt") {
		t.Fatalf("capped dir descended or was not counted: %+v %q", full.moreEncountered, full.b.String())
	}

	// The depth-cut marker itself can be the line that does not fit.
	cut := &treeWalkState{entryCap: 1, emitted: 1}
	if err := tool.emitDir(ctx, cut, dir, "plain", "plain", "", 1, 1); err != nil {
		t.Fatal(err)
	}
	if cut.moreEncountered != 1 {
		t.Fatalf("unemitted depth marker moreEncountered = %d, want 1", cut.moreEncountered)
	}
}

func TestEmitDirCountsAnUnreadableDepthCutChild(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{"file.txt": "x"})
	tool := listDirToolFor(t, dir)
	ctx := context.Background()

	// Not a directory: reading its children fails with something other than
	// "does not exist", which is what the unreadable notice counts.
	notDir := &treeWalkState{}
	if err := tool.emitDir(ctx, notDir, dir, "file.txt", "file.txt", "", 1, 1); err != nil {
		t.Fatal(err)
	}
	if notDir.unreadable != 1 {
		t.Fatalf("unreadable = %d, want 1", notDir.unreadable)
	}

	// Vanished between listing and descent: a race, not an unreadable entry.
	gone := &treeWalkState{}
	if err := tool.emitDir(ctx, gone, dir, "gone", "gone", "", 1, 1); err != nil {
		t.Fatal(err)
	}
	if gone.unreadable != 0 {
		t.Fatalf("a vanished child counted as unreadable: %d", gone.unreadable)
	}
}

func TestEmitFileHandlesEntriesItCannotStat(t *testing.T) {
	tool := listDirToolFor(t, t.TempDir())

	vanished := &treeWalkState{includeSize: true}
	tool.emitFile(vanished, stubDirEntry{name: "gone.txt", infoErr: os.ErrNotExist}, "gone.txt", "gone.txt", "")
	if vanished.b.Len() != 0 || vanished.unreadable != 0 {
		t.Fatalf("vanished entry emitted %q / unreadable=%d", vanished.b.String(), vanished.unreadable)
	}

	refused := &treeWalkState{includeSize: true}
	tool.emitFile(refused, stubDirEntry{name: "denied.txt", infoErr: os.ErrPermission}, "denied.txt", "denied.txt", "")
	if refused.unreadable != 1 {
		t.Fatalf("unreadable = %d, want 1", refused.unreadable)
	}
	if got := refused.b.String(); got != "denied.txt\n" {
		t.Fatalf("unstattable entry = %q, want its name with no size", got)
	}

	// A secret file that does not fit is counted rather than dropped silently.
	secret := &treeWalkState{stop: true, secretsPat: []string{".env"}}
	tool.emitFile(secret, stubDirEntry{name: ".env"}, ".env", ".env", "")
	if secret.moreEncountered != 1 {
		t.Fatalf("unemitted secret file moreEncountered = %d, want 1", secret.moreEncountered)
	}
}

func TestTryEmitRefusesOnceStopped(t *testing.T) {
	st := &treeWalkState{stop: true}
	if st.tryEmit("x") {
		t.Fatal("a stopped state accepted a line")
	}
}

func TestFinalizePrefersNoticesOverContent(t *testing.T) {
	build := func() *treeWalkState {
		st := &treeWalkState{unreadable: 2, beyondDepth: 1}
		st.b.WriteString("aaaa\nbbbb\ncccc\n")
		return st
	}
	uncapped := build().finalize(0)
	if !strings.Contains(uncapped, "aaaa") || !strings.Contains(uncapped, "2 entries unreadable") {
		t.Fatalf("uncapped finalize dropped output:\n%s", uncapped)
	}

	notices := build().formatNotices(64)
	tight := build().finalize(len(notices) + 5)
	if !strings.Contains(tight, "entries unreadable") {
		t.Fatalf("notices were dropped under a tight budget:\n%s", tight)
	}
	if len(tight) > len(notices)+5 {
		t.Fatalf("finalize returned %d bytes, over its %d budget", len(tight), len(notices)+5)
	}

	starved := build().finalize(len(notices) - 1)
	if len(starved) > len(notices)-1 {
		t.Fatalf("notice-only trim returned %d bytes, over budget", len(starved))
	}
	if strings.Contains(starved, "aaaa") {
		t.Fatalf("content survived a notice-only budget:\n%s", starved)
	}
}

func TestTrimLinesToBudgetKeepsWholeLines(t *testing.T) {
	cases := []struct {
		in     string
		budget int
		want   string
	}{
		{"", 10, ""},
		{"abc\n", 0, ""},
		{"abc\n", 10, "abc\n"},
		{"aaa\nbbb\n", 5, "aaa\n"},
		{"aaaaaaaa\n", 4, ""},
	}
	for _, c := range cases {
		if got := trimLinesToBudget(c.in, c.budget); got != c.want {
			t.Errorf("trimLinesToBudget(%q, %d) = %q, want %q", c.in, c.budget, got, c.want)
		}
	}
}
