package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// autosaveStatusFile is the name of the file in the session directory that
// records whether the last auto-save on exit succeeded. The welcome screen
// reads this to warn the user if persistence failed.
const autosaveStatusFile = ".autosave_last_status"

// WriteAutosaveStatus writes the result of the final SaveLast to a status file
// so the next session can report failures to the user.
func WriteAutosaveStatus(sessionDir string, saveErr error) {
	if sessionDir == "" {
		return
	}
	statusPath := filepath.Join(sessionDir, autosaveStatusFile)
	content := "ok"
	if saveErr != nil {
		content = fmt.Sprintf("failed: %v", saveErr)
	}
	_ = os.WriteFile(statusPath, []byte(content), 0o644)
}

// ReadAutosaveStatus returns a non-empty warning string if the previous
// session's auto-save on exit failed, or "" if it succeeded or there is
// no status file yet.
func ReadAutosaveStatus(sessionDir string) string {
	if sessionDir == "" {
		return ""
	}
	statusPath := filepath.Join(sessionDir, autosaveStatusFile)
	data, err := os.ReadFile(statusPath)
	if err != nil {
		return ""
	}
	content := strings.TrimSpace(string(data))
	if content == "ok" || content == "" {
		return ""
	}
	return content
}
