package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
)

func TestDisplaySessionName(t *testing.T) {
	if got := displaySessionName(chat.AutoSaveName); got != "Last session" {
		t.Fatalf("auto: got %q", got)
	}
	if got := displaySessionName("project-a"); got != "project-a" {
		t.Fatalf("named: got %q", got)
	}
}

func TestFormatSessionAge(t *testing.T) {
	if formatSessionAge(time.Time{}) != "" {
		t.Fatal("zero should be empty")
	}
	if got := formatSessionAge(time.Now().Add(-30 * time.Second)); got != "just now" {
		t.Fatalf("got %q", got)
	}
	if got := formatSessionAge(time.Now().Add(-5 * time.Minute)); got != "5m ago" {
		t.Fatalf("got %q", got)
	}
}

func TestLogoFramesBrandShape(t *testing.T) {
	if logoFrameCount() < 8 {
		t.Fatalf("need granular animation frames, got %d", logoFrameCount())
	}
	out := renderLogoFrame(0, 40)
	if out == "" {
		t.Fatal("empty render")
	}
	// High-fidelity path uses braille, not coarse /\\ ASCII.
	hasBraille := false
	for _, r := range out {
		if r >= 0x2800 && r <= 0x28FF {
			hasBraille = true
			break
		}
	}
	if !hasBraille {
		t.Fatal("expected braille pixel logo, got coarse art")
	}
	if !strings.Contains(renderWordmark(40), "MIVIA") {
		t.Fatal("wordmark missing MIVIA")
	}
}

func TestRenderSessionPickerEmpty(t *testing.T) {
	block, hits, _ := renderSessionPicker(nil, 0, 0, 80, 5, 10)
	if !strings.Contains(block, "No saved sessions") {
		t.Fatalf("expected empty hint, got %q", block)
	}
	if len(hits) != 0 {
		t.Fatal("no hits expected")
	}
}

func TestRenderSessionPickerSelection(t *testing.T) {
	sessions := []chat.SessionInfo{
		{Name: chat.AutoSaveName, MessageCount: 4, UpdatedAt: time.Now()},
		{Name: "work", MessageCount: 10, UpdatedAt: time.Now().Add(-time.Hour)},
	}
	block, hits, sc := renderSessionPicker(sessions, 0, 0, 80, 5, 10)
	if sc != 0 {
		t.Fatalf("scroll %d", sc)
	}
	if !strings.Contains(block, "Last session") {
		t.Fatalf("missing display name: %q", block)
	}
	if !strings.Contains(block, "work") {
		t.Fatal("missing work session")
	}
	if len(hits) != 2 {
		t.Fatalf("hits=%d", len(hits))
	}
	// Selected row 0 should use marker.
	if !strings.Contains(block, "▸") {
		t.Fatal("expected selection marker")
	}
	// Hit Y should be absolute from yBase+2
	if hits[0].y0 != 12 || hits[0].idx != 0 {
		t.Fatalf("hit0=%+v", hits[0])
	}
}
