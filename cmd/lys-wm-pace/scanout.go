package main

import (
	"errors"
	"fmt"
	"image"

	cw "github.com/stubbedev/lys-wm/internal/protocol/client/wayland"
	"github.com/stubbedev/lys-wm/internal/testclient"
)

const scaledTitle = "scaled"

type flipper struct {
	s       *session
	w       *testclient.Window
	buffers [2]*testclient.DMABUF
	size    image.Point
	next    int
	running bool
}

func (s *session) scaledFullscreen() {
	_, in := s.client.Seat()
	w := s.window(scaledTitle, testclient.Fill(0x30, 0x30, 0x30))
	s.dispatch("fullscreen")
	s.client.DispatchUntil("the fullscreen configure", func() bool { return w.Logical.X > 0 && w.Logical == w.Size })
	s.swipe("move 1 1")
	s.client.DispatchUntil("the pointer to enter", func() bool { return in.Serial != 0 })
	if err := errors.Join(in.Pointer.SetCursor(in.Serial, nil, 0, 0), s.client.Display.Flush()); err != nil {
		panic(fatal{fmt.Errorf("hide the cursor over the scaled window: %w", err)})
	}
	w.Stop()
	full := w.Logical
	vp, err := s.client.Viewporter.GetViewport(w.Surface)
	if err != nil {
		panic(fatal{fmt.Errorf("get the scaled window's viewport: %w", err)})
	}
	if err := vp.SetDestination(int32(full.X), int32(full.Y)); err != nil { //nolint:gosec // an output's logical size fits
		panic(fatal{fmt.Errorf("viewport the scaled window: %w", err)})
	}
	half := full.Div(2)
	s.flipper = &flipper{s: s, w: w, size: half, buffers: [2]*testclient.DMABUF{s.client.DMABUF(half, testclient.Fill(0xff, 0x40, 0)), s.client.DMABUF(half, testclient.Fill(0, 0x40, 0xff))}}
	s.flipper.commit()
}

func (f *flipper) commit() {
	b, size := f.buffers[f.next], f.size
	f.next = 1 - f.next
	if _, err := f.w.Surface.Frame(f); err != nil {
		panic(fatal{fmt.Errorf("ask the scaled window for a frame: %w", err)})
	}
	if err := errors.Join(f.w.Surface.Attach(b.WL, 0, 0), f.w.Surface.DamageBuffer(0, 0, int32(size.X), int32(size.Y)), f.w.Surface.Commit(), f.s.client.Display.Flush()); err != nil { //nolint:gosec // the buffer is half an output
		panic(fatal{fmt.Errorf("commit the scaled window's buffer: %w", err)})
	}
}

func (f *flipper) Done(*cw.WlCallback, uint32) error {
	if f.running {
		f.commit()
	}
	return nil
}

func flipsScaled(running bool) func(*session) {
	return func(s *session) {
		s.flipper.running = running
		if running {
			s.flipper.commit()
		}
	}
}
