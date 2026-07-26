package version

import "testing"

func TestBinaryName(t *testing.T) {
	if Binary != "mivia" {
		t.Fatalf("Binary = %q, want mivia", Binary)
	}
	if Product != "mivia" {
		t.Fatalf("Product = %q, want mivia", Product)
	}
	if Version == "" {
		t.Fatal("Version must be non-empty")
	}
}
