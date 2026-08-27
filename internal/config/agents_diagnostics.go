package config

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// AgentFileState describes the safe inspection state of one discovered file.
type AgentFileState string

const (
	// AgentFileLoaded means the file parsed and is available for resolution.
	AgentFileLoaded AgentFileState = "loaded"
	// AgentFileShadowed means a workspace file lost to a user-name match.
	AgentFileShadowed AgentFileState = "shadowed by user"
	// AgentFileMalformed means the file was readable but did not satisfy the
	// agent-file format or safety contract.
	AgentFileMalformed AgentFileState = "malformed"
	// AgentFileUnreadable means the file or containing directory could not be
	// safely inspected.
	AgentFileUnreadable AgentFileState = "unreadable"
)

// AgentCollectionState distinguishes a missing agent namespace from an existing
// namespace with no definition files.
type AgentCollectionState string

const (
	AgentCollectionNotPresent AgentCollectionState = "not present"
	AgentCollectionEmpty      AgentCollectionState = "empty"
	AgentCollectionHasEntries AgentCollectionState = "has entries"
)

// AgentFileDiagnostic is a bounded, non-secret inspection row. Error details
// are intentionally reduced to a class; callers must not print parser text.
type AgentFileDiagnostic struct {
	Name   string
	Source AgentSource
	Path   string
	State  AgentFileState
}

// AgentDiscoveryReport contains every safely loaded file and every independent
// file-level issue. A bad file does not hide valid files from inspection.
type AgentDiscoveryReport struct {
	Files         []LoadedAgentFile
	Diagnostics   []AgentFileDiagnostic
	Warnings      []string
	Collection    AgentCollectionState
	UserDirectory string
	WorkspaceDir  string
}

// DiscoverAgentFilesReport discovers both user and workspace definitions while
// collecting independent malformed, unreadable, and shadowed rows. Workspace
// agent files are always candidates; loadWorkspace is retained for compatibility
// and does not gate this collection.
func DiscoverAgentFilesReport(workspaceRoot string, loadWorkspace bool) (AgentDiscoveryReport, error) {
	_ = loadWorkspace
	root := workspaceRoot
	if strings.TrimSpace(root) == "" {
		root = "."
	}
	report := AgentDiscoveryReport{
		UserDirectory: UserAgentsDir(),
		WorkspaceDir:  WorkspaceAgentsDir(root),
	}
	report.Collection = collectionState(report.UserDirectory, report.WorkspaceDir)

	userFiles, userRows, userErr := loadAgentDirReport(report.UserDirectory, AgentSourceUser)
	report.Files = append(report.Files, userFiles...)
	report.Diagnostics = append(report.Diagnostics, userRows...)
	if userErr != nil {
		return sortAgentDiscoveryReport(report), userErr
	}

	byName := make(map[string]LoadedAgentFile, len(userFiles))
	for _, file := range userFiles {
		byName[file.Name] = file
	}
	if same, err := sameResolvedDir(report.UserDirectory, report.WorkspaceDir); err == nil && same {
		return sortAgentDiscoveryReport(report), nil
	}

	workspaceFiles, workspaceRows, workspaceErr := loadAgentDirReport(report.WorkspaceDir, AgentSourceWorkspace)
	if workspaceErr != nil {
		report.Diagnostics = append(report.Diagnostics, workspaceRows...)
		return sortAgentDiscoveryReport(report), workspaceErr
	}
	shadowed := make(map[string]struct{})
	for _, file := range workspaceFiles {
		if _, exists := byName[file.Name]; exists {
			shadowed[file.Name] = struct{}{}
		}
	}
	for _, row := range workspaceRows {
		if row.State == AgentFileLoaded {
			if _, exists := shadowed[row.Name]; exists {
				continue
			}
		}
		report.Diagnostics = append(report.Diagnostics, row)
	}
	for _, file := range workspaceFiles {
		if _, exists := byName[file.Name]; exists {
			report.Diagnostics = append(report.Diagnostics, AgentFileDiagnostic{
				Name: file.Name, Source: file.Source, Path: file.Path, State: AgentFileShadowed,
			})
			report.Warnings = append(report.Warnings, fmt.Sprintf(
				"workspace agent %q shadowed by user agent", file.Name))
			continue
		}
		report.Files = append(report.Files, file)
		byName[file.Name] = file
	}
	return sortAgentDiscoveryReport(report), nil
}

