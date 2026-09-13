package approval

import (
	"testing"
)

func TestHeaderRows_EmptyWhenNoHead(t *testing.T) {
	m := newModel()
	if rows := m.headerRows(); rows != nil {
		t.Fatalf("expected nil rows, got %v", rows)
	}
}
