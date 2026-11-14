package render

import "image"

type Effects struct {
	Textures []Texture
	Stops    Texture
	Sources  []Texture
	Cut      Texture
	Color    *ColorTransform
}

var (
	effectOpaque      = Color{R: 0x80, G: 0x40, B: 0x20, A: 0xff}
	effectTranslucent = Color{R: 0x40, G: 0x20, B: 0x10, A: 0x80}
)

func DrawEffects(p Pass, e Effects) {
	dst := image.Rect(0, 0, 1, 1)
	pixel := FRect{W: 1, H: 1}
	scaled := FRect{W: 2, H: 2}
	rounded := Mask{Keep: RoundRect{Rect: dst, Radius: 0.5}}
	colors := []*ColorTransform{nil}
	if e.Color != nil {
		colors = append(colors, e.Color)
	}
	cuts := []Texture{nil}
	if e.Cut != nil {
		cuts = append(cuts, e.Cut)
	}
	for _, color := range colors {
		p.SetColor(color)
		for _, cut := range cuts {
			p.ClearCut()
			if cut != nil {
				p.SetCut(cut, AlphaCut{Src: pixel, Dst: dst, Threshold: 0.5})
			}
			p.FillRect(dst, effectOpaque, Mask{})
			p.FillRect(dst, effectTranslucent, rounded)
			p.FillGradient(dst, Gradient{Stops: e.Stops, To: [2]float32{1, 1}, Alpha: 1}, Mask{})
			for _, t := range e.Textures {
				p.DrawTexture(t, pixel, dst, Normal, 1, Mask{})
				p.DrawTexture(t, scaled, dst, Normal, 0.5, rounded)
			}
			for _, s := range e.Sources {
				p.DrawGlass(s, scaled, dst, 1, Mask{}, Glass{Saturation: 1, Brightness: 1, Contrast: 1, Bezel: 1, Amount: 1})
			}
		}
		p.ClearCut()
		p.FillGoo(dst, effectTranslucent, Goo{A: RoundRect{Rect: dst}, B: RoundRect{Rect: dst}, Smooth: 1})
		for _, s := range e.Sources {
			p.Kawase(s, dst, 1, false)
			p.Kawase(s, dst, 1, true)
		}
	}
	p.SetColor(nil)
}
