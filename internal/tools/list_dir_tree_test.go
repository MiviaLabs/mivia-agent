package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// --- helpers ---

func listDirExec(t *testing.T, reg *Registry, args map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	out, err := reg.Execute(context.Background(), "list_dir", raw)
	if err != nil {
		t.Fatalf("list_dir %v: %v\n%s", args, err, out)
	}
	return out
}

func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for p, body := range files {
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if strings.HasSuffix(p, "/") {
			if err := os.MkdirAll(full, 0o755); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// TestListDirDepth1ByteIdentityGolden: depth unset/1 with include_size unset must
// be byte-identical to the historical flat listing (names + trailing / only).
func TestListDirDepth1ByteIdentityGolden(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{
		"a.txt":   "hello",
		"b/c.txt": "x",
		"zdir/":   "",
	})
	// Ensure a plain empty dir exists.
	if err := os.MkdirAll(filepath.Join(dir, "zdir"), 0o755); err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.Open(dir)
	if err != nil {
		t.Fatal(err)
	}

	// Historical tool shape (no ignore, no recursive params).
	legacy := &listDirTool{ws: ws, maxEntries: 0, maxBytes: 256 << 20}
	want, err := legacy.Execute(context.Background(), json.RawMessage(`{"path":"."}`))
	if err != nil {
		t.Fatal(err)
	}

	reg := NewDefaultRegistry(DefaultOptions{Workspace: ws})
	for _, args := range []map[string]any{
		{"path": "."},
		{"path": ".", "depth": 1},
	} {
		got := listDirExec(t, reg, args)
		if got != want {
			t.Fatalf("depth-1 golden mismatch for %v:\ngot:\n%q\nwant:\n%q", args, got, want)
		}
	}
}

// TestListDirRecursiveTreeGitignoreFixture covers Rust-like target/ and .venv/
// collapsed via fixture .gitignore, with sizes on files.
func TestListDirRecursiveTreeGitignoreFixture(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{
		".gitignore":       "target/\n.venv/\n",
		"src/main.rs":      "fn main() {}\n",
		"target/debug/app": "binary",
		".venv/lib/x.py":   "x",
		"README.md":        "hi",
	})
	ws, err := workspace.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	reg := NewDefaultRegistry(DefaultOptions{Workspace: ws})
	out := listDirExec(t, reg, map[string]any{"path": ".", "depth": 3})

	if !strings.Contains(out, "target/  (ignored)") {
		t.Fatalf("expected target/ (ignored):\n%s", out)
	}
	if !strings.Contains(out, ".venv/  (ignored)") {
		t.Fatalf("expected .venv/ (ignored):\n%s", out)
	}
	if strings.Contains(out, "debug") || strings.Contains(out, "lib/") {
		t.Fatalf("must not descend into ignored dirs:\n%s", out)
	}
	if !strings.Contains(out, "src/") {
		t.Fatalf("expected src/:\n%s", out)
	}
	if !strings.Contains(out, "main.rs") {
		t.Fatalf("expected main.rs under src:\n%s", out)
	}
	// File sizes present in recursive default.
	if !strings.Contains(out, "README.md  ") {
		t.Fatalf("expected size on README.md:\n%s", out)
	}
}

// TestListDirRecursiveNoGitignoreFloor: without .gitignore, built-in floor still
// collapses node_modules/ as (ignored).
func TestListDirRecursiveNoGitignoreFloor(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{
		"app.js":                    "x",
		"node_modules/pkg/index.js": "y",
		"vendor/lib.go":             "z",
	})
	ws, err := workspace.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	reg := NewDefaultRegistry(DefaultOptions{Workspace: ws})
	out := listDirExec(t, reg, map[string]any{"path": ".", "depth": 4})

	if !strings.Contains(out, "node_modules/  (ignored)") {
		t.Fatalf("expected node_modules/ (ignored) from built-in floor:\n%s", out)
	}
	if !strings.Contains(out, "vendor/  (ignored)") {
		t.Fatalf("expected vendor/ (ignored) from built-in floor:\n%s", out)
	}
	if strings.Contains(out, "pkg/") || strings.Contains(out, "index.js") {
		t.Fatalf("must not descend into node_modules:\n%s", out)
	}
}

