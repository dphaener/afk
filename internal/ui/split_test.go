package ui

import (
	"fmt"
	"testing"
	"time"
)

// legacySidebarOuterWidth is the pre-split fixed-38% arithmetic, restated
// here so the default-split rows pin the new function against the old one
// rather than against itself.
func legacySidebarOuterWidth(termWidth int) int {
	w := termWidth * 38 / 100
	if w < 30 {
		w = 30
	}
	if w > termWidth {
		w = termWidth
	}
	return w
}

func TestSidebarOuterWidthForDefaultMatchesLegacy(t *testing.T) {
	for _, width := range []int{20, 40, 80, 90, 100, 120, 200} {
		for _, pct := range []int{0, 38} {
			t.Run(fmt.Sprintf("w%d_pct%d", width, pct), func(t *testing.T) {
				want := legacySidebarOuterWidth(width)
				if got := sidebarOuterWidthFor(width, pct); got != want {
					t.Errorf("sidebarOuterWidthFor(%d, %d) = %d, want %d", width, pct, got, want)
				}
				if got := sidebarOuterWidth(width); got != want {
					t.Errorf("sidebarOuterWidth(%d) = %d, want %d", width, got, want)
				}
			})
		}
	}
}

func TestSidebarOuterWidthFor(t *testing.T) {
	tests := []struct {
		name  string
		width int
		pct   int
		want  int
	}{
		{"pinned origin 80", 80, 38, 30},
		{"pinned origin 90", 90, 38, 34},
		{"pinned origin 100", 100, 38, 38},
		{"pinned origin 120", 120, 38, 45},
		{"zero pct means default", 400, 0, 152},
		{"negative pct means default", 400, -7, 152},
		{"below min pct clamps up to 15", 400, 5, 60},
		{"min pct exactly", 400, 15, 60},
		{"above max pct clamps down to 85", 400, 95, 340},
		{"max pct exactly", 400, 85, 340},
		{"mid-range pct", 200, 50, 100},
		{"main floor holds back a wide sidebar", 100, 85, 70},
		{"main floor on a mid terminal", 80, 85, 50},
		{"sidebar floor wins over main floor", 60, 85, 30},
		{"tiny terminal: sidebar floor then width cap", 20, 85, 20},
		{"narrow terminal below sidebar floor", 50, 38, 30},
		{"zero width", 0, 38, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sidebarOuterWidthFor(tt.width, tt.pct); got != tt.want {
				t.Errorf("sidebarOuterWidthFor(%d, %d) = %d, want %d", tt.width, tt.pct, got, tt.want)
			}
		})
	}
}

func TestSidebarOuterWidthForKeepsMainRegionWhenTerminalAllows(t *testing.T) {
	for width := minSidebarWidth + minMainWidth; width <= 300; width++ {
		for pct := 1; pct <= 100; pct++ {
			got := sidebarOuterWidthFor(width, pct)
			if got < minSidebarWidth || width-got < minMainWidth {
				t.Fatalf("sidebarOuterWidthFor(%d, %d) = %d: sidebar floor %d / main floor %d violated",
					width, pct, got, minSidebarWidth, minMainWidth)
			}
		}
	}
}

func TestLayoutDimsAndMouseFrameLayoutAgreeAtNonDefaultPct(t *testing.T) {
	for _, pct := range []int{15, 50, 70, 85, 200} {
		for _, width := range []int{20, 80, 120, 200} {
			t.Run(fmt.Sprintf("pct%d_w%d", pct, width), func(t *testing.T) {
				m, _ := frameModel("live", width, 30, true)
				m.sidebarPct = pct

				wantList, wantMain, wantBody := m.layoutDims()
				listOuter, mainOuter, bodyHeight, _, _ := m.mouseFrameLayout()
				if listOuter != wantList || mainOuter != wantMain || bodyHeight != wantBody {
					t.Errorf("mouseFrameLayout = (%d, %d, %d), layoutDims = (%d, %d, %d)",
						listOuter, mainOuter, bodyHeight, wantList, wantMain, wantBody)
				}
				if want := sidebarOuterWidthFor(width, pct); listOuter != want {
					t.Errorf("listOuter = %d, want sidebarOuterWidthFor(%d, %d) = %d", listOuter, width, pct, want)
				}
			})
		}
	}
}

func TestSetSidebarPctResizesThePane(t *testing.T) {
	m, fp := frameModel("live", 120, 40, true)
	defaultCols, defaultRows := m.paneRegionSize()
	fp.resizes = nil

	m.setSidebarPct(60)

	if m.sidebarPct != 60 {
		t.Fatalf("sidebarPct = %d, want 60", m.sidebarPct)
	}
	if len(fp.resizes) != 1 {
		t.Fatalf("pane resized %d times, want exactly 1: %v", len(fp.resizes), fp.resizes)
	}
	wantCols, wantRows := m.paneRegionSize()
	if got := fp.resizes[0]; got != [2]int{wantCols, wantRows} {
		t.Errorf("pane resized to %v, want the new region %dx%d", got, wantCols, wantRows)
	}
	if wantCols >= defaultCols {
		t.Errorf("a wider sidebar should narrow the pane: %d cols, was %d", wantCols, defaultCols)
	}
	if wantRows != defaultRows {
		t.Errorf("rows changed from %d to %d on a horizontal-only move", defaultRows, wantRows)
	}
	if listOuter, _, _ := m.layoutDims(); listOuter != 72 {
		t.Errorf("listOuter = %d, want 72 (60%% of 120)", listOuter)
	}
}

func TestSetSidebarPctClamps(t *testing.T) {
	for _, tt := range []struct{ in, want int }{
		{-5, minSidebarPct},
		{0, minSidebarPct},
		{1, minSidebarPct},
		{15, 15},
		{38, 38},
		{85, 85},
		{99, maxSidebarPct},
	} {
		t.Run(fmt.Sprintf("in%d", tt.in), func(t *testing.T) {
			m, _ := frameModel("live", 200, 40, true)
			m.setSidebarPct(tt.in)
			if m.sidebarPct != tt.want {
				t.Errorf("setSidebarPct(%d) stored %d, want %d", tt.in, m.sidebarPct, tt.want)
			}
		})
	}
}

func TestNewAppliesLoadSidebarPct(t *testing.T) {
	tests := []struct {
		name string
		load func() int
		want int
	}{
		{"nil seam leaves the default", nil, 0},
		{"seam value is applied", func() int { return 55 }, 55},
		{"zero is kept as unset", func() int { return 0 }, 0},
		{"out of range is stored and clamped at layout time", func() int { return 200 }, 200},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := testDeps()
			d.LoadSidebarPct = tt.load
			m := New("/repo", time.Second, d)
			if m.sidebarPct != tt.want {
				t.Errorf("sidebarPct = %d, want %d", m.sidebarPct, tt.want)
			}
		})
	}

	d := testDeps()
	d.LoadSidebarPct = func() int { return 200 }
	m := New("/repo", time.Second, d)
	m.width, m.height = 400, 40
	if listOuter, _, _ := m.layoutDims(); listOuter != 340 {
		t.Errorf("listOuter = %d, want 340 (an out-of-range load clamps to 85%% of 400)", listOuter)
	}
}
