package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// Full-disk file-tool access is an OPERATOR grant, and the operator's own
// user config is its only persisted provenance:
//
//	[workspace_access]
//	full_disk = true
//
// read from UserConfigPath() and nowhere else. This mirrors the [agents]
// gate's provenance rule (LoadAgentsGlobal): the workspace's own
// .mivia/mivia.toml is repo-controlled, so a key decoded into File could be
// committed by any cloned repository and merged over the operator's user
// config by loadFile's overlay (later layer wins) - a repo would grant
// itself unconfined file tools with no operator consent. Keeping the key
// out of File/Resolved entirely and reading only the fixed user path is
// what makes persisted persistence equivalent to the operator typing
// `mivia chat --full-disk`: same trust level, no overlay in between.
// workspace.Root.Unrestricted's doc comment (internal/workspace/root.go)
// pins the same property from the consumer side; keep the two in sync.

// workspaceAccessTOML is the only shape this file decodes: one dedicated
// table, no other key of the user config is even modeled, so the section
// stays invisible to File's decode (decodeConfigInto has no
// DisallowUnknownFields) and can never leak into merged config.
type workspaceAccessTOML struct {
	WorkspaceAccess struct {
		FullDisk bool `toml:"full_disk"`
	} `toml:"workspace_access"`
}

// UserFullDiskAccessForWorkspace reports whether the operator's own user
// config enables full-disk file-tool access for sessions rooted at
// workspaceRoot ("" resolves to the process working directory, matching
// enterChatWorkspace). Fail-closed: a missing user config, a missing key,
// or an unreadable/unparsable file all report false - full disk is a
// deliberate grant, never a default. When the user config path IS the
// workspace's own config file (home directory used as a workspace, or a
// repo that plants .mivia/mivia.toml at $HOME), the read refuses: a file a
// repository controls must never answer this question.
func UserFullDiskAccessForWorkspace(workspaceRoot string) bool {
	path := UserConfigPath()
	if path == "" {
		return false
	}
	if sameFilePathAsWorkspaceConfig(path, workspaceRoot) {
		return false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var view workspaceAccessTOML
	if err := toml.Unmarshal(data, &view); err != nil {
		return false
	}
	return view.WorkspaceAccess.FullDisk
}

// SetUserFullDiskAccess persists the operator's full-disk grant to the user
// config and nothing else: locked and atomic via updateConfigFile, the same
// read-modify-write discipline every other config mutator uses, preserving
// all other keys in the file. It refuses when the user config path resolves
// to the workspace's own config file (see UserFullDiskAccessForWorkspace) -
// the write would hand the grant to whoever controls that file.
func SetUserFullDiskAccess(workspaceRoot string, on bool) error {
	path := UserConfigPath()
	if path == "" {
		return fmt.Errorf("user config path unavailable (no resolvable home directory)")
	}
	if sameFilePathAsWorkspaceConfig(path, workspaceRoot) {
		return fmt.Errorf("refusing to persist full-disk access: user config resolves to the workspace's own config; full disk may only be granted by the operator's own config or --full-disk")
	}
	return updateConfigFile(path, func(raw map[string]any) error {
		section, _ := raw["workspace_access"].(map[string]any)
		if section == nil {
			section = make(map[string]any)
		}
		section["full_disk"] = on
		raw["workspace_access"] = section
		return nil
	})
}

// sameFilePathAsWorkspaceConfig reports whether path names the workspace's
// own .mivia/mivia.toml. Empty workspaceRoot resolves to the process
// working directory, so the check also covers "mivia runs with cwd as the
// workspace". Mirrors LoadAgentsGlobal's two-step guard: resolved
// directories equal means the fixed filename lands on the same file even
// when lexical paths differ.
//
// Both operands are canonicalized to ABSOLUTE paths before comparing:
// relative roots ("", "." - what prepareChatStartup passes before
// enterChatWorkspace absolutizes, and what the ""-resolution contract
// documents) would otherwise compare a relative .mivia/mivia.toml against
// the absolute user path, and EvalSymlinks keeps relative input relative -
// the guard could never trip and a repo-planted $HOME/.mivia/mivia.toml
// would answer the grant question (bug-audit ec8a9ef4 findings a2-1/a3-1).
func sameFilePathAsWorkspaceConfig(path, workspaceRoot string) bool {
	wsRoot := strings.TrimSpace(workspaceRoot)
	if wsRoot == "" {
		wsRoot = "."
	}
	wsConfig := workspace.NamespacePath(wsRoot, "mivia.toml")
	if pathAbs, err := filepath.Abs(path); err == nil {
		path = pathAbs
	}
	if wsAbs, err := filepath.Abs(wsConfig); err == nil {
		wsConfig = wsAbs
	}
	if same, err := sameResolvedDir(filepath.Dir(path), filepath.Dir(wsConfig)); err == nil && same {
		return true
	}
	return sameFilePath(path, wsConfig)
}
