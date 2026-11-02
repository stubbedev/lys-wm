package compositor

import (
	"image"

	"github.com/stubbedev/lys-wm/internal/output"
	sxdg "github.com/stubbedev/lys-wm/internal/protocol/server/xdgshell"
	"github.com/stubbedev/lys-wm/internal/shell"
	"github.com/stubbedev/lys-wm/internal/surface"
	"github.com/stubbedev/lys-wm/internal/wm"
)

type want struct {
	rect      image.Rectangle
	bounds    image.Point
	layer     wm.PlacementLayer
	placed    bool
	activated bool
	minimized bool
	scale     output.Scale
}

type toplevel interface {
	shell.PopupRoot
	Geometry() image.Rectangle
	AppID() string
	Title() string
	Icon() *shell.Icon
	pid() int32
	parent() toplevel
	modal() bool
	prefersFloating() bool
	wantsFullscreen() bool
	sizeLimits() (minimum, maximum image.Point)
	scaled() bool
	settled(w want) bool
	apply(w want) error
	resizes(w want) bool
	answer() func(*surface.Update) bool
	synced() bool
	stale() bool
	shownSize() image.Point
	close() error
	ping() error
}

type xdgToplevel struct {
	*shell.Toplevel
}

func (t xdgToplevel) pid() int32 {
	return t.Surface().Client().Creds().Pid
}

func (t xdgToplevel) parent() toplevel {
	if p := t.Parent(); p != nil {
		return xdgToplevel{p}
	}
	return nil
}

func (t xdgToplevel) modal() bool {
	return t.Modal()
}

func (t xdgToplevel) prefersFloating() bool {
	return t.Dialog()
}

func (t xdgToplevel) wantsFullscreen() bool {
	return t.WantsFullscreen()
}

func (t xdgToplevel) sizeLimits() (minimum, maximum image.Point) {
	return t.MinSize(), t.MaxSize()
}

func (t xdgToplevel) scaled() bool {
	return t.Requested().Bounds != (image.Point{})
}

func xdgState(w want) shell.State {
	st := shell.State{Size: w.rect.Size(), Bounds: w.bounds}
	switch {
	case !w.placed:
	case w.layer == wm.LayerTiled:
		st.States = append(st.States,
			sxdg.XdgToplevelStateTiledLeft, sxdg.XdgToplevelStateTiledRight, sxdg.XdgToplevelStateTiledTop, sxdg.XdgToplevelStateTiledBottom,
			sxdg.XdgToplevelStateConstrainedLeft, sxdg.XdgToplevelStateConstrainedRight, sxdg.XdgToplevelStateConstrainedTop, sxdg.XdgToplevelStateConstrainedBottom)
	case w.layer == wm.LayerFullscreen:
		st.States = append(st.States, sxdg.XdgToplevelStateFullscreen)
	}
	if w.activated {
		st.States = append(st.States, sxdg.XdgToplevelStateActivated)
	}
	return st
}

func (t xdgToplevel) settled(w want) bool {
	return sameState(t.Requested(), xdgState(w))
}

func (t xdgToplevel) apply(w want) error {
	s := t.Surface()
	if w.scale != 0 {
		if err := s.SetPreferredScale(uint32(w.scale)); err != nil {
			return err
		}
	}
	_, err := t.Configure(xdgState(w))
	return err
}

func (t xdgToplevel) resizes(w want) bool {
	st, was := xdgState(w), t.Requested()
	flips := st.Has(sxdg.XdgToplevelStateFullscreen) != was.Has(sxdg.XdgToplevelStateFullscreen)
	return flips || (st.Size != was.Size && st.Size != t.Geometry().Size())
}

func (t xdgToplevel) answer() func(*surface.Update) bool {
	serial := t.LatestSerial()
	return func(u *surface.Update) bool {
		return t.Acked(serial) && u.Buffer() != nil
	}
}

func (xdgToplevel) synced() bool {
	return true
}

func (t xdgToplevel) stale() bool {
	return t.Requested().Size != t.Current().Size
}

func (t xdgToplevel) shownSize() image.Point {
	return t.Geometry().Size()
}

func (t xdgToplevel) close() error {
	return t.SendClose()
}

func (t xdgToplevel) ping() error {
	return t.Ping()
}
