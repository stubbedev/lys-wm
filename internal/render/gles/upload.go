package gles

import (
	"errors"
	"image"

	"github.com/stubbedev/lys-wm/internal/gpu"
)

const uploadBandBytes = 1 << 20

// upload hands the bound texture dst's pixels a band at a time, copying
// them in Go first: data can be a client's memory, which faults in Go
// but would take the process down inside the driver, and NVIDIA keeps a
// CPU copy of a whole texture for as long as it lives when it is
// uploaded from client memory, but not from a pixel unpack buffer.
func (r *Renderer) upload(u pixelUpload, dst image.Rectangle, stride int, data []byte) error {
	row := dst.Dx() * 4
	rows := max(uploadBandBytes/row, 1)
	if r.es3 {
		r.gl.BindBuffer(gpu.PixelUnpackBuffer, r.unpack)
		defer r.gl.BindBuffer(gpu.PixelUnpackBuffer, 0)
	}
	for y := 0; y < dst.Dy(); y += rows {
		n := min(rows, dst.Dy()-y)
		x0, y0, w, h := int32(dst.Min.X), int32(dst.Min.Y+y), int32(dst.Dx()), int32(n) //nolint:gosec // inside the texture
		band := data[y*stride:]
		if !r.es3 {
			if len(r.band) < row*n {
				r.band = make([]byte, max(uploadBandBytes, row))
			}
			copyRows(r.band, row, n, stride, band)
			r.gl.TexSubImage2D(gpu.Texture2D, x0, y0, w, h, u.format, u.typ, r.band)
			continue
		}
		at, err := r.fillRing(row, n, stride, band)
		if err != nil {
			return err
		}
		r.gl.TexSubImage2DFromBuffer(gpu.Texture2D, x0, y0, w, h, u.format, u.typ, at)
	}
	return nil
}

// fillRing packs n rows into the unpack buffer's next free range and
// returns its offset. The buffer is written behind the GPU's back, in
// ranges no queued upload reads, and given fresh storage only when it
// wraps, so a stream of small damage does not make the driver keep a
// retired copy of the buffer per rectangle.
func (r *Renderer) fillRing(row, n, stride int, data []byte) (int, error) {
	size := row * n
	capacity := max(uploadBandBytes, size)
	wrap := r.ringCap < capacity || r.ringAt+size > r.ringCap
	if r.ringCap < capacity {
		r.gl.AllocateBuffer(gpu.PixelUnpackBuffer, capacity, gpu.StreamDraw)
		r.ringCap = capacity
	}
	if wrap {
		r.ringAt = 0
	}
	mapped := r.gl.MapBufferRangeForWriting(gpu.PixelUnpackBuffer, r.ringAt, size, wrap)
	if mapped == nil {
		return 0, errors.New("gles: could not map the pixel unpack buffer")
	}
	defer r.gl.UnmapBuffer(gpu.PixelUnpackBuffer)
	copyRows(mapped, row, n, stride, data)
	at := r.ringAt
	r.ringAt += size
	return at, nil
}

func copyRows(dst []byte, row, n, stride int, data []byte) {
	for y := range n {
		copy(dst[y*row:(y+1)*row], data[y*stride:])
	}
}
