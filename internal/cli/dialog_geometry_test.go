package cli

import "testing"

func TestDialogRectFitsAndCenters(t *testing.T) {
	tests := []struct {
		name               string
		termW, termH       int
		prefs              dialogPrefs
		contentW, contentH int
		want               rect
	}{
		{name: "preferred odd spare", termW: 100, termH: 40, prefs: dialogPrefs{preferredW: 60, preferredH: 20}, contentW: 20, contentH: 10, want: rect{x: 20, y: 10, w: 60, h: 20}},
		{name: "percentage", termW: 101, termH: 41, prefs: dialogPrefs{preferredWPct: 90, preferredHPct: 80}, contentW: 1, contentH: 1, want: rect{x: 5, y: 4, w: 90, h: 32}},
		{name: "content with minimum", termW: 80, termH: 24, prefs: dialogPrefs{minW: 32, minH: 8}, contentW: 12, contentH: 3, want: rect{x: 24, y: 8, w: 32, h: 8}},
		{name: "clamp raw tiny", termW: 10, termH: 4, prefs: dialogPrefs{preferredW: 60, preferredH: 20, minW: 32, minH: 8}, contentW: 1, contentH: 1, want: rect{x: 0, y: 0, w: 10, h: 4}},
		{name: "zero dimensions", termW: 0, termH: -2, prefs: dialogPrefs{preferredW: 10, preferredH: 10}, contentW: 4, contentH: 4, want: rect{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := dialogRect(tt.termW, tt.termH, tt.prefs, tt.contentW, tt.contentH); got != tt.want {
				t.Fatalf("dialogRect() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestDialogViewsStayWithinTerminalBounds(t *testing.T) {
	for _, size := range []struct{ w, h int }{{1, 1}, {10, 4}, {20, 6}, {39, 10}, {100, 30}} {
		layout := makeDialogLayout(size.w, size.h, dialogPrefs{preferredW: 76, preferredHPct: 70, minW: 40, minH: 8, frameRows: 2}, func(innerW int) (int, int) {
			return innerW, 100
		})
		if layout.rect.x < 0 || layout.rect.y < 0 || layout.rect.x+layout.rect.w > max(0, size.w) || layout.rect.y+layout.rect.h > max(0, size.h) {
			t.Fatalf("size %dx%d produced out-of-bounds layout: %+v", size.w, size.h, layout)
		}
		if layout.rect.w < 0 || layout.rect.h < 0 || layout.innerW < 0 || layout.pageH < 0 {
			t.Fatalf("size %dx%d produced negative geometry: %+v", size.w, size.h, layout)
		}
	}
}

func TestDialogResizeRecentersAndReflows(t *testing.T) {
	p := dialogPrefs{preferredW: 40, preferredHPct: 70, minW: 20, minH: 8, frameRows: 2}
	wide := makeDialogLayout(100, 40, p, func(innerW int) (int, int) { return innerW, 12 })
	narrow := makeDialogLayout(50, 20, p, func(innerW int) (int, int) { return innerW, 24 })
	wider := makeDialogLayout(120, 40, p, func(innerW int) (int, int) { return innerW, 12 })
	if wide.rect.x != 30 || narrow.rect.x != 5 || wider.rect.x != 40 {
		t.Fatalf("layouts did not recenter: wide=%+v narrow=%+v wider=%+v", wide, narrow, wider)
	}
	if narrow.pageH >= wide.pageH || wider.pageH != wide.pageH {
		t.Fatalf("page heights did not follow resized geometry: wide=%d narrow=%d wider=%d", wide.pageH, narrow.pageH, wider.pageH)
	}
}

func TestDialogResizeClampsScroll(t *testing.T) {
	o := newDialog("test", []string{"a", "b", "c", "d", "e", "f", "g", "h"})
	o.yOffset = 100
	l := o.layout(20, 6)
	o.clamp(l.pageH)
	if o.yOffset != len(o.displayRows(l.innerW))-l.pageH {
		t.Fatalf("offset=%d, want final page start", o.yOffset)
	}
}
