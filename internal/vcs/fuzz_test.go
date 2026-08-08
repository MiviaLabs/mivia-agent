package vcs

import (
	"strings"
	"testing"
)

// FuzzParseWorktreeList checks that parseWorktreeList never panics and never
// returns an error for any input. The parser consumes the external
// 'git worktree list --porcelain' interface, so arbitrary bytes must parse
// safely. The seed corpus comes from the existing parseWorktreeList cases.
func FuzzParseWorktreeList(f *testing.F) {
	const prefix = "/repo/.mivia/worktrees/"
	seeds := []string{
		"",
		"worktree /repo/.mivia/worktrees/wt-a\nbranch refs/heads/wt/wt-a\n",
		"worktree /repo/.mivia/worktrees/wt-a\nbranch refs/heads/wt/wt-a",
		"worktree /other/repo\nHEAD 1234567\nbranch refs/heads/main\n\n",
		"bare\nlocked reason\n",
		"malformed line without a space\n",
		"worktree /repo/.mivia/worktrees/wt-detached\nHEAD 1234567\n",
		"worktree /repo/.mivia/worktrees/wt-a\nbranch refs/heads/wt/wt-a\n\nworktree /repo/.mivia/worktrees/wt-b\nbranch refs/heads/wt/wt-b",
	}
	for _, seed := range seeds {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, input string) {
		got, err := parseWorktreeList(input, prefix)
		if err != nil {
			t.Fatalf("parseWorktreeList returned an error: %v", err)
		}
		for _, wt := range got {
			if wt.Name == "" {
				t.Fatalf("parseWorktreeList returned a worktree with an empty name: %+v", wt)
			}
		}
	})
}

// FuzzGitdirPointer checks the gitdirPointer contract: ok is true only when
// the trimmed content starts with the 'gitdir: ' prefix and the pointer is
// non-empty, and a false result never carries a pointer. The helper must
// never panic, for any input.
func FuzzGitdirPointer(f *testing.F) {
	seeds := [][]byte{
		[]byte("gitdir: ../actual-git\n"),
		[]byte("gitdir: /abs/git\n"),
		[]byte(""),
		[]byte("gitdir:/abs\n"),
		[]byte("gitdir: first\ngitdir: second\n"),
		[]byte("gitdir: ../x\r\n"),
		[]byte("gitdir: ../actual-git\n" + strings.Repeat("a", 1<<20)),
	}
	for _, seed := range seeds {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		pointer, ok := gitdirPointer(data)
		trimmed := strings.TrimSpace(string(data))
		if ok {
			if pointer == "" {
				t.Fatalf("ok=true but pointer is empty for input %q", data)
			}
			if !strings.HasPrefix(trimmed, "gitdir: ") {
				t.Fatalf("ok=true but trimmed content has no 'gitdir: ' prefix for input %q", data)
			}
		} else if pointer != "" {
			t.Fatalf("ok=false but pointer is non-empty for input %q", data)
		}
	})
}