// TestListDirIgnoreMatrixIdenticalRendering: floor / config / gitignore / combined
// all render the same (ignored) marker.
func TestListDirIgnoreMatrixIdenticalRendering(t *testing.T) {
	cases := []struct {
		name  string
		opts  DefaultOptions
		setup func(dir string)
		want  string
	}{
		{
			name: "floor",
			opts: DefaultOptions{},
			setup: func(dir string) {
				writeTree(t, dir, map[string]string{"node_modules/x": "1", "ok.txt": "2"})
			},
			want: "node_modules/  (ignored)",
		},
		{
			name: "config",
			opts: DefaultOptions{SearchIgnorePatterns: []string{"dist"}},
			setup: func(dir string) {
				writeTree(t, dir, map[string]string{"dist/a": "1", "ok.txt": "2"})
			},
			want: "dist/  (ignored)",
		},
		{
			name: "gitignore",
			opts: DefaultOptions{},
			setup: func(dir string) {
				writeTree(t, dir, map[string]string{".gitignore": "build/\n", "build/o": "1", "ok.txt": "2"})
			},
			want: "build/  (ignored)",
		},
		{
			name: "combined",
			opts: DefaultOptions{SearchIgnorePatterns: []string{"cache"}},
			setup: func(dir string) {
				writeTree(t, dir, map[string]string{
					".gitignore":     "tmp/\n",
					"node_modules/x": "1",
					"cache/y":        "2",
					"tmp/z":          "3",
					"ok.txt":         "4",
				})
			},
			want: "node_modules/  (ignored)",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			tc.setup(dir)
			ws, err := workspace.Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			tc.opts.Workspace = ws
			reg := NewDefaultRegistry(tc.opts)
			out := listDirExec(t, reg, map[string]any{"path": ".", "depth": 3})
			if !strings.Contains(out, tc.want) {
				t.Fatalf("missing %q in:\n%s", tc.want, out)
			}
			if tc.name == "combined" {
				for _, marker := range []string{"cache/  (ignored)", "tmp/  (ignored)"} {
					if !strings.Contains(out, marker) {
						t.Fatalf("missing %q in:\n%s", marker, out)
					}
				}
			}
		})
	}
}

// TestListDirSecretDirAndFileRendering: secret dirs show (blocked); secret files
// are name-only without size.
func TestListDirSecretDirAndFileRendering(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{
		"src/app.go":       "package main\n",
		".env":             "SECRET=1\n",
		"creds/.env.local": "x=1\n",
	})
	ws, err := workspace.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	reg := NewDefaultRegistry(DefaultOptions{
		Workspace:          ws,
		SecretPathPatterns: []string{".env", "creds"},
	})
	out := listDirExec(t, reg, map[string]any{"path": ".", "depth": 3})

	if !strings.Contains(out, "creds/  (blocked)") {
		t.Fatalf("expected creds/ (blocked):\n%s", out)
	}
	// .env at root is a secret file — name only, no size digits after.
	lines := strings.Split(out, "\n")
	foundEnv := false
	for _, line := range lines {
		trim := strings.TrimSpace(line)
		if trim == ".env" {
			foundEnv = true
		}
		if strings.HasPrefix(trim, ".env  ") {
			t.Fatalf("secret file must not include size: %q", line)
		}
	}
	if !foundEnv {
		t.Fatalf("expected .env name-only line:\n%s", out)
	}
	if strings.Contains(out, ".env.local") {
		t.Fatalf("must not descend into blocked creds/:\n%s", out)
	}
}

// TestListDirExplicitPathIntoIgnoredLists: D2 — requesting a path inside an
// ignored tree still lists its contents.
func TestListDirExplicitPathIntoIgnoredLists(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{
		"node_modules/pkg/index.js": "module.exports = 1\n",
		"node_modules/pkg/util.js":  "exports.x = 2\n",
		"app.js":                    "require('pkg')\n",
	})
	ws, err := workspace.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	reg := NewDefaultRegistry(DefaultOptions{Workspace: ws})

	// From workspace root, node_modules is collapsed.
	rootOut := listDirExec(t, reg, map[string]any{"path": ".", "depth": 2})
	if !strings.Contains(rootOut, "node_modules/  (ignored)") {
		t.Fatalf("root should collapse node_modules:\n%s", rootOut)
	}

	// Explicit path into ignored dir lists children.
	out := listDirExec(t, reg, map[string]any{"path": "node_modules/pkg", "depth": 2})
	if !strings.Contains(out, "index.js") {
		t.Fatalf("explicit path should list index.js:\n%s", out)
	}
	if !strings.Contains(out, "util.js") {
		t.Fatalf("explicit path should list util.js:\n%s", out)
	}
}

