package cli

import "testing"

func TestDialogRectFitsAndCenters(t *testing.T) {
	tests := []struct {
		name               string
		termW, termH       int
		prefs              DialogPrefs
		contentW, contentH int
		want               Rect
	}{
		{name: "preferred odd spare", termW: 100, termH: 40, prefs: DialogPrefs{PreferredW: 60, preferredH: 20}, contentW: 20, contentH: 10, want: Rect{X: 20, Y: 10, W: 60, H: 20}},
		{name: "percentage", termW: 101, termH: 41, prefs: DialogPrefs{PreferredWPct: 90, PreferredHPct: 80}, contentW: 1, contentH: 1, want: Rect{X: 5, Y: 4, W: 90, H: 32}},
		{name: "content with minimum", termW: 80, termH: 24, prefs: DialogPrefs{MinW: 32, MinH: 8}, contentW: 12, contentH: 3, want: Rect{X: 24, Y: 8, W: 32, H: 8}},
		{name: "clamp raw tiny", termW: 10, termH: 4, prefs: DialogPrefs{PreferredW: 60, preferredH: 20, MinW: 32, MinH: 8}, contentW: 1, contentH: 1, want: Rect{X: 0, Y: 0, W: 10, H: 4}},
		{name: "zero dimensions", termW: 0, termH: -2, prefs: DialogPrefs{PreferredW: 10, preferredH: 10}, contentW: 4, contentH: 4, want: Rect{}},
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
		layout := MakeDialogLayout(size.w, size.h, DialogPrefs{PreferredW: 76, PreferredHPct: 70, MinW: 40, MinH: 8, FrameRows: 2}, func(innerW int) (int, int) {
			return innerW, 100
		})
		if layout.Rect.X < 0 || layout.Rect.Y < 0 || layout.Rect.X+layout.Rect.W > Max(0, size.w) || layout.Rect.Y+layout.Rect.H > Max(0, size.h) {
			t.Fatalf("size %dx%d produced out-of-bounds layout: %+v", size.w, size.h, layout)
		}
		if layout.Rect.W < 0 || layout.Rect.H < 0 || layout.InnerW < 0 || layout.PageH < 0 {
			t.Fatalf("size %dx%d produced negative geometry: %+v", size.w, size.h, layout)
		}
	}
}

func TestDialogResizeRecentersAndReflows(t *testing.T) {
	p := DialogPrefs{PreferredW: 40, PreferredHPct: 70, MinW: 20, MinH: 8, FrameRows: 2}
	wide := MakeDialogLayout(100, 40, p, func(innerW int) (int, int) { return innerW, 12 })
	narrow := MakeDialogLayout(50, 20, p, func(innerW int) (int, int) { return innerW, 24 })
	wider := MakeDialogLayout(120, 40, p, func(innerW int) (int, int) { return innerW, 12 })
	if wide.Rect.X != 30 || narrow.Rect.X != 5 || wider.Rect.X != 40 {
		t.Fatalf("layouts did not recenter: wide=%+v narrow=%+v wider=%+v", wide, narrow, wider)
	}
	if narrow.PageH >= wide.PageH || wider.PageH != wide.PageH {
		t.Fatalf("page heights did not follow resized geometry: wide=%d narrow=%d wider=%d", wide.PageH, narrow.PageH, wider.PageH)
	}
}

func TestDialogResizeClampsScroll(t *testing.T) {
	o := newDialog("test", []string{"a", "b", "c", "d", "e", "f", "g", "h"})
	o.yOffset = 100
	l := o.layout(20, 6)
	o.clamp(l.PageH)
	if o.yOffset != len(o.displayRows(l.InnerW))-l.PageH {
		t.Fatalf("offset=%d, want final page start", o.yOffset)
	}
}
