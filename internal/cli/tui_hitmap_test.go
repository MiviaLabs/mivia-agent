package cli

import "testing"

func TestTUIHitMapSyntheticZonesAndTypedRanges(t *testing.T) {
	var h tuiHitMap
	h.invalidate()
	h.rebuild(80, 24, 1, 10, 11, 14, 15, 19,
		map[string][2]int{"turn-1-block-2": {3, 5}}, 2)

	cases := []struct {
		name string
		y    int
		kind tuiHitZoneKind
		id   string
	}{
		{"transcript", 9, hitTranscript, ""},
		// The typed range is viewport-relative and must win over the broad zone.
		{"typed transcript block", 2, hitTranscript, "turn-1-block-2"},
		{"tools", 12, hitTools, ""},
		{"composer", 17, hitComposer, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			z, ok := h.hit(tc.y)
			if !ok || z.kind != tc.kind || z.blockID != tc.id {
				t.Fatalf("hit(%d) = %#v, %v; want kind=%d id=%q", tc.y, z, ok, tc.kind, tc.id)
			}
		})
	}
}

func TestTUIHitMapInvalidationRejectsStaleCoordinates(t *testing.T) {
	var h tuiHitMap
	h.invalidate()
	h.rebuild(80, 24, 1, 4, 5, 7, 8, 10, nil, 0)
	if _, ok := h.hit(6); !ok {
		t.Fatal("expected tool hit before invalidation")
	}
	version := h.version
	h.invalidate()
	if h.version != version+1 {
		t.Fatalf("version=%d, want %d", h.version, version+1)
	}
	if _, ok := h.hit(6); ok {
		t.Fatal("stale hit-map coordinate remained active after invalidation")
	}
}
