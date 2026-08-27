package config

import (
	"strings"
	"testing"
)

func TestNegativeMaxOutputBytesIsLoadError(t *testing.T) {
	path := writeToolResultCapConfig(t, "max_output_bytes = -100")
	_, err := Load(LoadOptions{ConfigPath: path})
	if err == nil {
		t.Fatal("expected error for negative max_output_bytes")
	}
	if !strings.Contains(err.Error(), "max_output_bytes must not be negative") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestNegativeMaxListDirEntriesIsLoadError(t *testing.T) {
	path := writeToolResultCapConfig(t, "max_list_dir_entries = -5")
	_, err := Load(LoadOptions{ConfigPath: path})
	if err == nil {
		t.Fatal("expected error for negative max_list_dir_entries")
	}
	if !strings.Contains(err.Error(), "max_list_dir_entries must not be negative") {
		t.Fatalf("unexpected error message: %v", err)
	}
}
