package golden

import (
	"bytes"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stubbedev/lys-wm/internal/render/soft"
)

type fatalRecorder struct {
	testing.TB
	fatal string
}

func (r *fatalRecorder) Helper() {}

func (r *fatalRecorder) Fatalf(format string, args ...any) {
	r.fatal = fmt.Sprintf(format, args...)
	panic(r)
}

func grayBuffer(w, h int, v byte) *soft.Buffer {
	b := &soft.Buffer{Pix: make([]byte, w*h*4), Stride: w * 4, W: w, H: h}
	for i := 0; i < len(b.Pix); i += 4 {
		b.Pix[i], b.Pix[i+1], b.Pix[i+2], b.Pix[i+3] = v, v, v, 0xff
	}
	return b
}

func writeGolden(t *testing.T, name string, b *soft.Buffer) {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, b.W, b.H))
	for i := 0; i < len(b.Pix); i += 4 {
		copy(img.Pix[i:], []byte{b.Pix[i+2], b.Pix[i+1], b.Pix[i], 0xff})
	}
	var enc bytes.Buffer
	if err := png.Encode(&enc, img); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll("testdata", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join("testdata", name+".png"), enc.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

func failure(t *testing.T, name string, frame *soft.Buffer, tolerance int) string {
	t.Helper()
	r := &fatalRecorder{TB: t}
	func() {
		defer func() {
			if p := recover(); p != nil && p != r {
				panic(p)
			}
		}()
		Within(r, name, frame, tolerance)
	}()
	return r.fatal
}

func TestAMismatchNamesTheFirstPixelHowManyAndWhere(t *testing.T) {
	t.Chdir(t.TempDir())
	writeGolden(t, "frame", grayBuffer(8, 8, 0x40))
	frame := grayBuffer(8, 8, 0x40)
	for _, p := range []image.Point{{2, 1}, {5, 3}, {3, 6}} {
		frame.Pix[p.Y*frame.Stride+p.X*4] = 0x90
	}
	got := failure(t, "frame", frame, 4)
	for _, want := range []string{"pixel (2, 1)", "3 pixels off within (2,1)-(6,7)"} {
		if !strings.Contains(got, want) {
			t.Errorf("failure %q, want it to say %q", got, want)
		}
	}
}

func TestAFrameWithinToleranceMatches(t *testing.T) {
	t.Chdir(t.TempDir())
	writeGolden(t, "frame", grayBuffer(4, 4, 0x40))
	if got := failure(t, "frame", grayBuffer(4, 4, 0x43), 4); got != "" {
		t.Errorf("a frame within tolerance failed: %s", got)
	}
}
