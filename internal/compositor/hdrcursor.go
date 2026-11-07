package compositor

import (
	"image"
	"math"

	"github.com/stubbedev/lys-wm/internal/colorspace"
	"github.com/stubbedev/lys-wm/internal/output"
	"github.com/stubbedev/lys-wm/internal/render"
	"github.com/stubbedev/lys-wm/internal/render/soft"
)

func (c *Compositor) cursorPlan(o *output.Output) *render.ColorTransform {
	hc := c.hdr[o.Backend()]
	if hc == nil {
		return nil
	}
	return c.plans.Of(colorspace.SDR, hc.signal)
}

func encodeCursor(plan *render.ColorTransform, dst, src []byte, dstStride, srcStride int, size image.Point) {
	for y := range size.Y {
		in, out := src[y*srcStride:][:size.X*4], dst[y*dstStride:][:size.X*4]
		for x := 0; x+4 <= len(in); x += 4 {
			s, d := (*[4]byte)(in[x:]), (*[4]byte)(out[x:])
			px := plan.Apply([4]float32{float32(s[2]) / 255, float32(s[1]) / 255, float32(s[0]) / 255, float32(s[3]) / 255})
			*d = [4]byte{code(px[2]), code(px[1]), code(px[0]), s[3]}
		}
	}
}

func code(v float32) byte {
	return byte(math.Round(float64(min(max(v, 0), 1)) * 255))
}

func (hw *hwCursor) load(plan *render.ColorTransform, pix []byte, stride int, size, hotspot image.Point) error {
	if plan == nil {
		return hw.plane.LoadCursor(pix, stride, size, hotspot)
	}
	n := size.X * 4 * size.Y
	if cap(hw.encoded) < n {
		hw.encoded = make([]byte, n)
	}
	hw.encoded = hw.encoded[:n]
	encodeCursor(plan, hw.encoded, pix, size.X*4, stride, size)
	return hw.plane.LoadCursor(hw.encoded, size.X*4, size, hotspot)
}

func encodeDrawn(plan *render.ColorTransform, t render.Target) {
	b, ok := t.(*soft.Buffer)
	if plan == nil || !ok {
		return
	}
	encodeCursor(plan, b.Pix, b.Pix, b.Stride, b.Stride, image.Pt(b.W, b.H))
}

func (c *Compositor) recolorCursor(o *output.Output) {
	if hw, ok := c.hwCursors[o]; ok {
		hw.holds = false
	}
}