// TestListDirSymlinkNotFollowed: symlink to a directory is listed as a plain
// entry and not descended into.
func TestListDirSymlinkNotFollowed(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{
		"real/secret.txt": "nope",
		"visible.txt":     "yes",
	})
	if err := os.Symlink(filepath.Join(dir, "real"), filepath.Join(dir, "linkdir")); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	// Loop-ish: symlink to self parent
	if err := os.Symlink(dir, filepath.Join(dir, "looplink")); err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	reg := NewDefaultRegistry(DefaultOptions{Workspace: ws})
	out := listDirExec(t, reg, map[string]any{"path": ".", "depth": 4})

	// real/ is a real directory — its contents may appear. Symlinks must not be
	// descended: no indented children under linkdir/looplink.
	if strings.Contains(out, "linkdir/") || strings.Contains(out, "looplink/") {
		t.Fatalf("symlink must not be treated as a directory to descend:\n%s", out)
	}
	if !strings.Contains(out, "linkdir") || !strings.Contains(out, "looplink") {
		t.Fatalf("symlink should still be listed as a plain entry:\n%s", out)
	}
	// Ensure we did not emit children under the symlink names (indent after them).
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "  ") && (strings.Contains(out, "linkdir\n"+line) || strings.Contains(out, "looplink\n"+line)) {
			// crude: better check blocks
		}
	}
	// Parse: after a "linkdir" line at indent 0, next line must not be more-indented
	// content that only exists via follow (secret.txt appears under real/, fine).
	lines := strings.Split(out, "\n")
	for i, line := range lines {
		if line == "linkdir" || line == "looplink" {
			if i+1 < len(lines) && strings.HasPrefix(lines[i+1], "  ") {
				t.Fatalf("descended into symlink %q:\n%s", line, out)
			}
		}
	}
}

// TestListDirDepthCutAndEntryCapNotices: depth cut emits dir/ ... plus beyond-depth
// notice; entry cap emits truncated (N more encountered).
func TestListDirDepthCutAndEntryCapNotices(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{
		"a/b/c/d.txt": "deep",
		"x.txt":       "1",
		"y.txt":       "2",
		"z.txt":       "3",
	})
	ws, err := workspace.Open(dir)
	if err != nil {
		t.Fatal(err)
	}

	// Depth cut at 1: a/ is listed as a/ ... with beyond-depth count for its kids.
	reg := NewDefaultRegistry(DefaultOptions{Workspace: ws})
	// Force include_size so we take the tree path at depth 1.
	trueVal := true
	raw, _ := json.Marshal(map[string]any{"path": ".", "depth": 1, "include_size": trueVal})
	out, err := reg.Execute(context.Background(), "list_dir", raw)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "a/ ...") {
		t.Fatalf("expected depth-cut marker:\n%s", out)
	}
	if !strings.Contains(out, "entries beyond depth") {
		t.Fatalf("expected beyond-depth notice:\n%s", out)
	}

	// Entry cap on recursive walk.
	reg2 := NewDefaultRegistry(DefaultOptions{Workspace: ws, MaxListDirEntries: 2})
	out2 := listDirExec(t, reg2, map[string]any{"path": ".", "depth": 3})
	if !strings.Contains(out2, "more encountered") {
		t.Fatalf("expected entry-cap notice:\n%s", out2)
	}
	// Content + notices must fit (sanity: not empty and has truncation).
	if strings.Count(out2, "\n") < 1 {
		t.Fatalf("expected some content:\n%s", out2)
	}
}

