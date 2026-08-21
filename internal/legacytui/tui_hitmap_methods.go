package legacytui

// tuiHitMap is derived render state. It must never be used after the frame
// that produced it has changed; invalidation makes stale mouse coordinates
// fail closed instead of selecting a different control.
type tuiHitZoneKind uint8

const (
	// HitTranscript marks a hit zone over the transcript pane. Shared with
	// internal/legacytui's mouse and message hit-testing.
	HitTranscript tuiHitZoneKind = iota + 1
	hitTools
	// HitComposer marks a hit zone over the composer pane. Shared with
	// internal/legacytui's mouse hit-testing.
	HitComposer
)

type tuiHitZone struct {
	// Kind identifies which pane the zone belongs to.
	Kind   tuiHitZoneKind
	y0, y1 int // inclusive screen coordinates
	// BlockID is the chat block this zone hit-tests against.
	BlockID string
}

type tuiHitMap struct {
	version       uint64
	frame         uint64
	width, height int
	zones         []tuiHitZone
}

func (h *tuiHitMap) invalidate() {
	h.version++
	h.frame = 0
	h.zones = nil
}

func (h *tuiHitMap) rebuild(width, height, headerY, transcriptLines, toolY0, toolY1, composerY0, composerY1 int, blockRanges map[string][2]int, viewportOffset int) {
	h.frame = h.version
	h.width, h.height = width, height
	h.zones = h.zones[:0]
	if transcriptLines > 0 {
		h.zones = append(h.zones, tuiHitZone{Kind: HitTranscript, y0: headerY, y1: headerY + transcriptLines - 1})
		for id, r := range blockRanges {
			start, end := r[0]+headerY-viewportOffset, r[1]+headerY-viewportOffset
			if end > start {
				h.zones = append(h.zones, tuiHitZone{Kind: HitTranscript, y0: start, y1: end - 1, BlockID: id})
			}
		}
	}
	if toolY1 >= toolY0 {
		h.zones = append(h.zones, tuiHitZone{Kind: hitTools, y0: toolY0, y1: toolY1})
	}
	if composerY1 >= composerY0 {
		h.zones = append(h.zones, tuiHitZone{Kind: HitComposer, y0: composerY0, y1: composerY1})
	}
}

func (h *tuiHitMap) hit(y int) (tuiHitZone, bool) {
	if h.frame == 0 || h.frame != h.version || y < 0 || y >= h.height {
		return tuiHitZone{}, false
	}
	// Later zones win: typed block ranges are more specific than the broad
	// transcript zone, and controls win over transcript overlap at boundaries.
	for i := len(h.zones) - 1; i >= 0; i-- {
		z := h.zones[i]
		if y >= z.y0 && y <= z.y1 {
			return z, true
		}
	}
	return tuiHitZone{}, false
}
