package delivery

import (
	"strings"
	"testing"
)

func TestLoadTemplatesRejectsDriveRelativeTraversal(t *testing.T) {
	for _, path := range []string{`C:..\templates`, `C:..\..\templates`} {
		_, err := LoadTemplates(path)
		if err == nil || !strings.Contains(err.Error(), "contains traversal") {
			t.Errorf("LoadTemplates(%q) error = %v, want traversal rejection", path, err)
		}
	}
}