// TestListDirByteCapNotice: recursive mode honors byte budget with notice.
// Includes budgets at/below the worst-case notice reserve (~199) so the
// reserve>=maxBytes collapse (budget==0 must not mean uncapped) is covered.
func TestListDirByteCapNotice(t *testing.T) {
	dir := t.TempDir()
	// Long names to burn budget quickly; enough entries that any tight cap withholds content.
	for i := 0; i < 50; i++ {
		name := fmt.Sprintf("%s_%02d.txt", strings.Repeat("n", 40), i)
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ws, err := workspace.Open(dir)
	if err != nil {
		t.Fatal(err)
	}

	for _, budget := range []int{50, 100, 150, 199, 400} {
		t.Run(fmt.Sprintf("maxBytes%d", budget), func(t *testing.T) {
			// MaxReadBytes drives list_dir's readClassMaxBytes when MaxToolResultBytes is 0.
			reg := NewDefaultRegistry(DefaultOptions{Workspace: ws, MaxReadBytes: budget})
			out := listDirExec(t, reg, map[string]any{"path": ".", "depth": 2})
			if len(out) > budget {
				t.Fatalf("result %d bytes exceeds budget %d:\n%s", len(out), budget, out)
			}
			// Content was withheld: must surface a truncation notice (byte and/or entry).
			if !strings.Contains(out, "truncated at") && !strings.Contains(out, "more encountered") {
				t.Fatalf("expected truncation notice under budget %d (len=%d):\n%s", budget, len(out), out)
			}
			// Must not dump the full tree: with 50 long names, untruncated would be >> budget.
			if strings.Count(out, ".txt") >= 50 {
				t.Fatalf("budget %d did not withhold content (emitted all names):\n%s", budget, out)
			}
		})
	}
}

// TestListDirReloadGitignoreNextCall: edit .gitignore → next list_dir reflects it;
// same-second same-size content edit is caught via hash.
func TestListDirReloadGitignoreNextCall(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{
		".gitignore": "build/\n",
		"build/a.o":  "obj",
		"dist/b.js":  "js",
		"ok.txt":     "ok",
	})
	ws, err := workspace.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	reg := NewDefaultRegistry(DefaultOptions{Workspace: ws})

	out1 := listDirExec(t, reg, map[string]any{"path": ".", "depth": 2})
	if !strings.Contains(out1, "build/  (ignored)") {
		t.Fatalf("build should be ignored initially:\n%s", out1)
	}
	if strings.Contains(out1, "dist/  (ignored)") {
		t.Fatalf("dist should not be ignored yet:\n%s", out1)
	}

	// Same-second same-size rewrite: "build/\n" (7) → "dist/\n\n" pad carefully.
	// "build/\n" is 7 bytes; "dist/\n " is also need same size.
	// "build/\n" = 7 chars. "dist/\n" = 6. Use "dist/\n\n" = 7.
	giPath := filepath.Join(dir, ".gitignore")
	newContent := []byte("dist/\n\n")
	if len(newContent) != len([]byte("build/\n")) {
		// Ensure same size for the hash-caught same-second test intent.
		newContent = []byte("dist/\n#")
	}
	// Force same mtime window: write then set times equal if possible.
	if err := os.WriteFile(giPath, newContent, 0o644); err != nil {
		t.Fatal(err)
	}
	// Attempt to pin mtime to previous (best-effort); hash must still catch.
	past := time.Now().Add(-time.Second)
	_ = os.Chtimes(giPath, past, past)

	out2 := listDirExec(t, reg, map[string]any{"path": ".", "depth": 2})
	if !strings.Contains(out2, "dist/  (ignored)") {
		t.Fatalf("dist should be ignored after reload:\n%s", out2)
	}
	// build is no longer in gitignore — but may still list as a normal dir.
	if strings.Contains(out2, "build/  (ignored)") {
		t.Fatalf("build should no longer be ignored:\n%s", out2)
	}
}

