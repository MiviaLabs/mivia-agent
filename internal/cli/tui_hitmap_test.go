package cli

import "testing"

func TestTUIHitMapSyntheticZonesAndTypedRanges(t *testing.T) {
	var h tuiHitMap
	h.invalidate()
	h.rebuild(80, 24, 1, 10, 1, 0, 15, 19,
		map[string][2]int{"turn-1-block-2": {3, 5}}, 2)

	cases := []struct {
		name string
		y    int
		kind tuiHitZoneKind
		id   string
	}{
		{"transcript", 9, HitTranscript, ""},
		{"typed transcript block", 2, HitTranscript, "turn-1-block-2"},
		{"composer", 17, HitComposer, ""},
		{"no tools zone - falls through", 12, 0, ""}, // should be no hit (0 = zero value)
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			z, ok := h.hit(tc.y)
			if tc.kind == 0 && ok {
				t.Fatalf("hit(%d) = %#v, %v; expected no hit", tc.y, z, ok)
			}
			if tc.kind != 0 && (!ok || z.Kind != tc.kind || z.BlockID != tc.id) {
				t.Fatalf("hit(%d) = %#v, %v; want kind=%d id=%q", tc.y, z, ok, tc.kind, tc.id)
			}
		})
	}
}

func TestTUIHitMapInvalidationRejectsStaleCoordinates(t *testing.T) {
	var h tuiHitMap
	h.invalidate()
	h.rebuild(80, 24, 1, 4, 1, 0, 8, 10, nil, 0)
	version := h.version
	h.invalidate()
	if h.version != version+1 {
		t.Fatalf("version=%d, want %d", h.version, version+1)
	}
	if _, ok := h.hit(6); ok {
		t.Fatal("stale hit-map coordinate remained active after invalidation")
	}
}

func TestTUIHitMapVersionChangesAfterViewportMutation(t *testing.T) {
	var h tuiHitMap
	h.invalidate()
	h.rebuild(80, 24, 1, 8, 1, 0, 11, 15, map[string][2]int{"block": {0, 2}}, 0)
	if _, ok := h.hit(1); !ok {
		t.Fatal("expected block hit before viewport mutation")
	}
	version := h.version
	h.invalidate()
	h.rebuild(80, 24, 1, 8, 1, 0, 11, 15, map[string][2]int{"block": {0, 2}}, 2)
	if h.version != version+1 {
		t.Fatalf("version=%d, want %d", h.version, version+1)
	}
	if zone, ok := h.hit(0); !ok || zone.BlockID != "block" {
		t.Fatalf("rebuild did not expose current block hit: %#v, %v", zone, ok)
	}
}
