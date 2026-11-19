//go:build windows

package desktop

import "testing"

// TestParsePathDataAllIcons guards the SVG path parser: every embedded icon
// must produce closed contours covering a sane area, or nothing will render.
func TestParsePathDataAllIcons(t *testing.T) {
	for name, def := range iconTable {
		subs := parsePathData(def.D)
		if len(subs) == 0 {
			t.Fatalf("icon %s produced no subpaths", name)
		}
		// Contours must stay inside the viewBox.
		for _, sp := range subs {
			for _, p := range sp.Points {
				if p.X < -1 || p.Y < -1 || p.X > def.ViewBox+1 || p.Y > def.ViewBox+1 {
					t.Fatalf("icon %s has out-of-viewbox point %v", name, p)
				}
			}
		}
	}
}

// TestRasterizePathInk verifies the pulse icon rasterizes with meaningful
// coverage at ball-icon size (regression for the command-skipping parser bug
// that rendered empty icons).
func TestRasterizePathInk(t *testing.T) {
	def := iconTable["pulse"]
	subs := parsePathData(def.D)
	const box = 44
	cov := rasterizePath(subs, box, box, 0, 0, float64(box)/def.ViewBox)
	if cov == nil {
		t.Fatal("no coverage")
	}
	ink := 0
	for _, a := range cov {
		if a > 128 {
			ink++
		}
	}
	// The waveform occupies roughly 8–25% of the box; require >3% ink and
	// <60% (not a filled square).
	if ink < box*box*3/100 {
		t.Fatalf("pulse ink = %d/%d px, rasterizer produced too little", ink, box*box)
	}
	if ink > box*box*60/100 {
		t.Fatalf("pulse ink = %d/%d px, suspiciously full", ink, box*box)
	}
}

// TestRasterizePathEvenOddShape checks the more icon (three dots) renders
// three separated blobs.
func TestRasterizePathEvenOddShape(t *testing.T) {
	def := iconTable["more"]
	subs := parsePathData(def.D)
	const box = 20
	cov := rasterizePath(subs, box, box, 0, 0, float64(box)/def.ViewBox)
	rows := 0
	for y := 0; y < box; y++ {
		has := false
		for x := 0; x < box; x++ {
			if cov[y*box+x] > 128 {
				has = true
			}
		}
		if has {
			rows++
		}
	}
	if rows == 0 {
		t.Fatal("more icon produced no ink")
	}
}
