package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
)

func TestDisplaySessionName(t *testing.T) {
	// Bare __last__ name (legacy).
	if got := displaySessionName(chat.AutoSaveName); got != "Last session" {
		t.Fatalf("bare __last__: got %q", got)
	}
	// Timestamped __last__* name.
	if got := displaySessionName(chat.AutoSaveName + "20250115T103000"); got != "Last session" {
		t.Fatalf("timestamped __last__: got %q", got)
	}
	// Named session unchanged.
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
	// Welcome diamond is multi-line braille art (unlike 1-cell status glyphs).
	if strings.Count(out, "\n") < 2 {
		t.Fatalf("welcome logo must be multi-line braille, got lines=%d %q", strings.Count(out, "\n")+1, out)
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
	// Spot-check several animation frames stay multi-line + braille.
	for _, fr := range []int{0, 1, logoFrameCount() / 2, logoFrameCount() - 1} {
		frame := renderLogoFrame(fr, 48)
		if strings.Count(frame, "\n") < 2 {
			t.Fatalf("frame %d not multi-line", fr)
		}
		ok := false
		for _, r := range frame {
			if r >= 0x2800 && r <= 0x28FF {
				ok = true
				break
			}
		}
		if !ok {
			t.Fatalf("frame %d missing braille", fr)
		}
	}
	wm := renderWordmark(40)
	if !strings.Contains(wm, "MIVIA") {
		t.Fatal("wordmark missing MIVIA")
	}
	if !strings.Contains(stripANSI(wm), "MIVIA") {
		t.Fatalf("wordmark strip lost MIVIA: %q", stripANSI(wm))
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
		{Name: chat.AutoSaveName + "20250115T103000", MessageCount: 4, UpdatedAt: time.Now()},
		{Name: "work", MessageCount: 10, UpdatedAt: time.Now().Add(-time.Hour)},
		{Name: chat.AutoSaveName, MessageCount: 2, UpdatedAt: time.Now().Add(-2 * time.Hour)},
	}
	block, hits, sc := renderSessionPicker(sessions, 0, 0, 80, 5, 10)
	if sc != 0 {
		t.Fatalf("scroll %d", sc)
	}
	// Both auto-save names should display as "Last session".
	if !strings.Contains(block, "Last session") {
		t.Fatalf("missing display name: %q", block)
	}
	if !strings.Contains(block, "work") {
		t.Fatal("missing work session")
	}
	if len(hits) != 3 {
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
