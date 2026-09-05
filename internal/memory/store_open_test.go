package memory

import "testing"

func TestOpenDefaultsEmptyBackendToMemory(t *testing.T) {
	s, err := Open(Config{})
	if err != nil {
		t.Fatalf("Open with an empty Backend: %v", err)
	}
	defer s.Close()
}

func TestOpenRejectsUnsupportedBackend(t *testing.T) {
	if _, err := Open(Config{Backend: "sqlite"}); err == nil {
		t.Fatal("Open accepted a backend other than \"memory\"")
	}
}
