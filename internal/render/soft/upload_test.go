package soft

import (
	"bytes"
	"image"
	"testing"
)

func TestUpdateStoresEachFormatDecoded(t *testing.T) {
	r := New()
	size := image.Pt(37, 5)
	stride := size.X*4 + 12
	data := make([]byte, stride*size.Y)
	for i := range data {
		data[i] = byte(i*131 + i>>8)
	}
	for _, f := range r.Formats() {
		tex, err := r.NewTexture(f, size, stride, data)
		if err != nil {
			t.Fatal(err)
		}
		want := make([]byte, 0, size.X*size.Y*4)
		for y := range size.Y {
			for x := range size.X {
				c := f.Decode(data[y*stride+x*4:])
				want = append(want, c.B, c.G, c.R, c.A)
			}
		}
		if got := tex.(*texture).pix; !bytes.Equal(got, want) {
			t.Errorf("format %d: uploaded pixels differ from Decode", f)
		}
	}
}