func collectionState(userDir, workspaceDir string) AgentCollectionState {
	for _, dir := range []string{userDir, workspaceDir} {
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			return AgentCollectionEmpty
		}
	}
	return AgentCollectionNotPresent
}

func loadAgentDirReport(dir string, source AgentSource) ([]LoadedAgentFile, []AgentFileDiagnostic, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, nil, nil
	}
	root, err := openAgentsRoot(dir)
	if os.IsNotExist(err) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, []AgentFileDiagnostic{{
			Name: filepath.Base(dir), Source: source, Path: dir, State: AgentFileUnreadable,
		}}, nil
	}
	defer root.Close()
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		return nil, []AgentFileDiagnostic{{
			Name: filepath.Base(dir), Source: source, Path: dir, State: AgentFileUnreadable,
		}}, nil
	}
	var files []LoadedAgentFile
	var rows []AgentFileDiagnostic
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || strings.HasPrefix(name, ".") || strings.EqualFold(name, "readme.md") {
			continue
		}
		if !strings.HasSuffix(name, ".md") && !strings.HasSuffix(name, ".toml") {
			continue
		}
		path := filepath.Join(dir, name)
		row := AgentFileDiagnostic{Name: canonicalNameForDiagnostic(name), Source: source, Path: path}
		data, readErr := readRegularAgent(root, name)
		if readErr != nil {
			row.State = AgentFileUnreadable
			rows = append(rows, row)
			continue
		}
		if len(data) > maxAgentFileBytes {
			row.State = AgentFileMalformed
			rows = append(rows, row)
			continue
		}
		var spec AgentFileSpec
		var canonical string
		var parseErr error
		if strings.HasSuffix(name, ".md") {
			spec, canonical, parseErr = ParseAgentFileMarkdown(data, name)
		} else {
			spec, canonical, parseErr = ParseAgentFileTOML(data, name)
		}
		if parseErr != nil {
			row.Name = canonicalNameForDiagnostic(name)
			row.State = AgentFileMalformed
			rows = append(rows, row)
			continue
		}
		row.Name = canonical
		row.State = AgentFileLoaded
		files = append(files, LoadedAgentFile{Name: canonical, Source: source, Path: path, Spec: spec})
		rows = append(rows, row)
	}
	return files, rows, nil
}

func canonicalNameForDiagnostic(name string) string {
	base := filepath.Base(name)
	if strings.HasSuffix(base, ".md") {
		return strings.TrimSuffix(base, ".md")
	}
	return strings.TrimSuffix(base, ".toml")
}

func sortAgentDiscoveryReport(report AgentDiscoveryReport) AgentDiscoveryReport {
	if len(report.Files) > 0 || len(report.Diagnostics) > 0 {
		report.Collection = AgentCollectionHasEntries
	}
	sort.Slice(report.Files, func(i, j int) bool {
		if report.Files[i].Name != report.Files[j].Name {
			return report.Files[i].Name < report.Files[j].Name
		}
		return report.Files[i].Source < report.Files[j].Source
	})
	sort.Slice(report.Diagnostics, func(i, j int) bool {
		if report.Diagnostics[i].Name != report.Diagnostics[j].Name {
			return report.Diagnostics[i].Name < report.Diagnostics[j].Name
		}
		return report.Diagnostics[i].Path < report.Diagnostics[j].Path
	})
	sort.Strings(report.Warnings)
	return report
}
