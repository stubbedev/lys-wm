package compositor

import (
	"image"
	"testing"

	"github.com/stubbedev/lys-wm/internal/output"
	"github.com/stubbedev/lys-wm/internal/render"
	"github.com/stubbedev/lys-wm/internal/render/soft"
)

func TestAFrameCostCountsWhatItFilteredRepaintedAllocatedAndCopied(t *testing.T) {
	r := soft.New()
	c := &Compositor{renderer: r, scenes: map[*output.Output]*scene{}}
	o := &output.Output{}
	target, err := r.NewTextureTarget(image.Pt(16, 16))
	if err != nil {
		t.Fatal(err)
	}
	src, err := r.NewTextureTarget(image.Pt(16, 16))
	if err != nil {
		t.Fatal(err)
	}
	damage := render.Region{image.Rect(0, 0, 4, 4)}
	p, err := r.Begin(target, damage)
	if err != nil {
		t.Fatal(err)
	}
	c.kawase(p, src.Texture(), image.Rect(2, 2, 16, 16), damage, 1, false)
	if err := p.End(); err != nil {
		t.Fatal(err)
	}
	c.tallied.repaintPx += 100
	c.copiedBytes.Add(64)
	cost := c.frameCost(o)
	if cost.FilterPx != 4 || cost.RepaintPx != 100 || cost.Copied != 64 || cost.Targets != 2 || cost.Filters != 1 || cost.Submits != 0 {
		t.Fatalf("cost = %+v", cost)
	}
	if again := c.frameCost(o); again.FilterPx != 0 || again.RepaintPx != 0 || again.Copied != 0 || again.Targets != 0 || again.Filters != 0 {
		t.Fatalf("a frame doing nothing after it costs %+v", again)
	}
}
