package main

import (
	"errors"
	"fmt"
	"image"
	"os"

	cw "github.com/stubbedev/lys-wm/internal/protocol/client/wayland"
	"github.com/stubbedev/lys-wm/internal/testclient"
)

const scanoutSteps = "scanout"

type flipper struct {
	s       *session
	w       *testclient.Window
	title   string
	buffers [2]*testclient.DMABUF
	size    image.Point
	next    int
	running bool
}

func (s *session) hideCursorOver(title string, in *testclient.Input) {
	s.client.Sync()
	in.Serial = 0
	s.swipe("move -100000 -100000")
	s.swipe("move 200 200")
	s.client.DispatchUntil("the pointer to enter "+title, func() bool { return in.Serial != 0 })
	if err := errors.Join(in.Pointer.SetCursor(in.Serial, nil, 0, 0), s.client.Display.Flush()); err != nil {
		panic(fatal{fmt.Errorf("hide the cursor over %s: %w", title, err)})
	}
}

func (s *session) flip(title string, w *testclient.Window) {
	w.Stop()
	full := w.Logical
	vp, err := s.client.Viewporter.GetViewport(w.Surface)
	if err != nil {
		panic(fatal{fmt.Errorf("get the viewport of %s: %w", title, err)})
	}
	if err := vp.SetDestination(int32(full.X), int32(full.Y)); err != nil { //nolint:gosec // an output's logical size fits
		panic(fatal{fmt.Errorf("viewport %s: %w", title, err)})
	}
	half := full.Div(2)
	f := &flipper{s: s, w: w, title: title, size: half, buffers: [2]*testclient.DMABUF{s.client.DMABUF(half, testclient.Fill(0xff, 0x40, 0)), s.client.DMABUF(half, testclient.Fill(0, 0x40, 0xff))}}
	s.flippers = append(s.flippers, f)
	f.commit()
}

func (s *session) scaledFullscreen() {
	_, in := s.client.Seat()
	w := s.window("scaled", testclient.Fill(0x30, 0x30, 0x30))
	s.dispatch("fullscreen")
	s.client.DispatchUntil("the fullscreen configure", func() bool { return w.Logical.X > 0 && w.Logical == w.Size })
	s.hideCursorOver("scaled", in)
	s.flip("scaled", w)
}

func squareCorners(on bool) func(*session) {
	return func(s *session) {
		var err error
		if on {
			err = os.WriteFile(s.runtime, []byte("decoration: {rounding: 0}\n"), 0o600)
		} else {
			err = os.Remove(s.runtime)
		}
		if err != nil {
			panic(fatal{fmt.Errorf("square the corners for overlay planes: %w", err)})
		}
		s.dispatch("reload")
		s.settle()
	}
}

func (s *session) windowedVideos() {
	squareCorners(true)(s)
	_, in := s.client.Seat()
	a := s.window("video a", testclient.Fill(0x30, 0x30, 0x30))
	b := s.window("video b", testclient.Fill(0x30, 0x30, 0x30))
	s.settle()
	s.hideCursorOver("video a", in)
	s.flip("video a", a)
	s.flip("video b", b)
}

func (f *flipper) commit() {
	b, size := f.buffers[f.next], f.size
	f.next = 1 - f.next
	if _, err := f.w.Surface.Frame(f); err != nil {
		panic(fatal{fmt.Errorf("ask %s for a frame: %w", f.title, err)})
	}
	if err := errors.Join(f.w.Surface.Attach(b.WL, 0, 0), f.w.Surface.DamageBuffer(0, 0, int32(size.X), int32(size.Y)), f.w.Surface.Commit(), f.s.client.Display.Flush()); err != nil { //nolint:gosec // the buffer is half an output
		panic(fatal{fmt.Errorf("commit the buffer of %s: %w", f.title, err)})
	}
}

func (f *flipper) Done(*cw.WlCallback, uint32) error {
	if f.running {
		f.commit()
	}
	return nil
}

func flips(running bool) func(*session) {
	return func(s *session) {
		for _, f := range s.flippers {
			f.running = running
			if running {
				f.commit()
			}
		}
	}
}
