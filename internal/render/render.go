// Package render defines the renderer interface that the software and GLES
// renderers implement.
package render

import "image"

type Transform uint8

const (
	Normal Transform = iota
	Rotate90
	Rotate180
	Rotate270
	Flipped
	Flipped90
	Flipped180
	Flipped270
)

func (t Transform) SwapsAxes() bool {
	return t&1 == 1
}

type Format uint8

const (
	ARGB8888 Format = iota + 1
	XRGB8888
)

type Color struct {
	R, G, B, A uint8
}

type FRect struct {
	X, Y, W, H float64
}

type Region []image.Rectangle

type Target interface {
	Size() image.Point
}

type Texture interface {
	Size() image.Point
	Destroy()
}

type Renderer interface {
	NewTexture(format Format, size image.Point, stride int, data []byte) (Texture, error)
	Begin(target Target, damage Region) (Pass, error)
}

type Pass interface {
	FillRect(r image.Rectangle, c Color)
	DrawTexture(t Texture, src FRect, dst image.Rectangle, transform Transform, alpha float32)
	End() error
}