// TestGitignoreSnapshotRaceConcurrentWalks: concurrent walks during reload must
// not race (-race) and each walk stays internally coherent.
func TestGitignoreSnapshotRaceConcurrentWalks(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{
		".gitignore": "a/\n",
		"a/x.txt":    "1",
		"b/y.txt":    "2",
		"c/z.txt":    "3",
	})
	// Make a wider tree so walks take a moment.
	for i := 0; i < 20; i++ {
		sub := filepath.Join(dir, fmt.Sprintf("d%02d", i))
		if err := os.MkdirAll(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(sub, "f.txt"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ws, err := workspace.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	reg := NewDefaultRegistry(DefaultOptions{Workspace: ws})
	giPath := filepath.Join(dir, ".gitignore")

	var wg sync.WaitGroup
	errCh := make(chan error, 32)
	// Walkers
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				_, err := reg.Execute(context.Background(), "list_dir", json.RawMessage(`{"path":".","depth":3}`))
				if err != nil {
					errCh <- err
					return
				}
				_, err = reg.Execute(context.Background(), "glob", json.RawMessage(`{"pattern":"**/*.txt"}`))
				if err != nil {
					errCh <- err
					return
				}
			}
		}()
	}
	// Reloader: yield between writes so walkers interleave (no time.Sleep).
	wg.Add(1)
	go func() {
		defer wg.Done()
		patterns := []string{"a/\n", "b/\n", "c/\n", "a/\nb/\n"}
		for j := 0; j < 40; j++ {
			if err := os.WriteFile(giPath, []byte(patterns[j%len(patterns)]), 0o644); err != nil {
				errCh <- err
				return
			}
			runtime.Gosched()
		}
	}()
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}
}

// TestGitignoreMatcherReloadSameSecondSameSize: unit-level stamp hash catches
// content swap when size matches.
func TestGitignoreMatcherReloadSameSecondSameSize(t *testing.T) {
	dir := t.TempDir()
	giPath := filepath.Join(dir, ".gitignore")
	// 8-byte patterns
	if err := os.WriteFile(giPath, []byte("alpha/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "alpha"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "bravo"), 0o755); err != nil {
		t.Fatal(err)
	}
	gi := newGitignoreMatcher(dir)
	if !gi.IsDir("alpha") {
		t.Fatal("alpha should be ignored")
	}
	if gi.IsDir("bravo") {
		t.Fatal("bravo should not be ignored yet")
	}
	// Same length: "bravo/\n" is also 7 bytes like "alpha/\n"
	if err := os.WriteFile(giPath, []byte("bravo/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	_ = os.Chtimes(giPath, now, now)
	if gi.IsDir("alpha") {
		t.Fatal("alpha should not be ignored after swap")
	}
	if !gi.IsDir("bravo") {
		t.Fatal("bravo should be ignored after same-size swap")
	}
}

// TestIgnoreViewShouldIgnoreComposesFloorAndGitignore pins the shared predicate.
func TestIgnoreViewShouldIgnoreComposesFloorAndGitignore(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("build/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	src := newIgnoreSource(dir, append([]string(nil), defaultIgnorePatterns...))
	v := src.snapshot()
	if !v.ShouldIgnoreDir("node_modules", "node_modules") {
		t.Error("floor should ignore node_modules")
	}
	if !v.ShouldIgnoreDir("build", "build") {
		t.Error("gitignore should ignore build")
	}
	if v.ShouldIgnoreDir("src", "src") {
		t.Error("src should not be ignored")
	}
}

// TestListDirAndSearchShareIgnoreSource: registry wires one decision to all three tools.
func TestListDirAndSearchShareIgnoreSource(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{
		".gitignore":                "secret_build/\n",
		"secret_build/hidden.go":    "package h\n",
		"src/visible.go":            "package v\n",
		"node_modules/pkg/index.js": "x",
	})
	ws, err := workspace.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	reg := NewDefaultRegistry(DefaultOptions{Workspace: ws})

	listOut := listDirExec(t, reg, map[string]any{"path": ".", "depth": 2})
	if !strings.Contains(listOut, "secret_build/  (ignored)") {
		t.Fatalf("list_dir gitignore:\n%s", listOut)
	}
	if !strings.Contains(listOut, "node_modules/  (ignored)") {
		t.Fatalf("list_dir floor:\n%s", listOut)
	}

	globOut, err := reg.Execute(context.Background(), "glob", json.RawMessage(`{"pattern":"**/*"}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(globOut, "secret_build") || strings.Contains(globOut, "node_modules") {
		t.Fatalf("glob should skip ignored:\n%s", globOut)
	}
	if !strings.Contains(globOut, "src/visible.go") {
		t.Fatalf("glob should find visible:\n%s", globOut)
	}

	grepOut, err := reg.Execute(context.Background(), "grep", json.RawMessage(`{"pattern":"package"}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(grepOut, "secret_build") {
		t.Fatalf("grep should skip gitignored:\n%s", grepOut)
	}
	if !strings.Contains(grepOut, "src/visible.go") {
		t.Fatalf("grep should find visible:\n%s", grepOut)
	}
}
